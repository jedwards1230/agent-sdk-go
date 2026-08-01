package provider_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/provider/anthropic"
	"github.com/jedwards1230/agent-sdk-go/provider/openai"
)

// TestContextOverflowNormalizesAcrossProviders is the test that proves the
// normalization claim: two vendors reporting the same failure in two entirely
// different wire shapes — Anthropic with no discrete code and the detail buried
// in prose, OpenAI with a discrete code — both satisfy the SAME sentinel. A
// consumer's compact-and-retry branch is therefore one errors.Is, with no
// per-provider knowledge and no message matching of its own.
func TestContextOverflowNormalizesAcrossProviders(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{{
		name: "anthropic pre-stream",
		err:  &anthropic.Error{StatusCode: http.StatusBadRequest, Type: "invalid_request_error", Message: "prompt is too long: 246049 tokens > 200000 maximum"},
		want: true,
	}, {
		name: "anthropic mid-stream (no http status)",
		err:  &anthropic.Error{Type: "invalid_request_error", Message: "input length and `max_tokens` exceed context limit: 195000 + 8192 > 200000"},
		want: true,
	}, {
		name: "openai pre-stream",
		err:  &openai.APIError{StatusCode: http.StatusBadRequest, Type: "invalid_request_error", Code: "context_length_exceeded", Param: "messages"},
		want: true,
	}, {
		name: "openai mid-stream",
		err:  &openai.StreamError{Code: "context_length_exceeded", Message: "This model's maximum context length is 272000 tokens."},
		want: true,
	}, {
		// Wrapping is the normal case once an error crosses a package
		// boundary; the sentinel must survive it.
		name: "wrapped anthropic",
		err:  fmt.Errorf("prompt: %w", &anthropic.Error{StatusCode: http.StatusBadRequest, Type: "invalid_request_error", Message: "prompt is too long: 1 tokens > 0 maximum"}),
		want: true,
	}, {
		name: "wrapped openai",
		err:  fmt.Errorf("prompt: %w", &openai.APIError{StatusCode: http.StatusBadRequest, Code: "context_length_exceeded"}),
		want: true,
	}, {
		name: "anthropic rate limit",
		err:  &anthropic.Error{StatusCode: http.StatusTooManyRequests, Type: "rate_limit_error", Message: "rate limit exceeded"},
	}, {
		name: "openai rate limit",
		err:  &openai.APIError{StatusCode: http.StatusTooManyRequests, Type: "rate_limit_error", Code: "rate_limit_exceeded"},
	}, {
		name: "anthropic request_too_large",
		err:  &anthropic.Error{StatusCode: http.StatusRequestEntityTooLarge, Type: "request_too_large", Message: "request exceeds the maximum allowed bytes"},
	}, {
		name: "unrelated error",
		err:  errors.New("something else went wrong"),
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errors.Is(tt.err, provider.ErrContextOverflow); got != tt.want {
				t.Errorf("errors.Is(%v, ErrContextOverflow) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestErrContextOverflowIsDistinct guards against an adapter's Is method
// answering true for any target it is handed. A classified overflow must
// satisfy ErrContextOverflow and nothing else; the package's other sentinels
// must not satisfy it either.
func TestErrContextOverflowIsDistinct(t *testing.T) {
	classified := []error{
		&anthropic.Error{StatusCode: http.StatusBadRequest, Type: "invalid_request_error", Message: "prompt is too long: 1 tokens > 0 maximum"},
		&openai.APIError{StatusCode: http.StatusBadRequest, Code: "context_length_exceeded"},
		&openai.StreamError{Code: "context_length_exceeded"},
	}
	for _, err := range classified {
		if !errors.Is(err, provider.ErrContextOverflow) {
			t.Fatalf("%T should classify as an overflow", err)
		}
		for _, other := range []error{provider.ErrNoModel, provider.ErrUnknownProvider} {
			if errors.Is(err, other) {
				t.Errorf("%T also matched %v — Is must answer for one sentinel only", err, other)
			}
		}
	}
	for _, s := range []error{provider.ErrNoModel, provider.ErrUnknownProvider} {
		if errors.Is(s, provider.ErrContextOverflow) {
			t.Errorf("errors.Is(%v, ErrContextOverflow) = true, want false", s)
		}
	}
}
