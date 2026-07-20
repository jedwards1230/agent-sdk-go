package acp

import (
	"encoding/json"
	"fmt"

	"github.com/jedwards1230/agent-sdk-go/event"
)

// DecodeOp decodes a client→agent JSON-RPC method and its raw params directly
// into the typed [event.Op] the ACP method projects to. It owns the
// ACP-JSON → Op boundary so a transport only frames bytes: the transport reads
// the JSON-RPC envelope, hands DecodeOp the method name and params, and
// dispatches the returned op to the supervisor.
//
// ok is false, with a nil op and nil error, for a well-formed method that
// carries no op — the methods the SDK does not pre-project to an op, which a
// transport answers directly via their exported request/response types:
//
//   - initialize / authenticate — handshakes (see [NewInitializeResponse]).
//   - session/list — a query answered from the session store (decode its
//     params with [DecodeListSessions], reply with [ListSessionsResponse]).
//   - session/set_config_option — a config mutation whose ConfigID/value
//     semantics are the application's business logic, not the SDK's (decode
//     with [DecodeSetConfigOption], reply with [SetConfigOptionResponse]).
//   - session/explain_permission — a read-only rationale query answered from
//     the daemon's held guard decision (decode with [DecodeExplainPermission],
//     reply with [ExplainPermissionResponse]).
//
// It errors on malformed params or an unknown method.
//
// The four op-bearing methods and their projections:
//
//   - session/prompt  → [event.PromptSend]     (via [FromPrompt])
//   - session/cancel  → [event.TurnInterrupt]  (via [FromCancel])
//   - session/new     → [event.SessionNew]     (via [FromNewSession])
//   - session/load    → [event.SessionResume]  (via [FromLoadSession])
func DecodeOp(method string, params json.RawMessage) (op event.Op, ok bool, err error) {
	switch method {
	case MethodSessionPrompt:
		var req PromptRequest
		if err := unmarshalParams(method, params, &req); err != nil {
			return nil, false, err
		}
		return FromPrompt(req), true, nil

	case MethodSessionCancel:
		var n CancelNotification
		if err := unmarshalParams(method, params, &n); err != nil {
			return nil, false, err
		}
		return FromCancel(n), true, nil

	case MethodSessionNew:
		var req NewSessionRequest
		if err := unmarshalParams(method, params, &req); err != nil {
			return nil, false, err
		}
		return FromNewSession(req), true, nil

	case MethodSessionLoad:
		var req LoadSessionRequest
		if err := unmarshalParams(method, params, &req); err != nil {
			return nil, false, err
		}
		return FromLoadSession(req), true, nil

	case MethodInitialize, MethodAuthenticate, MethodSessionList, MethodSessionSetConfigOption, MethodSessionExplainPermission:
		// These carry no pre-projected op: the handshakes, the session/list
		// query, session/set_config_option (its config semantics are the
		// application's business logic), and session/explain_permission (a
		// read-only rationale query answered from the daemon's held guard
		// decision, not a mutation). A transport answers them directly via the
		// exported request/response types (see [DecodeListSessions],
		// [DecodeSetConfigOption], and [DecodeExplainPermission]).
		return nil, false, nil

	default:
		return nil, false, fmt.Errorf("acp: unknown method %q", method)
	}
}

// DecodeInitialize decodes the params of an initialize request. A transport
// uses it to read the client's requested protocol version and capabilities
// before replying with [NewInitializeResponse]; the handshake has no op, so it
// is not covered by [DecodeOp].
func DecodeInitialize(params json.RawMessage) (InitializeRequest, error) {
	var req InitializeRequest
	if err := unmarshalParams(MethodInitialize, params, &req); err != nil {
		return InitializeRequest{}, err
	}
	return req, nil
}

// DecodeListSessions decodes the params of a session/list request. The method
// carries no op (it is a read query, not a mutation dispatched to the
// supervisor), so it is not covered by [DecodeOp]; a transport decodes the
// filter/cursor here, answers from its session store, and replies with a
// [ListSessionsResponse].
func DecodeListSessions(params json.RawMessage) (ListSessionsRequest, error) {
	var req ListSessionsRequest
	if err := unmarshalParams(MethodSessionList, params, &req); err != nil {
		return ListSessionsRequest{}, err
	}
	return req, nil
}

// DecodeSetConfigOption decodes the params of a session/set_config_option
// request. The SDK deliberately does not project this to an op: the meaning of
// ConfigID and the resulting config set are the application's business logic,
// so a transport decodes the typed request here, applies its own change, and
// replies with a [SetConfigOptionResponse].
func DecodeSetConfigOption(params json.RawMessage) (SetConfigOptionRequest, error) {
	var req SetConfigOptionRequest
	if err := unmarshalParams(MethodSessionSetConfigOption, params, &req); err != nil {
		return SetConfigOptionRequest{}, err
	}
	return req, nil
}

// DecodeExplainPermission decodes the params of a session/explain_permission
// request. The method carries no op (it is a read-only rationale query, not a
// mutation dispatched to the supervisor), so it is not covered by [DecodeOp];
// a transport decodes the request here, builds the [PermissionRationale] from
// the guard decision it holds for the still-pending permission request, and
// replies with an [ExplainPermissionResponse].
func DecodeExplainPermission(params json.RawMessage) (ExplainPermissionRequest, error) {
	var req ExplainPermissionRequest
	if err := unmarshalParams(MethodSessionExplainPermission, params, &req); err != nil {
		return ExplainPermissionRequest{}, err
	}
	return req, nil
}

// unmarshalParams unmarshals a method's params, wrapping the error with the
// method name for a transport's diagnostics.
func unmarshalParams(method string, params json.RawMessage, v any) error {
	if err := json.Unmarshal(params, v); err != nil {
		return fmt.Errorf("acp: decode %s params: %w", method, err)
	}
	return nil
}
