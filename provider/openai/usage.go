package openai

import "github.com/jedwards1230/agent-sdk-go/provider"

// respUsage is the Responses-API usage block. OpenAI reports input_tokens
// inclusive of cached tokens and output_tokens inclusive of reasoning tokens.
type respUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

// normalizeUsage maps Responses-API usage onto the vendor-neutral
// provider.Usage. cached_tokens become CacheReadTokens and are subtracted from
// InputTokens so the normalized input count is the uncached remainder (matching
// the Anthropic convention); this keeps cost accounting from double-charging
// cached input. OpenAI has no cache-write charge. Raw retains the provider's
// original fields verbatim for audit.
func normalizeUsage(u *respUsage) provider.Usage {
	if u == nil {
		return provider.Usage{}
	}
	cached := u.InputTokensDetails.CachedTokens
	input := u.InputTokens - cached
	if input < 0 {
		input = 0
	}
	return provider.Usage{
		InputTokens:     input,
		OutputTokens:    u.OutputTokens,
		CacheReadTokens: cached,
		Raw: map[string]int{
			"input_tokens":     u.InputTokens,
			"output_tokens":    u.OutputTokens,
			"total_tokens":     u.TotalTokens,
			"cached_tokens":    cached,
			"reasoning_tokens": u.OutputTokensDetails.ReasoningTokens,
		},
	}
}
