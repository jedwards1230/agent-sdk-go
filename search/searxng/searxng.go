// Package searxng implements [search.Provider] against a self-hosted SearXNG
// instance's JSON search API (GET /search?format=json). It registers itself
// under the name "searxng" via search.Register; importing this package for
// its side effect (blank import is enough) makes "searxng" available to
// search.Build.
package searxng

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/jedwards1230/agent-sdk-go/search"
)

// providerName is the search.Register key and search.Results.Provider value.
const providerName = "searxng"

const searchPath = "/search"

func init() {
	search.Register(providerName, New)
}

// Provider is a SearXNG JSON-API backend for one instance. It is safe for
// concurrent use.
type Provider struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	maxResults int
}

// New constructs a SearXNG provider from cfg. cfg.BaseURL is required — a
// self-hosted SearXNG instance has no universal default, so a missing
// BaseURL is a config error rather than silently pointing at some other
// operator's public instance. cfg.APIKey is optional: SearXNG itself has no
// native API-key concept, but when set it is sent as a bearer token for
// deployments fronted by an auth proxy. cfg.HTTPClient and cfg.MaxResults
// are optional; see [search.Config].
func New(cfg search.Config) (search.Provider, error) {
	if cfg.BaseURL == "" {
		return nil, &search.Error{Provider: providerName, Kind: search.ErrKindConfig, Err: errors.New("BaseURL is required (no default self-hosted instance)")}
	}
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return &Provider{
		apiKey:     cfg.APIKey,
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		httpClient: client,
		maxResults: cfg.MaxResults,
	}, nil
}

// Name returns "searxng".
func (p *Provider) Name() string { return providerName }

// Search issues one query against the instance's JSON search endpoint. See
// [search.Provider.Search].
func (p *Provider) Search(ctx context.Context, query string, opts search.Options) (*search.Results, error) {
	if query == "" {
		return nil, &search.Error{Provider: providerName, Kind: search.ErrKindConfig, Err: errors.New("query is empty")}
	}
	n := search.ClampMaxResults(p.maxResults, opts.MaxResults)

	u, err := url.Parse(p.baseURL + searchPath)
	if err != nil {
		return nil, &search.Error{Provider: providerName, Kind: search.ErrKindRequest, Err: err}
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("format", "json")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &search.Error{Provider: providerName, Kind: search.ErrKindRequest, Err: err}
	}
	req.Header.Set("Accept", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, &search.Error{Provider: providerName, Kind: search.ErrKindRequest, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &search.Error{
			Provider:   providerName,
			Kind:       search.ErrKindHTTP,
			StatusCode: resp.StatusCode,
			Err:        fmt.Errorf("%s", strings.TrimSpace(string(body))),
		}
	}

	var raw response
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, &search.Error{Provider: providerName, Kind: search.ErrKindDecode, Err: err}
	}

	// SearXNG's JSON API has no server-side result-count parameter; n is
	// applied client-side by slicing the parsed response.
	items := raw.Results
	truncated := len(items) > n
	if truncated {
		items = items[:n]
	}
	out := &search.Results{Query: query, Provider: providerName, Truncated: truncated, Items: make([]search.Result, 0, len(items))}
	for i, it := range items {
		out.Items = append(out.Items, search.Result{
			Rank:    i + 1,
			Title:   it.Title,
			URL:     it.URL,
			Snippet: search.TruncateSnippet(it.Content),
		})
	}
	return out, nil
}

// response is the subset of the SearXNG JSON search-API shape this adapter
// reads.
type response struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	} `json:"results"`
}
