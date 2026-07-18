package event

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// ErrUnknownKind is wrapped by the error [Unmarshal] returns when an envelope's
// "type" is not one of this package's event kinds. It is a sentinel a
// forward-compatible consumer streaming events from a newer producer can match
// with [errors.Is] to skip an unrecognized kind and continue, rather than
// treating it as a hard decode failure. The offending kind is quoted in the
// wrapped error's message.
var ErrUnknownKind = errors.New("event: unknown kind")

// Unmarshal decodes one event's JSON envelope — the output of an [Event]'s
// MarshalJSON — back into its concrete type, and is the inverse of that
// encoding: the type/session_id/seq/time envelope and every payload field a
// kind carries are restored, so a value round-trips
// (construct → MarshalJSON → Unmarshal) equal to the original. The one exception
// is that a nil [ConfigOptionsUpdated.Options] or [PlanUpdated.Entries]
// marshals to [] (its MarshalJSON normalizes the absent case) and so decodes
// back to an empty, non-nil slice rather than nil — the value re-marshals
// identically, which is the property the wire guarantees. It is the shared
// decoder for any cross-process consumer that reconstructs a typed event stream
// from its wire form.
//
// The returned error distinguishes two failure modes a mixed-version consumer
// treats differently. A "type" this package does not recognize (a kind emitted
// by a newer producer) yields an error wrapping [ErrUnknownKind] — non-fatal by
// intent, so a forward-compatible caller can match it with [errors.Is] and
// skip-and-continue. Malformed JSON, or a "time" field that is neither empty
// nor RFC 3339, yields an ordinary decode error carrying no sentinel.
//
// Seq and Time are restored verbatim from the envelope; a consumer that
// republishes the event through a [Broker] gets fresh seq/time reassigned at
// publish, exactly as for any other published event.
func Unmarshal(data []byte) (Event, error) {
	var w wireEvent
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, fmt.Errorf("event: decode envelope: %w", err)
	}
	ts, err := parseWireTime(w.Time)
	if err != nil {
		return nil, fmt.Errorf("event: parse time %q: %w", w.Time, err)
	}
	m := meta{session: w.SessionID, seq: w.Seq, ts: ts}

	switch w.Type {
	case KindSessionCreated:
		return SessionCreated{m}, nil
	case KindSessionResumed:
		return SessionResumed{m}, nil
	case KindSessionForked:
		return SessionForked{m}, nil
	case KindSessionCompacted:
		return SessionCompacted{m}, nil
	case KindSessionKilled:
		return SessionKilled{m}, nil
	case KindSessionArchived:
		return SessionArchived{m}, nil
	case KindSessionInfo:
		return SessionInfoUpdated{meta: m, Title: w.Title}, nil
	case KindSessionConfig:
		return ConfigOptionsUpdated{meta: m, Options: w.Options}, nil
	case KindPlan:
		return PlanUpdated{meta: m, Entries: w.Entries}, nil
	case KindSessionError:
		return SessionError{meta: m, Err: w.Err, Fatal: w.Fatal}, nil
	case KindTurnStarted:
		return TurnStarted{m}, nil
	case KindTurnFinished:
		return TurnFinished{
			meta:          m,
			StopReason:    w.StopReason,
			Usage:         w.Usage,
			Cost:          w.Cost,
			ContextWindow: w.ContextWindow,
		}, nil
	case KindMessageStarted:
		return MessageStarted{meta: m, MessageKind: w.MessageKind}, nil
	case KindMessageDelta:
		return MessageDelta{meta: m, MessageKind: w.MessageKind, Text: w.Text}, nil
	case KindMessageFinished:
		return MessageFinished{meta: m, MessageKind: w.MessageKind, Content: w.Content, Meta: w.Meta}, nil
	case KindToolCallStarted:
		return ToolCallStarted{meta: m, ID: w.ID, Name: w.Name, Input: w.Input}, nil
	case KindToolCallDelta:
		return ToolCallDelta{meta: m, ID: w.ID, Delta: w.Delta}, nil
	case KindToolCallFinished:
		return ToolCallFinished{
			meta:        m,
			ID:          w.ID,
			Input:       w.Input,
			Result:      w.Result,
			IsError:     w.IsError,
			Diagnostics: w.Diagnostics,
			Edits:       w.Edits,
			SpillPath:   w.SpillPath,
			SpillBytes:  w.SpillBytes,
			SpillSHA256: w.SpillSHA256,
		}, nil
	case KindPermissionRequested:
		return PermissionRequested{meta: m, ID: w.ID, Tool: w.Tool, Spec: w.Spec, Trace: w.Trace}, nil
	case KindPermissionResolved:
		return PermissionResolved{meta: m, ID: w.ID, Verdict: w.Verdict, Rule: w.Rule}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownKind, w.Type)
	}
}

// parseWireTime inverts formatTime: an empty string (a zero publish time) maps
// back to the zero [time.Time]; any other value is an RFC 3339 timestamp.
func parseWireTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

// wireEvent is the union of every concrete event's MarshalJSON payload: one
// struct decodes any envelope because encoding/json ignores payload fields
// absent from a given kind's JSON and leaves their Go fields zero — exactly
// what an unpopulated kind's fields should be. [Unmarshal] then dispatches on
// Type and reads only the fields that kind carries. Every tag mirrors the
// matching type's MarshalJSON in event.go verbatim.
type wireEvent struct {
	// envelope, shared by every kind (see envelope in event.go)
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Seq       uint64 `json:"seq"`
	Time      string `json:"time"`

	// session.error
	Err   string `json:"error"`
	Fatal bool   `json:"fatal"`

	// session.info
	Title string `json:"title"`

	// session.config
	Options []ConfigOption `json:"options"`

	// plan
	Entries []PlanEntry `json:"entries"`

	// turn.finished
	StopReason    string         `json:"stop_reason"`
	Usage         provider.Usage `json:"usage"`
	Cost          *provider.Cost `json:"cost"`
	ContextWindow int            `json:"context_window"`

	// message.started / message.delta / message.finished
	MessageKind MessageKind       `json:"kind"`
	Text        string            `json:"text"`
	Content     string            `json:"content"`
	Meta        map[string]string `json:"meta"`

	// tool.call.started / tool.call.delta / tool.call.finished
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Input       json.RawMessage `json:"input"`
	Delta       string          `json:"delta"`
	Result      string          `json:"result"`
	IsError     bool            `json:"is_error"`
	Diagnostics []string        `json:"diagnostics"`
	Edits       []FileEdit      `json:"edits"`
	SpillPath   string          `json:"spill_path"`
	SpillBytes  int64           `json:"spill_bytes"`
	SpillSHA256 string          `json:"spill_sha256"`

	// permission.requested / permission.resolved
	Tool    string         `json:"tool"`
	Spec    map[string]any `json:"spec"`
	Trace   []string       `json:"trace"`
	Verdict Verdict        `json:"verdict"`
	Rule    string         `json:"rule"`
}
