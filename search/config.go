package search

import "net/http"

// Config carries the connection details a [Provider] factory needs:
// credentials and the endpoint come from the embedder here, never from a
// package-chosen environment variable name and never a baked-in default
// instance. Which fields a given provider requires or ignores is documented
// on its own New (e.g. search/brave.New requires APIKey; search/searxng.New
// requires BaseURL and needs no APIKey).
type Config struct {
	// APIKey is the provider's credential, when it needs one. Empty for a
	// provider that needs none.
	APIKey string
	// BaseURL overrides the provider's endpoint. Required for a provider with
	// no universal default (e.g. a self-hosted SearXNG instance — there is no
	// safe default to point at someone else's instance); an optional
	// test/proxy override for a provider with one production endpoint.
	BaseURL string
	// HTTPClient is the client used for requests; nil uses
	// http.DefaultClient. A per-call context governs cancellation regardless
	// of the client's own timeout.
	HTTPClient *http.Client
	// MaxResults sets this provider's default result cap for a call that
	// does not set Options.MaxResults; zero uses [DefaultMaxResults]. See
	// [ClampMaxResults].
	MaxResults int
}
