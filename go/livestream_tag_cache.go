package main

import (
	"context"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
)

const livestreamTagCacheTTL = 60 * time.Second

type livestreamTagCacheEntry struct {
	tags     []Tag
	expireAt time.Time
}

type livestreamTagCache struct {
	mu      sync.RWMutex
	ttl     time.Duration
	nowFunc func() time.Time
	entries map[int64]livestreamTagCacheEntry
}

func newLivestreamTagCache(ttl time.Duration) *livestreamTagCache {
	return &livestreamTagCache{
		ttl:     ttl,
		nowFunc: time.Now,
		entries: make(map[int64]livestreamTagCacheEntry),
	}
}

func (c *livestreamTagCache) get(livestreamID int64) ([]Tag, bool) {
	now := c.nowFunc()

	c.mu.RLock()
	entry, ok := c.entries[livestreamID]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if !entry.expireAt.After(now) {
		c.mu.Lock()
		entry, ok = c.entries[livestreamID]
		if ok && !entry.expireAt.After(c.nowFunc()) {
			delete(c.entries, livestreamID)
		}
		c.mu.Unlock()
		return nil, false
	}
	return cloneTags(entry.tags), true
}

func (c *livestreamTagCache) set(livestreamID int64, tags []Tag) {
	c.mu.Lock()
	c.entries[livestreamID] = livestreamTagCacheEntry{
		tags:     cloneTags(tags),
		expireAt: c.nowFunc().Add(c.ttl),
	}
	c.mu.Unlock()
}

func (c *livestreamTagCache) invalidate(livestreamID int64) {
	c.mu.Lock()
	delete(c.entries, livestreamID)
	c.mu.Unlock()
}

func (c *livestreamTagCache) clear() {
	c.mu.Lock()
	c.entries = make(map[int64]livestreamTagCacheEntry)
	c.mu.Unlock()
}

func cloneTags(tags []Tag) []Tag {
	if len(tags) == 0 {
		return []Tag{}
	}
	copied := make([]Tag, len(tags))
	copy(copied, tags)
	return copied
}

var livestreamTagsCache = newLivestreamTagCache(livestreamTagCacheTTL)

func getLivestreamTagsMap(ctx context.Context, tx *sqlx.Tx, livestreamIDs []int64) (map[int64][]Tag, error) {
	result := make(map[int64][]Tag, len(livestreamIDs))
	missingIDs := make([]int64, 0, len(livestreamIDs))

	for _, livestreamID := range livestreamIDs {
		if tags, ok := livestreamTagsCache.get(livestreamID); ok {
			result[livestreamID] = tags
			continue
		}
		missingIDs = append(missingIDs, livestreamID)
	}

	if len(missingIDs) == 0 {
		return result, nil
	}

	dbTagsMap, err := fetchLivestreamTagsFromDB(ctx, tx, missingIDs)
	if err != nil {
		return nil, err
	}

	for _, livestreamID := range missingIDs {
		tags := dbTagsMap[livestreamID]
		if tags == nil {
			tags = []Tag{}
		}
		result[livestreamID] = tags
		livestreamTagsCache.set(livestreamID, tags)
	}

	return result, nil
}

func fetchLivestreamTagsFromDB(ctx context.Context, tx *sqlx.Tx, livestreamIDs []int64) (map[int64][]Tag, error) {
	query, args, err := sqlx.In("SELECT * FROM livestream_tags WHERE livestream_id IN (?)", livestreamIDs)
	if err != nil {
		return nil, err
	}
	var allTagModels []LivestreamTagModel
	if err := tx.SelectContext(ctx, &allTagModels, query, args...); err != nil {
		return nil, err
	}

	tagIDSet := make(map[int64]struct{})
	for _, t := range allTagModels {
		tagIDSet[t.TagID] = struct{}{}
	}

	tagMap := make(map[int64]TagModel)
	if len(tagIDSet) > 0 {
		tagIDs := make([]int64, 0, len(tagIDSet))
		for id := range tagIDSet {
			tagIDs = append(tagIDs, id)
		}
		query, args, err = sqlx.In("SELECT * FROM tags WHERE id IN (?)", tagIDs)
		if err != nil {
			return nil, err
		}
		var tagModels []TagModel
		if err := tx.SelectContext(ctx, &tagModels, query, args...); err != nil {
			return nil, err
		}
		for _, t := range tagModels {
			tagMap[t.ID] = t
		}
	}

	livestreamTagsMap := make(map[int64][]Tag)
	for _, lt := range allTagModels {
		tagModel, ok := tagMap[lt.TagID]
		if !ok {
			continue
		}
		livestreamTagsMap[lt.LivestreamID] = append(livestreamTagsMap[lt.LivestreamID], Tag{ID: tagModel.ID, Name: tagModel.Name})
	}

	return livestreamTagsMap, nil
}
