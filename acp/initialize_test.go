package acp_test

import (
	"encoding/json"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/acp"
)

func TestNewInitializeResponse(t *testing.T) {
	resp := acp.NewInitializeResponse()
	if resp.ProtocolVersion != acp.ProtocolVersion {
		t.Errorf("ProtocolVersion = %d, want %d", resp.ProtocolVersion, acp.ProtocolVersion)
	}

	got, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := `{"protocolVersion":1,"agentCapabilities":{},"authMethods":[]}`
	assertJSONEqual(t, got, want)
}

func TestInitializeRequestMarshal(t *testing.T) {
	req := acp.InitializeRequest{ProtocolVersion: 1}
	got, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	assertJSONEqual(t, got, `{"protocolVersion":1}`)
}

func TestAgentCapabilitiesMarshal(t *testing.T) {
	tests := []struct {
		name string
		caps acp.AgentCapabilities
		want string
	}{
		{
			name: "empty caps",
			caps: acp.AgentCapabilities{},
			want: `{}`,
		},
		{
			name: "loadSession and session/list advertised",
			caps: acp.AgentCapabilities{
				LoadSession:         true,
				SessionCapabilities: acp.SessionCapabilities{List: &acp.SessionListCapabilities{}},
			},
			want: `{"loadSession":true,"sessionCapabilities":{"list":{}}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.caps)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			// The empty-caps case asserts the exact byte shape too, since
			// AgentCapabilities{} must stay backward-compatible with the
			// pre-existing empty struct's "{}" wire form.
			if string(got) != tc.want {
				t.Errorf("Marshal() = %s, want %s", got, tc.want)
			}
		})
	}
}
