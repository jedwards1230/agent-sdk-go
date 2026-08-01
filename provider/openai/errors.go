package openai

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// APIError is a non-200 HTTP response from the Responses API. It carries the
// status code, the response body for diagnosis, and — when the body was the
// usual `{"error":{…}}` envelope — its structured fields.
type APIError struct {
	StatusCode int
	// Body is the response body as received, whitespace-trimmed and truncated
	// at the adapter's read cap. It is retained whether or not the structured
	// fields below could be parsed, so a body of an unexpected shape is never
	// silently discarded.
	Body string
	// Type, Code, and Param are parsed best-effort from the error envelope.
	// They are empty when the body was not JSON of that shape — including a
	// well-formed body truncated at the read cap, which no longer parses. They
	// are the structured signal classification prefers over Body's wording.
	Type  string
	Code  string
	Param string
}

// errBodyLimit caps how much of an error response is read into [APIError.Body],
// so a misrouted or hostile response cannot balloon an error value. A body cut
// off here no longer parses as JSON, which is why classification keeps a prose
// fallback for the truncated case.
const errBodyLimit = 64 << 10

// newAPIError builds an APIError from a raw response body, trimming it,
// retaining it verbatim, and best-effort parsing the `{"error":{…}}` envelope
// off it. Every APIError the adapter returns is built here — the Stream
// rejection path and the ListModels one — so the field contract documented
// above holds identically on both, rather than only on whichever path happened
// to inline the parse.
func newAPIError(status int, body []byte) *APIError {
	e := &APIError{StatusCode: status, Body: strings.TrimSpace(string(body))}
	var env struct {
		Error struct {
			Type  string `json:"type"`
			Code  string `json:"code"`
			Param string `json:"param"`
		} `json:"error"`
	}
	// A body that is not JSON of that shape — a proxy's HTML, an empty body,
	// one cut off at the read cap — leaves the fields empty rather than
	// failing the call differently.
	if json.Unmarshal(body, &env) == nil {
		e.Type, e.Code, e.Param = env.Error.Type, env.Error.Code, env.Error.Param
	}
	return e
}

// Error implements error.
func (e *APIError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("openai: http %d", e.StatusCode)
	}
	return fmt.Sprintf("openai: http %d: %s", e.StatusCode, e.Body)
}

// Is reports whether this error satisfies target, normalizing OpenAI's
// context-window rejection onto [provider.ErrContextOverflow]. Classification
// is a pure function of the exported fields, so a value a caller constructed by
// hand in its own test classifies exactly as a real response does.
func (e *APIError) Is(target error) bool {
	return target == provider.ErrContextOverflow && e.contextOverflow()
}

// contextOverflow reports whether this rejection was the prompt not fitting.
//
// A discrete overflow code answers on its own. Failing that, the structured
// fields gate a narrow prose match — and each is checked ONLY WHEN PRESENT,
// because neither is guaranteed: a gateway that rewrites the envelope can
// leave Type empty, and StatusCode is 0 on any value not built from an HTTP
// response. Demanding either outright would turn a real overflow into a false
// negative, which is the wedge this sentinel exists to prevent.
//
// Both gates must therefore be separate statements. Collapsing them into one
// condition reads naturally and is wrong in a way tests do not notice: a single
// `status != 400 && type != "invalid_request_error"` passes whenever EITHER
// matches, so a 500 whose body quotes overflow prose classifies — an
// unshrinkable failure a caller would compact-and-retry forever.
//
// There is deliberately no "any other code settles it" rule, and so no list of
// known-other codes to maintain: the gates already exclude them, since
// rate_limit_exceeded fails the type gate and server_error fails the status
// gate.
//
// Param is not a gate either: "messages"/"input" accompany many unrelated
// validation failures, and an overflow does not reliably set it. It is exported
// for a caller's own diagnosis instead.
func (e *APIError) contextOverflow() bool {
	if isContextOverflowCode(e.Code) {
		return true
	}
	if e.StatusCode != 0 && (e.StatusCode < 400 || e.StatusCode > 499) {
		return false
	}
	if e.Type != "" && e.Type != "invalid_request_error" {
		return false
	}
	return matchesContextOverflow(e.Body)
}

// StreamError is a failure signalled inside the SSE stream — a response.failed
// event or a top-level error frame — surfaced from StreamHandle.Next.
type StreamError struct {
	Code    string
	Message string
}

// Error implements error.
func (e *StreamError) Error() string {
	if e.Code == "" {
		return "openai: stream error: " + e.Message
	}
	return fmt.Sprintf("openai: stream error (%s): %s", e.Code, e.Message)
}

// Is reports whether this error satisfies target, normalizing OpenAI's
// mid-stream context-window rejection onto [provider.ErrContextOverflow].
func (e *StreamError) Is(target error) bool {
	return target == provider.ErrContextOverflow && e.contextOverflow()
}

// contextOverflow reports whether this stream failure was the prompt not
// fitting. A frame carries neither a status nor an error type, so there is
// nothing to gate on: an unrecognized code falls through to the same narrow
// prose match a code-less frame gets, rather than being treated as a verdict.
func (e *StreamError) contextOverflow() bool {
	if isContextOverflowCode(e.Code) {
		return true
	}
	return matchesContextOverflow(e.Message)
}

// Discrete error codes meaning "the prompt did not fit".
// context_length_exceeded is OpenAI's own, on both the HTTP envelope and the
// SSE error frame. context_window_exceeded is what gateways that re-emit the
// envelope (LiteLLM, OpenRouter) send in its place — and since the adapter
// ships WithBaseURL, sitting behind one is a supported deployment rather than a
// hypothetical, so failing to recognize it would be a live false negative.
const (
	codeContextLengthExceeded = "context_length_exceeded"
	codeContextWindowExceeded = "context_window_exceeded"
)

// isContextOverflowCode reports whether code is one of the discrete overflow
// codes. Recognizing one is sufficient on its own; recognizing none is not a
// verdict, since the code may simply be absent or rewritten.
func isContextOverflowCode(code string) bool {
	return code == codeContextLengthExceeded || code == codeContextWindowExceeded
}

// contextOverflowPhrases is a FALLBACK for when no discrete overflow code
// survived — a proxy or gateway can strip or rewrite the envelope, and a body
// truncated at the read cap no longer parses at all. Kept deliberately narrow:
// every entry must be specific to the context window, never a generic
// validation word, because a loose phrase here silently reclassifies unrelated
// 4xx responses as overflows.
var contextOverflowPhrases = []string{
	"maximum context length",
	"context window",
	codeContextLengthExceeded,
	codeContextWindowExceeded,
}

// matchesContextOverflow reports whether s contains a known overflow phrasing,
// case-insensitively.
func matchesContextOverflow(s string) bool {
	lower := strings.ToLower(s)
	for _, p := range contextOverflowPhrases {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}
