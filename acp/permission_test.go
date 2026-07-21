package acp_test

import (
	"encoding/json"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/acp"
)

func TestToRequestPermission(t *testing.T) {
	options := []acp.PermissionOption{
		{OptionID: "opt-1", Name: "Allow", Kind: acp.PermissionAllowOnce},
		{OptionID: "opt-2", Name: "Deny", Kind: acp.PermissionRejectOnce},
	}
	req := acp.ToRequestPermission("sess-1", "tc-1", "Run tests", options)

	got, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	// The bare toolCall object must NOT carry a sessionUpdate discriminator —
	// that tag belongs only to the tool_call_update session/update variant.
	want := `{"sessionId":"sess-1",` +
		`"toolCall":{"toolCallId":"tc-1","title":"Run tests"},` +
		`"options":[` +
		`{"optionId":"opt-1","name":"Allow","kind":"allow_once"},` +
		`{"optionId":"opt-2","name":"Deny","kind":"reject_once"}]}`
	assertJSONEqual(t, got, want)

	// Guard the invariant directly: no sessionUpdate key anywhere in toolCall.
	var decoded struct {
		ToolCall map[string]json.RawMessage `json:"toolCall"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if _, present := decoded.ToolCall["sessionUpdate"]; present {
		t.Errorf("toolCall unexpectedly carries a sessionUpdate key: %s", got)
	}
}

func TestRequestPermissionResponseRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		outcome acp.PermissionOutcome
		want    string
	}{
		{
			name:    "selected",
			outcome: acp.PermissionOutcomeSelected{OptionID: "opt-1"},
			want:    `{"outcome":{"outcome":"selected","optionId":"opt-1"}}`,
		},
		{
			name:    "cancelled",
			outcome: acp.PermissionOutcomeCancelled{},
			want:    `{"outcome":{"outcome":"cancelled"}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := acp.RequestPermissionResponse{Outcome: tc.outcome}
			data, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			assertJSONEqual(t, data, tc.want)

			var got acp.RequestPermissionResponse
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			if got.Outcome != tc.outcome {
				t.Errorf("round trip outcome = %#v, want %#v", got.Outcome, tc.outcome)
			}
		})
	}
}

func TestRequestPermissionResponseAmendedRoundTrip(t *testing.T) {
	// Amended carries a json.RawMessage, so it is not == comparable; assert on
	// the wire shape and on the decoded fields instead.
	outcome := acp.PermissionOutcomeAmended{
		OptionID: "opt-1",
		RawInput: []byte(`{"command":"ls -la"}`),
	}
	resp := acp.RequestPermissionResponse{Outcome: outcome}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := `{"outcome":{"outcome":"amended","optionId":"opt-1","rawInput":{"command":"ls -la"}}}`
	assertJSONEqual(t, data, want)

	var got acp.RequestPermissionResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	amended, ok := got.Outcome.(acp.PermissionOutcomeAmended)
	if !ok {
		t.Fatalf("round trip outcome type = %T, want acp.PermissionOutcomeAmended", got.Outcome)
	}
	if amended.OptionID != outcome.OptionID {
		t.Errorf("OptionID = %q, want %q", amended.OptionID, outcome.OptionID)
	}
	if string(amended.RawInput) != string(outcome.RawInput) {
		t.Errorf("RawInput = %s, want %s", amended.RawInput, outcome.RawInput)
	}
	if amended.Outcome() != "amended" {
		t.Errorf("Outcome() = %q, want %q", amended.Outcome(), "amended")
	}
}

func TestPermissionOutcomeAmendedNilInput(t *testing.T) {
	// A nil RawInput must omit the key (not emit "rawInput":null) and decode
	// back to nil, so a no-input amend is never mistaken for a real replacement.
	resp := acp.RequestPermissionResponse{Outcome: acp.PermissionOutcomeAmended{OptionID: "opt-1"}}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	assertJSONEqual(t, data, `{"outcome":{"outcome":"amended","optionId":"opt-1"}}`)

	// An explicit "rawInput":null from a non-conforming client also normalizes
	// to nil rather than the 4-byte "null".
	for _, in := range []string{
		`{"outcome":"amended","optionId":"opt-1"}`,
		`{"outcome":"amended","optionId":"opt-1","rawInput":null}`,
	} {
		got, err := acp.UnmarshalPermissionOutcome([]byte(in))
		if err != nil {
			t.Fatalf("UnmarshalPermissionOutcome(%s) error = %v", in, err)
		}
		amended, ok := got.(acp.PermissionOutcomeAmended)
		if !ok {
			t.Fatalf("outcome type = %T, want acp.PermissionOutcomeAmended", got)
		}
		if amended.RawInput != nil {
			t.Errorf("RawInput = %s (len %d), want nil", amended.RawInput, len(amended.RawInput))
		}
	}
}

func TestUnmarshalPermissionOutcomeUnknown(t *testing.T) {
	_, err := acp.UnmarshalPermissionOutcome([]byte(`{"outcome":"bogus"}`))
	if err == nil {
		t.Fatal("UnmarshalPermissionOutcome() error = nil, want error for unmodeled outcome")
	}
}
