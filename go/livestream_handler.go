package main

import (
	"context"
	"database/sql"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

type ReserveLivestreamRequest struct {
	Tags         []int64 `json:"tags"`
	Title        string  `json:"title"`
	Description  string  `json:"description"`
	PlaylistUrl  string  `json:"playlist_url"`
	ThumbnailUrl string  `json:"thumbnail_url"`
	StartAt      int64   `json:"start_at"`
	EndAt        int64   `json:"end_at"`
}

type LivestreamViewerModel struct {
	UserID       int64 `db:"user_id" json:"user_id"`
	LivestreamID int64 `db:"livestream_id" json:"livestream_id"`
	CreatedAt    int64 `db:"created_at" json:"created_at"`
}

type LivestreamModel struct {
	ID           int64  `db:"id" json:"id"`
	UserID       int64  `db:"user_id" json:"user_id"`
	Title        string `db:"title" json:"title"`
	Description  string `db:"description" json:"description"`
	PlaylistUrl  string `db:"playlist_url" json:"playlist_url"`
	ThumbnailUrl string `db:"thumbnail_url" json:"thumbnail_url"`
	StartAt      int64  `db:"start_at" json:"start_at"`
	EndAt        int64  `db:"end_at" json:"end_at"`
}

type Livestream struct {
	ID           int64  `json:"id"`
	Owner        User   `json:"owner"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	PlaylistUrl  string `json:"playlist_url"`
	ThumbnailUrl string `json:"thumbnail_url"`
	Tags         []Tag  `json:"tags"`
	StartAt      int64  `json:"start_at"`
	EndAt        int64  `json:"end_at"`
}

type LivestreamTagModel struct {
	ID           int64 `db:"id" json:"id"`
	LivestreamID int64 `db:"livestream_id" json:"livestream_id"`
	TagID        int64 `db:"tag_id" json:"tag_id"`
}

type ReservationSlotModel struct {
	ID      int64 `db:"id" json:"id"`
	Slot    int64 `db:"slot" json:"slot"`
	StartAt int64 `db:"start_at" json:"start_at"`
	EndAt   int64 `db:"end_at" json:"end_at"`
}

// livestream_tags のオンメモリキャッシュ（key: livestream_id）
// tagIDToLivestreamIDs: tag_id -> []livestream_id (livestream_id DESC)
var (
	livestreamTagCache    = make(map[int64][]LivestreamTagModel)
	tagIDToLivestreamIDs  = make(map[int64][]int64)
	livestreamTagCacheMu  sync.RWMutex
)

func getLivestreamIDsByTagID(tagID int64) []int64 {
	livestreamTagCacheMu.RLock()
	defer livestreamTagCacheMu.RUnlock()
	ids, ok := tagIDToLivestreamIDs[tagID]
	if !ok {
		return nil
	}
	result := make([]int64, len(ids))
	copy(result, ids)
	return result
}

func getLivestreamTagsByLivestreamIDs(livestreamIDs []int64) map[int64][]LivestreamTagModel {
	if len(livestreamIDs) == 0 {
		return nil
	}
	livestreamTagCacheMu.RLock()
	defer livestreamTagCacheMu.RUnlock()
	result := make(map[int64][]LivestreamTagModel, len(livestreamIDs))
	for _, id := range livestreamIDs {
		if tags, ok := livestreamTagCache[id]; ok {
			result[id] = tags
		}
	}
	return result
}

func setLivestreamTagsCache(livestreamID int64, tags []LivestreamTagModel) {
	if len(tags) == 0 {
		return
	}
	livestreamTagCacheMu.Lock()
	livestreamTagCache[livestreamID] = tags
	for _, t := range tags {
		tagIDToLivestreamIDs[t.TagID] = append([]int64{livestreamID}, tagIDToLivestreamIDs[t.TagID]...)
	}
	livestreamTagCacheMu.Unlock()
}

func clearLivestreamTagsCache() {
	livestreamTagCacheMu.Lock()
	livestreamTagCache = make(map[int64][]LivestreamTagModel)
	tagIDToLivestreamIDs = make(map[int64][]int64)
	livestreamTagCacheMu.Unlock()
}

func warmUpLivestreamTagsCache(ctx context.Context, db *sqlx.DB) error {
	var all []LivestreamTagModel
	if err := db.SelectContext(ctx, &all, "SELECT * FROM livestream_tags"); err != nil {
		return err
	}
	livestreamTagCacheMu.Lock()
	for _, lt := range all {
		livestreamTagCache[lt.LivestreamID] = append(livestreamTagCache[lt.LivestreamID], lt)
	}
	// tag_id -> livestream_ids (livestream_id DESC) を構築
	// allLivestreamIDsDesc の順序で走査すれば DESC になる
	livestreamCacheMu.RLock()
	orderedIDs := allLivestreamIDsDesc
	livestreamCacheMu.RUnlock()
	for _, livestreamID := range orderedIDs {
		for _, lt := range livestreamTagCache[livestreamID] {
			tagIDToLivestreamIDs[lt.TagID] = append(tagIDToLivestreamIDs[lt.TagID], livestreamID)
		}
	}
	livestreamTagCacheMu.Unlock()
	return nil
}

// livestreams のオンメモリキャッシュ
var (
	livestreamCache      = make(map[int64]LivestreamModel)
	livestreamsByUserID   = make(map[int64][]int64)
	allLivestreamIDsDesc []int64
	livestreamCacheMu    sync.RWMutex
)

func getLivestreamFromCache(id int64) (LivestreamModel, bool) {
	livestreamCacheMu.RLock()
	defer livestreamCacheMu.RUnlock()
	m, ok := livestreamCache[id]
	return m, ok
}

func getLivestreamModelsFromCache(ids []int64) []LivestreamModel {
	if len(ids) == 0 {
		return nil
	}
	livestreamCacheMu.RLock()
	defer livestreamCacheMu.RUnlock()
	result := make([]LivestreamModel, 0, len(ids))
	seen := make(map[int64]struct{})
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if m, ok := livestreamCache[id]; ok {
			result = append(result, m)
		}
	}
	return result
}

func getLivestreamModelsFromCachePreservingOrder(ids []int64) []LivestreamModel {
	if len(ids) == 0 {
		return nil
	}
	livestreamCacheMu.RLock()
	defer livestreamCacheMu.RUnlock()
	result := make([]LivestreamModel, 0, len(ids))
	for _, id := range ids {
		if m, ok := livestreamCache[id]; ok {
			result = append(result, m)
		}
	}
	return result
}

func getLivestreamModelsByUserID(userID int64) []LivestreamModel {
	livestreamCacheMu.RLock()
	ids, ok := livestreamsByUserID[userID]
	livestreamCacheMu.RUnlock()
	if !ok {
		return nil
	}
	return getLivestreamModelsFromCachePreservingOrder(ids)
}

func getAllLivestreamModelsDesc(limit int) []LivestreamModel {
	livestreamCacheMu.RLock()
	ids := allLivestreamIDsDesc
	livestreamCacheMu.RUnlock()
	if limit > 0 && limit < len(ids) {
		ids = ids[:limit]
	}
	return getLivestreamModelsFromCachePreservingOrder(ids)
}

func setLivestreamCache(model LivestreamModel) {
	livestreamCacheMu.Lock()
	livestreamCache[model.ID] = model
	livestreamsByUserID[model.UserID] = append([]int64{model.ID}, livestreamsByUserID[model.UserID]...)
	allLivestreamIDsDesc = append([]int64{model.ID}, allLivestreamIDsDesc...)
	livestreamCacheMu.Unlock()
}

func clearLivestreamCache() {
	livestreamCacheMu.Lock()
	livestreamCache = make(map[int64]LivestreamModel)
	livestreamsByUserID = make(map[int64][]int64)
	allLivestreamIDsDesc = nil
	livestreamCacheMu.Unlock()
}

func warmUpLivestreamCache(ctx context.Context, db *sqlx.DB) error {
	var all []LivestreamModel
	if err := db.SelectContext(ctx, &all, "SELECT * FROM livestreams ORDER BY id DESC"); err != nil {
		return err
	}
	livestreamCacheMu.Lock()
	for _, m := range all {
		livestreamCache[m.ID] = m
		livestreamsByUserID[m.UserID] = append(livestreamsByUserID[m.UserID], m.ID)
	}
	allLivestreamIDsDesc = make([]int64, len(all))
	for i, m := range all {
		allLivestreamIDsDesc[i] = m.ID
	}
	livestreamCacheMu.Unlock()
	return nil
}

func reserveLivestreamHandler(c echo.Context) error {
	ctx := c.Request().Context()
	defer c.Request().Body.Close()

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	var req *ReserveLivestreamRequest
	if err := json.UnmarshalRead(c.Request().Body, &req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	// 2023/11/25 10:00からの１年間の期間内であるかチェック
	var (
		termStartAt    = time.Date(2023, 11, 25, 1, 0, 0, 0, time.UTC)
		termEndAt      = time.Date(2024, 11, 25, 1, 0, 0, 0, time.UTC)
		reserveStartAt = time.Unix(req.StartAt, 0)
		reserveEndAt   = time.Unix(req.EndAt, 0)
	)
	if (reserveStartAt.Equal(termEndAt) || reserveStartAt.After(termEndAt)) || (reserveEndAt.Equal(termStartAt) || reserveEndAt.Before(termStartAt)) {
		return echo.NewHTTPError(http.StatusBadRequest, "bad reservation time range")
	}

	// 1時間スロット前提: end_at = start_at + 3600
	// start_at >= X AND end_at <= Y => start_at >= X AND start_at <= Y-3600 でインデックスを効率的に使用
	slotEndAt := req.EndAt - 3600
	if slotEndAt < req.StartAt {
		return echo.NewHTTPError(http.StatusBadRequest, "bad reservation time range")
	}

	// 予約枠をみて、予約が可能か調べる
	// NOTE: 並列な予約のoverbooking防止にFOR UPDATEが必要
	var slots []*ReservationSlotModel
	if err := tx.SelectContext(ctx, &slots, "SELECT * FROM reservation_slots WHERE start_at >= ? AND start_at <= ? FOR UPDATE", req.StartAt, slotEndAt); err != nil {
		c.Logger().Warnf("予約枠一覧取得でエラー発生: %+v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get reservation_slots: "+err.Error())
	}
	for _, slot := range slots {
		c.Logger().Infof("%d ~ %d予約枠の残数 = %d\n", slot.StartAt, slot.EndAt, slot.Slot)
		if slot.Slot < 1 {
			return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("予約期間 %d ~ %dに対して、予約区間 %d ~ %dが予約できません", termStartAt.Unix(), termEndAt.Unix(), req.StartAt, req.EndAt))
		}
	}

	var (
		livestreamModel = &LivestreamModel{
			UserID:       int64(userID),
			Title:        req.Title,
			Description:  req.Description,
			PlaylistUrl:  req.PlaylistUrl,
			ThumbnailUrl: req.ThumbnailUrl,
			StartAt:      req.StartAt,
			EndAt:        req.EndAt,
		}
	)

	if _, err := tx.ExecContext(ctx, "UPDATE reservation_slots SET slot = slot - 1 WHERE start_at >= ? AND start_at <= ?", req.StartAt, slotEndAt); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to update reservation_slot: "+err.Error())
	}

	rs, err := tx.NamedExecContext(ctx, "INSERT INTO livestreams (user_id, title, description, playlist_url, thumbnail_url, start_at, end_at) VALUES(:user_id, :title, :description, :playlist_url, :thumbnail_url, :start_at, :end_at)", livestreamModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert livestream: "+err.Error())
	}

	livestreamID, err := rs.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted livestream id: "+err.Error())
	}
	livestreamModel.ID = livestreamID

	// タグ追加
	tagModelsForCache := make([]LivestreamTagModel, 0, len(req.Tags))
	for _, tagID := range req.Tags {
		if _, err := tx.NamedExecContext(ctx, "INSERT INTO livestream_tags (livestream_id, tag_id) VALUES (:livestream_id, :tag_id)", &LivestreamTagModel{
			LivestreamID: livestreamID,
			TagID:        tagID,
		}); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert livestream tag: "+err.Error())
		}
		tagModelsForCache = append(tagModelsForCache, LivestreamTagModel{LivestreamID: livestreamID, TagID: tagID})
	}
	setLivestreamTagsCache(livestreamID, tagModelsForCache)
	setLivestreamCache(*livestreamModel)

	livestream, err := fillLivestreamResponse(ctx, tx, *livestreamModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestream: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusCreated, livestream)
}

func searchLivestreamsHandler(c echo.Context) error {
	ctx := c.Request().Context()
	keyTagName := c.QueryParam("tag")

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	var livestreamModels []*LivestreamModel
	if c.QueryParam("tag") != "" {
		// タグによる取得（オンメモリキャッシュから解決）
		tagModel, ok := getTagByName(keyTagName)
		if !ok {
			livestreamModels = []*LivestreamModel{}
		} else {
			livestreamIDs := getLivestreamIDsByTagID(tagModel.ID)
			if len(livestreamIDs) > 0 {
				fetched := getLivestreamModelsFromCachePreservingOrder(livestreamIDs)
				livestreamModels = make([]*LivestreamModel, 0, len(fetched))
				for i := range fetched {
					livestreamModels = append(livestreamModels, &fetched[i])
				}
			}
		}
	} else {
		// 検索条件なし（オンメモリキャッシュから取得）
		limit := 0
		if c.QueryParam("limit") != "" {
			var err error
			limit, err = strconv.Atoi(c.QueryParam("limit"))
			if err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, "limit query parameter must be integer")
			}
		}
		fetched := getAllLivestreamModelsDesc(limit)
		livestreamModels = make([]*LivestreamModel, 0, len(fetched))
		for i := range fetched {
			livestreamModels = append(livestreamModels, &fetched[i])
		}
	}

	// ポインタスライスを値スライスに変換
	lsModels := make([]LivestreamModel, len(livestreamModels))
	for i, lsPtr := range livestreamModels {
		lsModels[i] = *lsPtr
	}
	livestreams, err := fillLivestreamsResponse(ctx, tx, lsModels)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestreams: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, livestreams)
}

func getMyLivestreamsHandler(c echo.Context) error {
	ctx := c.Request().Context()
	if err := verifyUserSession(c); err != nil {
		return err
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	lsModels := getLivestreamModelsByUserID(userID)
	livestreams, err := fillLivestreamsResponse(ctx, tx, lsModels)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestreams: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, livestreams)
}

func getUserLivestreamsHandler(c echo.Context) error {
	ctx := c.Request().Context()
	if err := verifyUserSession(c); err != nil {
		return err
	}

	username := c.Param("username")

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	var user UserModel
	if err := tx.GetContext(ctx, &user, "SELECT * FROM users WHERE name = ?", username); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "user not found")
		} else {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
		}
	}

	lsModels := getLivestreamModelsByUserID(user.ID)
	livestreams, err := fillLivestreamsResponse(ctx, tx, lsModels)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestreams: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, livestreams)
}

// viewerテーブルの廃止
func enterLivestreamHandler(c echo.Context) error {
	ctx := c.Request().Context()
	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id must be integer")
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	viewer := LivestreamViewerModel{
		UserID:       int64(userID),
		LivestreamID: int64(livestreamID),
		CreatedAt:    time.Now().Unix(),
	}

	if _, err := tx.NamedExecContext(ctx, "INSERT INTO livestream_viewers_history (user_id, livestream_id, created_at) VALUES(:user_id, :livestream_id, :created_at)", viewer); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert livestream_view_history: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.NoContent(http.StatusOK)
}

func exitLivestreamHandler(c echo.Context) error {
	ctx := c.Request().Context()
	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "DELETE FROM livestream_viewers_history WHERE user_id = ? AND livestream_id = ?", userID, livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete livestream_view_history: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.NoContent(http.StatusOK)
}

func getLivestreamHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	livestreamModel, ok := getLivestreamFromCache(int64(livestreamID))
	if !ok {
		return echo.NewHTTPError(http.StatusNotFound, "not found livestream that has the given id")
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	livestream, err := fillLivestreamResponse(ctx, tx, livestreamModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestream: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, livestream)
}

func getLivecommentReportsHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	livestreamModel, ok := getLivestreamFromCache(int64(livestreamID))
	if !ok {
		return echo.NewHTTPError(http.StatusNotFound, "livestream not found")
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	// error already check
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already check
	userID := sess.Values[defaultUserIDKey].(int64)

	if livestreamModel.UserID != userID {
		return echo.NewHTTPError(http.StatusForbidden, "can't get other streamer's livecomment reports")
	}

	var reportModels []*LivecommentReportModel
	if err := tx.SelectContext(ctx, &reportModels, "SELECT * FROM livecomment_reports WHERE livestream_id = ?", livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livecomment reports: "+err.Error())
	}

	// ポインタスライスを値スライスに変換
	rModels := make([]LivecommentReportModel, len(reportModels))
	for i, rPtr := range reportModels {
		rModels[i] = *rPtr
	}
	reports, err := fillLivecommentReportsResponse(ctx, tx, rModels)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livecomment reports: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, reports)
}

func fillLivestreamResponse(ctx context.Context, tx *sqlx.Tx, livestreamModel LivestreamModel) (Livestream, error) {
	livestreams, err := fillLivestreamsResponse(ctx, tx, []LivestreamModel{livestreamModel})
	if err != nil {
		return Livestream{}, err
	}
	return livestreams[0], nil
}

func fillLivestreamsResponse(ctx context.Context, tx *sqlx.Tx, livestreamModels []LivestreamModel) ([]Livestream, error) {
	if len(livestreamModels) == 0 {
		return []Livestream{}, nil
	}

	// owner の user_id を収集
	userIDs := make([]int64, len(livestreamModels))
	livestreamIDs := make([]int64, len(livestreamModels))
	for i, ls := range livestreamModels {
		userIDs[i] = ls.UserID
		livestreamIDs[i] = ls.ID
	}

	// users をキャッシュまたはDBから一括取得
	userModels, err := getUserModelsFromCacheOrDB(ctx, tx, userIDs)
	if err != nil {
		return nil, err
	}

	// users を一括で fill
	users, err := fillUsersResponse(ctx, tx, userModels)
	if err != nil {
		return nil, err
	}
	userMap := make(map[int64]User)
	for i, u := range userModels {
		userMap[u.ID] = users[i]
	}

	// livestream_tags をオンメモリキャッシュから一括取得
	tagsByLivestreamID := getLivestreamTagsByLivestreamIDs(livestreamIDs)
	var allTagModels []LivestreamTagModel
	for _, tags := range tagsByLivestreamID {
		allTagModels = append(allTagModels, tags...)
	}

	// tag_id を収集
	tagIDSet := make(map[int64]struct{})
	for _, t := range allTagModels {
		tagIDSet[t.TagID] = struct{}{}
	}
	tagIDs := make([]int64, 0, len(tagIDSet))
	for id := range tagIDSet {
		tagIDs = append(tagIDs, id)
	}

	// tags をオンメモリキャッシュから取得
	tagMap := make(map[int64]TagModel, len(tagIDs))
	for _, id := range tagIDs {
		if t, ok := getTagByID(id); ok {
			tagMap[id] = t
		}
	}

	// livestream_id ごとの tags をマップ化
	livestreamTagsMap := make(map[int64][]Tag)
	for _, lt := range allTagModels {
		if tagModel, ok := tagMap[lt.TagID]; ok {
			livestreamTagsMap[lt.LivestreamID] = append(livestreamTagsMap[lt.LivestreamID], Tag{
				ID:   tagModel.ID,
				Name: tagModel.Name,
			})
		}
	}

	// Livestream を構築
	livestreams := make([]Livestream, len(livestreamModels))
	for i, lsModel := range livestreamModels {
		tags := livestreamTagsMap[lsModel.ID]
		if tags == nil {
			tags = []Tag{}
		}
		livestreams[i] = Livestream{
			ID:           lsModel.ID,
			Owner:        userMap[lsModel.UserID],
			Title:        lsModel.Title,
			Tags:         tags,
			Description:  lsModel.Description,
			PlaylistUrl:  lsModel.PlaylistUrl,
			ThumbnailUrl: lsModel.ThumbnailUrl,
			StartAt:      lsModel.StartAt,
			EndAt:        lsModel.EndAt,
		}
	}

	return livestreams, nil
}
