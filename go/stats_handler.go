package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"sync"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"golang.org/x/sync/singleflight"
)

// ユーザーランキングスコアのオンメモリキャッシュ (user_id -> score)
var (
	userScoreCache   = make(map[int64]int64)
	userScoreCacheMu sync.RWMutex
)

func getUserScore(userID int64) int64 {
	userScoreCacheMu.RLock()
	defer userScoreCacheMu.RUnlock()
	return userScoreCache[userID]
}

func addUserScore(userID int64, delta int64) {
	if delta == 0 {
		return
	}
	userScoreCacheMu.Lock()
	userScoreCache[userID] += delta
	userScoreCacheMu.Unlock()
}

func getAllUserScores() map[int64]int64 {
	userScoreCacheMu.RLock()
	defer userScoreCacheMu.RUnlock()
	cp := make(map[int64]int64, len(userScoreCache))
	for k, v := range userScoreCache {
		cp[k] = v
	}
	return cp
}

func clearUserScoreCache() {
	userScoreCacheMu.Lock()
	userScoreCache = make(map[int64]int64)
	userScoreCacheMu.Unlock()
}

type userScoreRow struct {
	UserID int64 `db:"user_id"`
	Score  int64 `db:"score"`
}

func warmUpUserScoreCache(ctx context.Context, db *sqlx.DB) error {
	query := `
		SELECT u.id AS user_id,
		       IFNULL(SUM(r.reaction_count), 0) + IFNULL(SUM(lc.tip_sum), 0) AS score
		FROM users u
		LEFT JOIN livestreams l ON l.user_id = u.id
		LEFT JOIN (SELECT livestream_id, COUNT(*) AS reaction_count FROM reactions GROUP BY livestream_id) r ON r.livestream_id = l.id
		LEFT JOIN (SELECT livestream_id, SUM(tip) AS tip_sum FROM livecomments GROUP BY livestream_id) lc ON lc.livestream_id = l.id
		GROUP BY u.id
	`
	var rows []userScoreRow
	if err := db.SelectContext(ctx, &rows, query); err != nil {
		return err
	}
	userScoreCacheMu.Lock()
	userScoreCache = make(map[int64]int64, len(rows))
	for _, r := range rows {
		userScoreCache[r.UserID] = r.Score
	}
	userScoreCacheMu.Unlock()
	return nil
}

// getUserRankFromDB はDBから全ユーザーのスコアを取得してランクを算出する（キャッシュ未構築時のフォールバック）
func getUserRankFromDB(ctx context.Context, tx *sqlx.Tx, username string) int64 {
	query := `
		SELECT u.id AS user_id, u.name AS user_name,
		       IFNULL(SUM(r.reaction_count), 0) + IFNULL(SUM(lc.tip_sum), 0) AS score
		FROM users u
		LEFT JOIN livestreams l ON l.user_id = u.id
		LEFT JOIN (SELECT livestream_id, COUNT(*) AS reaction_count FROM reactions GROUP BY livestream_id) r ON r.livestream_id = l.id
		LEFT JOIN (SELECT livestream_id, SUM(tip) AS tip_sum FROM livecomments GROUP BY livestream_id) lc ON lc.livestream_id = l.id
		GROUP BY u.id, u.name
	`
	type userScoreWithName struct {
		UserID   int64  `db:"user_id"`
		UserName string `db:"user_name"`
		Score    int64  `db:"score"`
	}
	var rows []userScoreWithName
	if err := tx.SelectContext(ctx, &rows, query); err != nil {
		return 1
	}
	var ranking UserRanking
	for _, r := range rows {
		ranking = append(ranking, UserRankingEntry{Username: r.UserName, Score: r.Score})
	}
	sort.Sort(ranking)
	var rank int64 = 1
	for i := len(ranking) - 1; i >= 0; i-- {
		if ranking[i].Username == username {
			break
		}
		rank++
	}
	return rank
}

type LivestreamStatistics struct {
	Rank           int64 `json:"rank"`
	ViewersCount   int64 `json:"viewers_count"`
	TotalReactions int64 `json:"total_reactions"`
	TotalReports   int64 `json:"total_reports"`
	MaxTip         int64 `json:"max_tip"`
}

type LivestreamRankingEntry struct {
	LivestreamID int64
	Score        int64
}
type LivestreamRanking []LivestreamRankingEntry

func (r LivestreamRanking) Len() int      { return len(r) }
func (r LivestreamRanking) Swap(i, j int) { r[i], r[j] = r[j], r[i] }
func (r LivestreamRanking) Less(i, j int) bool {
	if r[i].Score == r[j].Score {
		return r[i].LivestreamID < r[j].LivestreamID
	} else {
		return r[i].Score < r[j].Score
	}
}

type UserStatistics struct {
	Rank              int64  `json:"rank"`
	ViewersCount      int64  `json:"viewers_count"`
	TotalReactions    int64  `json:"total_reactions"`
	TotalLivecomments int64  `json:"total_livecomments"`
	TotalTip          int64  `json:"total_tip"`
	FavoriteEmoji     string `json:"favorite_emoji"`
}

type UserRankingEntry struct {
	Username string
	Score    int64
}
type UserRanking []UserRankingEntry

func (r UserRanking) Len() int      { return len(r) }
func (r UserRanking) Swap(i, j int) { r[i], r[j] = r[j], r[i] }
func (r UserRanking) Less(i, j int) bool {
	if r[i].Score == r[j].Score {
		return r[i].Username < r[j].Username
	} else {
		return r[i].Score < r[j].Score
	}
}

var (
	livestreamRankingSF singleflight.Group
)

func getUserStatisticsHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	username := c.Param("username")
	// ユーザごとに、紐づく配信について、累計リアクション数、累計ライブコメント数、累計売上金額を算出
	// また、現在の合計視聴者数もだす

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	var user UserModel
	if err := tx.GetContext(ctx, &user, "SELECT * FROM users WHERE name = ?", username); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusBadRequest, "not found user that has the given username")
		} else {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
		}
	}

	// ランク算出: オンメモリキャッシュから全ユーザーのスコアを取得
	allScores := getAllUserScores()
	var rank int64
	// 対象ユーザーがキャッシュにない場合（initialize未呼び出し等）はDBからランキングを取得
	if _, ok := allScores[user.ID]; !ok {
		rank = getUserRankFromDB(ctx, tx, username)
	} else {
		userIDs := make([]int64, 0, len(allScores))
		for uid := range allScores {
			userIDs = append(userIDs, uid)
		}
		userModels, err := getUserModelsFromCacheOrDB(ctx, tx, userIDs)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user models for ranking: "+err.Error())
		}
		userNameMap := make(map[int64]string, len(userModels))
		for _, u := range userModels {
			userNameMap[u.ID] = u.Name
		}

		var ranking UserRanking
		for uid, score := range allScores {
			name := userNameMap[uid]
			ranking = append(ranking, UserRankingEntry{
				Username: name,
				Score:    score,
			})
		}
		sort.Sort(ranking)

		rank = 1
		for i := len(ranking) - 1; i >= 0; i-- {
			entry := ranking[i]
			if entry.Username == username {
				break
			}
			rank++
		}
	}

	// 対象ユーザーの統計を一括取得
	var userStats struct {
		TotalReactions    int64 `db:"total_reactions"`
		TotalLivecomments int64 `db:"total_livecomments"`
		TotalTip          int64 `db:"total_tip"`
		ViewersCount      int64 `db:"viewers_count"`
	}
	statsQuery := `
		SELECT
			IFNULL(SUM(r.reaction_count), 0) AS total_reactions,
			IFNULL(SUM(lc.livecomment_count), 0) AS total_livecomments,
			IFNULL(SUM(lc.tip_sum), 0) AS total_tip,
			IFNULL(SUM(v.viewers_count), 0) AS viewers_count
		FROM livestreams l
		LEFT JOIN (
			SELECT livestream_id, COUNT(*) AS reaction_count
			FROM reactions
			GROUP BY livestream_id
		) r ON r.livestream_id = l.id
		LEFT JOIN (
			SELECT livestream_id, COUNT(*) AS livecomment_count, SUM(tip) AS tip_sum
			FROM livecomments
			GROUP BY livestream_id
		) lc ON lc.livestream_id = l.id
		LEFT JOIN (
			SELECT livestream_id, COUNT(*) AS viewers_count
			FROM livestream_viewers_history
			GROUP BY livestream_id
		) v ON v.livestream_id = l.id
		WHERE l.user_id = ?
	`
	if err := tx.GetContext(ctx, &userStats, statsQuery, user.ID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user stats: "+err.Error())
	}

	// お気に入り絵文字
	var favoriteEmoji string
	query := `
	SELECT r.emoji_name
	FROM users u
	INNER JOIN livestreams l ON l.user_id = u.id
	INNER JOIN reactions r ON r.livestream_id = l.id
	WHERE u.name = ?
	GROUP BY emoji_name
	ORDER BY COUNT(*) DESC, emoji_name DESC
	LIMIT 1
	`
	if err := tx.GetContext(ctx, &favoriteEmoji, query, username); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to find favorite emoji: "+err.Error())
	}

	stats := UserStatistics{
		Rank:              rank,
		ViewersCount:      userStats.ViewersCount,
		TotalReactions:    userStats.TotalReactions,
		TotalLivecomments: userStats.TotalLivecomments,
		TotalTip:          userStats.TotalTip,
		FavoriteEmoji:     favoriteEmoji,
	}
	return c.JSON(http.StatusOK, stats)
}

// LivestreamScoreEntry はライブストリームごとのスコア集計用
type LivestreamScoreEntry struct {
	LivestreamID int64 `db:"livestream_id"`
	Score        int64 `db:"score"`
}

func getLivestreamStatisticsHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		return err
	}

	id, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}
	livestreamID := int64(id)

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	var livestream LivestreamModel
	if err := tx.GetContext(ctx, &livestream, "SELECT * FROM livestreams WHERE id = ?", livestreamID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusBadRequest, "cannot get stats of not found livestream")
		} else {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream: "+err.Error())
		}
	}

	// ランク算出: 全ライブストリームのスコアを singleflight で一括取得（同時リクエストで共有）
	rankingResult, err, _ := livestreamRankingSF.Do("ranking", func() (interface{}, error) {
		var scores []LivestreamScoreEntry
		rankQuery := `
			SELECT
				l.id AS livestream_id,
				IFNULL(r.reaction_count, 0) + IFNULL(lc.tip_sum, 0) AS score
			FROM livestreams l
			LEFT JOIN (
				SELECT livestream_id, COUNT(*) AS reaction_count
				FROM reactions
				GROUP BY livestream_id
			) r ON r.livestream_id = l.id
			LEFT JOIN (
				SELECT livestream_id, SUM(tip) AS tip_sum
				FROM livecomments
				GROUP BY livestream_id
			) lc ON lc.livestream_id = l.id
		`
		if err := dbConn.SelectContext(ctx, &scores, rankQuery); err != nil {
			return nil, err
		}

		var ranking LivestreamRanking
		for _, ls := range scores {
			ranking = append(ranking, LivestreamRankingEntry{
				LivestreamID: ls.LivestreamID,
				Score:        ls.Score,
			})
		}
		sort.Sort(ranking)
		return ranking, nil
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream scores: "+err.Error())
	}
	ranking := rankingResult.(LivestreamRanking)

	var rank int64 = 1
	for i := len(ranking) - 1; i >= 0; i-- {
		entry := ranking[i]
		if entry.LivestreamID == livestreamID {
			break
		}
		rank++
	}

	// 対象ライブストリームの統計を一括取得
	var livestreamStats struct {
		ViewersCount   int64 `db:"viewers_count"`
		MaxTip         int64 `db:"max_tip"`
		TotalReactions int64 `db:"total_reactions"`
		TotalReports   int64 `db:"total_reports"`
	}
	statsQuery := `
		SELECT
			IFNULL(v.viewers_count, 0) AS viewers_count,
			IFNULL(lc.max_tip, 0) AS max_tip,
			IFNULL(r.reaction_count, 0) AS total_reactions,
			IFNULL(rep.report_count, 0) AS total_reports
		FROM livestreams l
		LEFT JOIN (
			SELECT livestream_id, COUNT(*) AS viewers_count
			FROM livestream_viewers_history
			GROUP BY livestream_id
		) v ON v.livestream_id = l.id
		LEFT JOIN (
			SELECT livestream_id, MAX(tip) AS max_tip
			FROM livecomments
			GROUP BY livestream_id
		) lc ON lc.livestream_id = l.id
		LEFT JOIN (
			SELECT livestream_id, COUNT(*) AS reaction_count
			FROM reactions
			GROUP BY livestream_id
		) r ON r.livestream_id = l.id
		LEFT JOIN (
			SELECT livestream_id, COUNT(*) AS report_count
			FROM livecomment_reports
			GROUP BY livestream_id
		) rep ON rep.livestream_id = l.id
		WHERE l.id = ?
	`
	if err := tx.GetContext(ctx, &livestreamStats, statsQuery, livestreamID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream stats: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, LivestreamStatistics{
		Rank:           rank,
		ViewersCount:   livestreamStats.ViewersCount,
		MaxTip:         livestreamStats.MaxTip,
		TotalReactions: livestreamStats.TotalReactions,
		TotalReports:   livestreamStats.TotalReports,
	})
}
