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
// carries no op — the handshake methods initialize and authenticate, which a
// transport answers itself (see [NewInitializeResponse]). It errors on
// malformed params or an unknown method.
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

	case MethodInitialize, MethodAuthenticate:
		// Handshake methods carry no op; the transport answers them directly.
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

// unmarshalParams unmarshals a method's params, wrapping the error with the
// method name for a transport's diagnostics.
func unmarshalParams(method string, params json.RawMessage, v any) error {
	if err := json.Unmarshal(params, v); err != nil {
		return fmt.Errorf("acp: decode %s params: %w", method, err)
	}
	return nil
}
