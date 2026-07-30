// Package brave implements [search.Provider] against the Brave Search API
// (https://api.search.brave.com/res/v1/web/search). It registers itself
// under the name "brave" via search.Register; importing this package for its
// side effect (blank import is enough) makes "brave" available to
// search.Build.
package brave

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jedwards1230/agent-sdk-go/search"
)

// providerName is the search.Register key and search.Results.Provider value.
const providerName = "brave"

// defaultBaseURL is Brave's one production endpoint. Config.BaseURL exists
// only to override it for tests and proxies.
const defaultBaseURL = "https://api.search.brave.com"

const searchPath = "/res/v1/web/search"

func init() {
	search.Register(providerName, New)
}

// Provider is a Brave Search API backend. It is safe for concurrent use.
type Provider struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	maxResults int
}

// New constructs a Brave Search provider from cfg. cfg.APIKey is required —
// Brave rejects unauthenticated requests, so a missing key is a config error
// rather than a deferred request failure. cfg.BaseURL, cfg.HTTPClient, and
// cfg.MaxResults are optional; see [search.Config].
func New(cfg search.Config) (search.Provider, error) {
	if cfg.APIKey == "" {
		return nil, &search.Error{Provider: providerName, Kind: search.ErrKindConfig, Err: errors.New("APIKey is required")}
	}
	base := cfg.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return &Provider{
		apiKey:     cfg.APIKey,
		baseURL:    strings.TrimRight(base, "/"),
		httpClient: client,
		maxResults: cfg.MaxResults,
	}, nil
}

// Name returns "brave".
func (p *Provider) Name() string { return providerName }

// Search issues one query against the Brave Search web endpoint. See
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
	q.Set("count", strconv.Itoa(n))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &search.Error{Provider: providerName, Kind: search.ErrKindRequest, Err: err}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", p.apiKey)

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

	items := raw.Web.Results
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
			Snippet: search.TruncateSnippet(it.Description),
		})
	}
	return out, nil
}

// response is the subset of the Brave Search web-results JSON shape this
// adapter reads. Brave's response carries substantially more (news, videos,
// FAQ, discussions, …); only the web results project onto [search.Result].
type response struct {
	Web struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
		} `json:"results"`
	} `json:"web"`
}
