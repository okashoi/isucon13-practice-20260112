package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
)

// tags のオンメモリキャッシュ（key: id / name）
var (
	tagCacheByID   map[int64]TagModel
	tagCacheByName map[string]TagModel
	tagCacheAll    []*Tag
	tagCacheMu     sync.RWMutex
)

func warmUpTagsCache(ctx context.Context, db *sqlx.DB) error {
	var models []TagModel
	if err := db.SelectContext(ctx, &models, "SELECT * FROM tags"); err != nil {
		return err
	}
	byID := make(map[int64]TagModel, len(models))
	byName := make(map[string]TagModel, len(models))
	all := make([]*Tag, len(models))
	for i := range models {
		m := &models[i]
		byID[m.ID] = *m
		byName[m.Name] = *m
		all[i] = &Tag{ID: m.ID, Name: m.Name}
	}
	tagCacheMu.Lock()
	tagCacheByID = byID
	tagCacheByName = byName
	tagCacheAll = all
	tagCacheMu.Unlock()
	return nil
}

func clearTagsCache() {
	tagCacheMu.Lock()
	tagCacheByID = nil
	tagCacheByName = nil
	tagCacheAll = nil
	tagCacheMu.Unlock()
}

func getTagByID(id int64) (TagModel, bool) {
	tagCacheMu.RLock()
	defer tagCacheMu.RUnlock()
	t, ok := tagCacheByID[id]
	return t, ok
}

func getTagByName(name string) (TagModel, bool) {
	tagCacheMu.RLock()
	defer tagCacheMu.RUnlock()
	t, ok := tagCacheByName[name]
	return t, ok
}

func getTagCacheAll() []*Tag {
	tagCacheMu.RLock()
	defer tagCacheMu.RUnlock()
	return tagCacheAll
}

type Tag struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type TagModel struct {
	ID   int64  `db:"id"`
	Name string `db:"name"`
}

type TagsResponse struct {
	Tags []*Tag `json:"tags"`
}

func getTagHandler(c echo.Context) error {
	return c.JSON(http.StatusOK, &TagsResponse{Tags: getTagCacheAll()})
}

// 配信者のテーマ取得API
// GET /api/user/:username/theme
func getStreamerThemeHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		c.Logger().Printf("verifyUserSession: %+v\n", err)
		return err
	}

	username := c.Param("username")

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	userModel := UserModel{}
	err = tx.GetContext(ctx, &userModel, "SELECT id FROM users WHERE name = ?", username)
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found user that has the given username")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	themeModel, ok := getThemeFromCache(userModel.ID)
	if !ok {
		tx2, err := dbConn.BeginTxx(ctx, nil)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
		}
		defer tx2.Rollback()
		if err := tx2.GetContext(ctx, &themeModel, "SELECT * FROM themes WHERE user_id = ?", userModel.ID); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user theme: "+err.Error())
		}
		if err := tx2.Commit(); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
		}
		setThemeCache(themeModel)
	}

	theme := Theme{
		ID:       themeModel.ID,
		DarkMode: themeModel.DarkMode,
	}

	return c.JSON(http.StatusOK, theme)
}
