package provider

import "errors"

// ErrContextOverflow reports that a request was REJECTED because it exceeded
// the model's context window. Nothing was generated: an overflow rejection
// carries no usage and produces no stream, which is precisely why a consumer
// watching settled per-turn usage cannot notice a single-turn overshoot on its
// own and needs this signal instead.
//
// It is distinct from its neighbours. [StopMaxTokens] is a turn that ran and
// hit its OUTPUT cap — a success. A rate limit, a payload-size limit
// (Anthropic's request_too_large), and a transport failure are all rejections
// too, but none is remedied by shortening the context. This sentinel means one
// thing: the prompt did not fit.
//
// Callers branch on it with errors.Is, never on message text — each adapter
// normalizes its own vendor signal onto this sentinel (a discrete error code
// where the vendor supplies one, a narrow phrase match confined to the adapter
// where it does not), so the wording of any provider's message is not part of
// the contract and may change without notice. The error propagates unwrapped
// through the loop and the runner, so errors.Is works on whatever loop.Run or
// runner.Runner.Prompt returned.
//
// The intended reaction is compact-and-retry: summarize or drop history, then
// re-issue the same turn. The SDK ships no automatic trigger — deciding when
// and how to shrink the context is embedder policy (see the compaction seam in
// docs/DESIGN.md).
//
// Its message carries no package prefix, for the same reason [ErrNoModel]
// does not: it is meant to be wrapped with the caller's own context.
var ErrContextOverflow = errors.New("request exceeds the model's context window")
