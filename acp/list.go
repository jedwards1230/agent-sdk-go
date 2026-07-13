package acp

// ListSessionsRequest is the payload of a session/list request. It has no
// tagged-union members, so the default json struct-tag encoding round-trips
// it without a custom Marshal/Unmarshal.
type ListSessionsRequest struct {
	// Cwd optionally filters sessions to those created under this absolute
	// path.
	Cwd string `json:"cwd,omitempty"`
	// Cursor is an opaque pagination token from a previous
	// [ListSessionsResponse.NextCursor]; empty requests the first page.
	Cursor string `json:"cursor,omitempty"`
}

// SessionInfo is one session's metadata entry in a [ListSessionsResponse].
type SessionInfo struct {
	// SessionID identifies the session.
	SessionID string `json:"sessionId"`
	// Cwd is the working directory the session was created in.
	Cwd string `json:"cwd"`
	// Title is an optional human-readable session title.
	Title string `json:"title,omitempty"`
	// UpdatedAt is an optional ISO 8601 timestamp of the session's last
	// activity.
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// ListSessionsResponse is the payload of a session/list response.
type ListSessionsResponse struct {
	// Sessions is the page of matching sessions. Callers pass a non-nil
	// (possibly empty) slice so it marshals as "[]" rather than "null".
	Sessions []SessionInfo `json:"sessions"`
	// NextCursor is an opaque token for the next page, or empty when this is
	// the last page.
	NextCursor string `json:"nextCursor,omitempty"`
}
