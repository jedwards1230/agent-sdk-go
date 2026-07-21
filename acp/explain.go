package acp

// ExplainPermissionRequest is the payload of a session/explain_permission
// request, sent client-to-agent while a session/request_permission is still
// pending to ask *why* the identified tool call was gated. It has no
// tagged-union members, so the default json struct-tag encoding round-trips it
// without a custom Marshal/Unmarshal.
type ExplainPermissionRequest struct {
	// SessionID identifies the session the gated tool call belongs to.
	SessionID string `json:"sessionId"`
	// ToolCallID identifies the tool call whose gating rationale is requested;
	// it matches the [RequestPermissionRequest.ToolCall]'s toolCallId.
	ToolCallID string `json:"toolCallId"`
}

// PermissionRationale explains why a tool call was gated: the human-readable
// reason plus the machine-readable provenance a client can surface (which
// matched policy gated it, where that policy came from, and the step-by-step
// decision trace). Every field beyond Reason is optional; the daemon fills
// them from the guard decision it holds for the pending request (e.g. the
// matched-rule label and decision trace carried on the permission events).
type PermissionRationale struct {
	// Reason is a human-readable summary of why the call was gated.
	Reason string `json:"reason"`
	// Policy is the matched rule/policy label that gated the call, or empty if
	// none — the same label carried on permission.resolved's rule field.
	Policy string `json:"policy,omitempty"`
	// Source is the provenance of the gating policy (e.g. "session", "project",
	// or a hook label), or empty if unknown.
	Source string `json:"source,omitempty"`
	// Trace is the step-by-step decision trace, or nil if none — the same trace
	// carried on the permission.requested event.
	Trace []string `json:"trace,omitempty"`
}

// ExplainPermissionResponse is the payload of a session/explain_permission
// response, sent agent-to-client with the gating [PermissionRationale]. The
// pending permission request is left unresolved: after reading the rationale
// the client re-prompts the human with the original options.
type ExplainPermissionResponse struct {
	// Rationale is the gating rationale for the requested tool call.
	Rationale PermissionRationale `json:"rationale"`
}
