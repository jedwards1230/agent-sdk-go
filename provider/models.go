package provider

import (
	"errors"
	"fmt"
	"strings"
)

// ModelInfo is a model's metadata: identity, limits, pricing, and capabilities.
// It backs both provider Info() reporting and usage/cost accounting.
type ModelInfo struct {
	// ID is the model identifier (e.g. "claude-sonnet-5").
	ID string
	// Provider is the backend that serves the model ("anthropic", "openai", …).
	Provider string
	// ContextWindow is the maximum input context in tokens, or 0 when unknown
	// (always 0 when Unregistered). Zero means "unknown", NOT "no context" —
	// a consumer must render it as unavailable rather than as an exhausted
	// budget, and must not divide by it.
	ContextWindow int
	// MaxOutput is the maximum output tokens per response, or 0 when unknown
	// (always 0 when Unregistered). Adapters fall back to their own default.
	MaxOutput int
	// Pricing is the model's per-million-token pricing in USD. It is the zero
	// value when Unregistered — which is NOT a price of zero. Never price
	// usage off this field without first checking Unregistered; use [CostOf],
	// which reports unknown pricing through its ok result.
	Pricing Pricing
	// Reasoning reports whether the model supports extended reasoning. It is
	// false when Unregistered — a conservative default, not a known answer.
	Reasoning bool
	// Unregistered reports that this record was synthesized by [Resolve] for a
	// model id the registry does not carry, rather than read from the registry.
	// Only ID and Provider are trustworthy on such a record: every other field
	// is a placeholder meaning "unknown". A consumer that surfaces pricing,
	// limits, or capabilities must branch on this flag and show the value as
	// unavailable instead of rendering the zero value as fact.
	Unregistered bool
}

// ErrNoModel reports that no model id was supplied at all — a caller-side
// failure to resolve a default, distinct from supplying an id the SDK does not
// recognize. Callers can errors.Is against it.
//
// Its message carries no package prefix: it is a sentinel meant to be wrapped
// with the caller's own context, and a self-prefix produced the confusing
// doubled "providers: provider: …" at the [providers.Build] call site.
var ErrNoModel = errors.New("no model specified")

// ErrUnknownProvider reports that a model id is not registered AND its backend
// could not be inferred from its id, so no adapter can be constructed for it.
// This is the only remaining reason [Resolve] refuses a non-empty id.
var ErrUnknownProvider = errors.New("cannot determine which provider serves model")

// providerPrefixes maps model-id prefixes to the backend that serves them, for
// ids the registry does not carry. It exists so a newly released model can be
// used the day it ships, without an SDK release: the registry supplies pricing
// and limits, but it is no longer the gate on whether a model may run at all.
//
// Extend it when a provider introduces a new id family. An id matching nothing
// here is genuinely unusable (there is no adapter to route it to), which is why
// that case still errors.
var providerPrefixes = []struct{ prefix, provider string }{
	{"claude-", "anthropic"},
	{"gpt-", "openai"},
}

// inferProvider resolves the backend for an unregistered model id from its
// shape. It returns "" when the id matches no known family.
func inferProvider(id string) string {
	for _, p := range providerPrefixes {
		if strings.HasPrefix(id, p.prefix) {
			return p.provider
		}
	}
	// OpenAI's reasoning family is "o" followed by a generation digit
	// ("o4-mini"), which no prefix constant can cover open-endedly.
	if len(id) >= 2 && id[0] == 'o' && id[1] >= '1' && id[1] <= '9' {
		return "openai"
	}
	return ""
}

// Resolve returns metadata for a model id, WITHOUT requiring the id to be
// registered. A registered id returns its full record. An unregistered id whose
// backend can be inferred from its shape returns a degraded record — correct ID
// and Provider, Unregistered set, and every metadata field at its zero value
// meaning "unknown" — so the model can still be run.
//
// This is the entry point callers should use to answer "can I run this model?".
// [Lookup] answers the narrower question "does the SDK know this model's
// pricing and limits?" and is the right call for metadata, not admission.
//
// It returns [ErrNoModel] for an empty id and [ErrUnknownProvider] for an id
// that matches no known provider family.
func Resolve(id string) (ModelInfo, error) {
	if id == "" {
		return ModelInfo{}, ErrNoModel
	}
	if m, ok := models[id]; ok {
		return m, nil
	}
	p := inferProvider(id)
	if p == "" {
		return ModelInfo{}, fmt.Errorf("%w %q", ErrUnknownProvider, id)
	}
	return ModelInfo{ID: id, Provider: p, Unregistered: true}, nil
}

// Pricing is per-million-token pricing in USD. CacheWrite is zero for providers
// that do not charge a distinct cache-write rate.
type Pricing struct {
	Input      float64 // USD per 1M input tokens
	Output     float64 // USD per 1M output tokens
	CacheRead  float64 // USD per 1M cache-read tokens
	CacheWrite float64 // USD per 1M cache-write tokens
}

// Cost is a computed USD cost, with a per-bucket breakdown.
type Cost struct {
	USD           float64 `json:"usd"`
	InputUSD      float64 `json:"input_usd,omitempty"`
	OutputUSD     float64 `json:"output_usd,omitempty"`
	CacheReadUSD  float64 `json:"cache_read_usd,omitempty"`
	CacheWriteUSD float64 `json:"cache_write_usd,omitempty"`
}

const perMillion = 1_000_000.0

// Cost prices a [Usage] against the pricing, returning the total and breakdown.
func (p Pricing) Cost(u Usage) Cost {
	in := float64(u.InputTokens) / perMillion * p.Input
	out := float64(u.OutputTokens) / perMillion * p.Output
	cr := float64(u.CacheReadTokens) / perMillion * p.CacheRead
	cw := float64(u.CacheWriteTokens) / perMillion * p.CacheWrite
	return Cost{USD: in + out + cr + cw, InputUSD: in, OutputUSD: out, CacheReadUSD: cr, CacheWriteUSD: cw}
}

// models is the embedded model metadata registry. It is a plain data table:
// extend it by adding rows. Pricing is per 1M tokens in USD.
//
// Anthropic cache rates follow the documented multipliers: cache reads ~0.1x
// input, cache writes ~1.25x input (5-minute TTL). OpenAI bills cached input at
// a discounted read rate and has no separate cache-write charge.
var models = map[string]ModelInfo{
	// --- Anthropic ---
	"claude-fable-5": {
		ID: "claude-fable-5", Provider: "anthropic",
		ContextWindow: 1_000_000, MaxOutput: 128_000, Reasoning: true,
		Pricing: Pricing{Input: 10, Output: 50, CacheRead: 1.0, CacheWrite: 12.5},
	},
	"claude-opus-4-8": {
		ID: "claude-opus-4-8", Provider: "anthropic",
		ContextWindow: 1_000_000, MaxOutput: 128_000, Reasoning: true,
		Pricing: Pricing{Input: 5, Output: 25, CacheRead: 0.5, CacheWrite: 6.25},
	},
	"claude-sonnet-5": {
		ID: "claude-sonnet-5", Provider: "anthropic",
		ContextWindow: 1_000_000, MaxOutput: 128_000, Reasoning: true,
		Pricing: Pricing{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75},
	},
	"claude-haiku-4-5": {
		ID: "claude-haiku-4-5", Provider: "anthropic",
		ContextWindow: 200_000, MaxOutput: 64_000, Reasoning: true,
		Pricing: Pricing{Input: 1, Output: 5, CacheRead: 0.1, CacheWrite: 1.25},
	},

	// --- OpenAI ---
	// Pricing verified against public OpenAI rates; OpenAI has no cache-write
	// charge (cached input bills at the CacheRead rate). Provider owner should
	// re-confirm rates when wiring provider/openai.
	"gpt-5": {
		ID: "gpt-5", Provider: "openai",
		ContextWindow: 400_000, MaxOutput: 128_000, Reasoning: true,
		Pricing: Pricing{Input: 1.25, Output: 10, CacheRead: 0.125},
	},
	"gpt-5-mini": {
		ID: "gpt-5-mini", Provider: "openai",
		ContextWindow: 400_000, MaxOutput: 128_000, Reasoning: true,
		Pricing: Pricing{Input: 0.25, Output: 2, CacheRead: 0.025},
	},
	"gpt-5-nano": {
		ID: "gpt-5-nano", Provider: "openai",
		ContextWindow: 400_000, MaxOutput: 128_000, Reasoning: true,
		Pricing: Pricing{Input: 0.05, Output: 0.4, CacheRead: 0.005},
	},
	"o4-mini": {
		ID: "o4-mini", Provider: "openai",
		ContextWindow: 200_000, MaxOutput: 100_000, Reasoning: true,
		Pricing: Pricing{Input: 1.1, Output: 4.4, CacheRead: 0.275},
	},
}

// Lookup returns the metadata for a model id, and whether it is registered.
// It reports what the SDK KNOWS about a model, not what it will allow: an
// unregistered id can still be run — see [Resolve].
func Lookup(id string) (ModelInfo, bool) {
	m, ok := models[id]
	return m, ok
}

// Models returns the registered model ids in no particular order.
func Models() []string {
	ids := make([]string, 0, len(models))
	for id := range models {
		ids = append(ids, id)
	}
	return ids
}

// CostOf prices usage for a model id via the registry. ok is false when the
// model is not registered (and Cost is the zero value).
//
// A false ok means "this model's cost is UNKNOWN", not "this model is free".
// Since unregistered models can now run ([Resolve]), a caller that discards ok
// and renders the zero Cost is reporting a paid model as costing $0.00. Render
// the cost as unavailable instead.
func CostOf(modelID string, u Usage) (Cost, bool) {
	m, ok := models[modelID]
	if !ok {
		return Cost{}, false
	}
	return m.Pricing.Cost(u), true
}
