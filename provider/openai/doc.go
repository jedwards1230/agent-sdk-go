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
// Model listing follows the same routing but NOT the same wire contract: the
// Codex backend requires a client_version query parameter and answers with a
// {"models":[{"slug":...}]} body, where the public API takes no parameters and
// answers with {"data":[{"id":...}]}. See [Provider.ListModels].
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
//     "medium"). Naming an Effort is itself enough to turn reasoning on
//     (provider.Thinking.Active) — Enabled need not also be set. Reasoning
//     config is emitted only for models the registry marks as reasoning, and an
//     unregistered model counts as non-reasoning, so an effort set on one is
//     dropped. Only reasoning summaries are streamed back; raw thinking is not
//     round-tripped into request history.
//   - Temperature is only sent to non-reasoning models; reasoning models reject
//     it, so it is dropped for them.
//   - Reasoning blocks are replayed on subsequent requests: the request opts in
//     via `include: ["reasoning.encrypted_content"]` whenever reasoning is
//     enabled, and the output item's encrypted_content is journaled onto the
//     assembled block under StreamEvent.Meta["openai.encrypted_content"]
//     alongside the item id under Meta["openai.item_id"]. A reasoning block
//     carrying both is replayed as a "reasoning" input item; a block missing
//     either (e.g. reasoning was disabled, or the block predates this feature)
//     is still dropped. Image blocks are placeholders and are skipped.
package openai
