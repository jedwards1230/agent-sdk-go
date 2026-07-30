package mcp

import (
	"encoding/json"
	"fmt"
)

// jsonrpcVersion is the fixed "jsonrpc" field value JSON-RPC 2.0 requires on
// every message.
const jsonrpcVersion = "2.0"

// protocolVersion is the MCP protocol version this client speaks in the
// "initialize" handshake. MCP's protocol versioning is date-stamped; a server
// speaking a different version negotiates down (or fails) inside its own
// "initialize" response — this client does not renegotiate, matching the
// stdlib-only, no-fallback-machinery posture of [lsp.Client].
const protocolVersion = "2025-06-18"

// rpcRequest is an outbound JSON-RPC 2.0 request: it expects a response
// correlated by ID.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcNotification is an outbound JSON-RPC 2.0 message with no ID — no
// response is expected.
type rpcNotification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse is a decoded JSON-RPC 2.0 response to one of our requests.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is a JSON-RPC 2.0 error object.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error implements the error interface so an *rpcError can be returned
// directly from a call.
func (e *rpcError) Error() string {
	return fmt.Sprintf("mcp: server error %d: %s", e.Code, e.Message)
}

// rpcInbound is the generic shape used to decode a frame arriving from the
// server: a response carries ID plus Result or Error and no Method; a
// notification carries Method and no ID. A well-behaved MCP server never
// sends this client a server-to-client request (this client advertises no
// capability that would prompt one), but decoding ID and Method together lets
// the read loop tell a response from a notification without a second parse
// pass — the same shape [lsp.Client]'s protocol.go uses for the same reason.
type rpcInbound struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// marshalParams marshals params, or returns a nil RawMessage when params is
// nil so the request/notification's "params" field is omitted entirely
// (omitempty on a json.RawMessage checks length) rather than sent as a
// literal JSON null.
func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	return json.Marshal(params)
}
