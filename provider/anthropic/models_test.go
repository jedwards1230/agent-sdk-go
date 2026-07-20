package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// --- hermeticity ---------------------------------------------------------
//
// Every test in this file is kept off the network by two independent
// mechanisms, both required:
//
//  1. WithBaseURL(srv.URL) redirects the adapter at an httptest server bound to
//     127.0.0.1, so no request is ever addressed to api.anthropic.com.
//  2. WithHTTPClient(pinnedClient(t, srv.URL)) installs a RoundTripper that
//     REFUSES, without dialing, any request whose host is not the test
//     server's. If a future edit drops the base-URL override, the request fails
//     with errOffHost instead of reaching a real vendor endpoint and billing a
//     real account.
//
// Credentials are always a StaticCredentialSource with a dummy token, never the
// environment, so a developer's real key cannot be picked up.

// errOffHost is returned by the pinned transport for any request addressed
// somewhere other than the test server.
var errOffHost = errors.New("test transport: refused request to non-test host")

// pinnedTransport fails any request whose host differs from allowHost.
type pinnedTransport struct {
	allowHost string
	base      http.RoundTripper
}

func (p pinnedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != p.allowHost {
		return nil, fmt.Errorf("%w: %s", errOffHost, r.URL.Host)
	}
	return p.base.RoundTrip(r)
}

// pinnedClient returns an HTTP client that can only reach serverURL's host.
func pinnedClient(t *testing.T, serverURL string) *http.Client {
	t.Helper()
	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	return &http.Client{Transport: pinnedTransport{allowHost: u.Host, base: http.DefaultTransport}}
}

// listProvider builds a Provider wired to srv and pinned to its host.
func listProvider(t *testing.T, srv *httptest.Server) *Provider {
	t.Helper()
	return New("claude-sonnet-5",
		provider.StaticCredentialSource{Cred: provider.Credential{Kind: provider.CredAPIKey, Token: "sk-test"}},
		WithBaseURL(srv.URL),
		WithHTTPClient(pinnedClient(t, srv.URL)),
	)
}

// TestListModelsPinnedTransportBlocksRealHost is the canary for the guard
// above: it deliberately omits WithBaseURL so the adapter targets the real
// endpoint, and asserts the pinned transport refuses the call. If this test
// ever passes by reaching the network, the guard is broken.
func TestListModelsPinnedTransportBlocksRealHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("test server must not be reached when the base URL is not overridden")
	}))
	defer srv.Close()

	p := New("claude-sonnet-5",
		provider.StaticCredentialSource{Cred: provider.Credential{Kind: provider.CredAPIKey, Token: "sk-test"}},
		WithHTTPClient(pinnedClient(t, srv.URL)),
	)
	if _, err := p.ListModels(context.Background()); !errors.Is(err, errOffHost) {
		t.Fatalf("want errOffHost, got %v", err)
	}
}

// TestListModelsSuccess parses a well-formed single-page 200 and asserts the
// request shape and the honest ModelInfo mapping.
func TestListModelsSuccess(t *testing.T) {
	var gotPath, gotQuery, gotKey, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery, gotKey = r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("X-Api-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": [
				{"type":"model","id":"claude-sonnet-5","display_name":"Claude Sonnet 5","created_at":"2025-05-22T00:00:00Z"},
				{"type":"model","id":"claude-haiku-4-5","display_name":"Claude Haiku 4.5","created_at":"2025-10-01T00:00:00Z"}
			],
			"has_more": false,
			"first_id": "claude-sonnet-5",
			"last_id": "claude-haiku-4-5"
		}`))
	}))
	defer srv.Close()

	got, err := listProvider(t, srv).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != modelsPath {
		t.Errorf("path = %q, want %q", gotPath, modelsPath)
	}
	if !strings.Contains(gotQuery, "limit=1000") {
		t.Errorf("query = %q, want limit=1000", gotQuery)
	}
	if gotKey != "sk-test" {
		t.Errorf("x-api-key = %q, want the configured credential", gotKey)
	}

	// display_name is the one metadata field this endpoint reports, so it is
	// carried; everything else stays zero meaning UNKNOWN.
	want := []provider.ModelInfo{
		{ID: "claude-sonnet-5", Provider: "anthropic", DisplayName: "Claude Sonnet 5", Unregistered: true},
		{ID: "claude-haiku-4-5", Provider: "anthropic", DisplayName: "Claude Haiku 4.5", Unregistered: true},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d models, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("model[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestListModelsReportsUnknownMetadata pins the honest-mapping contract: the
// endpoint reports no pricing or limits, so those fields must stay zero AND be
// flagged unknown — never presented as a free, zero-context model. It fails if
// anyone backfills from the registry, which does carry claude-sonnet-5.
func TestListModelsReportsUnknownMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"type":"model","id":"claude-sonnet-5"}],"has_more":false}`))
	}))
	defer srv.Close()

	got, err := listProvider(t, srv).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d models, want 1", len(got))
	}
	m := got[0]
	if !m.Unregistered {
		t.Error("Unregistered = false; the listing carries no metadata, so unknown fields must be flagged")
	}
	if m.ContextWindow != 0 || m.MaxOutput != 0 || m.Reasoning {
		t.Errorf("limits/capabilities invented: %+v", m)
	}
	if m.Pricing != (provider.Pricing{}) {
		t.Errorf("pricing invented: %+v", m.Pricing)
	}
	// The body carries no display_name, so the label stays empty meaning
	// UNKNOWN — the adapter must not synthesize one from the id.
	if m.DisplayName != "" {
		t.Errorf("DisplayName = %q, want empty when the vendor supplied none", m.DisplayName)
	}
	// The registry does know this id; ListModels must not silently merge it.
	if reg, ok := provider.Lookup(m.ID); ok && m.ContextWindow == reg.ContextWindow && reg.ContextWindow != 0 {
		t.Error("ListModels backfilled registry metadata; merging is the caller's job")
	}
}

// TestListModelsPagination follows has_more/last_id across pages.
func TestListModelsPagination(t *testing.T) {
	var mu sync.Mutex
	var afterIDs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		after := r.URL.Query().Get("after_id")
		mu.Lock()
		afterIDs = append(afterIDs, after)
		mu.Unlock()
		if after == "" {
			_, _ = w.Write([]byte(`{"data":[{"id":"model-a"}],"has_more":true,"last_id":"model-a"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"model-b"}],"has_more":false,"last_id":"model-b"}`))
	}))
	defer srv.Close()

	got, err := listProvider(t, srv).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(got) != 2 || got[0].ID != "model-a" || got[1].ID != "model-b" {
		t.Fatalf("got %+v, want both pages in order", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(afterIDs) != 2 || afterIDs[0] != "" || afterIDs[1] != "model-a" {
		t.Errorf("after_id sequence = %v, want [\"\" \"model-a\"]", afterIDs)
	}
}

// TestListModelsPaginationBounded stops a backend that always reports has_more
// instead of looping forever.
func TestListModelsPaginationBounded(t *testing.T) {
	var mu sync.Mutex
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}],"has_more":true,"last_id":"m"}`))
	}))
	defer srv.Close()

	if _, err := listProvider(t, srv).ListModels(context.Background()); err == nil {
		t.Fatal("want an error when pagination never terminates")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != maxListPages {
		t.Errorf("made %d requests, want the %d-page cap", calls, maxListPages)
	}
}

// TestListModelsEmpty asserts that a vendor listing nothing is a SUCCESS with
// an empty slice — not an error. The consumer distinguishes "vendor listed
// nothing" from "the call failed".
func TestListModelsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[],"has_more":false}`))
	}))
	defer srv.Close()

	got, err := listProvider(t, srv).ListModels(context.Background())
	if err != nil {
		t.Fatalf("empty listing must not error, got %v", err)
	}
	if got == nil {
		t.Fatal("want a non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("got %d models, want 0", len(got))
	}
}

// TestListModelsSkipsEmptyID drops entries that name no model.
func TestListModelsSkipsEmptyID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":""},{"id":"claude-real"}],"has_more":false}`))
	}))
	defer srv.Close()

	got, err := listProvider(t, srv).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(got) != 1 || got[0].ID != "claude-real" {
		t.Fatalf("got %+v, want only the identified model", got)
	}
}

// TestListModelsHTTPError surfaces a non-2xx as the adapter's *Error.
func TestListModelsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"type":"api_error","message":"internal failure"}}`))
	}))
	defer srv.Close()

	_, err := listProvider(t, srv).ListModels(context.Background())
	if err == nil {
		t.Fatal("want an error on HTTP 500")
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *Error, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want 500", apiErr.StatusCode)
	}
	if apiErr.Message != "internal failure" {
		t.Errorf("Message = %q, want the parsed API message", apiErr.Message)
	}
}

// TestListModelsMalformedBody rejects a 200 whose body is not the expected
// JSON, rather than returning a silently empty catalogue.
func TestListModelsMalformedBody(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"garbage", "not json at all"},
		{"truncated", `{"data":[{"id":"claude-x"`},
		{"wrong shape", `{"data":"a string, not a list"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			got, err := listProvider(t, srv).ListModels(context.Background())
			if err == nil {
				t.Fatalf("want a decode error, got %+v", got)
			}
			if !strings.Contains(err.Error(), "decode response") {
				t.Errorf("error = %v, want it to name the decode failure", err)
			}
			var syntaxErr *json.SyntaxError
			var typeErr *json.UnmarshalTypeError
			if !errors.As(err, &syntaxErr) && !errors.As(err, &typeErr) {
				t.Errorf("error = %v, want the JSON error preserved for unwrapping", err)
			}
		})
	}
}

// TestListModelsContextCancelled aborts an in-flight listing when the caller's
// context is cancelled, and reports the cancellation cause.
func TestListModelsContextCancelled(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done() // hold the response open until the client gives up
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-started
		cancel()
	}()

	_, err := listProvider(t, srv).ListModels(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

// TestListModelsContextDeadline reports a deadline that expires mid-call.
func TestListModelsContextDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := listProvider(t, srv).ListModels(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}
}

// TestListModelsCredentialError fails before any request when credentials
// cannot be resolved.
func TestListModelsCredentialError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("no request may be made without a credential")
	}))
	defer srv.Close()

	p := New("claude-sonnet-5", provider.StaticCredentialSource{}, // empty token -> error
		WithBaseURL(srv.URL), WithHTTPClient(pinnedClient(t, srv.URL)))
	if _, err := p.ListModels(context.Background()); err == nil {
		t.Fatal("want a credential error")
	}
}

// TestListModelsOAuthHeaders confirms the listing reuses the streaming path's
// credential-kind header logic.
func TestListModelsOAuthHeaders(t *testing.T) {
	var auth, apiKey, agent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, apiKey, agent = r.Header.Get("Authorization"), r.Header.Get("X-Api-Key"), r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{"data":[],"has_more":false}`))
	}))
	defer srv.Close()

	p := New("claude-sonnet-5",
		provider.StaticCredentialSource{Cred: provider.Credential{Kind: provider.CredOAuth, Token: "oauth-tok"}},
		WithBaseURL(srv.URL), WithHTTPClient(pinnedClient(t, srv.URL)))
	if _, err := p.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if auth != "Bearer oauth-tok" {
		t.Errorf("Authorization = %q, want the bearer token", auth)
	}
	if apiKey != "" {
		t.Errorf("X-Api-Key = %q, want it omitted for OAuth", apiKey)
	}
	if !strings.HasPrefix(agent, "claude-cli/") {
		t.Errorf("User-Agent = %q, want the CLI convention", agent)
	}
}
