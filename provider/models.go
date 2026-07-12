package provider

// ModelInfo is a model's metadata: identity, limits, pricing, and capabilities.
// It backs both provider Info() reporting and usage/cost accounting.
type ModelInfo struct {
	// ID is the model identifier (e.g. "claude-sonnet-5").
	ID string
	// Provider is the backend that serves the model ("anthropic", "openai", …).
	Provider string
	// ContextWindow is the maximum input context in tokens.
	ContextWindow int
	// MaxOutput is the maximum output tokens per response.
	MaxOutput int
	// Pricing is the model's per-million-token pricing in USD.
	Pricing Pricing
	// Reasoning reports whether the model supports extended reasoning.
	Reasoning bool
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
func CostOf(modelID string, u Usage) (Cost, bool) {
	m, ok := models[modelID]
	if !ok {
		return Cost{}, false
	}
	return m.Pricing.Cost(u), true
}
