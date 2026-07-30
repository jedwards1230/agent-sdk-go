// Package search defines a provider-agnostic web search interface plus a
// name-keyed factory registry. It is an optional SDK package (extension tier
// 2, [PRD.md] "Extension tiers"): it performs real network I/O, so nothing in
// the SDK core imports it — an embedder opts in by importing search and a
// concrete backend package.
//
// Two implementations ship alongside it: [search/brave] (the Brave Search
// API) and [search/searxng] (a self-hosted SearXNG instance). Both register
// themselves under a name via [Register]; [Build] constructs a [Provider] by
// that name from a [Config]. Adding a third backend means writing one more
// package that implements [Provider] and calls [Register] from its own
// init() — nothing in this package changes.
//
// Credentials and endpoints are supplied by the embedder through [Config],
// never read from a package-chosen environment variable or a baked-in
// default instance — see [Config] and each implementation's New for what it
// requires.
package search

import "context"

// Provider is a web search backend: Search issues one query and returns a
// bounded, ranked result set. Implementations must be safe for concurrent
// use — a single Provider is expected to serve many overlapping Search
// calls — and must honor ctx cancellation and deadlines for the in-flight
// request.
type Provider interface {
	// Search issues one query and returns its results, ranked best-first and
	// bounded to the effective MaxResults for this call (see
	// [ClampMaxResults]). An empty result set is a successful, non-error
	// response ([Results.Items] is empty, not nil-vs-empty distinguishing
	// anything). A non-2xx response, a malformed response body, a request
	// build failure, or ctx cancellation/deadline all return a *[Error]
	// wrapping the cause.
	Search(ctx context.Context, query string, opts Options) (*Results, error)

	// Name returns the provider's registry name (the string it was
	// registered under via [Register], e.g. "brave" or "searxng").
	Name() string
}

// Options configures one [Provider.Search] call. The zero value is valid and
// runs with the provider's configured defaults.
type Options struct {
	// MaxResults caps the number of results returned by this call,
	// overriding the provider's configured default (see [Config.MaxResults]
	// and [DefaultMaxResults]) for this call only. Zero uses that default.
	// Regardless of what is requested here, the effective count is always
	// clamped to [MaxResultsCeiling] — see [ClampMaxResults].
	MaxResults int
}

// Results is the ranked outcome of one [Provider.Search] call. A caller
// renders it directly or projects it into a model-facing tool result without
// loss — every field a renderer needs (query, backend, ranked items, whether
// the set was bounded) is already here.
type Results struct {
	// Query is the query string that produced these results, echoed back so
	// a caller need not thread it through separately.
	Query string `json:"query"`
	// Provider is the registry name of the backend that answered
	// (Provider.Name()).
	Provider string `json:"provider"`
	// Items is the ranked result list, best match first, already bounded to
	// the effective MaxResults for this call.
	Items []Result `json:"items"`
	// Truncated reports whether the backend had more matches than Items
	// carries because a bound was applied. False when the backend returned
	// everything it had, even if that count is small (including zero).
	Truncated bool `json:"truncated"`
}

// Result is one ranked search hit.
type Result struct {
	// Rank is the 1-based position in the provider's ranking for this query.
	Rank int `json:"rank"`
	// Title is the result page's title.
	Title string `json:"title"`
	// URL is the result's canonical link.
	URL string `json:"url"`
	// Snippet is a short excerpt or description, bounded to
	// [DefaultSnippetLimit] runes by [TruncateSnippet] — implementations
	// apply that bound before placing text here, so a Result never carries an
	// unbounded backend description into a caller's payload.
	Snippet string `json:"snippet"`
}
