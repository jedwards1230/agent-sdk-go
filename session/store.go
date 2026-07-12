package session

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// defaultTTL is how long a Create'd or Open'd journal stays hot in the
// store's cache before a subsequent Open reloads it from disk.
const defaultTTL = 5 * time.Minute

// Store persists and retrieves session journals. It is the pluggable seam
// docs/DESIGN.md calls out: JSONL ([FileStore]) is the only implementation
// today, but the interface leaves room for others.
type Store interface {
	// Create starts a new empty journal for projectSlug with a fresh UUIDv7
	// session id, ready for Append.
	Create(ctx context.Context, projectSlug string) (*Journal, error)
	// Open resumes an existing session by id, rebuilding its journal from
	// disk (torn-write safe). It scans every project dir under the store's
	// root for <id>.jsonl.
	Open(ctx context.Context, id string) (*Journal, error)
	// List returns the session ids under projectSlug, newest first (session
	// ids are UUIDv7, so lexical descending order is time-descending order).
	List(ctx context.Context, projectSlug string) ([]string, error)
	// Close releases any resources the store holds (e.g. its journal cache).
	Close() error
}

// ErrInvalidSlug indicates a project slug cannot safely be used as a single
// path component (empty, ".", "..", or containing a path separator). Build a
// safe slug from an arbitrary string (e.g. a project's cwd) with [Slugify].
var ErrInvalidSlug = errors.New("session: invalid project slug")

// ErrSessionNotFound indicates [Store.Open] found no journal for the given
// id under any project directory in the store's root.
var ErrSessionNotFound = errors.New("session: session not found")

// FileStore is a [Store] backed by one append-only JSONL file per session, at
// <root>/sessions/<projectSlug>/<id>.jsonl.
type FileStore struct {
	root   string
	idGen  func() string
	clock  func() time.Time
	logger func(string, ...any)
	cache  *journalCache
}

// storeConfig holds [NewFileStore] options.
type storeConfig struct {
	root   string
	idGen  func() string
	clock  func() time.Time
	logger func(string, ...any)
	ttl    time.Duration
}

// StoreOption configures a [FileStore] at construction.
type StoreOption func(*storeConfig)

// WithRoot sets the store's root directory. Default: "~/.gofer". Tests
// should always pass a [testing.T.TempDir].
func WithRoot(dir string) StoreOption {
	return func(c *storeConfig) {
		if dir != "" {
			c.root = dir
		}
	}
}

// WithStoreClock overrides the clock used to timestamp journal entries and
// drive the store's hot-journal cache TTL. A nil clock is ignored.
//
// Named WithStoreClock, not WithClock, because the package already exports
// [WithClock] as a [Session] [Option] and Go forbids two package-level
// functions sharing a name regardless of differing option types.
func WithStoreClock(f func() time.Time) StoreOption {
	return func(c *storeConfig) {
		if f != nil {
			c.clock = f
		}
	}
}

// WithStoreIDGen overrides the session/entry id generator (default: UUIDv7).
// A nil generator is ignored.
//
// Named WithStoreIDGen, not WithIDGen, for the same reason as
// [WithStoreClock]: the package already exports [WithIDGen] as a [Session]
// [Option].
func WithStoreIDGen(f func() string) StoreOption {
	return func(c *storeConfig) {
		if f != nil {
			c.idGen = f
		}
	}
}

// WithTTL sets how long a hot journal stays cached before Open reloads it
// from disk. Default 5 minutes. A non-positive duration disables caching.
func WithTTL(d time.Duration) StoreOption {
	return func(c *storeConfig) { c.ttl = d }
}

// WithLogger overrides the store's warning logger (used for torn-write
// repair and cache-eviction diagnostics). Default [log.Printf]. A nil logger
// is ignored.
func WithLogger(f func(string, ...any)) StoreOption {
	return func(c *storeConfig) {
		if f != nil {
			c.logger = f
		}
	}
}

// NewFileStore constructs a [FileStore], creating its root/sessions
// directory if needed.
func NewFileStore(opts ...StoreOption) (*FileStore, error) {
	cfg := storeConfig{
		idGen:  newV7,
		clock:  time.Now,
		logger: log.Printf,
		ttl:    defaultTTL,
	}
	for _, o := range opts {
		o(&cfg)
	}

	if cfg.root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("session: resolve default store root: %w", err)
		}
		cfg.root = filepath.Join(home, ".gofer")
	}

	sessionsDir := filepath.Join(cfg.root, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		return nil, fmt.Errorf("session: create store root %s: %w", sessionsDir, err)
	}

	return &FileStore{
		root:   cfg.root,
		idGen:  cfg.idGen,
		clock:  cfg.clock,
		logger: cfg.logger,
		cache:  newJournalCache(cfg.ttl, cfg.clock, cfg.logger),
	}, nil
}

// Slugify turns an arbitrary string (typically an absolute project cwd) into
// a safe, filesystem-friendly project slug: lowercased, runs of
// non-alphanumeric characters collapsed to a single '-', and leading/
// trailing '-' trimmed.
func Slugify(path string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(path) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case !prevDash:
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// validateSlug rejects a project slug that cannot safely be used as a single
// path component under the store root. It does not transform the slug —
// callers building a slug from an arbitrary string (e.g. a cwd) should call
// [Slugify] first.
func validateSlug(slug string) error {
	if slug == "" || slug == "." || slug == ".." || slug != filepath.Base(slug) {
		return fmt.Errorf("session: project slug %q: %w", slug, ErrInvalidSlug)
	}
	return nil
}

// Create starts a new empty journal for projectSlug with a fresh session id.
func (s *FileStore) Create(ctx context.Context, projectSlug string) (*Journal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSlug(projectSlug); err != nil {
		return nil, err
	}

	id := s.idGen()
	dir := filepath.Join(s.root, "sessions", projectSlug)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("session: create project dir %s: %w", dir, err)
	}

	path := filepath.Join(dir, id+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("session: create journal %s: %w", path, err)
	}

	j := newJournal(id, projectSlug, path, nil, f, s.idGen, s.clock)
	s.cache.put(id, j)
	return j, nil
}

// Open resumes an existing session by id.
func (s *FileStore) Open(ctx context.Context, id string) (*Journal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if j, ok := s.cache.get(id); ok {
		return j, nil
	}

	path, slug, err := s.find(id)
	if err != nil {
		return nil, err
	}

	entries, err := readJournal(path, s.logger)
	if err != nil {
		return nil, err
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("session: open journal %s for append: %w", path, err)
	}

	j := newJournal(id, slug, path, entries, f, s.idGen, s.clock)
	s.cache.put(id, j)
	return j, nil
}

// find scans every project directory under the store root for id + ".jsonl",
// returning its path and owning project slug.
func (s *FileStore) find(id string) (path, slug string, err error) {
	base := filepath.Join(s.root, "sessions")
	projectDirs, err := os.ReadDir(base)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", fmt.Errorf("session: open %s: %w", id, ErrSessionNotFound)
		}
		return "", "", fmt.Errorf("session: list %s: %w", base, err)
	}

	name := id + ".jsonl"
	for _, de := range projectDirs {
		if !de.IsDir() {
			continue
		}
		candidate := filepath.Join(base, de.Name(), name)
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate, de.Name(), nil
		}
	}
	return "", "", fmt.Errorf("session: open %s: %w", id, ErrSessionNotFound)
}

// List returns the session ids under projectSlug, newest first.
func (s *FileStore) List(ctx context.Context, projectSlug string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSlug(projectSlug); err != nil {
		return nil, err
	}

	dir := filepath.Join(s.root, "sessions", projectSlug)
	des, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: list project %s: %w", dir, err)
	}

	const ext = ".jsonl"
	ids := make([]string, 0, len(des))
	for _, de := range des {
		if de.IsDir() {
			continue
		}
		if name := de.Name(); strings.HasSuffix(name, ext) {
			ids = append(ids, strings.TrimSuffix(name, ext))
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	return ids, nil
}

// Close releases the store's cached journals.
func (s *FileStore) Close() error {
	return s.cache.closeAll()
}

var _ Store = (*FileStore)(nil)
