// Package auth persists provider credentials and drives the OAuth login flows
// for subscription auth (Anthropic Claude Pro/Max, OpenAI ChatGPT). It owns
// auth.json (mode 0600, atomic replace) under a caller-supplied root directory
// (see [WithRoot]) and resolves a [CredentialSource] the agent loop's
// providers consume without importing this package.
//
// The store never launches a browser and never contacts a live auth server
// except through the explicit [Store.Login] / refresh paths; see login.go for
// the flow API.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// fileVersion is the on-disk schema version of auth.json.
const fileVersion = 1

// refreshSkew renews an OAuth token this long before its stated expiry so a
// token never expires mid-request (matches pi-mono's 5-minute safety margin).
const refreshSkew = 5 * time.Minute

// ErrNoCredential is returned when no entry exists for a provider id.
var ErrNoCredential = errors.New("auth: no credential for provider")

// ErrNoRefresh is returned when an OAuth entry is expired but carries no
// refresh token (the caller must re-run Login).
var ErrNoRefresh = errors.New("auth: oauth credential expired and has no refresh token")

// Entry is one provider's persisted credential. For KindAPIKey, Access holds
// the key and the refresh/expiry fields are unset. For KindOAuth, Access is the
// bearer token, Refresh renews it, and Expires is its unix-second expiry.
type Entry struct {
	Kind    CredKind `json:"kind"`
	Access  string   `json:"access,omitempty"`
	Refresh string   `json:"refresh,omitempty"`
	Expires int64    `json:"expires,omitempty"`
	// Extra holds vendor-specific fields that survive a round-trip but are not
	// part of the core contract (e.g. an OpenAI chatgpt-account-id, an id_token).
	Extra map[string]string `json:"extra,omitempty"`
}

// authFile is the root JSON document.
type authFile struct {
	Version   int              `json:"version"`
	Providers map[string]Entry `json:"providers"`
}

// Store reads and writes auth.json and resolves credentials. It is safe for
// concurrent use; OAuth refreshes are single-flighted per provider in-process
// and guarded by an advisory file lock across processes.
type Store struct {
	root       string
	now        func() time.Time
	httpClient httpDoer
	flows      map[string]loginFlow

	// writeCh is a size-1 semaphore serializing every read-modify-write of
	// auth.json in this process; paired with the cross-process advisory file
	// lock it makes each mutation atomic against all others (this process and
	// others sharing the file). A channel (not a sync.Mutex) so acquisition can
	// honor a caller's context — a refresh holds this across a network call, so
	// a cancelled caller must not have to wait it out.
	writeCh chan struct{}
}

// Option configures a [Store].
type Option func(*Store)

// WithRoot sets the directory that holds auth.json. Required: [New] returns
// an error if no root is set. Tests pass a t.TempDir(); a real application
// picks its own data directory and passes it explicitly — the SDK invents no
// directory name.
func WithRoot(dir string) Option { return func(s *Store) { s.root = dir } }

// WithClock overrides the time source (for expiry tests).
func WithClock(now func() time.Time) Option { return func(s *Store) { s.now = now } }

// WithHTTPClient overrides the HTTP client used for token exchange and refresh
// (tests point it at an httptest.Server).
func WithHTTPClient(c httpDoer) Option { return func(s *Store) { s.httpClient = c } }

// withFlows overrides the registered login flows (used by tests to point the
// vendor flows at a fake OAuth server).
func withFlows(flows map[string]loginFlow) Option {
	return func(s *Store) { s.flows = flows }
}

// New builds a Store. [WithRoot] is required — the SDK does not invent a
// default directory, so New returns an error if no root is set. With no other
// options it uses the default HTTP client and registers the built-in
// Anthropic and OpenAI flows.
func New(opts ...Option) (*Store, error) {
	s := &Store{
		now:        time.Now,
		httpClient: defaultHTTPClient(),
		writeCh:    make(chan struct{}, 1),
	}
	for _, o := range opts {
		o(s)
	}
	if s.root == "" {
		return nil, errors.New("auth: no store root — pass WithRoot(dir)")
	}
	if s.flows == nil {
		s.flows = defaultFlows()
	}
	return s, nil
}

// path returns the auth.json path.
func (s *Store) path() string { return filepath.Join(s.root, "auth.json") }

// load reads auth.json, returning an empty document if the file is absent.
func (s *Store) load() (authFile, error) {
	af := authFile{Version: fileVersion, Providers: map[string]Entry{}}
	b, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return af, nil
	}
	if err != nil {
		return af, fmt.Errorf("auth: read %s: %w", s.path(), err)
	}
	if len(b) == 0 {
		return af, nil
	}
	if err := json.Unmarshal(b, &af); err != nil {
		return af, fmt.Errorf("auth: parse %s: %w", s.path(), err)
	}
	if af.Providers == nil {
		af.Providers = map[string]Entry{}
	}
	return af, nil
}

// save writes auth.json atomically at mode 0600. It writes a sibling temp file
// then renames it over the target so a crash mid-write never leaves a
// truncated or partially written credential file.
func (s *Store) save(af authFile) error {
	af.Version = fileVersion
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return fmt.Errorf("auth: create %s: %w", s.root, err)
	}
	b, err := json.MarshalIndent(af, "", "  ")
	if err != nil {
		return fmt.Errorf("auth: encode auth.json: %w", err)
	}
	b = append(b, '\n')

	tmp, err := os.CreateTemp(s.root, ".auth-*.json.tmp")
	if err != nil {
		return fmt.Errorf("auth: create temp: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename.
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("auth: chmod temp: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("auth: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("auth: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("auth: close temp: %w", err)
	}
	if err := os.Rename(tmpName, s.path()); err != nil {
		return fmt.Errorf("auth: replace auth.json: %w", err)
	}
	return nil
}

// Get returns the raw persisted entry for a provider id. It is a lock-free
// read: auth.json is only ever replaced atomically (temp file + rename), so a
// reader always sees a complete prior or current document, never a torn write.
func (s *Store) Get(providerID string) (Entry, bool, error) {
	af, err := s.load()
	if err != nil {
		return Entry{}, false, err
	}
	e, ok := af.Providers[providerID]
	return e, ok, nil
}

// withWriteLock runs fn while holding the in-process write semaphore and the
// cross-process advisory file lock, so any load-modify-save inside fn is atomic
// against every other store mutation. Semaphore acquisition honors ctx: because
// refreshEntry holds this lock across a token-endpoint call, a cancelled caller
// must return promptly rather than wait out an unrelated refresh.
func (s *Store) withWriteLock(ctx context.Context, fn func() error) error {
	select {
	case s.writeCh <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.writeCh }()

	unlock, err := s.lockFile()
	if err != nil {
		return err
	}
	defer unlock()

	// Bail before doing work if ctx was cancelled while taking the file lock.
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

// mutate atomically loads auth.json, applies fn, and saves the result. All
// mutations route through here (or through refreshEntry, which needs the same
// lock held across a network call), so no two read-modify-write cycles can
// interleave and lose an update. Local mutations (Set/Logout) are not
// cancellable — they take context.Background; ctx-awareness matters for the
// refresh path, which is the one that can block on I/O.
func (s *Store) mutate(ctx context.Context, fn func(*authFile) error) error {
	return s.withWriteLock(ctx, func() error {
		af, err := s.load()
		if err != nil {
			return err
		}
		if err := fn(&af); err != nil {
			return err
		}
		return s.save(af)
	})
}

// Set persists an entry for a provider id, replacing any existing one.
func (s *Store) Set(providerID string, e Entry) error {
	return s.mutate(context.Background(), func(af *authFile) error {
		af.Providers[providerID] = e
		return nil
	})
}

// SetAPIKey stores a static API key for a provider id.
func (s *Store) SetAPIKey(providerID, key string) error {
	return s.Set(providerID, Entry{Kind: KindAPIKey, Access: key})
}

// Logout removes a provider's credential. It is not an error to log out a
// provider that has no entry.
func (s *Store) Logout(providerID string) error {
	return s.mutate(context.Background(), func(af *authFile) error {
		delete(af.Providers, providerID)
		return nil
	})
}

// StatusEntry is a redacted view of a provider's credential for `auth status`.
type StatusEntry struct {
	Provider string
	Kind     CredKind
	// Expires is the OAuth token expiry (zero for API keys / no expiry).
	Expires time.Time
	// Expired reports whether an OAuth token is past its refresh-skew window.
	Expired bool
}

// Status lists every configured provider and the kind of credential it
// resolves to. It never returns token material.
func (s *Store) Status() ([]StatusEntry, error) {
	af, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]StatusEntry, 0, len(af.Providers))
	for id, e := range af.Providers {
		st := StatusEntry{Provider: id, Kind: e.Kind}
		if e.Kind == KindOAuth && e.Expires > 0 {
			st.Expires = time.Unix(e.Expires, 0)
			st.Expired = s.expired(e)
		}
		out = append(out, st)
	}
	return out, nil
}

// expired reports whether an OAuth entry is at or past its refresh-skew window.
func (s *Store) expired(e Entry) bool {
	if e.Expires == 0 {
		return false
	}
	return !s.now().Add(refreshSkew).Before(time.Unix(e.Expires, 0))
}

// Credential resolves the current credential for a provider id, refreshing an
// expired OAuth token transparently. It implements [CredentialSource].
func (s *Store) Credential(ctx context.Context, providerID string) (Credential, error) {
	e, ok, err := s.Get(providerID)
	if err != nil {
		return Credential{}, err
	}
	if !ok {
		return Credential{}, fmt.Errorf("%w: %s", ErrNoCredential, providerID)
	}
	switch e.Kind {
	case KindAPIKey:
		return Credential{Kind: KindAPIKey, Token: e.Access}, nil
	case KindOAuth:
		if s.expired(e) {
			e, err = s.refreshEntry(ctx, providerID, e)
			if err != nil {
				return Credential{}, err
			}
		}
		// Account carries the ChatGPT account id for the OpenAI subscription
		// path (provider/openai sends it as the ChatGPT-Account-ID header). It
		// is empty for Anthropic OAuth, which persists no such claim.
		return Credential{Kind: KindOAuth, Token: e.Access, Account: e.Extra[openaiAccountIDKey]}, nil
	default:
		return Credential{}, fmt.Errorf("auth: unknown credential kind %q for %s", e.Kind, providerID)
	}
}

// refreshEntry renews an expired OAuth entry. The whole sequence — the
// double-check read, the network refresh, and the save — runs inside one
// withWriteLock critical section (in-process mutex + cross-process file lock).
// Holding the lock across the network call is deliberate: it single-flights the
// refresh both in-process and across processes, so a concurrent caller observes
// the freshly-saved token instead of issuing a second refresh (refresh tokens
// rotate, so a duplicate refresh would invalidate the winner's token). The
// bounded HTTP client timeout caps how long other mutations can be blocked; a
// refresh is rare relative to a token's lifetime, so the throughput cost is
// negligible.
func (s *Store) refreshEntry(ctx context.Context, providerID string, e Entry) (Entry, error) {
	flow, ok := s.flows[providerID]
	if !ok {
		return Entry{}, fmt.Errorf("auth: no oauth flow registered for %s", providerID)
	}
	if e.Refresh == "" {
		return Entry{}, fmt.Errorf("%w: %s", ErrNoRefresh, providerID)
	}

	var refreshed Entry
	err := s.withWriteLock(ctx, func() error {
		af, err := s.load()
		if err != nil {
			return err
		}
		// Double-check under the lock: another goroutine or process may have
		// refreshed while we waited for it.
		cur, ok := af.Providers[providerID]
		if ok && cur.Kind == KindOAuth && !s.expired(cur) {
			refreshed = cur
			return nil
		}
		if ok {
			e = cur
		}
		if e.Refresh == "" {
			return fmt.Errorf("%w: %s", ErrNoRefresh, providerID)
		}
		out, err := flow.refresh(ctx, s.httpClient, e)
		if err != nil {
			return fmt.Errorf("auth: refresh %s: %w", providerID, err)
		}
		af.Providers[providerID] = out
		if err := s.save(af); err != nil {
			return err
		}
		refreshed = out
		return nil
	})
	if err != nil {
		return Entry{}, err
	}
	return refreshed, nil
}
