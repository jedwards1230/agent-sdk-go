package search_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/search"
)

func TestErrorUnwrap(t *testing.T) {
	err := &search.Error{Provider: "brave", Kind: search.ErrKindRequest, Err: context.Canceled}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("errors.Is(err, context.Canceled) = false, want true (err = %v)", err)
	}
}

func TestErrorMessage(t *testing.T) {
	tests := []struct {
		name string
		err  *search.Error
		want string
	}{
		{
			name: "with status code",
			err:  &search.Error{Provider: "brave", Kind: search.ErrKindHTTP, StatusCode: 429, Err: errors.New("rate limited")},
			want: "search: brave: http (status 429): rate limited",
		},
		{
			name: "without status code",
			err:  &search.Error{Provider: "searxng", Kind: search.ErrKindDecode, Err: errors.New("unexpected token")},
			want: "search: searxng: decode: unexpected token",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestErrKindString(t *testing.T) {
	tests := []struct {
		kind search.ErrKind
		want string
	}{
		{search.ErrKindConfig, "config"},
		{search.ErrKindRequest, "request"},
		{search.ErrKindHTTP, "http"},
		{search.ErrKindDecode, "decode"},
		{search.ErrKind(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.kind.String(); got != tt.want {
			t.Errorf("ErrKind(%d).String() = %q, want %q", tt.kind, got, tt.want)
		}
	}
}
