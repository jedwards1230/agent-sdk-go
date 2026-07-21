package event

import "encoding/json"

// Op kind identifiers. Each is the value returned by an op's Kind method and
// the "type" field of its JSON envelope.
const (
	OpSessionNew       = "session.new"
	OpSessionResume    = "session.resume"
	OpSessionFork      = "session.fork"
	OpPromptSend       = "prompt.send"
	OpPromptQueueList  = "prompt.queue.list"
	OpPromptQueueClear = "prompt.queue.clear"
	OpPermissionReply  = "permission.reply"
	OpTurnInterrupt    = "turn.interrupt"
	OpToolCancel       = "tool.cancel"
	OpSessionCompact   = "session.compact"
	OpSessionSetModel  = "session.set_model"
	OpSessionSetEffort = "session.set_effort"
	OpSessionKill      = "session.kill"
	OpSessionArchive   = "session.archive"
)

// Op is a message a client sends to the agent. Like [Event] it serializes to a
// tagged JSON envelope ({"type", ...payload}); unlike events, ops are not
// ordered through the broker.
type Op interface {
	// Kind returns the op's kind identifier (e.g. "prompt.send").
	Kind() string
	json.Marshaler
}

// opEnvelope is the wire header shared by every op.
type opEnvelope struct {
	Type string `json:"type"`
}

// marshalSessionOp encodes an op that carries only a session id.
func marshalSessionOp(o Op, session string) ([]byte, error) {
	return json.Marshal(struct {
		opEnvelope
		SessionID string `json:"session_id"`
	}{opEnvelope{o.Kind()}, session})
}

// SessionNew requests a new session for an agent in a working directory.
type SessionNew struct {
	Agent    string
	Cwd      string
	Worktree string
}

// Kind returns OpSessionNew.
func (SessionNew) Kind() string { return OpSessionNew }

// MarshalJSON encodes the envelope plus {agent, cwd, worktree?}.
func (o SessionNew) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		opEnvelope
		Agent    string `json:"agent"`
		Cwd      string `json:"cwd"`
		Worktree string `json:"worktree,omitempty"`
	}{opEnvelope{o.Kind()}, o.Agent, o.Cwd, o.Worktree})
}

// SessionResume requests reloading a persisted session.
type SessionResume struct{ SessionID string }

// Kind returns OpSessionResume.
func (SessionResume) Kind() string { return OpSessionResume }

// MarshalJSON encodes the envelope plus {session_id}.
func (o SessionResume) MarshalJSON() ([]byte, error) { return marshalSessionOp(o, o.SessionID) }

// SessionFork requests branching a session at a given entry.
type SessionFork struct {
	SessionID string
	At        string
}

// Kind returns OpSessionFork.
func (SessionFork) Kind() string { return OpSessionFork }

// MarshalJSON encodes the envelope plus {session_id, at}.
func (o SessionFork) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		opEnvelope
		SessionID string `json:"session_id"`
		At        string `json:"at"`
	}{opEnvelope{o.Kind()}, o.SessionID, o.At})
}

// PromptSend queues a prompt for a session. It runs immediately if the session
// is idle and queues otherwise.
type PromptSend struct {
	SessionID   string
	Text        string
	Attachments []string
}

// Kind returns OpPromptSend.
func (PromptSend) Kind() string { return OpPromptSend }

// MarshalJSON encodes the envelope plus {session_id, text, attachments?}.
func (o PromptSend) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		opEnvelope
		SessionID   string   `json:"session_id"`
		Text        string   `json:"text"`
		Attachments []string `json:"attachments,omitempty"`
	}{opEnvelope{o.Kind()}, o.SessionID, o.Text, o.Attachments})
}

// PromptQueueList requests the pending prompt queue for a session.
type PromptQueueList struct{ SessionID string }

// Kind returns OpPromptQueueList.
func (PromptQueueList) Kind() string { return OpPromptQueueList }

// MarshalJSON encodes the envelope plus {session_id}.
func (o PromptQueueList) MarshalJSON() ([]byte, error) { return marshalSessionOp(o, o.SessionID) }

// PromptQueueClear clears the pending prompt queue for a session.
type PromptQueueClear struct{ SessionID string }

// Kind returns OpPromptQueueClear.
func (PromptQueueClear) Kind() string { return OpPromptQueueClear }

// MarshalJSON encodes the envelope plus {session_id}.
func (o PromptQueueClear) MarshalJSON() ([]byte, error) { return marshalSessionOp(o, o.SessionID) }

// PermissionReply answers a permission request. Remember persists the verdict
// as a grant.
type PermissionReply struct {
	ID       string
	Verdict  Verdict
	Remember bool
	// Input, when non-nil, is replacement tool input supplied with an amended
	// allow (the ACP amend-before-approve outcome): an allow whose call runs
	// with this input in place of the model's original arguments. It is nil for
	// a plain allow/deny, and is ignored unless Verdict is VerdictAllow.
	Input json.RawMessage
}

// Kind returns OpPermissionReply.
func (PermissionReply) Kind() string { return OpPermissionReply }

// MarshalJSON encodes the envelope plus {id, verdict, remember?, input?}.
func (o PermissionReply) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		opEnvelope
		ID       string          `json:"id"`
		Verdict  Verdict         `json:"verdict"`
		Remember bool            `json:"remember,omitempty"`
		Input    json.RawMessage `json:"input,omitempty"`
	}{opEnvelope{o.Kind()}, o.ID, o.Verdict, o.Remember, o.Input})
}

// TurnInterrupt interrupts the running turn of a session.
type TurnInterrupt struct{ SessionID string }

// Kind returns OpTurnInterrupt.
func (TurnInterrupt) Kind() string { return OpTurnInterrupt }

// MarshalJSON encodes the envelope plus {session_id}.
func (o TurnInterrupt) MarshalJSON() ([]byte, error) { return marshalSessionOp(o, o.SessionID) }

// ToolCancel cancels an in-flight tool call.
type ToolCancel struct {
	SessionID string
	ID        string
}

// Kind returns OpToolCancel.
func (ToolCancel) Kind() string { return OpToolCancel }

// MarshalJSON encodes the envelope plus {session_id, id}.
func (o ToolCancel) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		opEnvelope
		SessionID string `json:"session_id"`
		ID        string `json:"id"`
	}{opEnvelope{o.Kind()}, o.SessionID, o.ID})
}

// SessionCompact requests compaction of a session's history.
type SessionCompact struct{ SessionID string }

// Kind returns OpSessionCompact.
func (SessionCompact) Kind() string { return OpSessionCompact }

// MarshalJSON encodes the envelope plus {session_id}.
func (o SessionCompact) MarshalJSON() ([]byte, error) { return marshalSessionOp(o, o.SessionID) }

// SessionSetModel changes the model bound to a session.
type SessionSetModel struct {
	SessionID string
	Model     string
}

// Kind returns OpSessionSetModel.
func (SessionSetModel) Kind() string { return OpSessionSetModel }

// MarshalJSON encodes the envelope plus {session_id, model}.
func (o SessionSetModel) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		opEnvelope
		SessionID string `json:"session_id"`
		Model     string `json:"model"`
	}{opEnvelope{o.Kind()}, o.SessionID, o.Model})
}

// SessionSetEffort changes the reasoning effort bound to a session. It is the
// effort-axis parallel to [SessionSetModel]; Effort is a unified level ("low",
// "medium", "high", or "" to clear to the provider default).
type SessionSetEffort struct {
	SessionID string
	Effort    string
}

// Kind returns OpSessionSetEffort.
func (SessionSetEffort) Kind() string { return OpSessionSetEffort }

// MarshalJSON encodes the envelope plus {session_id, effort}.
func (o SessionSetEffort) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		opEnvelope
		SessionID string `json:"session_id"`
		Effort    string `json:"effort"`
	}{opEnvelope{o.Kind()}, o.SessionID, o.Effort})
}

// SessionKill terminates a session.
type SessionKill struct{ SessionID string }

// Kind returns OpSessionKill.
func (SessionKill) Kind() string { return OpSessionKill }

// MarshalJSON encodes the envelope plus {session_id}.
func (o SessionKill) MarshalJSON() ([]byte, error) { return marshalSessionOp(o, o.SessionID) }

// SessionArchive archives a session.
type SessionArchive struct{ SessionID string }

// Kind returns OpSessionArchive.
func (SessionArchive) Kind() string { return OpSessionArchive }

// MarshalJSON encodes the envelope plus {session_id}.
func (o SessionArchive) MarshalJSON() ([]byte, error) { return marshalSessionOp(o, o.SessionID) }
