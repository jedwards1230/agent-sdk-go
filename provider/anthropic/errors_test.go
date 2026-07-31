package anthropic

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// statusHandler returns a handler writing status with body as the response.
func statusHandler(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// TestPreStreamContextOverflow asserts both real Anthropic phrasings of a
// context-window rejection classify as provider.ErrContextOverflow, and that
// the parsed type/message survive on the typed error.
func TestPreStreamContextOverflow(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{{
		name: "prompt is too long",
		body: `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 246049 tokens > 200000 maximum"}}`,
	}, {
		name: "input length and max_tokens exceed context limit",
		body: "{\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"input length and `max_tokens` exceed context limit: 195000 + 8192 > 200000, decrease input length or max_tokens and try again\"}}",
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := testProvider(t, provider.CredAPIKey, statusHandler(http.StatusBadRequest, tt.body))
			_, err := p.Stream(context.Background(), provider.Request{Messages: []provider.Message{provider.UserText("hi")}})
			if err == nil {
				t.Fatal("Stream returned nil error, want a context-overflow rejection")
			}
			if !errors.Is(err, provider.ErrContextOverflow) {
				t.Fatalf("errors.Is(err, ErrContextOverflow) = false, err = %v", err)
			}
			var apiErr *Error
			if !errors.As(err, &apiErr) {
				t.Fatalf("errors.As(*Error) = false, err = %v", err)
			}
			if apiErr.StatusCode != http.StatusBadRequest || apiErr.Type != "invalid_request_error" {
				t.Errorf("parsed error = %+v, want 400 invalid_request_error", apiErr)
			}
		})
	}
}

// TestMidStreamContextOverflow asserts an SSE error frame classifies even
// though it carries no HTTP status of its own. This is the case a classifier
// gated on StatusCode == 400 silently misses.
func TestMidStreamContextOverflow(t *testing.T) {
	body := sse(frame("error", `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 246049 tokens > 200000 maximum"}}`))
	p := testProvider(t, provider.CredAPIKey, sseHandler(body))
	h, err := p.Stream(context.Background(), provider.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer func() { _ = h.Close() }()

	_, err = h.Next()
	if err == nil {
		t.Fatal("Next returned nil error, want the stream error frame")
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("errors.As(*Error) = false, err = %v", err)
	}
	if apiErr.StatusCode != 0 {
		t.Fatalf("StatusCode = %d, want 0 — this test exists to cover the statusless path", apiErr.StatusCode)
	}
	if !errors.Is(err, provider.ErrContextOverflow) {
		t.Fatalf("errors.Is(err, ErrContextOverflow) = false, err = %v", err)
	}
}

// TestPreStreamNonOverflowStatuses drives real non-overflow responses through
// Stream, so the negatives exercise the same parse path a live rejection does
// rather than only hand-built values.
func TestPreStreamNonOverflowStatuses(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{{
		name:   "server error",
		status: http.StatusInternalServerError,
		body:   `{"type":"error","error":{"type":"api_error","message":"Internal server error"}}`,
	}, {
		name:   "rate limit",
		status: http.StatusTooManyRequests,
		body:   `{"type":"error","error":{"type":"rate_limit_error","message":"Number of request tokens has exceeded your per-minute rate limit"}}`,
	}, {
		// A byte-size limit, not a context-window limit — compacting history
		// would not help, so a caller must not retry it as an overflow.
		name:   "request_too_large",
		status: http.StatusRequestEntityTooLarge,
		body:   `{"type":"error","error":{"type":"request_too_large","message":"Request exceeds the maximum allowed number of bytes"}}`,
	}, {
		name:   "unrelated invalid_request_error",
		status: http.StatusBadRequest,
		body:   `{"type":"error","error":{"type":"invalid_request_error","message":"messages: at least one message is required"}}`,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := testProvider(t, provider.CredAPIKey, statusHandler(tt.status, tt.body))
			_, err := p.Stream(context.Background(), provider.Request{})
			if err == nil {
				t.Fatal("Stream returned nil error")
			}
			if errors.Is(err, provider.ErrContextOverflow) {
				t.Errorf("http %d must not classify as a context overflow: %v", tt.status, err)
			}
		})
	}
}

// TestTransportFailureNotOverflow asserts a request that never reaches the API
// does not classify.
func TestTransportFailureNotOverflow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := testProvider(t, provider.CredAPIKey, sseHandler(textTurn))
	_, err := p.Stream(ctx, provider.Request{})
	if err == nil {
		t.Fatal("Stream on a cancelled context returned nil error")
	}
	if errors.Is(err, provider.ErrContextOverflow) {
		t.Errorf("a transport failure must not classify as a context overflow: %v", err)
	}
}

// TestErrorClassificationNegatives asserts the classifier is narrow. The values
// are built by hand, which is itself part of the contract — classification is a
// pure function of the exported fields, so a consumer can construct one in its
// own test.
func TestErrorClassificationNegatives(t *testing.T) {
	tests := []struct {
		name string
		err  *Error
		want bool
	}{{
		name: "pre-stream overflow",
		err:  &Error{StatusCode: 400, Type: "invalid_request_error", Message: "prompt is too long: 246049 tokens > 200000 maximum"},
		want: true,
	}, {
		name: "mid-stream overflow, no status",
		err:  &Error{Type: "invalid_request_error", Message: "input length and `max_tokens` exceed context limit: 195000 + 8192 > 200000"},
		want: true,
	}, {
		// request_too_large is a limit on the request's BYTE SIZE, not the
		// context window. Compacting history is not its remedy, so treating it
		// as an overflow would send a caller into a retry loop that can never
		// succeed.
		name: "request_too_large is not an overflow",
		err:  &Error{StatusCode: 413, Type: "request_too_large", Message: "Request exceeds the maximum allowed number of bytes"},
	}, {
		name: "server error",
		err:  &Error{StatusCode: 500, Type: "api_error", Message: "Internal server error"},
	}, {
		name: "rate limit",
		err:  &Error{StatusCode: 429, Type: "rate_limit_error", Message: "Number of request tokens has exceeded your per-minute rate limit"},
	}, {
		// Proves the phrase list is genuinely narrow rather than matching any
		// invalid_request_error.
		name: "unrelated invalid_request_error",
		err:  &Error{StatusCode: 400, Type: "invalid_request_error", Message: "messages: at least one message is required"},
	}, {
		// The type gate holds even when the prose would match.
		name: "overflow phrasing under the wrong type",
		err:  &Error{StatusCode: 413, Type: "request_too_large", Message: "prompt is too long"},
	}, {
		// The status gate holds too: a 5xx is never an overflow, however the
		// upstream worded it. Paired with the statusless mid-stream case above,
		// this pins the gate to "check status only when present".
		name: "overflow phrasing under a 5xx",
		err:  &Error{StatusCode: 500, Type: "invalid_request_error", Message: "prompt is too long"},
	}, {
		name: "unparsed body with no type",
		err:  &Error{StatusCode: 400, Message: "prompt is too long: 246049 tokens > 200000 maximum"},
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errors.Is(tt.err, provider.ErrContextOverflow); got != tt.want {
				t.Errorf("errors.Is = %v, want %v (err = %v)", got, tt.want, tt.err)
			}
		})
	}
}
