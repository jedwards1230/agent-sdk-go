package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// errorProvider starts an httptest server with handler and returns a provider
// pointed at it, credentialed with a static API key. No network, no real key.
func errorProvider(t *testing.T, handler http.HandlerFunc) *Provider {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cred := provider.StaticCredentialSource{Cred: provider.Credential{Kind: provider.CredAPIKey, Token: "sk-test"}}
	return New("gpt-5", cred, WithBaseURL(srv.URL))
}

// jsonStatus returns a handler writing status with body as the response.
func jsonStatus(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// overflowEnvelope is a real-shaped OpenAI context-length rejection.
const overflowEnvelope = `{"error":{"message":"This model's maximum context length is 272000 tokens. However, your messages resulted in 301402 tokens. Please reduce the length of the messages.","type":"invalid_request_error","param":"messages","code":"context_length_exceeded"}}`

// TestPreStreamContextOverflow asserts a 400 context-length rejection from the
// Responses API classifies as provider.ErrContextOverflow, and that the
// structured envelope fields are parsed onto APIError rather than being left in
// the raw body only.
func TestPreStreamContextOverflow(t *testing.T) {
	p := errorProvider(t, jsonStatus(http.StatusBadRequest, overflowEnvelope))
	_, err := p.Stream(context.Background(), provider.Request{Messages: []provider.Message{provider.UserText("hi")}})
	if err == nil {
		t.Fatal("Stream returned nil error, want a context-overflow rejection")
	}
	if !errors.Is(err, provider.ErrContextOverflow) {
		t.Fatalf("errors.Is(err, ErrContextOverflow) = false, err = %v", err)
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("errors.As(*APIError) = false, err = %v", err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want 400", apiErr.StatusCode)
	}
	if apiErr.Code != "context_length_exceeded" {
		t.Errorf("Code = %q, want context_length_exceeded", apiErr.Code)
	}
	if apiErr.Type != "invalid_request_error" {
		t.Errorf("Type = %q, want invalid_request_error", apiErr.Type)
	}
	if apiErr.Param != "messages" {
		t.Errorf("Param = %q, want messages", apiErr.Param)
	}
	if apiErr.Body != overflowEnvelope {
		t.Errorf("Body must be retained verbatim, got %q", apiErr.Body)
	}
}

// TestPreStreamContextOverflowNoDiscreteCode asserts the narrow text fallback:
// a gateway that strips the envelope down to prose still classifies, because
// the status is 400 and the body carries a known phrasing.
func TestPreStreamContextOverflowNoDiscreteCode(t *testing.T) {
	body := `{"error":{"message":"This model's maximum context length is 272000 tokens.","type":"invalid_request_error"}}`
	p := errorProvider(t, jsonStatus(http.StatusBadRequest, body))
	_, err := p.Stream(context.Background(), provider.Request{})
	if !errors.Is(err, provider.ErrContextOverflow) {
		t.Fatalf("errors.Is(err, ErrContextOverflow) = false, err = %v", err)
	}
}

// TestPreStreamNonJSONBodyKeepsBehavior asserts the envelope parse is
// best-effort: a non-JSON body leaves the structured fields empty, keeps Body
// verbatim, and does not classify.
func TestPreStreamNonJSONBodyKeepsBehavior(t *testing.T) {
	p := errorProvider(t, jsonStatus(http.StatusBadGateway, "<html>502 Bad Gateway</html>"))
	_, err := p.Stream(context.Background(), provider.Request{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("errors.As(*APIError) = false, err = %v", err)
	}
	if apiErr.Body != "<html>502 Bad Gateway</html>" {
		t.Errorf("Body = %q, want the raw body", apiErr.Body)
	}
	if apiErr.Type != "" || apiErr.Code != "" || apiErr.Param != "" {
		t.Errorf("structured fields must stay empty on a non-JSON body, got %+v", apiErr)
	}
	if errors.Is(err, provider.ErrContextOverflow) {
		t.Error("a 502 HTML body must not classify as a context overflow")
	}
}

// TestPreStreamTruncatedBodyStillClassifies covers the case that makes the
// empty-Type arm of the gate load-bearing: an envelope larger than the read cap
// arrives truncated, so it no longer parses as JSON and every structured field
// is empty — including the discrete code that would otherwise settle it. Only
// the prose fallback is left, and a classifier that required a non-empty Type
// would false-negative on a genuine overflow.
func TestPreStreamTruncatedBodyStillClassifies(t *testing.T) {
	// Pad past errBodyLimit with a run of 'x', so the cut lands mid-run: no
	// whitespace at the boundary for TrimSpace to shave, and the assertion on
	// the retained length stays exact.
	filler := strings.Repeat("x", 70<<10)
	body := `{"error":{"message":"This model's maximum context length is 272000 tokens. ` + filler +
		`","type":"invalid_request_error","code":"context_length_exceeded"}}`

	p := errorProvider(t, jsonStatus(http.StatusBadRequest, body))
	_, err := p.Stream(context.Background(), provider.Request{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("errors.As(*APIError) = false, err = %v", err)
	}
	if len(apiErr.Body) != errBodyLimit {
		t.Errorf("len(Body) = %d, want the read cap %d", len(apiErr.Body), errBodyLimit)
	}
	if apiErr.Type != "" || apiErr.Code != "" || apiErr.Param != "" {
		t.Errorf("a truncated body cannot parse; structured fields must be empty, got %+v",
			&APIError{Type: apiErr.Type, Code: apiErr.Code, Param: apiErr.Param})
	}
	if !errors.Is(err, provider.ErrContextOverflow) {
		t.Fatal("a truncated overflow envelope must still classify via the prose fallback")
	}
}

// TestPreStreamNonOverflowStatuses drives real non-overflow responses through
// Stream, so the negatives exercise the same envelope parse a live rejection
// does rather than only hand-built values.
func TestPreStreamNonOverflowStatuses(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{{
		name:   "server error",
		status: http.StatusInternalServerError,
		body:   `{"error":{"message":"The server had an error processing your request.","type":"server_error","code":null}}`,
	}, {
		name:   "rate limit",
		status: http.StatusTooManyRequests,
		body:   `{"error":{"message":"Rate limit reached for gpt-5","type":"rate_limit_error","code":"rate_limit_exceeded"}}`,
	}, {
		name:   "unrelated invalid_request_error",
		status: http.StatusBadRequest,
		body:   `{"error":{"message":"Invalid value for 'temperature'","type":"invalid_request_error","param":"temperature","code":null}}`,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := errorProvider(t, jsonStatus(tt.status, tt.body))
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

// TestMidStreamContextOverflow asserts an SSE error frame carrying the discrete
// context_length_exceeded code classifies from StreamHandle.Next.
func TestMidStreamContextOverflow(t *testing.T) {
	body := "data: " + `{"type":"error","code":"context_length_exceeded","message":"This model's maximum context length is 272000 tokens."}` + "\n\n"
	p := errorProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	})
	h, err := p.Stream(context.Background(), provider.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer func() { _ = h.Close() }()

	_, err = h.Next()
	if err == nil {
		t.Fatal("Next returned nil error, want the stream error frame")
	}
	if !errors.Is(err, provider.ErrContextOverflow) {
		t.Fatalf("errors.Is(err, ErrContextOverflow) = false, err = %v", err)
	}
	var streamErr *StreamError
	if !errors.As(err, &streamErr) || streamErr.Code != "context_length_exceeded" {
		t.Fatalf("errors.As(*StreamError) mismatch, err = %v", err)
	}
}

// TestTransportFailureNotOverflow asserts a connection the server closes without
// a response surfaces a transport error that does not classify.
func TestTransportFailureNotOverflow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("ResponseWriter is not a Hijacker")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_ = conn.Close()
	}))
	defer srv.Close()
	cred := provider.StaticCredentialSource{Cred: provider.Credential{Kind: provider.CredAPIKey, Token: "sk-test"}}
	p := New("gpt-5", cred, WithBaseURL(srv.URL))

	_, err := p.Stream(context.Background(), provider.Request{})
	if err == nil {
		t.Fatal("Stream returned nil error, want a transport failure")
	}
	if errors.Is(err, provider.ErrContextOverflow) {
		t.Errorf("a transport failure must not classify as a context overflow: %v", err)
	}
}

// TestAPIErrorClassificationNegatives asserts the classifier is narrow: only a
// genuine context-window rejection satisfies the sentinel. The values are built
// by hand, which is itself part of the contract — classification is a pure
// function of the exported fields, so a consumer can construct one in its own
// test.
func TestAPIErrorClassificationNegatives(t *testing.T) {
	tests := []struct {
		name string
		err  *APIError
		want bool
	}{{
		name: "discrete code",
		err:  &APIError{StatusCode: 400, Type: "invalid_request_error", Code: "context_length_exceeded"},
		want: true,
	}, {
		name: "server error",
		err:  &APIError{StatusCode: 500, Body: `{"error":{"message":"internal server error","type":"server_error"}}`, Type: "server_error"},
	}, {
		name: "rate limit",
		err:  &APIError{StatusCode: 429, Body: `{"error":{"message":"Rate limit reached for gpt-5","type":"rate_limit_error","code":"rate_limit_exceeded"}}`, Type: "rate_limit_error", Code: "rate_limit_exceeded"},
	}, {
		name: "unrelated invalid_request_error",
		err:  &APIError{StatusCode: 400, Body: `{"error":{"message":"Invalid value for 'temperature'","type":"invalid_request_error","param":"temperature"}}`, Type: "invalid_request_error", Param: "temperature"},
	}, {
		// A gateway (LiteLLM, OpenRouter) that re-emits the envelope renames
		// the code. Reachable through WithBaseURL, so a classifier that keyed
		// only on OpenAI's own spelling would false-negative in production —
		// the exact wedge this sentinel exists to prevent. The message is
		// deliberately worded to match NO phrase in the fallback list, so only
		// recognizing the code can make this case pass.
		name: "gateway-renamed overflow code",
		err:  &APIError{StatusCode: 400, Type: "invalid_request_error", Code: "context_window_exceeded", Body: `{"error":{"message":"This request would exceed the model's limit of 8192 tokens"}}`},
		want: true,
	}, {
		// A hand-built value carries no status. The guard must not read 0 as
		// "not 4xx" and reject it — consumers construct these in their own
		// tests, which is the contract APIError.Is documents.
		name: "no status, overflow prose",
		err:  &APIError{Type: "invalid_request_error", Body: "This model's maximum context length is 8192 tokens"},
		want: true,
	}, {
		// The load-bearing status-gate case. An unrecognized code must NOT be
		// a verdict, but a 5xx must still lose: a server error is not
		// shrinkable, so classifying it would send a caller into a
		// compact-and-retry loop that can never succeed. A gate written as one
		// `status != 400 && type != "invalid_request_error"` condition passes
		// this input, which is why the two checks are separate statements.
		name: "5xx quoting overflow prose",
		err:  &APIError{StatusCode: 500, Type: "invalid_request_error", Body: `{"error":{"message":"upstream: maximum context length exceeded"}}`},
	}, {
		// The load-bearing type-gate case: 4xx passes the status gate, so only
		// the type check can reject it.
		name: "overflow phrasing under a rate limit",
		err:  &APIError{StatusCode: 429, Body: "slow down; your maximum context length is unrelated here", Type: "rate_limit_error"},
	}, {
		name: "rate limit with its own code and prose",
		err:  &APIError{StatusCode: 429, Type: "rate_limit_error", Code: "rate_limit_exceeded", Body: "maximum context length is irrelevant here"},
	}, {
		// An unrecognized code alongside an unrelated message: the code is not
		// a verdict either way, and the prose does not match. Pins narrowness.
		name: "unrecognized code, unrelated message",
		err:  &APIError{StatusCode: 400, Type: "invalid_request_error", Code: "invalid_prompt", Body: `{"error":{"message":"messages: at least one message is required"}}`},
	}, {
		// param alone is not a signal: plenty of validation failures name it.
		name: "param messages without overflow text",
		err:  &APIError{StatusCode: 400, Type: "invalid_request_error", Param: "messages", Body: `{"error":{"message":"messages: at least one message is required"}}`},
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errors.Is(tt.err, provider.ErrContextOverflow); got != tt.want {
				t.Errorf("errors.Is = %v, want %v (err = %v)", got, tt.want, tt.err)
			}
		})
	}
}

// TestStreamErrorClassificationNegatives mirrors the above for the mid-stream
// error type.
func TestStreamErrorClassificationNegatives(t *testing.T) {
	tests := []struct {
		name string
		err  *StreamError
		want bool
	}{{
		name: "discrete code",
		err:  &StreamError{Code: "context_length_exceeded", Message: "too long"},
		want: true,
	}, {
		name: "no code, overflow phrasing",
		err:  &StreamError{Message: "This model's maximum context length is 272000 tokens."},
		want: true,
	}, {
		name: "gateway-renamed overflow code",
		err:  &StreamError{Code: "context_window_exceeded", Message: "too long"},
		want: true,
	}, {
		name: "server error code",
		err:  &StreamError{Code: "server_error", Message: "internal error"},
	}, {
		// A frame carries neither a status nor a type, so an unrecognized code
		// cannot be a verdict — this falls through to the prose and classifies.
		// Deliberate: the harm is asymmetric. A false negative wedges the
		// session (the whole bug); a false positive costs one bounded retry.
		name: "unrecognized code, overflow prose",
		err:  &StreamError{Code: "rate_limit_exceeded", Message: "maximum context length is irrelevant here"},
		want: true,
	}, {
		name: "unrecognized code, unrelated message",
		err:  &StreamError{Code: "invalid_prompt", Message: "messages: at least one message is required"},
	}, {
		name: "bare response failure",
		err:  &StreamError{Message: "response failed"},
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errors.Is(tt.err, provider.ErrContextOverflow); got != tt.want {
				t.Errorf("errors.Is = %v, want %v (err = %v)", got, tt.want, tt.err)
			}
		})
	}
}
