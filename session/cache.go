package session

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// journalCache is an in-memory, TTL-bounded cache of hot journals keyed by
// session id, used by [FileStore] so repeated Opens of the same id within
// TTL return the identical *Journal. There is no background goroutine:
// expiry is lazy, evaluated on access against an injected clock.
type journalCache struct {
	mu     sync.Mutex
	ttl    time.Duration
	clock  func() time.Time
	logger func(string, ...any)
	items  map[string]*cacheEntry
}

// cacheEntry pairs a cached journal with its last access time.
type cacheEntry struct {
	journal    *Journal
	lastAccess time.Time
}

// newJournalCache constructs a cache with the given ttl and clock. A
// non-positive ttl disables caching (every get misses).
func newJournalCache(ttl time.Duration, clock func() time.Time, logger func(string, ...any)) *journalCache {
	if clock == nil {
		clock = time.Now
	}
	if logger == nil {
		logger = func(string, ...any) {}
	}
	return &journalCache{
		ttl:    ttl,
		clock:  clock,
		logger: logger,
		items:  make(map[string]*cacheEntry),
	}
}

// get returns the cached journal for id if present and within ttl, touching
// its last-access time. A miss (absent or expired) returns (nil, false); an
// expired entry is evicted and its journal closed (best-effort).
func (c *journalCache) get(id string) (*Journal, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.items[id]
	if !ok {
		return nil, false
	}
	if c.ttl <= 0 || c.clock().Sub(e.lastAccess) > c.ttl {
		delete(c.items, id)
		if err := e.journal.Close(); err != nil {
			c.logger("session: close evicted journal %s: %v", id, err)
		}
		return nil, false
	}

	e.lastAccess = c.clock()
	return e.journal, true
}

// put caches j under id, resetting its last-access time. If a different
// journal was already cached under id, it is closed first (best-effort).
func (c *journalCache) put(id string, j *Journal) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if old, ok := c.items[id]; ok && old.journal != j {
		if err := old.journal.Close(); err != nil {
			c.logger("session: close superseded journal %s: %v", id, err)
		}
	}
	c.items[id] = &cacheEntry{journal: j, lastAccess: c.clock()}
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
