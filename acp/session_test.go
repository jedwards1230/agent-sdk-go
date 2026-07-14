package acp_test

import (
	"encoding/json"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/acp"
)

func TestNewSessionRequestMarshal(t *testing.T) {
	tests := []struct {
		name string
		req  acp.NewSessionRequest
		want string
	}{
		{
			name: "no mcp servers",
			req:  acp.NewSessionRequest{Cwd: "/work"},
			want: `{"cwd":"/work"}`,
		},
		{
			name: "with mcp servers",
			req: acp.NewSessionRequest{
				Cwd:        "/work",
				McpServers: []json.RawMessage{[]byte(`{"name":"contextforge"}`)},
			},
			want: `{"cwd":"/work","mcpServers":[{"name":"contextforge"}]}`,
		},
		{
			name: "with model",
			req: acp.NewSessionRequest{
				Cwd:   "/work",
				Model: "claude-sonnet-5",
			},
			want: `{"cwd":"/work","model":"claude-sonnet-5"}`,
		},
		{
			name: "model omitted when empty",
			req:  acp.NewSessionRequest{Cwd: "/work", Model: ""},
			want: `{"cwd":"/work"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.req)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			assertJSONEqual(t, got, tc.want)
		})
	}
}

func TestNewSessionRequestUnmarshal(t *testing.T) {
	tests := []struct {
		name string
		json string
		want acp.NewSessionRequest
	}{
		{
			name: "model set round-trips",
			json: `{"cwd":"/work","model":"claude-sonnet-5"}`,
			want: acp.NewSessionRequest{Cwd: "/work", Model: "claude-sonnet-5"},
		},
		{
			name: "model omitted yields zero value",
			json: `{"cwd":"/work"}`,
			want: acp.NewSessionRequest{Cwd: "/work", Model: ""},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got acp.NewSessionRequest
			if err := json.Unmarshal([]byte(tc.json), &got); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			if got.Cwd != tc.want.Cwd || got.Model != tc.want.Model {
				t.Errorf("Unmarshal() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestNewSessionResponseMarshal(t *testing.T) {
	got, err := json.Marshal(acp.NewSessionResponse{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	assertJSONEqual(t, got, `{"sessionId":"sess-1"}`)
}

func TestLoadSessionRequestMarshal(t *testing.T) {
	got, err := json.Marshal(acp.LoadSessionRequest{SessionID: "sess-1", Cwd: "/work"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	assertJSONEqual(t, got, `{"sessionId":"sess-1","cwd":"/work"}`)
}

func TestCancelNotificationMarshal(t *testing.T) {
	got, err := json.Marshal(acp.CancelNotification{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	assertJSONEqual(t, got, `{"sessionId":"sess-1"}`)
}
