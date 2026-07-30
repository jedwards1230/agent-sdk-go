package searxng_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/search"
	"github.com/jedwards1230/agent-sdk-go/search/searxng"
)

// TestNewRequiresBaseURL asserts the instance endpoint comes from Config,
// never a baked-in default pointing at someone else's SearXNG instance.
func TestNewRequiresBaseURL(t *testing.T) {
	_, err := searxng.New(search.Config{})
	if err == nil {
		t.Fatal("New with empty BaseURL: want an error, got nil")
	}
	var searchErr *search.Error
	if !errors.As(err, &searchErr) || searchErr.Kind != search.ErrKindConfig {
		t.Fatalf("New with empty BaseURL: err = %v, want a *search.Error with Kind ErrKindConfig", err)
	}
}

// TestSearchAppliesBaseURLAndOptionalKey asserts the configured (fake, obviously
// non-production) base URL is used and an optional API key rides as a bearer
// token when set — never a package-chosen default host.
func TestSearchAppliesBaseURLAndOptionalKey(t *testing.T) {
	var gotAuth, gotPath, gotFormat string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotFormat = r.URL.Query().Get("format")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	p, err := searxng.New(search.Config{BaseURL: srv.URL, APIKey: "proxy-token"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := p.Search(context.Background(), "golang", search.Options{}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotAuth != "Bearer proxy-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer proxy-token")
	}
	if gotPath != "/search" {
		t.Errorf("request path = %q, want %q", gotPath, "/search")
	}
	if gotFormat != "json" {
		t.Errorf("format param = %q, want %q", gotFormat, "json")
	}
}

// TestSearchNoAPIKeySendsNoAuthHeader asserts an unset APIKey never sends an
// Authorization header — a plain self-hosted instance with no auth proxy.
func TestSearchNoAPIKeySendsNoAuthHeader(t *testing.T) {
	var gotAuth string
	var sawHeader bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, sawHeader = r.Header.Get("Authorization"), r.Header.Get("Authorization") != ""
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	p, err := searxng.New(search.Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := p.Search(context.Background(), "golang", search.Options{}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if sawHeader {
		t.Errorf("Authorization header = %q, want none when APIKey is unset", gotAuth)
	}
}

// TestSearchNormalResults covers a well-formed, non-empty result set.
func TestSearchNormalResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[
			{"title":"Go Programming Language","url":"https://go.dev","content":"The Go home page"},
			{"title":"Effective Go","url":"https://go.dev/doc/effective_go","content":"Tips for writing clear Go"}
		]}`))
	}))
	defer srv.Close()

	p, err := searxng.New(search.Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := p.Search(context.Background(), "golang", search.Options{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got.Provider != "searxng" {
		t.Errorf("Provider = %q, want %q", got.Provider, "searxng")
	}
	if len(got.Items) != 2 {
		t.Fatalf("len(Items) = %d, want 2", len(got.Items))
	}
	if got.Items[0].Rank != 1 || got.Items[1].Rank != 2 {
		t.Errorf("Items ranks = %d,%d, want 1,2", got.Items[0].Rank, got.Items[1].Rank)
	}
	if p.Name() != "searxng" {
		t.Errorf("Name() = %q, want %q", p.Name(), "searxng")
	}
}

// TestSearchEmptyResults asserts an empty result set is a success, not an
// error.
func TestSearchEmptyResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	p, err := searxng.New(search.Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := p.Search(context.Background(), "no such thing exists anywhere", search.Options{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got.Items) != 0 {
		t.Errorf("len(Items) = %d, want 0", len(got.Items))
	}
	if got.Truncated {
		t.Errorf("Truncated = true, want false")
	}
}

// TestSearchHTTPError surfaces a non-2xx response as a *search.Error naming
// the status code.
func TestSearchHTTPError(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{name: "not found", status: http.StatusNotFound},
		{name: "bad gateway", status: http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte("upstream error"))
			}))
			defer srv.Close()

			p, err := searxng.New(search.Config{BaseURL: srv.URL})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_, err = p.Search(context.Background(), "golang", search.Options{})
			if err == nil {
				t.Fatal("Search: want an error, got nil")
			}
			var searchErr *search.Error
			if !errors.As(err, &searchErr) {
				t.Fatalf("Search err = %v (%T), want a *search.Error", err, err)
			}
			if searchErr.Kind != search.ErrKindHTTP {
				t.Errorf("Kind = %v, want ErrKindHTTP", searchErr.Kind)
			}
			if searchErr.StatusCode != tt.status {
				t.Errorf("StatusCode = %d, want %d", searchErr.StatusCode, tt.status)
			}
		})
	}
}

// TestSearchMalformedJSON surfaces a body that fails to decode as the
// expected shape, rather than returning an empty result set silently.
func TestSearchMalformedJSON(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"not json", "this is not json"},
		{"truncated", `{"results":[{"title":"x"`},
		{"wrong shape", `{"results":"a string, not a list"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			p, err := searxng.New(search.Config{BaseURL: srv.URL})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_, err = p.Search(context.Background(), "golang", search.Options{})
			if err == nil {
				t.Fatal("Search: want a decode error, got nil")
			}
			var searchErr *search.Error
			if !errors.As(err, &searchErr) || searchErr.Kind != search.ErrKindDecode {
				t.Fatalf("Search err = %v, want a *search.Error with Kind ErrKindDecode", err)
			}
		})
	}
}

// TestSearchContextCancelled aborts an in-flight request when the caller's
// context is cancelled mid-request, and reports the cancellation cause.
func TestSearchContextCancelled(t *testing.T) {
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

	p, err := searxng.New(search.Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = p.Search(ctx, "golang", search.Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Search err = %v, want context.Canceled", err)
	}
}

// TestSearchResultBound asserts Options.MaxResults bounds the returned slice
// and sets Truncated when the backend has more than the bound.
func TestSearchResultBound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[
			{"title":"a","url":"https://a.example","content":"a"},
			{"title":"b","url":"https://b.example","content":"b"},
			{"title":"c","url":"https://c.example","content":"c"}
		]}`))
	}))
	defer srv.Close()

	p, err := searxng.New(search.Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := p.Search(context.Background(), "golang", search.Options{MaxResults: 2})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("len(Items) = %d, want 2", len(got.Items))
	}
	if !got.Truncated {
		t.Errorf("Truncated = false, want true (3 available, 2 requested)")
	}
}
