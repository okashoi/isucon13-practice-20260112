package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/sessions"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
)

const (
	defaultSessionIDKey      = "SESSIONID"
	defaultSessionExpiresKey = "EXPIRES"
	defaultUserIDKey         = "USERID"
	defaultUsernameKey       = "USERNAME"
	bcryptDefaultCost        = bcrypt.MinCost
)

var fallbackImage = "../img/NoImage.jpg"

// アイコンキャッシュ用ディレクトリ
var iconCacheDir = "../icons"

// アイコンハッシュのメモリキャッシュ
var (
	iconHashCache   = make(map[int64]string)
	iconHashCacheMu sync.RWMutex
)

// fallback 画像のハッシュ（起動時に計算）
var fallbackImageHash string

func initIconCache() error {
	// キャッシュディレクトリを作成
	if err := os.MkdirAll(iconCacheDir, 0755); err != nil {
		return err
	}

	// fallback 画像のハッシュを計算
	fallbackImageData, err := os.ReadFile(fallbackImage)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(fallbackImageData)
	fallbackImageHash = fmt.Sprintf("%x", hash)

	return nil
}

func clearIconCache() error {
	// メモリキャッシュをクリア
	iconHashCacheMu.Lock()
	iconHashCache = make(map[int64]string)
	iconHashCacheMu.Unlock()

	// キャッシュディレクトリ内のファイルを削除
	entries, err := os.ReadDir(iconCacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			return os.MkdirAll(iconCacheDir, 0755)
		}
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			os.Remove(fmt.Sprintf("%s/%s", iconCacheDir, entry.Name()))
		}
	}
	return nil
}

func getIconPath(userID int64) string {
	return fmt.Sprintf("%s/%d.jpg", iconCacheDir, userID)
}

func getIconHash(userID int64) (string, bool) {
	iconHashCacheMu.RLock()
	hash, ok := iconHashCache[userID]
	iconHashCacheMu.RUnlock()
	return hash, ok
}

func setIconHash(userID int64, hash string) {
	iconHashCacheMu.Lock()
	iconHashCache[userID] = hash
	iconHashCacheMu.Unlock()
}

type UserModel struct {
	ID             int64  `db:"id"`
	Name           string `db:"name"`
	DisplayName    string `db:"display_name"`
	Description    string `db:"description"`
	HashedPassword string `db:"password"`
}

type User struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
	Theme       Theme  `json:"theme,omitempty"`
	IconHash    string `json:"icon_hash,omitempty"`
}

type Theme struct {
	ID       int64 `json:"id"`
	DarkMode bool  `json:"dark_mode"`
}

type ThemeModel struct {
	ID       int64 `db:"id"`
	UserID   int64 `db:"user_id"`
	DarkMode bool  `db:"dark_mode"`
}

type PostUserRequest struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	// Password is non-hashed password.
	Password string               `json:"password"`
	Theme    PostUserRequestTheme `json:"theme"`
}

type PostUserRequestTheme struct {
	DarkMode bool `json:"dark_mode"`
}

type LoginRequest struct {
	Username string `json:"username"`
	// Password is non-hashed password.
	Password string `json:"password"`
}

type PostIconRequest struct {
	Image []byte `json:"image"`
}

type PostIconResponse struct {
	ID int64 `json:"id"`
}

func getIconHandler(c echo.Context) error {
	ctx := c.Request().Context()

	username := c.Param("username")

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	var user UserModel
	if err := tx.GetContext(ctx, &user, "SELECT * FROM users WHERE name = ?", username); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "not found user that has the given username")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
	}

	// If-None-Match ヘッダをチェック
	ifNoneMatch := c.Request().Header.Get("If-None-Match")

	// メモリキャッシュからハッシュを取得
	iconHash, hashCached := getIconHash(user.ID)

	// ファイルシステムからアイコンを読み込む
	iconPath := getIconPath(user.ID)
	if _, err := os.Stat(iconPath); err == nil {
		// ファイルが存在する場合
		// ハッシュがキャッシュされていない場合は計算
		if !hashCached {
			imageData, err := os.ReadFile(iconPath)
			if err == nil {
				hash := sha256.Sum256(imageData)
				iconHash = fmt.Sprintf("%x", hash)
				setIconHash(user.ID, iconHash)
				hashCached = true
			}
		}

		// If-None-Match が一致すれば 304 を返す
		if hashCached && ifNoneMatch == fmt.Sprintf(`"%s"`, iconHash) {
			return c.NoContent(http.StatusNotModified)
		}

		// ETag ヘッダを付与してファイルを返す
		if hashCached {
			c.Response().Header().Set("ETag", fmt.Sprintf(`"%s"`, iconHash))
		}
		return c.File(iconPath)
	}

	// ファイルが存在しない場合は DB から取得してキャッシュ
	var image []byte
	if err := tx.GetContext(ctx, &image, "SELECT image FROM icons WHERE user_id = ?", user.ID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// fallback 画像の場合も If-None-Match をチェック
			if ifNoneMatch == fmt.Sprintf(`"%s"`, fallbackImageHash) {
				return c.NoContent(http.StatusNotModified)
			}
			c.Response().Header().Set("ETag", fmt.Sprintf(`"%s"`, fallbackImageHash))
			return c.File(fallbackImage)
		} else {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user icon: "+err.Error())
		}
	}

	// ファイルに保存（キャッシュ）
	if err := os.WriteFile(iconPath, image, 0644); err != nil {
		// 書き込み失敗してもレスポンスは返す
		c.Logger().Warnf("failed to cache icon: %v", err)
	}

	// ハッシュも計算してキャッシュ
	hash := sha256.Sum256(image)
	iconHash = fmt.Sprintf("%x", hash)
	setIconHash(user.ID, iconHash)

	// If-None-Match が一致すれば 304 を返す
	if ifNoneMatch == fmt.Sprintf(`"%s"`, iconHash) {
		return c.NoContent(http.StatusNotModified)
	}

	// ETag ヘッダを付与
	c.Response().Header().Set("ETag", fmt.Sprintf(`"%s"`, iconHash))
	return c.Blob(http.StatusOK, "image/jpeg", image)
}

func postIconHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	var req *PostIconRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "DELETE FROM icons WHERE user_id = ?", userID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete old user icon: "+err.Error())
	}

	rs, err := tx.ExecContext(ctx, "INSERT INTO icons (user_id, image) VALUES (?, ?)", userID, req.Image)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert new user icon: "+err.Error())
	}

	iconID, err := rs.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted icon id: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	// ファイルシステムにも保存（キャッシュ）
	iconPath := getIconPath(userID)
	if err := os.WriteFile(iconPath, req.Image, 0644); err != nil {
		c.Logger().Warnf("failed to cache icon: %v", err)
	}

	// ハッシュを計算してメモリキャッシュに保存
	hash := sha256.Sum256(req.Image)
	setIconHash(userID, fmt.Sprintf("%x", hash))

	return c.JSON(http.StatusCreated, &PostIconResponse{
		ID: iconID,
	})
}

func getMeHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	userModel := UserModel{}
	err = tx.GetContext(ctx, &userModel, "SELECT * FROM users WHERE id = ?", userID)
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found user that has the userid in session")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
	}

	user, err := fillUserResponse(ctx, tx, userModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill user: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, user)
}

// ユーザ登録API
// POST /api/register
func registerHandler(c echo.Context) error {
	ctx := c.Request().Context()
	defer c.Request().Body.Close()

	req := PostUserRequest{}
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	if req.Name == "pipe" {
		return echo.NewHTTPError(http.StatusBadRequest, "the username 'pipe' is reserved")
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcryptDefaultCost)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to generate hashed password: "+err.Error())
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	userModel := UserModel{
		Name:           req.Name,
		DisplayName:    req.DisplayName,
		Description:    req.Description,
		HashedPassword: string(hashedPassword),
	}

	result, err := tx.NamedExecContext(ctx, "INSERT INTO users (name, display_name, description, password) VALUES(:name, :display_name, :description, :password)", userModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert user: "+err.Error())
	}

	userID, err := result.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted user id: "+err.Error())
	}

	userModel.ID = userID

	themeModel := ThemeModel{
		UserID:   userID,
		DarkMode: req.Theme.DarkMode,
	}
	if _, err := tx.NamedExecContext(ctx, "INSERT INTO themes (user_id, dark_mode) VALUES(:user_id, :dark_mode)", themeModel); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert user theme: "+err.Error())
	}

	if out, err := exec.Command("pdnsutil", "add-record", "t.isucon.pw", req.Name, "A", "0", powerDNSSubdomainAddress).CombinedOutput(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, string(out)+": "+err.Error())
	}

	user, err := fillUserResponse(ctx, tx, userModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill user: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusCreated, user)
}

// ユーザログインAPI
// POST /api/login
func loginHandler(c echo.Context) error {
	ctx := c.Request().Context()
	defer c.Request().Body.Close()

	req := LoginRequest{}
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	userModel := UserModel{}
	// usernameはUNIQUEなので、whereで一意に特定できる
	err = tx.GetContext(ctx, &userModel, "SELECT * FROM users WHERE name = ?", req.Username)
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid username or password")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	err = bcrypt.CompareHashAndPassword([]byte(userModel.HashedPassword), []byte(req.Password))
	if err == bcrypt.ErrMismatchedHashAndPassword {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid username or password")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to compare hash and password: "+err.Error())
	}

	sessionEndAt := time.Now().Add(1 * time.Hour)

	sessionID := uuid.NewString()

	sess, err := session.Get(defaultSessionIDKey, c)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "failed to get session")
	}

	sess.Options = &sessions.Options{
		Domain: "t.isucon.pw",
		MaxAge: int(60000),
		Path:   "/",
	}
	sess.Values[defaultSessionIDKey] = sessionID
	sess.Values[defaultUserIDKey] = userModel.ID
	sess.Values[defaultUsernameKey] = userModel.Name
	sess.Values[defaultSessionExpiresKey] = sessionEndAt.Unix()

	if err := sess.Save(c.Request(), c.Response()); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save session: "+err.Error())
	}

	return c.NoContent(http.StatusOK)
}

// ユーザ詳細API
// GET /api/user/:username
func getUserHandler(c echo.Context) error {
	ctx := c.Request().Context()
	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	username := c.Param("username")

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	userModel := UserModel{}
	if err := tx.GetContext(ctx, &userModel, "SELECT * FROM users WHERE name = ?", username); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "not found user that has the given username")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
	}

	user, err := fillUserResponse(ctx, tx, userModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill user: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, user)
}

func verifyUserSession(c echo.Context) error {
	sess, err := session.Get(defaultSessionIDKey, c)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "failed to get session")
	}

	sessionExpires, ok := sess.Values[defaultSessionExpiresKey]
	if !ok {
		return echo.NewHTTPError(http.StatusForbidden, "failed to get EXPIRES value from session")
	}

	_, ok = sess.Values[defaultUserIDKey].(int64)
	if !ok {
		return echo.NewHTTPError(http.StatusUnauthorized, "failed to get USERID value from session")
	}

	now := time.Now()
	if now.Unix() > sessionExpires.(int64) {
		return echo.NewHTTPError(http.StatusUnauthorized, "session has expired")
	}

	return nil
}

func fillUserResponse(ctx context.Context, tx *sqlx.Tx, userModel UserModel) (User, error) {
	users, err := fillUsersResponse(ctx, tx, []UserModel{userModel})
	if err != nil {
		return User{}, err
	}
	return users[0], nil
}

// IconModel はアイコン画像取得用の構造体
type IconModel struct {
	UserID int64  `db:"user_id"`
	Image  []byte `db:"image"`
}

func fillUsersResponse(ctx context.Context, tx *sqlx.Tx, userModels []UserModel) ([]User, error) {
	if len(userModels) == 0 {
		return []User{}, nil
	}

	// ユーザーIDを収集（重複排除）
	userIDSet := make(map[int64]struct{})
	for _, u := range userModels {
		userIDSet[u.ID] = struct{}{}
	}
	userIDs := make([]int64, 0, len(userIDSet))
	for id := range userIDSet {
		userIDs = append(userIDs, id)
	}

	// themes を一括取得
	query, args, err := sqlx.In("SELECT * FROM themes WHERE user_id IN (?)", userIDs)
	if err != nil {
		return nil, err
	}
	var themeModels []ThemeModel
	if err := tx.SelectContext(ctx, &themeModels, query, args...); err != nil {
		return nil, err
	}
	themeMap := make(map[int64]ThemeModel)
	for _, t := range themeModels {
		themeMap[t.UserID] = t
	}

	// メモリキャッシュにないユーザーIDを収集
	var uncachedUserIDs []int64
	iconHashMap := make(map[int64]string)
	for _, userID := range userIDs {
		if hash, ok := getIconHash(userID); ok {
			iconHashMap[userID] = hash
		} else {
			uncachedUserIDs = append(uncachedUserIDs, userID)
		}
	}

	// キャッシュにないユーザーのアイコンのみDBから取得
	if len(uncachedUserIDs) > 0 {
		query, args, err = sqlx.In("SELECT user_id, image FROM icons WHERE user_id IN (?)", uncachedUserIDs)
		if err != nil {
			return nil, err
		}
		var iconModels []IconModel
		if err := tx.SelectContext(ctx, &iconModels, query, args...); err != nil {
			return nil, err
		}

		// アイコンがあるユーザーのハッシュを計算してキャッシュ
		iconUserIDSet := make(map[int64]struct{})
		for _, icon := range iconModels {
			hash := sha256.Sum256(icon.Image)
			hashStr := fmt.Sprintf("%x", hash)
			iconHashMap[icon.UserID] = hashStr
			setIconHash(icon.UserID, hashStr)
			iconUserIDSet[icon.UserID] = struct{}{}

			// ファイルにも保存
			iconPath := getIconPath(icon.UserID)
			if _, err := os.Stat(iconPath); os.IsNotExist(err) {
				os.WriteFile(iconPath, icon.Image, 0644)
			}
		}

		// アイコンがないユーザーは fallback ハッシュを使用
		for _, userID := range uncachedUserIDs {
			if _, ok := iconUserIDSet[userID]; !ok {
				iconHashMap[userID] = fallbackImageHash
			}
		}
	}

	// User を構築
	users := make([]User, len(userModels))
	for i, userModel := range userModels {
		theme := themeMap[userModel.ID]
		iconHash := iconHashMap[userModel.ID]
		if iconHash == "" {
			iconHash = fallbackImageHash
		}

		users[i] = User{
			ID:          userModel.ID,
			Name:        userModel.Name,
			DisplayName: userModel.DisplayName,
			Description: userModel.Description,
			Theme: Theme{
				ID:       theme.ID,
				DarkMode: theme.DarkMode,
			},
			IconHash: iconHash,
		}
	}

	return users, nil
}
