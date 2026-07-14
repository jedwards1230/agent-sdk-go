package acp

import "github.com/jedwards1230/agent-sdk-go/event"

// FromPrompt projects a session/prompt request to a [event.PromptSend] op.
// Text is the concatenation of the prompt's text blocks ([BlocksText]);
// Attachments carries the URI of every resource_link block, in order.
func FromPrompt(req PromptRequest) event.PromptSend {
	var attachments []string
	for _, b := range req.Prompt {
		if link, ok := b.(ResourceLinkContentBlock); ok {
			attachments = append(attachments, link.URI)
		}
	}
	return event.PromptSend{
		SessionID:   req.SessionID,
		Text:        BlocksText(req.Prompt),
		Attachments: attachments,
	}
}

// FromCancel projects a session/cancel notification to a
// [event.TurnInterrupt] op.
func FromCancel(n CancelNotification) event.TurnInterrupt {
	return event.TurnInterrupt{SessionID: n.SessionID}
}

// FromNewSession projects a session/new request to a [event.SessionNew] op.
// Agent is left empty: ACP's session/new has no agent-selection field, so the
// daemon fills it in from its own routing (e.g. the ACP connection's bound
// agent) before dispatching the op. Model is not projected: [event.SessionNew]
// has no model field yet, so a consuming application reads req.Model directly
// off the decoded request if it wants to honor a per-session model.
func FromNewSession(req NewSessionRequest) event.SessionNew {
	return event.SessionNew{Cwd: req.Cwd}
}

// FromLoadSession projects a session/load request to a [event.SessionResume]
// op.
func FromLoadSession(req LoadSessionRequest) event.SessionResume {
	return event.SessionResume{SessionID: req.SessionID}
}

// ToPermissionReply projects a session/request_permission response to a
// [event.PermissionReply] op for the permission request identified by id.
//
// ACP's [RequestPermissionResponse] only carries back the chosen option's id
// (or a cancellation); it does not carry the option's [PermissionOptionKind].
// The daemon holds the original [PermissionOption] set for a request (it
// built it in [ToRequestPermission]), so it resolves outcome's selected
// optionId against that set and passes the matching option as chosen. chosen
// is ignored when outcome is a cancellation.
//
// A cancelled outcome, or a chosen option with an unmodeled kind, both
// fail-safe to a non-remembered deny.
func ToPermissionReply(id string, outcome RequestPermissionResponse, chosen PermissionOption) event.PermissionReply {
	if _, cancelled := outcome.Outcome.(PermissionOutcomeCancelled); cancelled || outcome.Outcome == nil {
		return event.PermissionReply{ID: id, Verdict: event.VerdictDeny, Remember: false}
	}

	switch chosen.Kind {
	case PermissionAllowOnce:
		return event.PermissionReply{ID: id, Verdict: event.VerdictAllow, Remember: false}
	case PermissionAllowAlways:
		return event.PermissionReply{ID: id, Verdict: event.VerdictAllow, Remember: true}
	case PermissionRejectOnce:
		return event.PermissionReply{ID: id, Verdict: event.VerdictDeny, Remember: false}
	case PermissionRejectAlways:
		return event.PermissionReply{ID: id, Verdict: event.VerdictDeny, Remember: true}
	default:
		return event.PermissionReply{ID: id, Verdict: event.VerdictDeny, Remember: false}
	}
}
