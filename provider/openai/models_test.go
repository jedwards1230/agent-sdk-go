package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
//     127.0.0.1, so no request is ever addressed to api.openai.com or the Codex
//     backend.
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

// testProvider builds a Provider wired to srv and pinned to its host.
func testProvider(t *testing.T, srv *httptest.Server) *Provider {
	t.Helper()
	return New("gpt-5",
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

	p := New("gpt-5",
		provider.StaticCredentialSource{Cred: provider.Credential{Kind: provider.CredAPIKey, Token: "sk-test"}},
		WithHTTPClient(pinnedClient(t, srv.URL)),
	)
	if _, err := p.ListModels(context.Background()); !errors.Is(err, errOffHost) {
		t.Fatalf("want errOffHost, got %v", err)
	}
}

// TestListModelsSuccess parses a well-formed 200 and asserts the request shape
// and the honest ModelInfo mapping.
func TestListModelsSuccess(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object": "list",
			"data": [
				{"id":"gpt-5","object":"model","created":1746057600,"owned_by":"openai"},
				{"id":"o4-mini","object":"model","created":1744243200,"owned_by":"openai"}
			]
		}`))
	}))
	defer srv.Close()

	got, err := testProvider(t, srv).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != modelsPath {
		t.Errorf("path = %q, want %q", gotPath, modelsPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want the configured credential", gotAuth)
	}

	want := []provider.ModelInfo{
		{ID: "gpt-5", Provider: "openai", Unregistered: true},
		{ID: "o4-mini", Provider: "openai", Unregistered: true},
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
// anyone backfills from the registry, which does carry gpt-5.
func TestListModelsReportsUnknownMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5","object":"model"}]}`))
	}))
	defer srv.Close()

	got, err := testProvider(t, srv).ListModels(context.Background())
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
	if reg, ok := provider.Lookup(m.ID); ok && m.ContextWindow == reg.ContextWindow && reg.ContextWindow != 0 {
		t.Error("ListModels backfilled registry metadata; merging is the caller's job")
	}
}

// TestListModelsEmpty asserts that a vendor listing nothing is a SUCCESS with
// an empty slice — not an error. The consumer distinguishes "vendor listed
// nothing" from "the call failed".
func TestListModelsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer srv.Close()

	got, err := testProvider(t, srv).ListModels(context.Background())
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
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":""},{"id":"gpt-real"}]}`))
	}))
	defer srv.Close()

	got, err := testProvider(t, srv).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(got) != 1 || got[0].ID != "gpt-real" {
		t.Fatalf("got %+v, want only the identified model", got)
	}
}

// TestListModelsHTTPError surfaces a non-200 as *APIError.
func TestListModelsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"internal failure"}}`))
	}))
	defer srv.Close()

	_, err := testProvider(t, srv).ListModels(context.Background())
	if err == nil {
		t.Fatal("want an error on HTTP 500")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want 500", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Body, "internal failure") {
		t.Errorf("Body = %q, want the response body retained", apiErr.Body)
	}
}

// TestListModelsMalformedBody rejects a 200 whose body is not the expected
// JSON, rather than returning a silently empty catalogue.
func TestListModelsMalformedBody(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"garbage", "not json at all"},
		{"truncated", `{"data":[{"id":"gpt-x"`},
		{"wrong shape", `{"data":"a string, not a list"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			got, err := testProvider(t, srv).ListModels(context.Background())
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

	_, err := testProvider(t, srv).ListModels(ctx)
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

	_, err := testProvider(t, srv).ListModels(ctx)
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

	p := New("gpt-5", provider.StaticCredentialSource{}, // empty token -> error
		WithBaseURL(srv.URL), WithHTTPClient(pinnedClient(t, srv.URL)))
	if _, err := p.ListModels(context.Background()); err == nil {
		t.Fatal("want a credential error")
	}
}

// TestListModelsOAuthHeaders confirms the listing reuses the streaming path's
// credential-kind routing, including the account header.
func TestListModelsOAuthHeaders(t *testing.T) {
	var auth, account string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, account = r.Header.Get("Authorization"), r.Header.Get(headerAccountID)
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer srv.Close()

	if _, err := codexProvider(t, srv).ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if auth != "Bearer oauth-tok" {
		t.Errorf("Authorization = %q, want the bearer token", auth)
	}
	if account != "acct-123" {
		t.Errorf("%s = %q, want the account id", headerAccountID, account)
	}
}

// --- Codex (OAuth) route -------------------------------------------------
//
// The Codex backend and the public API do not share a listing contract: Codex
// requires a client_version query parameter and answers {"models":[{"slug":…}]}
// where the public API takes no parameters and answers {"data":[{"id":…}]}.
// The tests below pin both halves of that difference.

// codexProvider builds a Provider on the OAuth/Codex route, pinned to srv.
func codexProvider(t *testing.T, srv *httptest.Server, opts ...Option) *Provider {
	t.Helper()
	return New("gpt-5",
		provider.StaticCredentialSource{Cred: provider.Credential{
			Kind: provider.CredOAuth, Token: "oauth-tok", Account: "acct-123",
		}},
		append([]Option{WithBaseURL(srv.URL), WithHTTPClient(pinnedClient(t, srv.URL))}, opts...)...)
}

// TestListModelsCodexSendsClientVersion pins the fix for the HTTP 400 the Codex
// backend returns when client_version is absent: without this parameter the
// listing can never succeed for an OAuth credential.
func TestListModelsCodexSendsClientVersion(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer srv.Close()

	if _, err := codexProvider(t, srv).ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if !gotQuery.Has(codexClientVersionParam) {
		t.Fatalf("query = %v, want %s sent; the backend answers 400 without it",
			gotQuery, codexClientVersionParam)
	}
	if got := gotQuery.Get(codexClientVersionParam); got != defaultCodexClientVersion {
		t.Errorf("%s = %q, want %q", codexClientVersionParam, got, defaultCodexClientVersion)
	}
}

// TestListModelsCodexClientVersionOverride confirms the value is injectable,
// since the parameter is undocumented and may start being validated.
func TestListModelsCodexClientVersionOverride(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get(codexClientVersionParam)
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer srv.Close()

	p := codexProvider(t, srv, WithCodexClientVersion("9.9.9"))
	if _, err := p.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if got != "9.9.9" {
		t.Errorf("%s = %q, want the override", codexClientVersionParam, got)
	}

	// An empty override must not blank the parameter — that reproduces the 400.
	p = codexProvider(t, srv, WithCodexClientVersion(""))
	if _, err := p.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if got != defaultCodexClientVersion {
		t.Errorf("%s = %q, want the empty override ignored", codexClientVersionParam, got)
	}
}

// TestListModelsAPIKeyOmitsClientVersion keeps the Codex-only parameter off the
// public API, which never asked for it.
func TestListModelsAPIKeyOmitsClientVersion(t *testing.T) {
	var raw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer srv.Close()

	if _, err := testProvider(t, srv).ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if raw != "" {
		t.Errorf("query = %q, want none on the API-key route", raw)
	}
}

// TestListModelsCodexShape is the regression test for a silently empty
// catalogue: the Codex body keys models on "models"/"slug", and decoding it
// with the public API's {"data":[{"id"}]} decoder does NOT fail — it returns
// zero models and a nil error, which is worse than the 400 it replaced.
func TestListModelsCodexShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"models":[
			{"slug":"gpt-5-codex","display_name":"GPT-5-Codex","description":"d",
			 "max_context_window":272000,"visibility":"list",
			 "supported_reasoning_levels":["low","medium","high"],
			 "available_in_plans":["plus","pro"]},
			{"slug":"gpt-5","display_name":"GPT-5","visibility":"hide"}
		]}`))
	}))
	defer srv.Close()

	got, err := codexProvider(t, srv).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	want := []provider.ModelInfo{
		{ID: "gpt-5-codex", Provider: "openai", Unregistered: true},
		{ID: "gpt-5", Provider: "openai", Unregistered: true},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d models, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("model[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	// The Codex catalogue reports no price — subscription models have none —
	// so pricing stays zero meaning UNKNOWN and is never synthesized.
	for _, m := range got {
		if m.Pricing != (provider.Pricing{}) {
			t.Errorf("pricing invented for %s: %+v", m.ID, m.Pricing)
		}
		if m.ContextWindow != 0 || m.MaxOutput != 0 {
			t.Errorf("limits invented for %s: %+v", m.ID, m)
		}
	}
}

// TestListModelsCodexSkipsEmptySlug drops entries that name no model.
func TestListModelsCodexSkipsEmptySlug(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"slug":""},{"slug":"gpt-real"}]}`))
	}))
	defer srv.Close()

	got, err := codexProvider(t, srv).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(got) != 1 || got[0].ID != "gpt-real" {
		t.Fatalf("got %+v, want only the identified model", got)
	}
}

// TestListModelsCodexEmpty keeps "the vendor listed nothing" a success.
func TestListModelsCodexEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer srv.Close()

	got, err := codexProvider(t, srv).ListModels(context.Background())
	if err != nil {
		t.Fatalf("empty listing must not error, got %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("got %+v, want a non-nil empty slice", got)
	}
}

// TestListModelsCodexWrongShape reports a body without a models key as a
// failure rather than as an empty catalogue — the distinction the silently
// empty decode destroyed.
func TestListModelsCodexWrongShape(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"public api shape", `{"object":"list","data":[{"id":"gpt-5"}]}`},
		{"unrelated object", `{}`},
		{"wrong type", `{"models":"a string, not a list"}`},
		{"garbage", "not json at all"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			got, err := codexProvider(t, srv).ListModels(context.Background())
			if err == nil {
				t.Fatalf("want an error, got %+v", got)
			}
			if !strings.Contains(err.Error(), "decode response") {
				t.Errorf("error = %v, want it to name the decode failure", err)
			}
		})
	}
}
