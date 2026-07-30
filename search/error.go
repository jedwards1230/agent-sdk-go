package search

import "fmt"

// ErrKind classifies a *[Error].
type ErrKind int

// The error kinds a [Provider] reports.
const (
	// ErrKindConfig marks a missing or invalid [Config] field (e.g.
	// search/searxng.New with an empty BaseURL), or an empty query.
	ErrKindConfig ErrKind = iota
	// ErrKindRequest marks a failure to build or send the HTTP request —
	// including a cancelled or expired ctx, which Err unwraps to.
	ErrKindRequest
	// ErrKindHTTP marks a non-2xx HTTP response. StatusCode is set.
	ErrKindHTTP
	// ErrKindDecode marks a response body that could not be parsed as the
	// backend's expected shape.
	ErrKindDecode
)

// String renders the kind for error messages and test diagnostics.
func (k ErrKind) String() string {
	switch k {
	case ErrKindConfig:
		return "config"
	case ErrKindRequest:
		return "request"
	case ErrKindHTTP:
		return "http"
	case ErrKindDecode:
		return "decode"
	default:
		return "unknown"
	}
}

// Error is a typed search failure. Provider names the offending backend
// (Provider.Name()), Kind classifies the failure, StatusCode is the HTTP
// status for [ErrKindHTTP] (zero otherwise), and Err carries the underlying
// cause — a context error on cancellation/deadline, a *json.SyntaxError on a
// malformed body, or the transport error from the failed request.
type Error struct {
	Provider   string
	Kind       ErrKind
	StatusCode int
	Err        error
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("search: %s: %s (status %d): %v", e.Provider, e.Kind, e.StatusCode, e.Err)
	}
	return fmt.Sprintf("search: %s: %s: %v", e.Provider, e.Kind, e.Err)
}

// Unwrap exposes Err so callers can errors.Is/As through to the underlying
// cause (e.g. context.Canceled, a *json.SyntaxError).
func (e *Error) Unwrap() error { return e.Err }
