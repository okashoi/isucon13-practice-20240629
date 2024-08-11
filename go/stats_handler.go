package main

import (
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"sync"

	"github.com/labstack/echo/v4"
)

var (
	scoreByLivestreamID sync.Map
)

func addScoreByLivestreamID(livestreamID, score int64) {
	currentScore := getScoreByLivestreamID(livestreamID)
	scoreByLivestreamID.Store(livestreamID, currentScore+score)
}

func getScoreByLivestreamID(livestreamID int64) int64 {
	scoreAny, ok := scoreByLivestreamID.Load(livestreamID)
	if !ok {
		return 0
	}

	return scoreAny.(int64)
}

func InitScoreCache(c echo.Context) error {
	ctx := c.Request().Context()
	scoreByLivestreamID = sync.Map{}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	var result1 []*struct {
		LivestreamID int64 `db:"livestream_id"`
		TotalTip     int64 `db:"total_tip"`
	}
	if err := tx.SelectContext(ctx, &result1, "SELECT ls.id as livestream_id, IFNULL(SUM(lc.tip), 0) as total_tip FROM livestreams ls INNER JOIN livecomments lc ON ls.id = lc.livestream_id GROUP BY ls.id"); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get total tip: "+err.Error())
	}
	for _, res := range result1 {
		addScoreByLivestreamID(res.LivestreamID, res.TotalTip)
	}

	var result2 []*struct {
		LivestreamID  int64 `db:"livestream_id"`
		ReactionCount int64 `db:"reaction_count"`
	}
	if err := tx.SelectContext(ctx, &result2, "SELECT ls.id as livestream_id, COUNT(*) as reaction_count FROM livestreams ls INNER JOIN reactions r ON ls.id = r.livestream_id GROUP BY ls.id"); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get reaction count: "+err.Error())
	}
	for _, res := range result2 {
		addScoreByLivestreamID(res.LivestreamID, res.ReactionCount)
	}

	if err = tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return nil
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

	var targetUser UserModel
	if err := tx.GetContext(ctx, &targetUser, "SELECT * FROM users WHERE name = ?", username); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusBadRequest, "not found user that has the given username")
		} else {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
		}
	}

	// ランク算出
	userScore := make(map[int64]int64)
	var result1 []*struct {
		UserID        int64 `db:"user_id"`
		ReactionCount int64 `db:"reactions_count"`
	}
	if err := tx.SelectContext(ctx, &result1, "SELECT ls.user_id as user_id, COUNT(*) as reactions_count FROM livestreams ls INNER JOIN reactions r ON ls.id = r.livestream_id GROUP BY ls.user_id"); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count reactions: "+err.Error())
	}
	for _, res := range result1 {
		userScore[res.UserID] = res.ReactionCount
	}

	var result2 []*struct {
		UserID   int64 `db:"user_id"`
		TotalTip int64 `db:"total_tip"`
	}
	if err := tx.SelectContext(ctx, &result2, "SELECT ls.user_id as user_id, IFNULL(SUM(lc.tip), 0) as total_tip FROM livestreams ls INNER JOIN livecomments lc ON ls.id = lc.livestream_id GROUP BY ls.user_id"); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count tips: "+err.Error())
	}
	for _, res := range result2 {
		userScore[res.UserID] += res.TotalTip
	}

	var users []*UserModel
	if err := tx.SelectContext(ctx, &users, "SELECT * FROM users"); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get users: "+err.Error())
	}

	var ranking UserRanking
	for _, user := range users {
		ranking = append(ranking, UserRankingEntry{
			Username: user.Name,
			Score:    userScore[user.ID],
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

	// リアクション数
	var totalReactions int64
	query := `SELECT COUNT(*) FROM users u 
    INNER JOIN livestreams l ON l.user_id = u.id 
    INNER JOIN reactions r ON r.livestream_id = l.id
    WHERE u.name = ?
	`
	if err := tx.GetContext(ctx, &totalReactions, query, username); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count total reactions: "+err.Error())
	}

	// ライブコメント数、チップ合計
	var result3 struct {
		TotalLivecomments int64 `db:"total_livecomments"`
		TotalTip          int64 `db:"total_tips"`
	}
	if err := tx.GetContext(ctx, &result3, "SELECT COUNT(*) as total_livecomments, SUM(livecomments.tip) as total_tips FROM livecomments INNER JOIN livestreams ON livecomments.livestream_id = livestreams.id WHERE livestreams.user_id = ?", targetUser.ID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestreams: "+err.Error())
	}
	totalTip := result3.TotalTip
	totalLivecomments := result3.TotalLivecomments

	// 合計視聴者数
	var viewersCount int64
	if err := tx.GetContext(ctx, &viewersCount, "SELECT COUNT(*) FROM livestream_viewers_history lvh INNER JOIN livestreams l ON lvh.livestream_id = l.id WHERE l.user_id = ?", targetUser.ID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream_view_history: "+err.Error())
	}

	// お気に入り絵文字
	var favoriteEmoji string
	query = `
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
		ViewersCount:      viewersCount,
		TotalReactions:    totalReactions,
		TotalLivecomments: totalLivecomments,
		TotalTip:          totalTip,
		FavoriteEmoji:     favoriteEmoji,
	}
	return c.JSON(http.StatusOK, stats)
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

	_, exists := getLivestreamModelsCache(livestreamID)
	if !exists {
		return echo.NewHTTPError(http.StatusBadRequest, "cannot get stats of not found livestream")
	}

	var livestreams []*LivestreamModel
	if err := tx.SelectContext(ctx, &livestreams, "SELECT * FROM livestreams"); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestreams: "+err.Error())
	}

	// ランク算出
	var ranking LivestreamRanking
	for _, livestream := range livestreams {
		score := getScoreByLivestreamID(livestream.ID)
		ranking = append(ranking, LivestreamRankingEntry{
			LivestreamID: livestream.ID,
			Score:        score,
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

	// 視聴者数算出
	var viewersCount int64
	if err := tx.GetContext(ctx, &viewersCount, `SELECT COUNT(*) FROM livestreams l INNER JOIN livestream_viewers_history h ON h.livestream_id = l.id WHERE l.id = ?`, livestreamID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count livestream viewers: "+err.Error())
	}

	// 最大チップ額
	var maxTip int64
	if err := tx.GetContext(ctx, &maxTip, `SELECT IFNULL(MAX(tip), 0) FROM livestreams l INNER JOIN livecomments l2 ON l2.livestream_id = l.id WHERE l.id = ?`, livestreamID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to find maximum tip livecomment: "+err.Error())
	}

	// リアクション数
	var totalReactions int64
	if err := tx.GetContext(ctx, &totalReactions, "SELECT COUNT(*) FROM livestreams l INNER JOIN reactions r ON r.livestream_id = l.id WHERE l.id = ?", livestreamID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count total reactions: "+err.Error())
	}

	// スパム報告数
	var totalReports int64
	if err := tx.GetContext(ctx, &totalReports, `SELECT COUNT(*) FROM livestreams l INNER JOIN livecomment_reports r ON r.livestream_id = l.id WHERE l.id = ?`, livestreamID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count total spam reports: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, LivestreamStatistics{
		Rank:           rank,
		ViewersCount:   viewersCount,
		MaxTip:         maxTip,
		TotalReactions: totalReactions,
		TotalReports:   totalReports,
	})
}
