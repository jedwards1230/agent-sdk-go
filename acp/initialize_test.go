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
