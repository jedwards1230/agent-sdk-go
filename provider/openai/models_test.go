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
		// Element shape and effort vocabulary are the catalogue's real ones. Only
		// the "low" description is a verbatim sampled string; the other entries
		// carry effort alone, which doubles as coverage that description is
		// optional to this decoder.
		_, _ = w.Write([]byte(`{"models":[
			{"slug":"gpt-5.6-sol","display_name":"GPT-5.6 Sol","description":"d",
			 "max_context_window":272000,"visibility":"list",
			 "supported_reasoning_levels":[
			   {"effort":"low","description":"Fast responses with lighter reasoning"},
			   {"effort":"medium"},{"effort":"high"},
			   {"effort":"xhigh"},{"effort":"max"},{"effort":"ultra"}],
			 "available_in_plans":["plus","pro"]},
			{"slug":"gpt-5","display_name":"GPT-5","visibility":"hide"}
		]}`))
	}))
	defer srv.Close()

	got, err := codexProvider(t, srv).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	// Four of the Codex-only fields are carried — display_name, the context
	// window, visibility normalized to Hidden, and supported_reasoning_levels
	// collapsed to the Reasoning bit. The rest (description, available_in_plans,
	// and the reasoning level values themselves) have no home on ModelInfo and
	// are still dropped, which the exact struct comparison below enforces: any
	// field this adapter starts inventing shows up as a diff.
	want := []provider.ModelInfo{
		{
			ID: "gpt-5.6-sol", Provider: "openai", DisplayName: "GPT-5.6 Sol",
			ContextWindow: 272000, Reasoning: true, Unregistered: true,
		},
		{
			ID: "gpt-5", Provider: "openai", DisplayName: "GPT-5",
			Hidden: true, Unregistered: true,
		},
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
	// so pricing stays zero meaning UNKNOWN and is never synthesized, and nor is
	// a max output, which it also never reports. Reasoning IS reported, via
	// supported_reasoning_levels: every level the catalogue names is a reasoning
	// EFFORT and the vocabulary has no "none" member, so a non-empty list is
	// positive vendor evidence. The per-model split is asserted above — the
	// entry that lists levels reasons, the one that lists none stays false.
	for _, m := range got {
		if m.Pricing != (provider.Pricing{}) {
			t.Errorf("pricing invented for %s: %+v", m.ID, m.Pricing)
		}
		if m.MaxOutput != 0 {
			t.Errorf("unreported metadata invented for %s: %+v", m.ID, m)
		}
	}
	// gpt-5 IS in the embedded registry, with a real context window and real
	// pricing. It must come back bare anyway: merging the registry into a live
	// listing is the caller's decision. This is the backfill guard.
	reg, ok := provider.Lookup("gpt-5")
	if !ok || reg.ContextWindow == 0 || reg.Pricing == (provider.Pricing{}) {
		t.Fatal("test premise broken: gpt-5 must be registered with a window and pricing")
	}
	if got[1].ContextWindow != 0 {
		t.Errorf("gpt-5 ContextWindow = %d; the Codex entry reported none, so it must stay unknown",
			got[1].ContextWindow)
	}
	if got[1].Pricing != (provider.Pricing{}) {
		t.Errorf("gpt-5 pricing backfilled from the registry: %+v", got[1].Pricing)
	}
	if !got[1].Unregistered {
		t.Error("gpt-5 Unregistered = false; a listing record is never registry-sourced")
	}
}

// TestListModelsCodexContextWindow pins the precedence between the catalogue's
// two window spellings, and — more importantly — that neither being present
// yields UNKNOWN rather than an invented number.
func TestListModelsCodexContextWindow(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry string
		want  int
	}{
		{
			// Both present and DIFFERENT, so a swapped precedence cannot pass.
			name:  "context_window wins over max_context_window",
			entry: `{"slug":"m","context_window":111000,"max_context_window":272000}`,
			want:  111000,
		},
		{
			name:  "max_context_window used when context_window absent",
			entry: `{"slug":"m","max_context_window":272000}`,
			want:  272000,
		},
		{
			// An explicit zero is not a real limit, so it falls back too.
			name:  "max_context_window used when context_window is zero",
			entry: `{"slug":"m","context_window":0,"max_context_window":272000}`,
			want:  272000,
		},
		{
			name:  "neither present is unknown",
			entry: `{"slug":"m"}`,
			want:  0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"models":[` + tc.entry + `]}`))
			}))
			defer srv.Close()

			got, err := codexProvider(t, srv).ListModels(context.Background())
			if err != nil {
				t.Fatalf("ListModels: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d models, want 1: %+v", len(got), got)
			}
			if got[0].ContextWindow != tc.want {
				t.Errorf("ContextWindow = %d, want %d", got[0].ContextWindow, tc.want)
			}
		})
	}
}

// TestListModelsCodexVisibility pins the fail-open normalization of the
// vendor's visibility marker: only the exact value "hide" hides a model, so an
// unrecognized or absent marker can never make the catalogue disappear.
func TestListModelsCodexVisibility(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry string
		want  bool
	}{
		{"hide hides", `{"slug":"m","visibility":"hide"}`, true},
		{"list does not", `{"slug":"m","visibility":"list"}`, false},
		{"unrecognized fails open", `{"slug":"m","visibility":"archived"}`, false},
		{"absent fails open", `{"slug":"m"}`, false},
		{"empty fails open", `{"slug":"m","visibility":""}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"models":[` + tc.entry + `]}`))
			}))
			defer srv.Close()

			got, err := codexProvider(t, srv).ListModels(context.Background())
			if err != nil {
				t.Fatalf("ListModels: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d models, want 1: %+v", len(got), got)
			}
			if got[0].Hidden != tc.want {
				t.Errorf("Hidden = %v, want %v", got[0].Hidden, tc.want)
			}
		})
	}
}

// TestListModelsCodexReasoning pins the collapse of supported_reasoning_levels
// into the Reasoning bit. The vendor names reasoning EFFORTS ("low", "medium",
// "high", "xhigh", "max", "ultra") with no "none" member in the vocabulary, so a
// non-empty list is positive evidence the model reasons and an empty or absent
// one leaves the bit false — which on an Unregistered record reads as UNKNOWN.
//
// The comparison is against the whole struct on purpose: it is the same
// anti-invention guard as [TestListModelsCodexShape], so a mapping that starts
// filling in some other field fails here too.
func TestListModelsCodexReasoning(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry string
		want  bool
	}{
		{
			// The real element shape, with a verbatim sampled description.
			name:  "object shape with efforts",
			entry: `{"slug":"m","supported_reasoning_levels":[{"effort":"low","description":"Fast responses with lighter reasoning"},{"effort":"ultra"}]}`,
			want:  true,
		},
		{
			// Not the live shape, but a plausible enough spelling to accept
			// rather than read as "no reasoning".
			name:  "flat string shape is tolerated",
			entry: `{"slug":"m","supported_reasoning_levels":["low","medium"]}`,
			want:  true,
		},
		{
			name:  "empty array is not evidence",
			entry: `{"slug":"m","supported_reasoning_levels":[]}`,
			want:  false,
		},
		{
			name:  "absent field is unknown",
			entry: `{"slug":"m"}`,
			want:  false,
		},
		{
			name:  "null is unknown",
			entry: `{"slug":"m","supported_reasoning_levels":null}`,
			want:  false,
		},
		{
			// Objects that name no effort name no level.
			name:  "objects with empty effort do not count",
			entry: `{"slug":"m","supported_reasoning_levels":[{"description":"d"}]}`,
			want:  false,
		},
		{
			name:  "bare string degrades to false",
			entry: `{"slug":"m","supported_reasoning_levels":"high"}`,
			want:  false,
		},
		{
			name:  "object degrades to false",
			entry: `{"slug":"m","supported_reasoning_levels":{"a":1}}`,
			want:  false,
		},
		{
			name:  "number list degrades to false",
			entry: `{"slug":"m","supported_reasoning_levels":[1,2,3]}`,
			want:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"models":[` + tc.entry + `]}`))
			}))
			defer srv.Close()

			got, err := codexProvider(t, srv).ListModels(context.Background())
			if err != nil {
				t.Fatalf("ListModels: %v", err)
			}
			want := provider.ModelInfo{
				ID: "m", Provider: "openai", Reasoning: tc.want, Unregistered: true,
			}
			if len(got) != 1 {
				t.Fatalf("got %d models, want 1: %+v", len(got), got)
			}
			if got[0] != want {
				t.Errorf("model = %+v, want %+v", got[0], want)
			}
		})
	}
}

// TestListModelsCodexReasoningPerModel proves the bit is per-model data read
// from each entry, not a blanket flag set for the whole response.
//
// The first two slugs and their level sets are real, and they differ in SIZE
// (six levels vs four) — the vendor genuinely varies this per model, so a
// blanket flag would be wrong even among models that all reason. The third
// entry omits the field entirely: every model in the sampled response carried
// it, so that row is constructed rather than observed, and it is what pins the
// false half of the split.
func TestListModelsCodexReasoningPerModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"models":[
			{"slug":"gpt-5.6-sol","supported_reasoning_levels":[
			  {"effort":"low"},{"effort":"medium"},{"effort":"high"},
			  {"effort":"xhigh"},{"effort":"max"},{"effort":"ultra"}]},
			{"slug":"gpt-5.5","supported_reasoning_levels":[
			  {"effort":"low"},{"effort":"medium"},{"effort":"high"},
			  {"effort":"xhigh"}]},
			{"slug":"no-levels-reported"}
		]}`))
	}))
	defer srv.Close()

	got, err := codexProvider(t, srv).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	want := []provider.ModelInfo{
		{ID: "gpt-5.6-sol", Provider: "openai", Reasoning: true, Unregistered: true},
		{ID: "gpt-5.5", Provider: "openai", Reasoning: true, Unregistered: true},
		{ID: "no-levels-reported", Provider: "openai", Unregistered: true},
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

// TestListModelsCodexReasoningMalformedKeepsCatalogue is the degrade-never-fail
// guarantee. supported_reasoning_levels is advisory; a vendor shape change there
// must cost one capability bit and nothing else. If it ever fails the decode,
// the whole catalogue disappears behind an error — the same silently-broken
// listing failure class [TestListModelsCodexShape] exists to prevent.
func TestListModelsCodexReasoningMalformedKeepsCatalogue(t *testing.T) {
	for _, tc := range []struct{ name, levels string }{
		{"bare string", `"high"`},
		{"object", `{"a":1}`},
		{"nested garbage", `[["low"],["high"]]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"models":[
					{"slug":"a","supported_reasoning_levels":` + tc.levels + `},
					{"slug":"b","display_name":"B","context_window":1000},
					{"slug":"c","supported_reasoning_levels":[{"effort":"low"}]}
				]}`))
			}))
			defer srv.Close()

			got, err := codexProvider(t, srv).ListModels(context.Background())
			if err != nil {
				t.Fatalf("a malformed advisory field must not fail the listing, got %v", err)
			}
			want := []provider.ModelInfo{
				{ID: "a", Provider: "openai", Unregistered: true},
				{ID: "b", Provider: "openai", DisplayName: "B", ContextWindow: 1000, Unregistered: true},
				{ID: "c", Provider: "openai", Reasoning: true, Unregistered: true},
			}
			if len(got) != len(want) {
				t.Fatalf("got %d models, want the full catalogue of %d: %+v", len(got), len(want), got)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("model[%d] = %+v, want %+v", i, got[i], want[i])
				}
			}
		})
	}
}

// TestListModelsAPIKeyRouteIgnoresReasoningLevels keeps the capability bit off
// the public API's route. That endpoint reports identity only and never
// documents supported_reasoning_levels, so reading it there would mean trusting
// a field the endpoint does not claim to serve.
func TestListModelsAPIKeyRouteIgnoresReasoningLevels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[
			{"id":"gpt-5","supported_reasoning_levels":[{"effort":"high"}]}
		]}`))
	}))
	defer srv.Close()

	got, err := testProvider(t, srv).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	want := provider.ModelInfo{ID: "gpt-5", Provider: "openai", Unregistered: true}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestListModelsCodexDisplayName carries the vendor's label when it has one and
// leaves it empty — meaning UNKNOWN, with the id as the fallback label — when
// it does not. The adapter never synthesizes a name from the slug.
func TestListModelsCodexDisplayName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"models":[
			{"slug":"gpt-5-codex","display_name":"GPT-5-Codex"},
			{"slug":"gpt-5-nameless"}
		]}`))
	}))
	defer srv.Close()

	got, err := codexProvider(t, srv).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d models, want 2: %+v", len(got), got)
	}
	if got[0].DisplayName != "GPT-5-Codex" {
		t.Errorf("DisplayName = %q, want the vendor label", got[0].DisplayName)
	}
	if got[1].DisplayName != "" {
		t.Errorf("DisplayName = %q, want empty when the vendor supplied none", got[1].DisplayName)
	}
}

// TestListModelsAPIKeyRouteCarriesNoMetadata keeps the public API's decoder
// narrow. That endpoint reports none of the Codex catalogue's metadata, so even
// a body that happens to carry those keys must yield bare records: reading them
// there would mean trusting fields the endpoint does not actually document.
func TestListModelsAPIKeyRouteCarriesNoMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[
			{"id":"gpt-5","display_name":"GPT-5","context_window":400000,
			 "max_context_window":400000,"visibility":"hide"}
		]}`))
	}))
	defer srv.Close()

	got, err := testProvider(t, srv).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	want := []provider.ModelInfo{{ID: "gpt-5", Provider: "openai", Unregistered: true}}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("got %+v, want %+v", got, want)
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
