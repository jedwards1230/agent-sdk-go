package openai

import "fmt"

// APIError is a non-200 HTTP response from the Responses API. It carries the
// status code and the (truncated) response body for diagnosis.
type APIError struct {
	StatusCode int
	Body       string
}

// Error implements error.
func (e *APIError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("openai: http %d", e.StatusCode)
	}
	return fmt.Sprintf("openai: http %d: %s", e.StatusCode, e.Body)
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
