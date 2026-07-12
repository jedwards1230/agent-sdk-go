package session

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// journalCache is an in-memory cache of hot journals keyed by session id,
// used by [FileStore] so repeated Opens of the same id return the identical
// *Journal for as long as it stays open. There is no background goroutine:
// reaping of stale (already-closed) entries is lazy, evaluated on access
// against an injected clock.
//
// The cache never closes a journal except in closeAll (used by
// [FileStore.Close]): a journal with an open write handle is pinned and never
// evicted while open, however long it has sat idle. Evicting (and closing) a
// still-live journal would pull its append handle out from under whoever
// holds it, and rebuilding a second journal for the same id from disk would
// silently fork the tree — both are correctness bugs, not just cache
// behavior. This means there is at most one live *Journal per id at any
// time. Once a journal's owner calls [Journal.Close] directly, its cache
// entry is no longer usable and is dropped on the next access.
//
// Lock order is always cache.mu → journal.mu (the cache calls into the
// journal via isOpen/Close, never the reverse), so there is no deadlock risk.
type journalCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	clock func() time.Time
	items map[string]*cacheEntry
}

// cacheEntry pairs a cached journal with its last access time.
type cacheEntry struct {
	journal    *Journal
	lastAccess time.Time
}

// newJournalCache constructs a cache with the given ttl and clock. ttl bounds
// how long an already-closed journal's now-unusable cache entry lingers
// before [journalCache.put] reaps it for memory; it has no effect on a live
// (open) journal, which stays cached for as long as it remains open
// regardless of ttl — caching live journals is never "disabled". A
// non-positive ttl reaps closed entries immediately.
func newJournalCache(ttl time.Duration, clock func() time.Time) *journalCache {
	if clock == nil {
		clock = time.Now
	}
	return &journalCache{
		ttl:   ttl,
		clock: clock,
		items: make(map[string]*cacheEntry),
	}
}

// get returns the cached journal for id, touching its last-access time. A
// journal with an open write handle is always a hit, however long it has sat
// idle past ttl — a live journal is never TTL-evicted (see the type doc for
// why). A cached entry whose journal has already been closed by its holder
// is no longer usable: get drops it and reports a miss so the caller (see
// [FileStore.Open]) rebuilds a fresh journal from disk. get never calls
// [Journal.Close]; closeAll is the cache's only close path.
func (c *journalCache) get(id string) (*Journal, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.items[id]
	if !ok {
		return nil, false
	}
	if !e.journal.isOpen() {
		delete(c.items, id)
		return nil, false
	}

	e.lastAccess = c.clock()
	return e.journal, true
}

// put caches j under id, resetting its last-access time. If a different
// journal was already cached under id, the stale entry is overwritten
// WITHOUT closing it (put never calls [Journal.Close]) — this should not
// arise in practice, since single-flight [FileStore.Open] plus get's
// drop-if-closed rule mean put never supersedes a still-live journal. put
// also opportunistically reaps other cached entries whose journal has
// already been closed, bounding cache memory without ever closing anything
// itself: a non-positive ttl reaps them immediately, otherwise only once
// idle past ttl.
func (c *journalCache) put(id string, j *Journal) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.clock()
	for cid, e := range c.items {
		if cid == id {
			continue
		}
		if !e.journal.isOpen() && (c.ttl <= 0 || now.Sub(e.lastAccess) > c.ttl) {
			delete(c.items, cid)
		}
	}

	c.items[id] = &cacheEntry{journal: j, lastAccess: now}
}

// closeAll closes every cached journal and empties the cache, joining any
// individual Close errors.
func (c *journalCache) closeAll() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var errs []error
	for id, e := range c.items {
		if err := e.journal.Close(); err != nil {
			errs = append(errs, fmt.Errorf("session: close journal %s: %w", id, err))
		}
	}
	c.items = make(map[string]*cacheEntry)
	return errors.Join(errs...)
}
