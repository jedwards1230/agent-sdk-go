package brave_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/search"
	"github.com/jedwards1230/agent-sdk-go/search/brave"
)

// TestNewRequiresAPIKey asserts credentials come from Config, never a
// package-chosen env var or a baked-in default — an empty key is a config
// error, not a silent no-auth request.
func TestNewRequiresAPIKey(t *testing.T) {
	_, err := brave.New(search.Config{})
	if err == nil {
		t.Fatal("New with empty APIKey: want an error, got nil")
	}
	var searchErr *search.Error
	if !errors.As(err, &searchErr) || searchErr.Kind != search.ErrKindConfig {
		t.Fatalf("New with empty APIKey: err = %v, want a *search.Error with Kind ErrKindConfig", err)
	}
}

// TestSearchAppliesCredentialsAndBaseURL asserts the constructed provider
// sends the configured API key and hits the configured (not default)
// base URL — proving credentials/endpoint are Config-supplied.
func TestSearchAppliesCredentialsAndBaseURL(t *testing.T) {
	var gotKey, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Subscription-Token")
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"web":{"results":[]}}`))
	}))
	defer srv.Close()

	p, err := brave.New(search.Config{APIKey: "secret-token", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := p.Search(context.Background(), "golang", search.Options{}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotKey != "secret-token" {
		t.Errorf("X-Subscription-Token = %q, want %q", gotKey, "secret-token")
	}
	if gotPath != "/res/v1/web/search" {
		t.Errorf("request path = %q, want the Brave web-search path", gotPath)
	}
}

// TestSearchNormalResults covers a well-formed, non-empty result set,
// including the Results.Provider/Name and rank assignment.
func TestSearchNormalResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"web":{"results":[
			{"title":"Go Programming Language","url":"https://go.dev","description":"The Go home page"},
			{"title":"Effective Go","url":"https://go.dev/doc/effective_go","description":"Tips for writing clear Go"}
		]}}`))
	}))
	defer srv.Close()

	p, err := brave.New(search.Config{APIKey: "k", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := p.Search(context.Background(), "golang", search.Options{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got.Provider != "brave" {
		t.Errorf("Provider = %q, want %q", got.Provider, "brave")
	}
	if got.Query != "golang" {
		t.Errorf("Query = %q, want %q", got.Query, "golang")
	}
	if len(got.Items) != 2 {
		t.Fatalf("len(Items) = %d, want 2", len(got.Items))
	}
	if got.Items[0].Rank != 1 || got.Items[1].Rank != 2 {
		t.Errorf("Items ranks = %d,%d, want 1,2", got.Items[0].Rank, got.Items[1].Rank)
	}
	if got.Items[0].Title != "Go Programming Language" || got.Items[0].URL != "https://go.dev" {
		t.Errorf("Items[0] = %+v, unexpected", got.Items[0])
	}
	if got.Truncated {
		t.Errorf("Truncated = true, want false (fewer results than the bound)")
	}
	if p.Name() != "brave" {
		t.Errorf("Name() = %q, want %q", p.Name(), "brave")
	}
}

// TestSearchEmptyResults asserts an empty result set is a success, not an
// error.
func TestSearchEmptyResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"web":{"results":[]}}`))
	}))
	defer srv.Close()

	p, err := brave.New(search.Config{APIKey: "k", BaseURL: srv.URL})
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
		{name: "unauthorized", status: http.StatusUnauthorized},
		{name: "rate limited", status: http.StatusTooManyRequests},
		{name: "server error", status: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(`{"error":"denied"}`))
			}))
			defer srv.Close()

			p, err := brave.New(search.Config{APIKey: "k", BaseURL: srv.URL})
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
		{"truncated", `{"web":{"results":[{"title":"x"`},
		{"wrong shape", `{"web":"a string, not an object"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			p, err := brave.New(search.Config{APIKey: "k", BaseURL: srv.URL})
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

	p, err := brave.New(search.Config{APIKey: "k", BaseURL: srv.URL})
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
		_, _ = w.Write([]byte(`{"web":{"results":[
			{"title":"a","url":"https://a.example","description":"a"},
			{"title":"b","url":"https://b.example","description":"b"},
			{"title":"c","url":"https://c.example","description":"c"}
		]}}`))
	}))
	defer srv.Close()

	p, err := brave.New(search.Config{APIKey: "k", BaseURL: srv.URL})
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
