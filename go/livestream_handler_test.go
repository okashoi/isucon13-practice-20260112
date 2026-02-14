package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gorilla/sessions"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

func resetTestCaches() {
	livestreamTagsCache.clear()
	iconHashCacheMu.Lock()
	iconHashCache = make(map[int64]string)
	iconHashCacheMu.Unlock()
}

func TestSearchLivestreamsHandler_WithTagUsesDBFilterAndReturnsTaggedLivestreams(t *testing.T) {
	resetTestCaches()

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	originalDBConn := dbConn
	dbConn = sqlx.NewDb(mockDB, "sqlmock")
	defer func() {
		dbConn = originalDBConn
	}()

	setIconHash(1, "icon-1")
	setIconHash(2, "icon-2")
	livestreamTagsCache.set(102, []Tag{{ID: 1, Name: "music"}, {ID: 2, Name: "game"}})
	livestreamTagsCache.set(101, []Tag{{ID: 1, Name: "music"}})

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM tags WHERE name = ?")).
		WithArgs("music").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(`SELECT DISTINCT l\.\* FROM livestreams l`).
		WithArgs(int64(1)).
		WillReturnRows(
			sqlmock.NewRows([]string{"id", "user_id", "title", "description", "playlist_url", "thumbnail_url", "start_at", "end_at"}).
				AddRow(102, 2, "second", "desc2", "p2", "t2", 10, 20).
				AddRow(101, 1, "first", "desc1", "p1", "t1", 5, 15),
		)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM users WHERE id IN (?, ?)")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(
			sqlmock.NewRows([]string{"id", "name", "display_name", "description", "password"}).
				AddRow(2, "u2", "U2", "d2", "x").
				AddRow(1, "u1", "U1", "d1", "x"),
		)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM themes WHERE user_id IN (?, ?)")).
		WithArgs(int64(2), int64(1)).
		WillReturnRows(
			sqlmock.NewRows([]string{"id", "user_id", "dark_mode"}).
				AddRow(20, 2, false).
				AddRow(10, 1, true),
		)
	mock.ExpectCommit()

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/livestream/search?tag=music", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := searchLivestreamsHandler(c); err != nil {
		t.Fatalf("handler failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status code: %d body=%s", rec.Code, rec.Body.String())
	}

	var got []Livestream
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("unexpected livestream count: %d", len(got))
	}
	if got[0].ID != 102 || got[1].ID != 101 {
		t.Fatalf("unexpected order: %+v", got)
	}
	for _, livestream := range got {
		hasMusic := false
		for _, tag := range livestream.Tags {
			if tag.ID == 1 && tag.Name == "music" {
				hasMusic = true
				break
			}
		}
		if !hasMusic {
			t.Fatalf("livestream does not include requested tag: %+v", livestream)
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestReserveLivestreamHandler_InvalidatesTagCacheAfterCommit(t *testing.T) {
	resetTestCaches()

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	originalDBConn := dbConn
	dbConn = sqlx.NewDb(mockDB, "sqlmock")
	defer func() {
		dbConn = originalDBConn
	}()

	setIconHash(1, "icon-1")
	livestreamTagsCache.set(200, []Tag{{ID: 99, Name: "stale"}})

	startAt := time.Date(2023, 11, 25, 2, 0, 0, 0, time.UTC).Unix()
	endAt := startAt + 3600

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM reservation_slots WHERE start_at >= ? AND end_at <= ? FOR UPDATE")).
		WithArgs(startAt, endAt).
		WillReturnRows(
			sqlmock.NewRows([]string{"id", "slot", "start_at", "end_at"}).
				AddRow(1, 1, startAt, endAt),
		)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT slot FROM reservation_slots WHERE start_at = ? AND end_at = ?")).
		WithArgs(startAt, endAt).
		WillReturnRows(sqlmock.NewRows([]string{"slot"}).AddRow(1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE reservation_slots SET slot = slot - 1 WHERE start_at >= ? AND end_at <= ?")).
		WithArgs(startAt, endAt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO livestreams (user_id, title, description, playlist_url, thumbnail_url, start_at, end_at) VALUES(?, ?, ?, ?, ?, ?, ?)")).
		WithArgs(int64(1), "title", "desc", "playlist", "thumb", startAt, endAt).
		WillReturnResult(sqlmock.NewResult(200, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO livestream_tags (livestream_id, tag_id) VALUES (?, ?)")).
		WithArgs(int64(200), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM users WHERE id IN (?)")).
		WithArgs(int64(1)).
		WillReturnRows(
			sqlmock.NewRows([]string{"id", "name", "display_name", "description", "password"}).
				AddRow(1, "u1", "U1", "d1", "x"),
		)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM themes WHERE user_id IN (?)")).
		WithArgs(int64(1)).
		WillReturnRows(
			sqlmock.NewRows([]string{"id", "user_id", "dark_mode"}).
				AddRow(10, 1, false),
		)
	mock.ExpectCommit()

	body := bytes.NewBufferString(fmt.Sprintf(
		`{"tags":[1],"title":"title","description":"desc","playlist_url":"playlist","thumbnail_url":"thumb","start_at":%d,"end_at":%d}`,
		startAt,
		endAt,
	))
	req := httptest.NewRequest(http.MethodPost, "/api/livestream/reservation", body)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	e := echo.New()
	c := e.NewContext(req, rec)

	store := sessions.NewCookieStore(secret)
	handler := session.Middleware(store)(func(c echo.Context) error {
		sess, err := session.Get(defaultSessionIDKey, c)
		if err != nil {
			return err
		}
		sess.Values[defaultUserIDKey] = int64(1)
		sess.Values[defaultSessionExpiresKey] = time.Now().Add(time.Hour).Unix()
		return reserveLivestreamHandler(c)
	})

	if err := handler(c); err != nil {
		t.Fatalf("handler failed: %v", err)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("unexpected status code: %d body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := livestreamTagsCache.get(200); ok {
		t.Fatalf("expected cache to be invalidated for livestream 200")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestInitializeHandler_ClearsLivestreamTagCache(t *testing.T) {
	resetTestCaches()
	livestreamTagsCache.set(300, []Tag{{ID: 1, Name: "music"}})

	originalRunInitScript := runInitScript
	originalTriggerCollect := triggerPProteinCollect
	originalIconCacheDir := iconCacheDir
	runInitScript = func() ([]byte, error) {
		return []byte("ok"), nil
	}
	triggerPProteinCollect = func() {}
	iconCacheDir = t.TempDir()
	defer func() {
		runInitScript = originalRunInitScript
		triggerPProteinCollect = originalTriggerCollect
		iconCacheDir = originalIconCacheDir
	}()

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/initialize", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := initializeHandler(c); err != nil {
		t.Fatalf("handler failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status code: %d body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := livestreamTagsCache.get(300); ok {
		t.Fatalf("expected cache to be cleared")
	}
}
