package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeFlow is a loginFlow stand-in for store tests: refresh mints a new token
// and counts how many times it ran.
type fakeFlow struct {
	id       string
	refreshN atomic.Int64
	// delay lets a test widen the refresh window to exercise single-flight.
	delay time.Duration
	// started, if non-nil, is closed the first time refresh begins (so a test
	// can wait until the write lock is held).
	started   chan struct{}
	startOnce sync.Once
}

func (f *fakeFlow) provider() string            { return f.id }
func (f *fakeFlow) usesCallback() bool          { return false }
func (f *fakeFlow) callbackListenAddr() string  { return "" }
func (f *fakeFlow) callbackPath() string        { return "" }
func (f *fakeFlow) callbackRedirectURI() string { return "" }
func (f *fakeFlow) manualRedirectURI() string   { return "urn:test" }

func (f *fakeFlow) authorize(redirectURI string) (string, pkce, error) {
	p, err := newPKCE(redirectURI)
	return "https://example.test/authorize", p, err
}

func (f *fakeFlow) exchange(_ context.Context, _ httpDoer, code string, _ pkce) (Entry, error) {
	return Entry{Kind: KindOAuth, Access: "access-" + code, Refresh: "refresh-" + code, Expires: 0}, nil
}

func (f *fakeFlow) refresh(_ context.Context, _ httpDoer, e Entry) (Entry, error) {
	if f.started != nil {
		f.startOnce.Do(func() { close(f.started) })
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	n := f.refreshN.Add(1)
	return Entry{
		Kind:    KindOAuth,
		Access:  "refreshed-token",
		Refresh: e.Refresh,
		Expires: time.Now().Add(time.Hour).Unix(),
		Extra:   map[string]string{"refreshes": string(rune('0' + n))},
	}, nil
}

func newTestStore(t *testing.T, flows map[string]loginFlow, now func() time.Time) *Store {
	t.Helper()
	opts := []Option{WithRoot(t.TempDir())}
	if flows != nil {
		opts = append(opts, withFlows(flows))
	}
	if now != nil {
		opts = append(opts, WithClock(now))
	}
	s, err := New(opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestSaveIsAtomicAnd0600(t *testing.T) {
	s := newTestStore(t, map[string]loginFlow{}, nil)
	if err := s.SetAPIKey("anthropic", "sk-test"); err != nil {
		t.Fatalf("SetAPIKey: %v", err)
	}

	info, err := os.Stat(s.path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("auth.json mode = %o, want 0600", perm)
		}
	}

	// No temp files left behind.
	entries, err := os.ReadDir(s.root)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" || (len(e.Name()) > 5 && e.Name()[:5] == ".auth") {
			t.Fatalf("stray temp file left behind: %s", e.Name())
		}
	}
}

func TestRoundTrip(t *testing.T) {
	s := newTestStore(t, map[string]loginFlow{}, nil)
	want := Entry{Kind: KindOAuth, Access: "a", Refresh: "r", Expires: 123, Extra: map[string]string{"k": "v"}}
	if err := s.Set("openai", want); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, ok, err := s.Get("openai")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.Access != want.Access || got.Refresh != want.Refresh || got.Expires != want.Expires || got.Extra["k"] != "v" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestCredentialAPIKey(t *testing.T) {
	s := newTestStore(t, map[string]loginFlow{}, nil)
	if err := s.SetAPIKey("anthropic", "sk-xyz"); err != nil {
		t.Fatalf("SetAPIKey: %v", err)
	}
	c, err := s.Credential(context.Background(), "anthropic")
	if err != nil {
		t.Fatalf("Credential: %v", err)
	}
	if c.Kind != KindAPIKey || c.Token != "sk-xyz" {
		t.Fatalf("got %+v", c)
	}
}

func TestCredentialAccount(t *testing.T) {
	s := newTestStore(t, map[string]loginFlow{}, nil)
	future := time.Now().Add(time.Hour).Unix()

	// OpenAI OAuth surfaces the persisted chatgpt account id as Credential.Account.
	if err := s.Set("openai", Entry{Kind: KindOAuth, Access: "tok", Refresh: "r", Expires: future, Extra: map[string]string{openaiAccountIDKey: "acct_z"}}); err != nil {
		t.Fatalf("Set openai: %v", err)
	}
	c, err := s.Credential(context.Background(), "openai")
	if err != nil {
		t.Fatalf("Credential openai: %v", err)
	}
	if c.Kind != KindOAuth || c.Token != "tok" || c.Account != "acct_z" {
		t.Fatalf("openai credential = %+v, want oauth/tok/acct_z", c)
	}

	// Anthropic OAuth persists no account claim → Account stays empty.
	if err := s.Set("anthropic", Entry{Kind: KindOAuth, Access: "atok", Refresh: "r", Expires: future}); err != nil {
		t.Fatalf("Set anthropic: %v", err)
	}
	c, err = s.Credential(context.Background(), "anthropic")
	if err != nil {
		t.Fatalf("Credential anthropic: %v", err)
	}
	if c.Account != "" {
		t.Fatalf("anthropic oauth Account = %q, want empty", c.Account)
	}

	// API keys never carry an account.
	if err := s.SetAPIKey("k", "sk-x"); err != nil {
		t.Fatalf("SetAPIKey: %v", err)
	}
	c, err = s.Credential(context.Background(), "k")
	if err != nil {
		t.Fatalf("Credential k: %v", err)
	}
	if c.Account != "" {
		t.Fatalf("api-key Account = %q, want empty", c.Account)
	}
}

func TestCredentialMissing(t *testing.T) {
	s := newTestStore(t, map[string]loginFlow{}, nil)
	_, err := s.Credential(context.Background(), "nope")
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("want ErrNoCredential, got %v", err)
	}
}

func TestCredentialOAuthNotExpired(t *testing.T) {
	flow := &fakeFlow{id: "openai"}
	s := newTestStore(t, map[string]loginFlow{"openai": flow}, nil)
	exp := time.Now().Add(time.Hour).Unix()
	if err := s.Set("openai", Entry{Kind: KindOAuth, Access: "live", Refresh: "r", Expires: exp}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	c, err := s.Credential(context.Background(), "openai")
	if err != nil {
		t.Fatalf("Credential: %v", err)
	}
	if c.Token != "live" {
		t.Fatalf("token = %q, want live (no refresh expected)", c.Token)
	}
	if n := flow.refreshN.Load(); n != 0 {
		t.Fatalf("refresh ran %d times, want 0", n)
	}
}

func TestCredentialOAuthExpiredRefreshes(t *testing.T) {
	flow := &fakeFlow{id: "openai"}
	s := newTestStore(t, map[string]loginFlow{"openai": flow}, nil)
	past := time.Now().Add(-time.Hour).Unix()
	if err := s.Set("openai", Entry{Kind: KindOAuth, Access: "stale", Refresh: "r", Expires: past}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	c, err := s.Credential(context.Background(), "openai")
	if err != nil {
		t.Fatalf("Credential: %v", err)
	}
	if c.Token != "refreshed-token" {
		t.Fatalf("token = %q, want refreshed-token", c.Token)
	}
	if n := flow.refreshN.Load(); n != 1 {
		t.Fatalf("refresh ran %d times, want 1", n)
	}
	// Persisted.
	e, _, _ := s.Get("openai")
	if e.Access != "refreshed-token" {
		t.Fatalf("persisted access = %q", e.Access)
	}
}

func TestCredentialOAuthExpiredNoRefreshToken(t *testing.T) {
	flow := &fakeFlow{id: "openai"}
	s := newTestStore(t, map[string]loginFlow{"openai": flow}, nil)
	past := time.Now().Add(-time.Hour).Unix()
	if err := s.Set("openai", Entry{Kind: KindOAuth, Access: "stale", Expires: past}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	_, err := s.Credential(context.Background(), "openai")
	if !errors.Is(err, ErrNoRefresh) {
		t.Fatalf("want ErrNoRefresh, got %v", err)
	}
}

func TestRefreshSingleFlight(t *testing.T) {
	flow := &fakeFlow{id: "openai", delay: 50 * time.Millisecond}
	s := newTestStore(t, map[string]loginFlow{"openai": flow}, nil)
	past := time.Now().Add(-time.Hour).Unix()
	if err := s.Set("openai", Entry{Kind: KindOAuth, Access: "stale", Refresh: "r", Expires: past}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.Credential(context.Background(), "openai")
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	if got := flow.refreshN.Load(); got != 1 {
		t.Fatalf("refresh ran %d times, want exactly 1 (single-flight)", got)
	}
}

func TestConcurrentSetDistinctProvidersNoLostUpdate(t *testing.T) {
	// Concurrent Set of different providers is a read-modify-write of the whole
	// file; without serialization the load-modify-save cycles clobber each
	// other and lose entries.
	s := newTestStore(t, map[string]loginFlow{}, nil)
	const n = 24
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.SetAPIKey("p"+strconv.Itoa(i), "k"+strconv.Itoa(i))
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Set %d: %v", i, err)
		}
	}
	sts, err := s.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(sts) != n {
		t.Fatalf("persisted %d providers, want %d (lost updates)", len(sts), n)
	}
}

func TestConcurrentSetDuringRefreshNoClobber(t *testing.T) {
	// A slow refresh of "a" must not clobber a concurrent Set of "b" (and vice
	// versa): both writes must survive.
	flow := &fakeFlow{id: "a", delay: 40 * time.Millisecond}
	s := newTestStore(t, map[string]loginFlow{"a": flow}, nil)
	past := time.Now().Add(-time.Hour).Unix()
	if err := s.Set("a", Entry{Kind: KindOAuth, Access: "stale", Refresh: "r", Expires: past}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	var credErr, setErr error
	go func() { defer wg.Done(); _, credErr = s.Credential(context.Background(), "a") }()
	go func() { defer wg.Done(); setErr = s.SetAPIKey("b", "key-b") }()
	wg.Wait()

	if credErr != nil || setErr != nil {
		t.Fatalf("cred=%v set=%v", credErr, setErr)
	}
	ea, oka, _ := s.Get("a")
	eb, okb, _ := s.Get("b")
	if !oka || ea.Access != "refreshed-token" {
		t.Fatalf("a refreshed entry lost/clobbered: %+v ok=%v", ea, oka)
	}
	if !okb || eb.Access != "key-b" {
		t.Fatalf("b write lost/clobbered by refresh: %+v ok=%v", eb, okb)
	}
}

func TestRefreshRespectsContextCancellation(t *testing.T) {
	// While one goroutine holds the write lock across a slow refresh, a caller
	// with an already-cancelled context must return promptly instead of
	// blocking for the refresh duration.
	started := make(chan struct{})
	slow := &fakeFlow{id: "a", delay: 500 * time.Millisecond, started: started}
	other := &fakeFlow{id: "b"}
	s := newTestStore(t, map[string]loginFlow{"a": slow, "b": other}, nil)
	past := time.Now().Add(-time.Hour).Unix()
	if err := s.Set("a", Entry{Kind: KindOAuth, Access: "stale", Refresh: "r", Expires: past}); err != nil {
		t.Fatalf("seed a: %v", err)
	}
	if err := s.Set("b", Entry{Kind: KindOAuth, Access: "stale", Refresh: "r", Expires: past}); err != nil {
		t.Fatalf("seed b: %v", err)
	}

	// Grab the write lock via a slow refresh of "a".
	go func() { _, _ = s.Credential(context.Background(), "a") }()
	<-started // "a" now holds the write lock, mid-refresh

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	t0 := time.Now()
	_, err := s.Credential(ctx, "b")
	elapsed := time.Since(t0)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("cancelled caller blocked %v on an unrelated refresh (want prompt return)", elapsed)
	}
	if other.refreshN.Load() != 0 {
		t.Fatalf("cancelled caller should not have refreshed")
	}
}

func TestLogout(t *testing.T) {
	s := newTestStore(t, map[string]loginFlow{}, nil)
	if err := s.SetAPIKey("anthropic", "sk"); err != nil {
		t.Fatalf("SetAPIKey: %v", err)
	}
	if err := s.Logout("anthropic"); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, ok, _ := s.Get("anthropic"); ok {
		t.Fatalf("entry still present after logout")
	}
	// Logging out an absent provider is not an error.
	if err := s.Logout("ghost"); err != nil {
		t.Fatalf("Logout absent: %v", err)
	}
}

func TestStatusRedaction(t *testing.T) {
	future := time.Now().Add(2 * time.Hour)
	s := newTestStore(t, map[string]loginFlow{}, func() time.Time { return time.Now() })
	_ = s.SetAPIKey("anthropic", "sk-secret")
	_ = s.Set("openai", Entry{Kind: KindOAuth, Access: "tok-secret", Refresh: "r", Expires: future.Unix()})

	sts, err := s.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(sts) != 2 {
		t.Fatalf("status len = %d, want 2", len(sts))
	}
	for _, st := range sts {
		switch st.Provider {
		case "anthropic":
			if st.Kind != KindAPIKey || st.Expired {
				t.Fatalf("anthropic status wrong: %+v", st)
			}
		case "openai":
			if st.Kind != KindOAuth || st.Expired {
				t.Fatalf("openai status wrong: %+v", st)
			}
			if st.Expires.IsZero() {
				t.Fatalf("openai expiry not reported")
			}
		}
	}
}

func TestExpiredWithinSkew(t *testing.T) {
	// A token 60s from expiry is treated as expired (refresh skew is 5m).
	base := time.Unix(1_000_000, 0)
	flow := &fakeFlow{id: "openai"}
	s := newTestStore(t, map[string]loginFlow{"openai": flow}, func() time.Time { return base })
	if err := s.Set("openai", Entry{Kind: KindOAuth, Access: "soon", Refresh: "r", Expires: base.Add(60 * time.Second).Unix()}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := s.Credential(context.Background(), "openai"); err != nil {
		t.Fatalf("Credential: %v", err)
	}
	if flow.refreshN.Load() != 1 {
		t.Fatalf("token within skew should refresh")
	}
}

func TestNewRequiresRoot(t *testing.T) {
	// The SDK invents no directory name: New with no WithRoot must fail
	// clearly rather than fall back to a hardcoded default.
	if _, err := New(); err == nil {
		t.Fatal("New() with no root: want error, got nil")
	}
}
