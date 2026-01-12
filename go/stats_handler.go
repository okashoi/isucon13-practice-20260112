package main

import (
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strconv"

	"github.com/labstack/echo/v4"
)

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

// UserScoreEntry はユーザーごとのスコア集計用
type UserScoreEntry struct {
	UserID   int64  `db:"user_id"`
	Username string `db:"username"`
	Score    int64  `db:"score"`
}

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

	// ランク算出: 全ユーザーのスコア（リアクション数 + チップ合計）を一括取得
	var userScores []UserScoreEntry
	rankQuery := `
		SELECT
			u.id AS user_id,
			u.name AS username,
			IFNULL(SUM(r.reaction_count), 0) + IFNULL(SUM(lc.tip_sum), 0) AS score
		FROM users u
		LEFT JOIN livestreams l ON l.user_id = u.id
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
		GROUP BY u.id, u.name
	`
	if err := tx.SelectContext(ctx, &userScores, rankQuery); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user scores: "+err.Error())
	}

	// ランキングをソート
	var ranking UserRanking
	for _, us := range userScores {
		ranking = append(ranking, UserRankingEntry{
			Username: us.Username,
			Score:    us.Score,
		})
	}
	sort.Sort(ranking)

	var rank int64 = 1
	for i := len(ranking) - 1; i >= 0; i-- {
		entry := ranking[i]
		if entry.Username == username {
			break
		}
		rank++
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

	// ランク算出: 全ライブストリームのスコア（リアクション数 + チップ合計）を一括取得
	var livestreamScores []LivestreamScoreEntry
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
	if err := tx.SelectContext(ctx, &livestreamScores, rankQuery); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream scores: "+err.Error())
	}

	// ランキングをソート
	var ranking LivestreamRanking
	for _, ls := range livestreamScores {
		ranking = append(ranking, LivestreamRankingEntry{
			LivestreamID: ls.LivestreamID,
			Score:        ls.Score,
		})
	}
	sort.Sort(ranking)

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
