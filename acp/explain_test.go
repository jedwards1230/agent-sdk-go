package acp_test

import (
	"encoding/json"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/acp"
)

func TestExplainPermissionRequestRoundTrip(t *testing.T) {
	req := acp.ExplainPermissionRequest{SessionID: "sess-1", ToolCallID: "tc-1"}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	assertJSONEqual(t, data, `{"sessionId":"sess-1","toolCallId":"tc-1"}`)

	var got acp.ExplainPermissionRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got != req {
		t.Errorf("round trip = %#v, want %#v", got, req)
	}
}

func TestExplainPermissionResponseRoundTrip(t *testing.T) {
	resp := acp.ExplainPermissionResponse{Rationale: acp.PermissionRationale{
		Reason: "bash cannot be sandboxed on this host",
		Policy: "unmatched",
		Source: "project",
		Trace:  []string{"rule: unmatched", "containable: false"},
	}}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := `{"rationale":{` +
		`"reason":"bash cannot be sandboxed on this host",` +
		`"policy":"unmatched",` +
		`"source":"project",` +
		`"trace":["rule: unmatched","containable: false"]}}`
	assertJSONEqual(t, data, want)

	var got acp.ExplainPermissionResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got.Rationale.Reason != resp.Rationale.Reason ||
		got.Rationale.Policy != resp.Rationale.Policy ||
		got.Rationale.Source != resp.Rationale.Source ||
		len(got.Rationale.Trace) != len(resp.Rationale.Trace) {
		t.Errorf("round trip = %#v, want %#v", got, resp)
	}
}

func TestExplainPermissionResponseOmitsEmptyProvenance(t *testing.T) {
	// Only Reason is required; the machine-readable provenance fields omit when
	// empty so a minimal rationale stays terse.
	resp := acp.ExplainPermissionResponse{Rationale: acp.PermissionRationale{Reason: "gated"}}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	assertJSONEqual(t, data, `{"rationale":{"reason":"gated"}}`)
}

func TestDecodeExplainPermission(t *testing.T) {
	req, err := acp.DecodeExplainPermission(json.RawMessage(`{"sessionId":"sess-1","toolCallId":"tc-1"}`))
	if err != nil {
		t.Fatalf("DecodeExplainPermission() error = %v", err)
	}
	if req.SessionID != "sess-1" || req.ToolCallID != "tc-1" {
		t.Errorf("DecodeExplainPermission() = %#v", req)
	}
}

func TestDecodeOpExplainPermissionHasNoOp(t *testing.T) {
	// session/explain_permission is a read-only rationale query, not a mutation:
	// DecodeOp must recognize it (no error) yet return no op.
	op, ok, err := acp.DecodeOp(acp.MethodSessionExplainPermission, json.RawMessage(`{"sessionId":"s","toolCallId":"t"}`))
	if err != nil {
		t.Errorf("DecodeOp() error = %v, want nil", err)
	}
	if ok {
		t.Errorf("DecodeOp() ok = true, want false (explain carries no op)")
	}
	if op != nil {
		t.Errorf("DecodeOp() op = %v, want nil", op)
	}
}
