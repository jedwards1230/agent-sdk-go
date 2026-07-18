package acp_test

import (
	"encoding/json"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/acp"
)

func TestSetConfigOptionRequestMarshal(t *testing.T) {
	tests := []struct {
		name string
		req  acp.SetConfigOptionRequest
		want string
	}{
		{
			name: "select value id has no type field",
			req: acp.SetConfigOptionRequest{
				SessionID: "sess_1",
				ConfigID:  "model",
				Value:     acp.SelectValue{Value: "model-1"},
			},
			want: `{"sessionId":"sess_1","configId":"model","value":"model-1"}`,
		},
		{
			name: "boolean carries type:boolean",
			req: acp.SetConfigOptionRequest{
				SessionID: "sess_1",
				ConfigID:  "brave_mode",
				Value:     acp.BooleanValue{Value: true},
			},
			want: `{"sessionId":"sess_1","configId":"brave_mode","type":"boolean","value":true}`,
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

func TestSetConfigOptionRequestMarshalNilValue(t *testing.T) {
	if _, err := json.Marshal(acp.SetConfigOptionRequest{SessionID: "s", ConfigID: "c"}); err == nil {
		t.Fatal("Marshal() with nil value: want error, got nil")
	}
}

func TestSetConfigOptionRequestUnmarshal(t *testing.T) {
	tests := []struct {
		name      string
		data      string
		wantValue acp.ConfigOptionValue
	}{
		{
			name:      "no type decodes to select value id",
			data:      `{"sessionId":"sess_1","configId":"model","value":"model-1"}`,
			wantValue: acp.SelectValue{Value: "model-1"},
		},
		{
			name:      "type:boolean decodes to boolean value",
			data:      `{"sessionId":"sess_1","configId":"brave_mode","type":"boolean","value":true}`,
			wantValue: acp.BooleanValue{Value: true},
		},
		{
			name:      "unknown type with string payload falls back to select value id",
			data:      `{"sessionId":"sess_1","configId":"x","type":"future","value":"opt-a"}`,
			wantValue: acp.SelectValue{Value: "opt-a"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var req acp.SetConfigOptionRequest
			if err := json.Unmarshal([]byte(tc.data), &req); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			if req.SessionID == "" || req.ConfigID == "" {
				t.Errorf("req = %+v, want SessionID and ConfigID set", req)
			}
			if req.Value != tc.wantValue {
				t.Errorf("Value = %#v, want %#v", req.Value, tc.wantValue)
			}
		})
	}
}

func TestSetConfigOptionRequestUnmarshalMissingRequired(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{
			name: "missing value errors",
			data: `{"sessionId":"sess_1","configId":"model"}`,
		},
		{
			name: "null value errors",
			data: `{"sessionId":"sess_1","configId":"model","value":null}`,
		},
		{
			name: "null boolean value errors",
			data: `{"sessionId":"sess_1","configId":"brave_mode","type":"boolean","value":null}`,
		},
		{
			name: "empty configId errors",
			data: `{"sessionId":"sess_1","configId":"","value":"model-1"}`,
		},
		{
			name: "empty sessionId errors",
			data: `{"sessionId":"","configId":"model","value":"model-1"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var req acp.SetConfigOptionRequest
			if err := json.Unmarshal([]byte(tc.data), &req); err == nil {
				t.Fatalf("Unmarshal(%s): want error, got nil (req = %+v)", tc.data, req)
			}
		})
	}
}

func TestSetConfigOptionRequestUnmarshalMalformedValue(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{
			name: "number value in select branch errors",
			data: `{"sessionId":"s","configId":"model","value":123}`,
		},
		{
			name: "object value in select branch errors",
			data: `{"sessionId":"s","configId":"model","value":{"nested":true}}`,
		},
		{
			name: "non-bool value in boolean branch errors",
			data: `{"sessionId":"s","configId":"brave_mode","type":"boolean","value":"notabool"}`,
		},
		{
			name: "number value in boolean branch errors",
			data: `{"sessionId":"s","configId":"brave_mode","type":"boolean","value":123}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var req acp.SetConfigOptionRequest
			if err := json.Unmarshal([]byte(tc.data), &req); err == nil {
				t.Fatalf("Unmarshal(%s): want decode error, got nil (req = %+v)", tc.data, req)
			}
		})
	}
}

func TestSetConfigOptionRequestRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		req  acp.SetConfigOptionRequest
	}{
		{
			name: "select",
			req:  acp.SetConfigOptionRequest{SessionID: "s", ConfigID: "c", Value: acp.SelectValue{Value: "v"}},
		},
		{
			name: "boolean false",
			req:  acp.SetConfigOptionRequest{SessionID: "s", ConfigID: "c", Value: acp.BooleanValue{Value: false}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.req)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			var got acp.SetConfigOptionRequest
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			if got != tc.req {
				t.Errorf("round-trip = %#v, want %#v", got, tc.req)
			}
		})
	}
}

func TestConfigOptionMarshal(t *testing.T) {
	tests := []struct {
		name string
		opt  acp.ConfigOption
		want string
	}{
		{
			name: "boolean option",
			opt: acp.ConfigOption{
				ID:          "brave_mode",
				Name:        "Brave Mode",
				Description: "Skip confirmation prompts",
				Kind:        acp.BooleanKind{CurrentValue: false},
			},
			want: `{"id":"brave_mode","name":"Brave Mode","description":"Skip confirmation prompts","type":"boolean","currentValue":false}`,
		},
		{
			name: "select option with category and choices",
			opt: acp.ConfigOption{
				ID:       "model",
				Name:     "Model",
				Category: acp.ConfigCategoryModel,
				Kind: acp.SelectKind{
					CurrentValue: "model-1",
					Options: []acp.SelectOption{
						{Value: "model-1", Name: "Model 1"},
						{Value: "model-2", Name: "Model 2"},
					},
				},
			},
			want: `{"id":"model","name":"Model","category":"model","type":"select","currentValue":"model-1",` +
				`"options":[{"value":"model-1","name":"Model 1"},{"value":"model-2","name":"Model 2"}]}`,
		},
		{
			name: "select option with no choices marshals options as empty array",
			opt: acp.ConfigOption{
				ID:   "empty",
				Name: "Empty",
				Kind: acp.SelectKind{CurrentValue: ""},
			},
			want: `{"id":"empty","name":"Empty","type":"select","currentValue":"","options":[]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.opt)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			assertJSONEqual(t, got, tc.want)
		})
	}
}

func TestConfigOptionMarshalNilKind(t *testing.T) {
	if _, err := json.Marshal(acp.ConfigOption{ID: "x", Name: "X"}); err == nil {
		t.Fatal("Marshal() with nil kind: want error, got nil")
	}
}

func TestConfigOptionRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		opt  acp.ConfigOption
	}{
		{
			name: "boolean",
			opt:  acp.ConfigOption{ID: "b", Name: "B", Kind: acp.BooleanKind{CurrentValue: true}},
		},
		{
			name: "select",
			opt: acp.ConfigOption{
				ID:       "s",
				Name:     "S",
				Category: acp.ConfigCategoryMode,
				Kind: acp.SelectKind{
					CurrentValue: "a",
					Options:      []acp.SelectOption{{Value: "a", Name: "A", Description: "the a"}},
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.opt)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			var got acp.ConfigOption
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			if got.ID != tc.opt.ID || got.Name != tc.opt.Name || got.Category != tc.opt.Category {
				t.Errorf("scalar fields = %+v, want %+v", got, tc.opt)
			}
			// Kind carries a slice for SelectKind, so compare the re-marshaled
			// shape rather than with == (which would panic on the slice).
			reGot, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("re-marshal error = %v", err)
			}
			reWant, err := json.Marshal(tc.opt)
			if err != nil {
				t.Fatalf("marshal want error = %v", err)
			}
			if string(reGot) != string(reWant) {
				t.Errorf("round-trip = %s, want %s", reGot, reWant)
			}
		})
	}
}

func TestSetConfigOptionResponseMarshal(t *testing.T) {
	tests := []struct {
		name string
		resp acp.SetConfigOptionResponse
		want string
	}{
		{
			name: "empty options marshal as empty array",
			resp: acp.SetConfigOptionResponse{ConfigOptions: []acp.ConfigOption{}},
			want: `{"configOptions":[]}`,
		},
		{
			name: "one boolean option",
			resp: acp.SetConfigOptionResponse{ConfigOptions: []acp.ConfigOption{
				{ID: "brave_mode", Name: "Brave Mode", Kind: acp.BooleanKind{CurrentValue: true}},
			}},
			want: `{"configOptions":[{"id":"brave_mode","name":"Brave Mode","type":"boolean","currentValue":true}]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.resp)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			assertJSONEqual(t, got, tc.want)
			if tc.name == "empty options marshal as empty array" && string(got) != tc.want {
				t.Errorf("Marshal() = %s, want exact %s", got, tc.want)
			}
		})
	}
}

func TestSetConfigOptionResponseUnmarshalSkipsInvalid(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantIDs []string
	}{
		{
			name: "unknown option kind is dropped, known kept",
			data: `{"configOptions":[` +
				`{"id":"model","name":"Model","type":"select","currentValue":"m1","options":[]},` +
				`{"id":"future","name":"Future","type":"quantum","currentValue":"?"}` +
				`]}`,
			wantIDs: []string{"model"},
		},
		{
			name: "all known kept in order",
			data: `{"configOptions":[` +
				`{"id":"brave_mode","name":"Brave Mode","type":"boolean","currentValue":true},` +
				`{"id":"model","name":"Model","type":"select","currentValue":"m1","options":[]}` +
				`]}`,
			wantIDs: []string{"brave_mode", "model"},
		},
		{
			name: "malformed currentValue entry is dropped, known kept",
			data: `{"configOptions":[` +
				`{"id":"brave_mode","name":"Brave Mode","type":"boolean","currentValue":"notabool"},` +
				`{"id":"model","name":"Model","type":"select","currentValue":"m1","options":[]}` +
				`]}`,
			wantIDs: []string{"model"},
		},
		{
			name: "non-object entry is dropped, known kept",
			data: `{"configOptions":[` +
				`123,` +
				`{"id":"model","name":"Model","type":"select","currentValue":"m1","options":[]}` +
				`]}`,
			wantIDs: []string{"model"},
		},
		{
			name:    "empty options decodes to empty slice",
			data:    `{"configOptions":[]}`,
			wantIDs: []string{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var resp acp.SetConfigOptionResponse
			if err := json.Unmarshal([]byte(tc.data), &resp); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			gotIDs := make([]string, 0, len(resp.ConfigOptions))
			for _, opt := range resp.ConfigOptions {
				gotIDs = append(gotIDs, opt.ID)
			}
			if len(gotIDs) != len(tc.wantIDs) {
				t.Fatalf("ids = %v, want %v", gotIDs, tc.wantIDs)
			}
			for i, id := range tc.wantIDs {
				if gotIDs[i] != id {
					t.Errorf("ids = %v, want %v", gotIDs, tc.wantIDs)
					break
				}
			}
		})
	}
}

func TestSetConfigOptionResponseUnmarshalMalformed(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{
			name: "configOptions not an array errors",
			data: `{"configOptions":"notanarray"}`,
		},
		{
			name: "configOptions is an object errors",
			data: `{"configOptions":{"id":"x"}}`,
		},
		{
			name: "truncated json errors",
			data: `{"configOptions":[`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var resp acp.SetConfigOptionResponse
			if err := json.Unmarshal([]byte(tc.data), &resp); err == nil {
				t.Fatalf("Unmarshal(%s): want decode error, got nil (resp = %+v)", tc.data, resp)
			}
		})
	}
}

func TestDecodeSetConfigOption(t *testing.T) {
	req, err := acp.DecodeSetConfigOption(json.RawMessage(
		`{"sessionId":"s-1","configId":"model","value":"model-2"}`))
	if err != nil {
		t.Fatalf("DecodeSetConfigOption() error = %v", err)
	}
	if req.SessionID != "s-1" || req.ConfigID != "model" {
		t.Errorf("req = %+v, want SessionID=s-1 ConfigID=model", req)
	}
	if got, want := req.Value, (acp.SelectValue{Value: "model-2"}); got != want {
		t.Errorf("Value = %#v, want %#v", got, want)
	}

	if _, err := acp.DecodeSetConfigOption(json.RawMessage(`{`)); err == nil {
		t.Fatal("DecodeSetConfigOption() with malformed params: want error, got nil")
	}
}

func TestDecodeListSessions(t *testing.T) {
	req, err := acp.DecodeListSessions(json.RawMessage(`{"cwd":"/work","cursor":"page-2"}`))
	if err != nil {
		t.Fatalf("DecodeListSessions() error = %v", err)
	}
	if req.Cwd != "/work" || req.Cursor != "page-2" {
		t.Errorf("req = %+v, want Cwd=/work Cursor=page-2", req)
	}

	if _, err := acp.DecodeListSessions(json.RawMessage(`{`)); err == nil {
		t.Fatal("DecodeListSessions() with malformed params: want error, got nil")
	}
}
