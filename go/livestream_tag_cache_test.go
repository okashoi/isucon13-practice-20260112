package main

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
)

func TestLivestreamTagCache_TTL(t *testing.T) {
	now := time.Date(2026, 2, 14, 12, 0, 0, 0, time.UTC)
	cache := newLivestreamTagCache(60 * time.Second)
	cache.nowFunc = func() time.Time {
		return now
	}

	cache.set(10, []Tag{{ID: 1, Name: "music"}})

	tags, ok := cache.get(10)
	if !ok {
		t.Fatalf("expected cache hit")
	}
	if len(tags) != 1 || tags[0].ID != 1 {
		t.Fatalf("unexpected tags: %+v", tags)
	}

	now = now.Add(59 * time.Second)
	if _, ok := cache.get(10); !ok {
		t.Fatalf("expected cache hit before ttl")
	}

	now = now.Add(2 * time.Second)
	if _, ok := cache.get(10); ok {
		t.Fatalf("expected cache miss after ttl")
	}
}

func TestLivestreamTagCache_UpdateAndInvalidate(t *testing.T) {
	now := time.Date(2026, 2, 14, 12, 0, 0, 0, time.UTC)
	cache := newLivestreamTagCache(60 * time.Second)
	cache.nowFunc = func() time.Time {
		return now
	}

	cache.set(20, []Tag{{ID: 1, Name: "old"}})
	cache.set(20, []Tag{{ID: 2, Name: "new"}})

	tags, ok := cache.get(20)
	if !ok {
		t.Fatalf("expected cache hit")
	}
	if len(tags) != 1 || tags[0].ID != 2 || tags[0].Name != "new" {
		t.Fatalf("unexpected tags after update: %+v", tags)
	}

	cache.invalidate(20)
	if _, ok := cache.get(20); ok {
		t.Fatalf("expected cache miss after invalidate")
	}
}

func TestLivestreamTagCache_ConcurrentAccess(t *testing.T) {
	cache := newLivestreamTagCache(60 * time.Second)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		id := int64(i % 8)
		wg.Add(1)
		go func(base int64) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				cache.set(base, []Tag{{ID: base + int64(j%3), Name: "tag"}})
				cache.get(base)
				if j%25 == 0 {
					cache.invalidate(base)
				}
			}
		}(id)
	}
	wg.Wait()
}

func TestLivestreamTagCache_CloneTags(t *testing.T) {
	cache := newLivestreamTagCache(60 * time.Second)
	input := []Tag{{ID: 1, Name: "original"}}
	cache.set(30, input)
	input[0].Name = "mutated"

	tags, ok := cache.get(30)
	if !ok {
		t.Fatalf("expected cache hit")
	}
	if tags[0].Name != "original" {
		t.Fatalf("expected immutable cached value, got: %+v", tags)
	}
}

func TestGetLivestreamTagsMap_CacheMissThenHit(t *testing.T) {
	livestreamTagsCache.clear()
	t.Cleanup(livestreamTagsCache.clear)

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	mock.ExpectBegin()

	sqlxDB := sqlx.NewDb(mockDB, "sqlmock")
	tx, err := sqlxDB.BeginTxx(context.Background(), nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}
	defer tx.Rollback()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM livestream_tags WHERE livestream_id IN (?)")).
		WithArgs(int64(10)).
		WillReturnRows(
			sqlmock.NewRows([]string{"id", "livestream_id", "tag_id"}).
				AddRow(1, 10, 1),
		)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM tags WHERE id IN (?)")).
		WithArgs(int64(1)).
		WillReturnRows(
			sqlmock.NewRows([]string{"id", "name"}).
				AddRow(1, "music"),
		)

	first, err := getLivestreamTagsMap(context.Background(), tx, []int64{10})
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if len(first[10]) != 1 || first[10][0].ID != 1 || first[10][0].Name != "music" {
		t.Fatalf("unexpected first result: %+v", first[10])
	}

	second, err := getLivestreamTagsMap(context.Background(), tx, []int64{10})
	if err != nil {
		t.Fatalf("second call failed: %v", err)
	}
	if len(second[10]) != 1 || second[10][0].ID != 1 || second[10][0].Name != "music" {
		t.Fatalf("unexpected second result: %+v", second[10])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected sql execution: %v", err)
	}
}

func TestGetLivestreamTagsMap_DBError(t *testing.T) {
	livestreamTagsCache.clear()
	t.Cleanup(livestreamTagsCache.clear)

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	mock.ExpectBegin()

	sqlxDB := sqlx.NewDb(mockDB, "sqlmock")
	tx, err := sqlxDB.BeginTxx(context.Background(), nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}
	defer tx.Rollback()

	dbErr := errors.New("db select failed")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM livestream_tags WHERE livestream_id IN (?)")).
		WithArgs(int64(20)).
		WillReturnError(dbErr)

	_, err = getLivestreamTagsMap(context.Background(), tx, []int64{20})
	if !errors.Is(err, dbErr) {
		t.Fatalf("expected db error, got: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected sql execution: %v", err)
	}
}
