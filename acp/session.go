package acp

import "encoding/json"

// NewSessionRequest is the payload of a session/new request. It has no
// tagged-union members, so the default json struct-tag encoding round-trips
// it without a custom Marshal/Unmarshal.
type NewSessionRequest struct {
	// Cwd is the working directory the session runs in.
	Cwd string `json:"cwd"`
	// McpServers carries MCP server configs opaquely; this package does not
	// project them.
	McpServers []json.RawMessage `json:"mcpServers,omitempty"`
	// Model optionally requests a specific model id for the session. Empty
	// means the daemon resolves its own default; this field is additive and
	// backward-compatible — a client that omits it sees no behavior change.
	Model string `json:"model,omitempty"`
}

// NewSessionResponse is the payload of a session/new response.
type NewSessionResponse struct {
	// SessionID is the id of the newly created session.
	SessionID string `json:"sessionId"`
}

// LoadSessionRequest is the payload of a session/load request.
type LoadSessionRequest struct {
	// SessionID identifies the session to reload.
	SessionID string `json:"sessionId"`
	// Cwd is the working directory to reload the session into.
	Cwd string `json:"cwd"`
}

// CancelNotification is the payload of a session/cancel notification: the id
// of the session whose running turn should be interrupted.
type CancelNotification struct {
	// SessionID identifies the session to cancel.
	SessionID string `json:"sessionId"`
}

// LoadSessionResponse is the payload of a session/load response. Per the ACP
// v1 schema, every LoadSessionResponse field (modes, configOptions, _meta) is
// optional, so an empty object is a conformant result; a consuming
// application returns this instead of a local ad-hoc type once it has
// replayed the session's history via session/update notifications (see
// [ReplayNotifications]).
type LoadSessionResponse struct{}
