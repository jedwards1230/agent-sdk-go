package lsp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// jsonrpcVersion is the fixed "jsonrpc" field value the LSP base protocol
// requires on every message.
const jsonrpcVersion = "2.0"

// contentLengthHeader is the LSP base-protocol framing header carrying the
// payload's byte length. Header names are matched case-insensitively per the
// spec.
const contentLengthHeader = "content-length"

// errMissingContentLength is returned by readFrame when a frame's header
// block ends without a Content-Length header.
var errMissingContentLength = errors.New("lsp: frame missing Content-Length header")

// writeFrame writes payload to w using the LSP base protocol: a
// Content-Length header, a blank line, then the raw bytes. No other headers
// are written.
func writeFrame(w io.Writer, payload []byte) error {
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(payload)); err != nil {
		return fmt.Errorf("lsp: write frame header: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("lsp: write frame body: %w", err)
	}
	return nil
}

// readFrame reads one LSP base-protocol frame from r: a run of "Name: Value"
// header lines terminated by a blank line, then exactly Content-Length body
// bytes. Header names are matched case-insensitively; headers other than
// Content-Length (e.g. Content-Type) are tolerated and ignored. It returns
// io.EOF only when the stream ends cleanly before any bytes of a new frame
// are read (the normal end-of-connection signal); a header line malformed or
// truncated mid-frame, a missing or non-numeric Content-Length, or a body cut
// short by EOF are all reported as a wrapped error, never as io.EOF.
func readFrame(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				if line == "" {
					return nil, io.EOF
				}
				// A header line started but the stream ended before its
				// newline: report as a distinct truncation, not io.EOF, so
				// errors.Is(err, io.EOF) stays false for callers that use it
				// to detect a clean end of connection.
				return nil, fmt.Errorf("lsp: read frame header: %w", io.ErrUnexpectedEOF)
			}
			return nil, fmt.Errorf("lsp: read frame header: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // blank line ends the header block
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("lsp: malformed frame header: %q", line)
		}
		if strings.EqualFold(strings.TrimSpace(name), contentLengthHeader) {
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || n < 0 {
				return nil, fmt.Errorf("lsp: invalid Content-Length: %q", value)
			}
			length = n
		}
	}
	if length < 0 {
		return nil, errMissingContentLength
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		// io.ReadFull returns io.EOF itself only when zero bytes were read
		// before the stream ended; normalize that to ErrUnexpectedEOF too, so
		// a body cut short (however little of it arrived) never reads as
		// io.EOF to a caller checking errors.Is(err, io.EOF) for a clean
		// end of connection — we are mid-frame, past the header, so any EOF
		// here is a truncation, not a clean close.
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("lsp: read frame body: %w", err)
	}
	return body, nil
}

// requestMessage is an outbound JSON-RPC 2.0 request: it expects a response
// correlated by ID.
type requestMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// notificationMessage is an outbound JSON-RPC 2.0 message with no ID — no
// response is expected.
type notificationMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// responseMessage is a decoded JSON-RPC 2.0 response to one of our requests.
type responseMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *responseError  `json:"error,omitempty"`
}

// responseError is a JSON-RPC 2.0 error object.
type responseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Error implements the error interface so a responseError can be returned
// directly from a call.
func (e *responseError) Error() string {
	return fmt.Sprintf("lsp: server error %d: %s", e.Code, e.Message)
}

// inboundMessage is the generic shape used to decode a frame from the server:
// a response carries ID plus Result or Error and no Method; a notification
// carries Method and no ID. This client never expects a server-to-client
// request (LSP servers only send diagnostics and other notifications to a
// client that hasn't advertised request-capable capabilities), but decoding
// ID and Method together lets readLoop tell the two apart without a second
// parse pass.
type inboundMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *responseError  `json:"error,omitempty"`
}
