package search

// DefaultMaxResults is the number of results a [Provider] returns per query
// when both [Config.MaxResults] and [Options.MaxResults] are zero.
//
// Ten is chosen to bound context cost while staying useful: at roughly 30-60
// tokens per title+URL+snippet, ten results run about 400-700 tokens — a
// small, predictable slice of a turn's budget — while still covering the
// same first-page depth a person skims before refining a query. A caller
// that needs more raises it explicitly via Config or Options, up to
// [MaxResultsCeiling].
const DefaultMaxResults = 10

// MaxResultsCeiling is the hard cap on results returned per query, regardless
// of what [Config.MaxResults] or [Options.MaxResults] request. It exists so a
// misconfigured (or hostile) config value cannot turn one search call into a
// context bomb; a caller that genuinely needs more results pages via
// repeated calls, keeping each individual payload bounded.
const MaxResultsCeiling = 25

// DefaultSnippetLimit is the maximum rune length of a [Result.Snippet].
// Backend descriptions occasionally run to multiple sentences; bounding each
// one keeps a single unusually verbose result from dominating a [Results]
// payload the way an unbounded count would. It is fixed rather than
// per-provider configurable — unlike the result count, snippet length is not
// a lever an embedder has reason to tune, and a single well-chosen bound
// keeps every implementation's payload shape predictable.
const DefaultSnippetLimit = 400

// ClampMaxResults resolves the effective per-call result count from a
// provider's configured default and an optional per-call override:
// optsOverride wins when positive, else cfgDefault, else
// [DefaultMaxResults] — and the result is always clamped to
// [MaxResultsCeiling] regardless of what either input requested.
// Implementations call this once per Search, both to size the request sent
// to the backend and to bound the slice of results returned.
func ClampMaxResults(cfgDefault, optsOverride int) int {
	n := cfgDefault
	if optsOverride > 0 {
		n = optsOverride
	}
	if n <= 0 {
		n = DefaultMaxResults
	}
	if n > MaxResultsCeiling {
		n = MaxResultsCeiling
	}
	return n
}

// TruncateSnippet bounds s to [DefaultSnippetLimit] runes, appending an
// ellipsis when it truncates. Implementations call this on every backend
// description before placing it on a [Result.Snippet].
func TruncateSnippet(s string) string {
	r := []rune(s)
	if len(r) <= DefaultSnippetLimit {
		return s
	}
	return string(r[:DefaultSnippetLimit]) + "…"
}
