package session

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemStore is an in-memory [Store]: journals live in process memory and nothing
// is written to disk. It is the ephemeral opt-in for fire-and-forget automation
// agents — same fold/resume semantics as [FileStore] within the process, no
// journal file, no auditable on-disk record. A [FileStore] (the default) is what
// preserves the auditability tenet; reach for MemStore only when you explicitly
// want a session that leaves no trace.
//
// Resume works within the process: [MemStore.Open] returns the same *Journal
// [MemStore.Create] built (re-arming it if the caller has since Closed it).
// Across processes there is nothing to resume — the journals vanish with the
// store.
type MemStore struct {
	idGen func() string
	clock func() time.Time
	// newWriter builds the sink installed on each journal this store creates or
	// reopens; nil means the discarding default (see WithMemJournalWriter).
	newWriter func(id string) JournalWriter

	mu       sync.Mutex
	journals map[string]*Journal
}

// NewMemStore constructs a [MemStore]. It reuses the [StoreOption] set for the
// id generator and clock ([WithStoreIDGen] / [WithStoreClock]); disk-only
// options ([WithRoot], [WithTTL], [WithLogger]) are accepted but ignored, since
// a MemStore has no disk, cache, or root.
func NewMemStore(opts ...StoreOption) *MemStore {
	cfg := storeConfig{idGen: newV7, clock: time.Now}
	for _, o := range opts {
		o(&cfg)
	}
	return &MemStore{idGen: cfg.idGen, clock: cfg.clock, newWriter: cfg.memJournalWriter, journals: make(map[string]*Journal)}
}

// writerFor returns the sink to install on the journal for id: the configured
// substitute when [WithMemJournalWriter] was passed, else the discarding
// default.
func (s *MemStore) writerFor(id string) JournalWriter {
	if s.newWriter == nil {
		return memWriter{}
	}
	return s.newWriter(id)
}

// Create starts a new empty in-memory journal for projectSlug.
func (s *MemStore) Create(ctx context.Context, projectSlug string) (*Journal, error) {
	return s.create(ctx, projectSlug, "")
}

// CreateWithID starts a new empty in-memory journal for projectSlug using id
// verbatim as the session id (or a fresh one when id is empty). See
// [Store.CreateWithID]; a MemStore reuse of an existing id replaces the prior
// in-memory journal (last write wins).
func (s *MemStore) CreateWithID(ctx context.Context, projectSlug, id string) (*Journal, error) {
	return s.create(ctx, projectSlug, id)
}

// create is the shared body of Create/CreateWithID: an empty id is generated,
// a non-empty id is used verbatim after validation. Entry-id generation is
// unaffected — the journal still carries s.idGen for its Append ids.
func (s *MemStore) create(ctx context.Context, projectSlug, id string) (*Journal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSlug(projectSlug); err != nil {
		return nil, err
	}
	if id == "" {
		id = s.idGen()
	} else if err := validateID(id); err != nil {
		return nil, err
	}
	// path is synthetic: <id>.jsonl with no directory, never created on disk.
	// It keeps Journal.Path/Dir well-formed for callers that derive per-session
	// artifact paths, without implying a real file.
	j := newJournal(id, projectSlug, id+".jsonl", nil, s.writerFor(id), s.idGen, s.clock)
	s.mu.Lock()
	s.journals[id] = j
	s.mu.Unlock()
	return j, nil
}

// Open resumes an in-memory session by id, returning the same *Journal Create
// built and re-arming it if it was Closed. It returns [ErrSessionNotFound] for
// an id this store never created.
func (s *MemStore) Open(ctx context.Context, id string) (*Journal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	j, ok := s.journals[id]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("session: open %s: %w", id, ErrSessionNotFound)
	}
	j.reopen(s.writerFor(id))
	return j, nil
}

// List returns the ids of in-memory sessions under projectSlug, newest first
// (ids are UUIDv7, so lexical-descending is time-descending).
func (s *MemStore) List(ctx context.Context, projectSlug string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSlug(projectSlug); err != nil {
		return nil, err
	}
	s.mu.Lock()
	ids := make([]string, 0, len(s.journals))
	for id, j := range s.journals {
		if j.ProjectSlug() == projectSlug {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	return ids, nil
}

// Close closes every in-memory journal. It is idempotent (Journal.Close is),
// so a runner that owns the store and also closes its own journal is safe.
func (s *MemStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	for _, j := range s.journals {
		if err := j.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Root returns "" — a MemStore persists nothing, so there is no root directory.
func (s *MemStore) Root() string { return "" }

// memWriter is the in-memory [JournalWriter]: it discards every entry (the
// journal keeps entries in memory for Fold/Cost/resume regardless of the sink),
// so an in-memory session writes nothing to disk.
type memWriter struct{}

func (memWriter) Write(p []byte) (int, error) { return len(p), nil }
func (memWriter) Sync() error                 { return nil }
func (memWriter) Close() error                { return nil }

var _ Store = (*MemStore)(nil)
