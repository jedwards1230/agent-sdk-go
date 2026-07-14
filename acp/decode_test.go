package acp

import (
	"encoding/json"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/event"
)

func TestDecodeOp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		params string
		wantOK bool
		want   event.Op
	}{
		{
			name:   "session/prompt -> PromptSend with text and attachment",
			method: MethodSessionPrompt,
			params: `{"sessionId":"s-1","prompt":[` +
				`{"type":"text","text":"hello "},` +
				`{"type":"text","text":"world"},` +
				`{"type":"resource_link","uri":"file:///a.go","name":"a.go"}]}`,
			wantOK: true,
			want:   event.PromptSend{SessionID: "s-1", Text: "hello world", Attachments: []string{"file:///a.go"}},
		},
		{
			name:   "session/cancel -> TurnInterrupt",
			method: MethodSessionCancel,
			params: `{"sessionId":"s-2"}`,
			wantOK: true,
			want:   event.TurnInterrupt{SessionID: "s-2"},
		},
		{
			name:   "session/new -> SessionNew",
			method: MethodSessionNew,
			params: `{"cwd":"/work","mcpServers":[]}`,
			wantOK: true,
			want:   event.SessionNew{Cwd: "/work"},
		},
		{
			// event.SessionNew has no model field yet, so a model in the
			// request does not change the projected op; see
			// TestNewSessionRequestUnmarshal for the field itself round-tripping.
			name:   "session/new with model -> SessionNew unchanged",
			method: MethodSessionNew,
			params: `{"cwd":"/work","model":"claude-sonnet-5"}`,
			wantOK: true,
			want:   event.SessionNew{Cwd: "/work"},
		},
		{
			name:   "session/load -> SessionResume",
			method: MethodSessionLoad,
			params: `{"sessionId":"s-3","cwd":"/work"}`,
			wantOK: true,
			want:   event.SessionResume{SessionID: "s-3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			op, ok, err := DecodeOp(tt.method, json.RawMessage(tt.params))
			if err != nil {
				t.Fatalf("DecodeOp() error = %v", err)
			}
			if ok != tt.wantOK {
				t.Fatalf("DecodeOp() ok = %v, want %v", ok, tt.wantOK)
			}
			// Compare by marshaled op envelope: Op is marshal-only, and this is
			// the shape a transport would forward.
			gotJSON, err := json.Marshal(op)
			if err != nil {
				t.Fatalf("marshal got op: %v", err)
			}
			wantJSON, err := json.Marshal(tt.want)
			if err != nil {
				t.Fatalf("marshal want op: %v", err)
			}
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("DecodeOp() op = %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestDecodeOpHandshakeHasNoOp(t *testing.T) {
	t.Parallel()

	for _, method := range []string{MethodInitialize, MethodAuthenticate} {
		op, ok, err := DecodeOp(method, json.RawMessage(`{}`))
		if err != nil {
			t.Errorf("DecodeOp(%q) error = %v, want nil", method, err)
		}
		if ok {
			t.Errorf("DecodeOp(%q) ok = true, want false (handshake carries no op)", method)
		}
		if op != nil {
			t.Errorf("DecodeOp(%q) op = %v, want nil", method, op)
		}
	}
}

func TestDecodeOpErrors(t *testing.T) {
	t.Parallel()

	t.Run("unknown method", func(t *testing.T) {
		t.Parallel()
		if _, _, err := DecodeOp("session/bogus", json.RawMessage(`{}`)); err == nil {
			t.Fatal("DecodeOp() with unknown method: want error, got nil")
		}
	})

	t.Run("malformed params", func(t *testing.T) {
		t.Parallel()
		if _, _, err := DecodeOp(MethodSessionCancel, json.RawMessage(`{`)); err == nil {
			t.Fatal("DecodeOp() with malformed params: want error, got nil")
		}
	})
}

func TestDecodeInitialize(t *testing.T) {
	t.Parallel()

	req, err := DecodeInitialize(json.RawMessage(`{"protocolVersion":1}`))
	if err != nil {
		t.Fatalf("DecodeInitialize() error = %v", err)
	}
	if req.ProtocolVersion != ProtocolVersion {
		t.Errorf("ProtocolVersion = %d, want %d", req.ProtocolVersion, ProtocolVersion)
	}

	if _, err := DecodeInitialize(json.RawMessage(`{`)); err == nil {
		t.Fatal("DecodeInitialize() with malformed params: want error, got nil")
	}
}
