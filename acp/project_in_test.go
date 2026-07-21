package acp_test

import (
	"reflect"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/acp"
	"github.com/jedwards1230/agent-sdk-go/event"
)

func TestFromPrompt(t *testing.T) {
	req := acp.PromptRequest{
		SessionID: "sess-1",
		Prompt: []acp.ContentBlock{
			acp.TextBlock("look at "),
			acp.ResourceLink("file:///a.go", "a.go"),
			acp.TextBlock(" and "),
			acp.ResourceLink("file:///b.go", "b.go"),
		},
	}
	got := acp.FromPrompt(req)

	if got.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want %q", got.SessionID, "sess-1")
	}
	if want := "look at  and "; got.Text != want {
		t.Errorf("Text = %q, want %q", got.Text, want)
	}
	wantAttachments := []string{"file:///a.go", "file:///b.go"}
	if len(got.Attachments) != len(wantAttachments) {
		t.Fatalf("Attachments = %v, want %v", got.Attachments, wantAttachments)
	}
	for i, uri := range wantAttachments {
		if got.Attachments[i] != uri {
			t.Errorf("Attachments[%d] = %q, want %q", i, got.Attachments[i], uri)
		}
	}
}

func TestFromPromptNoAttachments(t *testing.T) {
	req := acp.PromptRequest{
		SessionID: "sess-1",
		Prompt:    []acp.ContentBlock{acp.TextBlock("hi")},
	}
	got := acp.FromPrompt(req)
	if got.Attachments != nil {
		t.Errorf("Attachments = %v, want nil", got.Attachments)
	}
}

func TestFromCancel(t *testing.T) {
	got := acp.FromCancel(acp.CancelNotification{SessionID: "sess-1"})
	want := event.TurnInterrupt{SessionID: "sess-1"}
	if got != want {
		t.Errorf("FromCancel() = %#v, want %#v", got, want)
	}
}

func TestFromNewSession(t *testing.T) {
	got := acp.FromNewSession(acp.NewSessionRequest{Cwd: "/work"})
	if got.Cwd != "/work" {
		t.Errorf("Cwd = %q, want %q", got.Cwd, "/work")
	}
	if got.Agent != "" {
		t.Errorf("Agent = %q, want empty (daemon fills it in)", got.Agent)
	}
}

// TestFromNewSessionIgnoresModel documents that a requested Model is not
// projected onto event.SessionNew, which has no model field yet: a consuming
// application that wants to honor it reads req.Model off the decoded request
// directly, before or after calling FromNewSession.
func TestFromNewSessionIgnoresModel(t *testing.T) {
	req := acp.NewSessionRequest{Cwd: "/work", Model: "claude-sonnet-5"}
	got := acp.FromNewSession(req)
	want := event.SessionNew{Cwd: "/work"}
	if got != want {
		t.Errorf("FromNewSession() = %#v, want %#v", got, want)
	}
	if req.Model != "claude-sonnet-5" {
		t.Errorf("req.Model = %q, want %q (unchanged by projection)", req.Model, "claude-sonnet-5")
	}
}

func TestFromLoadSession(t *testing.T) {
	got := acp.FromLoadSession(acp.LoadSessionRequest{SessionID: "sess-1", Cwd: "/work"})
	want := event.SessionResume{SessionID: "sess-1"}
	if got != want {
		t.Errorf("FromLoadSession() = %#v, want %#v", got, want)
	}
}

func TestToPermissionReply(t *testing.T) {
	tests := []struct {
		name    string
		outcome acp.RequestPermissionResponse
		chosen  acp.PermissionOption
		want    event.PermissionReply
	}{
		{
			name:    "cancelled",
			outcome: acp.RequestPermissionResponse{Outcome: acp.PermissionOutcomeCancelled{}},
			chosen:  acp.PermissionOption{Kind: acp.PermissionAllowAlways}, // ignored
			want:    event.PermissionReply{ID: "req-1", Verdict: event.VerdictDeny, Remember: false},
		},
		{
			name:    "selected allow_once",
			outcome: acp.RequestPermissionResponse{Outcome: acp.PermissionOutcomeSelected{OptionID: "opt-1"}},
			chosen:  acp.PermissionOption{OptionID: "opt-1", Kind: acp.PermissionAllowOnce},
			want:    event.PermissionReply{ID: "req-1", Verdict: event.VerdictAllow, Remember: false},
		},
		{
			name:    "selected allow_always",
			outcome: acp.RequestPermissionResponse{Outcome: acp.PermissionOutcomeSelected{OptionID: "opt-1"}},
			chosen:  acp.PermissionOption{OptionID: "opt-1", Kind: acp.PermissionAllowAlways},
			want:    event.PermissionReply{ID: "req-1", Verdict: event.VerdictAllow, Remember: true},
		},
		{
			name:    "selected reject_once",
			outcome: acp.RequestPermissionResponse{Outcome: acp.PermissionOutcomeSelected{OptionID: "opt-2"}},
			chosen:  acp.PermissionOption{OptionID: "opt-2", Kind: acp.PermissionRejectOnce},
			want:    event.PermissionReply{ID: "req-1", Verdict: event.VerdictDeny, Remember: false},
		},
		{
			name:    "selected reject_always",
			outcome: acp.RequestPermissionResponse{Outcome: acp.PermissionOutcomeSelected{OptionID: "opt-2"}},
			chosen:  acp.PermissionOption{OptionID: "opt-2", Kind: acp.PermissionRejectAlways},
			want:    event.PermissionReply{ID: "req-1", Verdict: event.VerdictDeny, Remember: true},
		},
		{
			name:    "selected unknown kind fails safe to deny",
			outcome: acp.RequestPermissionResponse{Outcome: acp.PermissionOutcomeSelected{OptionID: "opt-3"}},
			chosen:  acp.PermissionOption{OptionID: "opt-3", Kind: acp.PermissionOptionKind("bogus")},
			want:    event.PermissionReply{ID: "req-1", Verdict: event.VerdictDeny, Remember: false},
		},
		{
			name: "amended allow_once carries replacement input",
			outcome: acp.RequestPermissionResponse{Outcome: acp.PermissionOutcomeAmended{
				OptionID: "opt-1",
				RawInput: []byte(`{"command":"ls -la"}`),
			}},
			chosen: acp.PermissionOption{OptionID: "opt-1", Kind: acp.PermissionAllowOnce},
			want: event.PermissionReply{
				ID: "req-1", Verdict: event.VerdictAllow, Remember: false,
				Input: []byte(`{"command":"ls -la"}`),
			},
		},
		{
			name: "amended allow_always remembers and carries input",
			outcome: acp.RequestPermissionResponse{Outcome: acp.PermissionOutcomeAmended{
				OptionID: "opt-1",
				RawInput: []byte(`{"command":"ls -la"}`),
			}},
			chosen: acp.PermissionOption{OptionID: "opt-1", Kind: acp.PermissionAllowAlways},
			want: event.PermissionReply{
				ID: "req-1", Verdict: event.VerdictAllow, Remember: true,
				Input: []byte(`{"command":"ls -la"}`),
			},
		},
		{
			name: "amended with reject kind fails safe to deny and drops input",
			outcome: acp.RequestPermissionResponse{Outcome: acp.PermissionOutcomeAmended{
				OptionID: "opt-2",
				RawInput: []byte(`{"command":"rm -rf /"}`),
			}},
			chosen: acp.PermissionOption{OptionID: "opt-2", Kind: acp.PermissionRejectOnce},
			want:   event.PermissionReply{ID: "req-1", Verdict: event.VerdictDeny, Remember: false},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := acp.ToPermissionReply("req-1", tc.outcome, tc.chosen)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ToPermissionReply() = %#v, want %#v", got, tc.want)
			}
		})
	}
}
