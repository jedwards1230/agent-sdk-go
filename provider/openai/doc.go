// Package openai implements a [provider.Provider] for OpenAI models over the
// Responses API (POST /responses with stream:true), not the older Chat
// Completions API.
//
// # Routing by credential kind
//
// The credential kind selects the endpoint, not just the auth header:
//
//   - api_key: base https://api.openai.com/v1, Authorization: Bearer <key>.
//   - oauth (ChatGPT subscription): base https://chatgpt.com/backend-api/codex,
//     Authorization: Bearer <access_token> plus ChatGPT-Account-ID: <account>.
//
// Both post to <base>/responses and consume the same SSE stream shape.
//
// # Normalized stream
//
// Server-sent events are mapped onto the vendor-neutral
// [provider.StreamEvent] union: response.output_text.delta ->
// StreamTextDelta; response.reasoning_summary_text.delta (and
// response.reasoning_text.delta) -> StreamReasoningDelta; a function_call
// output item -> StreamToolCallStart / StreamToolCallDelta / StreamToolCallEnd;
// and response.completed -> a terminal StreamFinished carrying the stop reason
// and normalized usage.
//
// # Graceful degradation (Responses API vs the Anthropic peer)
//
//   - Usage: OpenAI reports input_tokens inclusive of cached tokens. The
//     adapter reports InputTokens as the uncached remainder and CacheReadTokens
//     as cached_tokens (matching the Anthropic convention so cost accounting is
//     not double-counted); the raw provider fields are retained in Usage.Raw.
//     OpenAI has no cache-write charge, so CacheWriteTokens is always zero.
//   - Reasoning: OpenAI takes a reasoning effort level, not a token budget, so
//     Params.Thinking.BudgetTokens is ignored and Effort is used (default
//     "medium"). Only reasoning summaries are streamed back; raw thinking is not
//     round-tripped into request history.
//   - Temperature is only sent to non-reasoning models; reasoning models reject
//     it, so it is dropped for them.
//   - Reasoning blocks are not replayed on subsequent requests in M1: full
//     replay requires reasoning.encrypted_content (opted in via the request
//     `include` field) round-tripped back as reasoning input items — tracked for
//     M2. Inbound reasoning deltas are already tagged with their Responses-API
//     item id under StreamEvent.Meta["openai.item_id"] (journaled onto the
//     assembled block) so the M2 change is small. Image blocks are placeholders
//     and are skipped.
package openai
