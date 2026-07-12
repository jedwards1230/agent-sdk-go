package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// oauthServer is a scripted fake token endpoint for both vendors. It records
// the last request it saw so tests can assert the wire shape.
type oauthServer struct {
	*httptest.Server
	idToken string

	lastGrant     string
	lastVerifier  string
	lastRefresh   string
	lastClientID  string
	lastCodeParam string
	lastRedirect  string
	contentType   string
}

func newOAuthServer(t *testing.T) *oauthServer {
	t.Helper()
	o := &oauthServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		o.contentType = r.Header.Get("Content-Type")
		var vals url.Values
		if strings.HasPrefix(o.contentType, "application/json") {
			body, _ := io.ReadAll(r.Body)
			m := map[string]string{}
			_ = json.Unmarshal(body, &m)
			vals = url.Values{}
			for k, v := range m {
				vals.Set(k, v)
			}
		} else {
			_ = r.ParseForm()
			vals = r.PostForm
		}
		o.lastGrant = vals.Get("grant_type")
		o.lastVerifier = vals.Get("code_verifier")
		o.lastRefresh = vals.Get("refresh_token")
		o.lastClientID = vals.Get("client_id")
		o.lastCodeParam = vals.Get("code")
		o.lastRedirect = vals.Get("redirect_uri")

		resp := map[string]any{
			"access_token":  "access-" + o.lastGrant,
			"refresh_token": "refresh-next",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"scope":         "openid",
		}
		if o.idToken != "" {
			resp["id_token"] = o.idToken
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	o.Server = httptest.NewServer(mux)
	t.Cleanup(o.Close)
	return o
}

// makeIDToken builds an unsigned JWT whose payload carries the OpenAI auth
// claim with a chatgpt_account_id and an exp one hour out.
func makeIDToken(accountID string) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload := map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": accountID},
		"exp":                         time.Now().Add(time.Hour).Unix(),
	}
	pb, _ := json.Marshal(payload)
	return hdr + "." + base64.RawURLEncoding.EncodeToString(pb) + ".sig"
}

func TestAnthropicManualFlow(t *testing.T) {
	srv := newOAuthServer(t)
	flow := &anthropicFlow{
		authorizeURL: srv.URL + "/authorize",
		tokenURL:     srv.URL + "/token",
		clientID:     "anthropic-cid",
		scopes:       "org:create_api_key user:profile user:inference",
		redirect:     "https://redirect.test/callback",
	}
	s := newTestStore(t, map[string]loginFlow{"anthropic": flow}, nil)
	s.httpClient = srv.Client()

	login, err := s.Login(context.Background(), "anthropic")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if login.Mode != LoginModeManualCode || login.Redeem == nil || login.Wait != nil {
		t.Fatalf("expected manual-code login, got %+v", login)
	}

	u, err := url.Parse(login.AuthorizeURL)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	q := u.Query()
	if q.Get("client_id") != "anthropic-cid" {
		t.Fatalf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatalf("missing PKCE challenge: %v", q)
	}
	if q.Get("redirect_uri") != "https://redirect.test/callback" {
		t.Fatalf("redirect_uri = %q", q.Get("redirect_uri"))
	}
	state := q.Get("state")
	if state == "" {
		t.Fatalf("missing state")
	}

	if err := login.Redeem("the-code#" + state); err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if srv.lastGrant != "authorization_code" {
		t.Fatalf("grant = %q", srv.lastGrant)
	}
	if srv.lastVerifier == "" {
		t.Fatalf("code_verifier not sent")
	}
	// Anthropic quirk: state IS the code_verifier.
	if srv.lastVerifier != state {
		t.Fatalf("anthropic state (%q) must equal code_verifier (%q)", state, srv.lastVerifier)
	}
	if srv.lastCodeParam != "the-code" {
		t.Fatalf("code = %q, want the-code (state stripped)", srv.lastCodeParam)
	}
	if !strings.HasPrefix(srv.contentType, "application/json") {
		t.Fatalf("anthropic token request should be JSON, got %q", srv.contentType)
	}

	e, ok, _ := s.Get("anthropic")
	if !ok || e.Kind != KindOAuth || e.Access != "access-authorization_code" || e.Refresh != "refresh-next" {
		t.Fatalf("persisted entry wrong: %+v", e)
	}
	if e.Expires == 0 {
		t.Fatalf("expiry not recorded")
	}
}

func TestAnthropicStateMismatch(t *testing.T) {
	srv := newOAuthServer(t)
	flow := &anthropicFlow{
		authorizeURL: srv.URL + "/authorize",
		tokenURL:     srv.URL + "/token",
		clientID:     "cid",
		scopes:       "s",
		redirect:     "https://redirect.test/callback",
	}
	s := newTestStore(t, map[string]loginFlow{"anthropic": flow}, nil)
	s.httpClient = srv.Client()

	login, err := s.Login(context.Background(), "anthropic")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if err := login.Redeem("code#wrong-state"); err == nil {
		t.Fatalf("expected state mismatch error")
	}
	if _, ok, _ := s.Get("anthropic"); ok {
		t.Fatalf("entry should not be persisted on mismatch")
	}
}

func TestOpenAICallbackFlow(t *testing.T) {
	srv := newOAuthServer(t)
	srv.idToken = makeIDToken("acct_123")
	flow := &openaiFlow{
		authorizeURL: srv.URL + "/authorize",
		tokenURL:     srv.URL + "/token",
		clientID:     "openai-cid",
		scopes:       "openid profile email offline_access",
		listenAddr:   "127.0.0.1:0",
		cbPath:       "/auth/callback",
		redirectURI:  "http://127.0.0.1:1455/auth/callback",
	}
	s := newTestStore(t, map[string]loginFlow{"openai": flow}, nil)
	s.httpClient = srv.Client()

	login, err := s.Login(context.Background(), "openai")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if login.Mode != LoginModeCallback || login.Wait == nil || login.Redeem != nil {
		t.Fatalf("expected callback login, got %+v", login)
	}

	u, err := url.Parse(login.AuthorizeURL)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	q := u.Query()
	if q.Get("originator") != "codex_cli_rs" {
		t.Fatalf("originator = %q", q.Get("originator"))
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Fatalf("missing S256")
	}
	redirect := q.Get("redirect_uri")
	state := q.Get("state")
	if redirect == "" || state == "" {
		t.Fatalf("missing redirect/state: %v", q)
	}

	// Drive the browser redirect: hit the local callback with code+state.
	waitErr := make(chan error, 1)
	go func() { waitErr <- login.Wait() }()

	cbURL := redirect + "?code=cb-code&state=" + url.QueryEscape(state)
	resp, err := http.Get(cbURL)
	if err != nil {
		t.Fatalf("callback GET: %v", err)
	}
	_ = resp.Body.Close()

	if err := <-waitErr; err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if srv.lastGrant != "authorization_code" || srv.lastVerifier == "" {
		t.Fatalf("token exchange wire wrong: grant=%q verifier=%q", srv.lastGrant, srv.lastVerifier)
	}
	if strings.HasPrefix(srv.contentType, "application/json") {
		t.Fatalf("openai token request should be form-encoded, got %q", srv.contentType)
	}

	e, ok, _ := s.Get("openai")
	if !ok || e.Kind != KindOAuth || e.Access != "access-authorization_code" {
		t.Fatalf("persisted entry wrong: %+v", e)
	}
	if e.Extra[openaiAccountIDKey] != "acct_123" {
		t.Fatalf("account id not extracted: %+v", e.Extra)
	}
	// Codex returns no expires_in; expiry must come from the id_token exp claim.
	if e.Expires == 0 {
		t.Fatalf("expiry not derived from id_token exp")
	}
}

func TestOpenAICallbackStateMismatch(t *testing.T) {
	srv := newOAuthServer(t)
	flow := &openaiFlow{
		authorizeURL: srv.URL + "/authorize",
		tokenURL:     srv.URL + "/token",
		clientID:     "cid",
		scopes:       "s",
		listenAddr:   "127.0.0.1:0",
		cbPath:       "/auth/callback",
		redirectURI:  "http://127.0.0.1:1455/auth/callback",
	}
	s := newTestStore(t, map[string]loginFlow{"openai": flow}, nil)
	s.httpClient = srv.Client()

	login, err := s.Login(context.Background(), "openai")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	u, _ := url.Parse(login.AuthorizeURL)
	redirect := u.Query().Get("redirect_uri")

	waitErr := make(chan error, 1)
	go func() { waitErr <- login.Wait() }()

	resp, err := http.Get(redirect + "?code=x&state=bogus")
	if err != nil {
		t.Fatalf("callback GET: %v", err)
	}
	_ = resp.Body.Close()

	if err := <-waitErr; err == nil {
		t.Fatalf("expected state mismatch error")
	}
	if _, ok, _ := s.Get("openai"); ok {
		t.Fatalf("entry should not be persisted on mismatch")
	}
}

func TestFlowRefreshOverHTTP(t *testing.T) {
	srv := newOAuthServer(t)
	// Anthropic (JSON) refresh.
	af := &anthropicFlow{tokenURL: srv.URL + "/token", clientID: "cid"}
	e, err := af.refresh(context.Background(), srv.Client(), Entry{Kind: KindOAuth, Refresh: "old-refresh"})
	if err != nil {
		t.Fatalf("anthropic refresh: %v", err)
	}
	if srv.lastGrant != "refresh_token" || srv.lastRefresh != "old-refresh" {
		t.Fatalf("refresh wire wrong: grant=%q refresh=%q", srv.lastGrant, srv.lastRefresh)
	}
	if e.Access != "access-refresh_token" {
		t.Fatalf("access = %q", e.Access)
	}

	// OpenAI refresh is JSON (no scope) and preserves account id + refresh
	// token when the response omits them.
	of := &openaiFlow{tokenURL: srv.URL + "/token", clientID: "cid", scopes: "s"}
	e2, err := of.refresh(context.Background(), srv.Client(), Entry{Kind: KindOAuth, Refresh: "old2", Expires: 42, Extra: map[string]string{openaiAccountIDKey: "acct_9"}})
	if err != nil {
		t.Fatalf("openai refresh: %v", err)
	}
	if !strings.HasPrefix(srv.contentType, "application/json") {
		t.Fatalf("openai refresh must be JSON, got %q", srv.contentType)
	}
	if srv.lastRefresh != "old2" {
		t.Fatalf("refresh_token = %q", srv.lastRefresh)
	}
	if e2.Extra[openaiAccountIDKey] != "acct_9" {
		t.Fatalf("account id not preserved across refresh: %+v", e2.Extra)
	}
	if e2.Refresh != "refresh-next" {
		t.Fatalf("rotated refresh token not taken: %q", e2.Refresh)
	}
}

func TestAnthropicURLPaste(t *testing.T) {
	// The user may paste the full redirect URL instead of code#state.
	code, state := parseAuthorizationInput("https://platform.claude.com/oauth/code/callback?code=abc&state=xyz", "fallback")
	if code != "abc" || state != "xyz" {
		t.Fatalf("URL paste: code=%q state=%q", code, state)
	}
	// A bare code falls back to the flow's own state.
	code, state = parseAuthorizationInput("justcode", "fallback")
	if code != "justcode" || state != "fallback" {
		t.Fatalf("bare paste: code=%q state=%q", code, state)
	}
	// code#state still works.
	code, state = parseAuthorizationInput("c#s", "fallback")
	if code != "c" || state != "s" {
		t.Fatalf("hash paste: code=%q state=%q", code, state)
	}
}

func TestTokenEndpointError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	t.Cleanup(srv.Close)
	af := &anthropicFlow{tokenURL: srv.URL, clientID: "cid"}
	_, err := af.refresh(context.Background(), srv.Client(), Entry{Refresh: "r"})
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("expected surfaced endpoint error, got %v", err)
	}
}

func TestPKCEChallengeIsS256(t *testing.T) {
	p, err := newPKCE("https://x")
	if err != nil {
		t.Fatalf("newPKCE: %v", err)
	}
	// Recompute the challenge from the verifier and compare.
	if got := s256Challenge(p.verifier); got != p.challenge {
		t.Fatalf("challenge %q != recomputed %q", p.challenge, got)
	}
	if p.state == "" || p.verifier == p.state {
		t.Fatalf("state/verifier must be distinct non-empty values")
	}
}

// freePort returns a currently-free TCP port on the loopback interface.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// fixedPortOpenAIFlow builds an openai callback flow bound to a concrete port so
// a second Login on the same port collides unless the first released it.
func fixedPortOpenAIFlow(addr string) *openaiFlow {
	return &openaiFlow{
		authorizeURL: "https://example.test/authorize",
		tokenURL:     "https://example.test/token",
		clientID:     "cid",
		scopes:       "s",
		listenAddr:   addr,
		cbPath:       "/auth/callback",
		redirectURI:  "http://" + addr + "/auth/callback",
	}
}

func TestCallbackLoginCloseReleasesPort(t *testing.T) {
	addr := "127.0.0.1:" + strconv.Itoa(freePort(t))
	s := newTestStore(t, map[string]loginFlow{"openai": fixedPortOpenAIFlow(addr)}, nil)

	l1, err := s.Login(context.Background(), "openai")
	if err != nil {
		t.Fatalf("first login: %v", err)
	}
	// While l1 holds the fixed port, a second bind must fail.
	if _, err := s.Login(context.Background(), "openai"); err == nil {
		t.Fatalf("expected bind conflict while first login holds the port")
	}

	l1.Close() // release the listener
	l1.Close() // idempotent

	l2 := mustLoginWithin(t, s, 2*time.Second)
	l2.Close()
}

func TestCallbackLoginCtxCancelReleasesPort(t *testing.T) {
	addr := "127.0.0.1:" + strconv.Itoa(freePort(t))
	s := newTestStore(t, map[string]loginFlow{"openai": fixedPortOpenAIFlow(addr)}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	if _, err := s.Login(ctx, "openai"); err != nil {
		t.Fatalf("first login: %v", err)
	}
	// Abandon the login without ever calling Wait; cancelling ctx must free the
	// port so a later login can bind it.
	cancel()

	l2 := mustLoginWithin(t, s, 2*time.Second)
	l2.Close()
}

// mustLoginWithin retries Login until it binds the port or the deadline passes.
func mustLoginWithin(t *testing.T, s *Store, d time.Duration) *Login {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		l, err := s.Login(context.Background(), "openai")
		if err == nil {
			return l
		}
		if time.Now().After(deadline) {
			t.Fatalf("port not released within %v: %v", d, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
