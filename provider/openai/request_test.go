package openai

import (
	"encoding/json"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// decodeReq unmarshals a built request body into a generic map for assertions.
func decodeReq(t *testing.T, model string, req provider.Request, reasoning bool) map[string]any {
	t.Helper()
	body, err := buildRequest(model, req, reasoning)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	return m
}

func TestBuildRequestBasics(t *testing.T) {
	req := provider.Request{
		System:   "be helpful",
		Messages: []provider.Message{provider.UserText("hi")},
		Params:   provider.Params{MaxTokens: 256},
	}
	m := decodeReq(t, "gpt-5", req, true)

	if m["model"] != "gpt-5" {
		t.Errorf("model = %v", m["model"])
	}
	if m["instructions"] != "be helpful" {
		t.Errorf("instructions = %v", m["instructions"])
	}
	if m["stream"] != true {
		t.Errorf("stream = %v, want true", m["stream"])
	}
	if m["store"] != false {
		t.Errorf("store = %v, want false", m["store"])
	}
	if m["max_output_tokens"].(float64) != 256 {
		t.Errorf("max_output_tokens = %v", m["max_output_tokens"])
	}

	input := m["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("input len = %d, want 1", len(input))
	}
	msg := input[0].(map[string]any)
	if msg["type"] != "message" || msg["role"] != "user" {
		t.Errorf("input[0] = %v", msg)
	}
	part := msg["content"].([]any)[0].(map[string]any)
	if part["type"] != "input_text" || part["text"] != "hi" {
		t.Errorf("content part = %v", part)
	}
}

func TestBuildRequestAssistantUsesOutputText(t *testing.T) {
	req := provider.Request{Messages: []provider.Message{provider.AssistantText("done")}}
	m := decodeReq(t, "gpt-5", req, true)
	part := m["input"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if part["type"] != "output_text" {
		t.Errorf("assistant text part type = %v, want output_text", part["type"])
	}
}

// TestBuildRequestToolRoundTrip asserts a tool_use block becomes a standalone
// function_call item and a tool_result becomes a function_call_output item, in
// order, splitting the surrounding message text.
func TestBuildRequestToolRoundTrip(t *testing.T) {
	req := provider.Request{Messages: []provider.Message{
		{Role: provider.RoleAssistant, Content: []provider.ContentBlock{
			provider.TextBlock("let me check"),
			provider.ToolUseBlock("call_1", "get_weather", json.RawMessage(`{"city":"paris"}`)),
		}},
		{Role: provider.RoleUser, Content: []provider.ContentBlock{
			provider.ToolResultBlock("call_1", "sunny", false),
		}},
	}}
	m := decodeReq(t, "gpt-5", req, true)
	input := m["input"].([]any)
	if len(input) != 3 {
		t.Fatalf("input len = %d, want 3 (message, function_call, function_call_output)", len(input))
	}

	fc := input[1].(map[string]any)
	if fc["type"] != "function_call" || fc["call_id"] != "call_1" || fc["name"] != "get_weather" {
		t.Errorf("function_call item = %v", fc)
	}
	if fc["arguments"] != `{"city":"paris"}` {
		t.Errorf("arguments = %v", fc["arguments"])
	}

	fo := input[2].(map[string]any)
	if fo["type"] != "function_call_output" || fo["call_id"] != "call_1" || fo["output"] != "sunny" {
		t.Errorf("function_call_output item = %v", fo)
	}
}

func TestBuildRequestToolsFlatShape(t *testing.T) {
	req := provider.Request{Tools: []provider.ToolSpec{
		{Name: "bash", Description: "run a command", InputSchema: json.RawMessage(`{"type":"object","properties":{"cmd":{"type":"string"}}}`)},
		{Name: "noargs"},
	}}
	m := decodeReq(t, "gpt-5", req, true)
	tools := m["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools len = %d", len(tools))
	}
	t0 := tools[0].(map[string]any)
	if t0["type"] != "function" || t0["name"] != "bash" || t0["description"] != "run a command" {
		t.Errorf("tool[0] = %v", t0)
	}
	if _, ok := t0["parameters"].(map[string]any); !ok {
		t.Errorf("tool[0] parameters missing/not object: %v", t0["parameters"])
	}
	// A tool without a schema still emits a valid empty object schema.
	t1 := tools[1].(map[string]any)
	params := t1["parameters"].(map[string]any)
	if params["type"] != "object" {
		t.Errorf("tool[1] default parameters = %v", params)
	}
}

// TestReasoningGating asserts reasoning models get a reasoning config and drop
// temperature, while non-reasoning models get temperature and no reasoning.
func TestReasoningGating(t *testing.T) {
	temp := 0.7
	req := provider.Request{
		Params: provider.Params{
			Temperature: &temp,
			Thinking:    provider.Thinking{Enabled: true, Effort: "high", BudgetTokens: 4096},
		},
	}

	reasoning := decodeReq(t, "gpt-5", req, true)
	rc, ok := reasoning["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("reasoning config missing: %v", reasoning["reasoning"])
	}
	if rc["effort"] != "high" || rc["summary"] != "auto" {
		t.Errorf("reasoning config = %v", rc)
	}
	if _, present := reasoning["temperature"]; present {
		t.Error("reasoning model should not send temperature")
	}

	nonReasoning := decodeReq(t, "some-chat-model", req, false)
	if _, present := nonReasoning["reasoning"]; present {
		t.Error("non-reasoning model should not send reasoning config")
	}
	if nonReasoning["temperature"].(float64) != 0.7 {
		t.Errorf("temperature = %v, want 0.7", nonReasoning["temperature"])
	}
}

func TestReasoningDefaultEffort(t *testing.T) {
	req := provider.Request{Params: provider.Params{Thinking: provider.Thinking{Enabled: true}}}
	m := decodeReq(t, "gpt-5", req, true)
	rc := m["reasoning"].(map[string]any)
	if rc["effort"] != "medium" {
		t.Errorf("default effort = %v, want medium", rc["effort"])
	}
}

// TestBuildRequestDropsReasoningAndImageBlocks asserts non-replayable blocks are
// omitted from input rather than emitted as bad items.
func TestBuildRequestDropsReasoningAndImageBlocks(t *testing.T) {
	req := provider.Request{Messages: []provider.Message{
		{Role: provider.RoleAssistant, Content: []provider.ContentBlock{
			provider.ReasoningBlock("secret thoughts"),
			provider.TextBlock("visible"),
			{Type: provider.BlockImage, ImageRef: "img-1"},
		}},
	}}
	m := decodeReq(t, "gpt-5", req, true)
	input := m["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("input len = %d, want 1 message", len(input))
	}
	parts := input[0].(map[string]any)["content"].([]any)
	if len(parts) != 1 || parts[0].(map[string]any)["text"] != "visible" {
		t.Errorf("expected only the visible text part, got %v", parts)
	}
}

// TestReasoningIncludesEncryptedContent asserts the request opts into
// reasoning.encrypted_content whenever reasoning is enabled, and omits it
// otherwise.
func TestReasoningIncludesEncryptedContent(t *testing.T) {
	req := provider.Request{Params: provider.Params{Thinking: provider.Thinking{Enabled: true}}}
	m := decodeReq(t, "gpt-5", req, true)
	include, ok := m["include"].([]any)
	if !ok || len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Errorf("include = %v, want [reasoning.encrypted_content]", m["include"])
	}

	noReasoning := decodeReq(t, "gpt-5", provider.Request{}, true)
	if _, present := noReasoning["include"]; present {
		t.Errorf("include should be omitted when reasoning is disabled, got %v", noReasoning["include"])
	}
}

// TestBuildRequestReplaysReasoningBlock asserts a reasoning block carrying both
// its item id and encrypted content is replayed as a reasoning input item in
// position.
func TestBuildRequestReplaysReasoningBlock(t *testing.T) {
	reasoning := provider.ReasoningBlock("thinking...")
	reasoning.Meta = map[string]string{metaItemID: "rs_1", metaEncrypted: "enc-blob"}
	req := provider.Request{Messages: []provider.Message{
		{Role: provider.RoleAssistant, Content: []provider.ContentBlock{
			reasoning,
			provider.TextBlock("the answer"),
		}},
	}}
	m := decodeReq(t, "gpt-5", req, true)
	input := m["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("input len = %d, want 2 (reasoning, message)", len(input))
	}
	r := input[0].(map[string]any)
	if r["type"] != "reasoning" || r["id"] != "rs_1" || r["encrypted_content"] != "enc-blob" {
		t.Errorf("reasoning item = %v", r)
	}
	summary, ok := r["summary"].([]any)
	if !ok || len(summary) != 0 {
		t.Errorf("reasoning summary = %v, want empty array", r["summary"])
	}

	msg := input[1].(map[string]any)
	if msg["type"] != "message" {
		t.Errorf("input[1] = %v, want the trailing message", msg)
	}
}

// TestBuildRequestDropsReasoningWithoutEncryptedContent asserts a reasoning
// block missing encrypted_content (even with an item id) is dropped, not sent
// malformed.
func TestBuildRequestDropsReasoningWithoutEncryptedContent(t *testing.T) {
	reasoning := provider.ReasoningBlock("thinking...")
	reasoning.Meta = map[string]string{metaItemID: "rs_1"}
	req := provider.Request{Messages: []provider.Message{
		{Role: provider.RoleAssistant, Content: []provider.ContentBlock{
			reasoning,
			provider.TextBlock("the answer"),
		}},
	}}
	m := decodeReq(t, "gpt-5", req, true)
	input := m["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("input len = %d, want 1 (reasoning block dropped)", len(input))
	}
	if input[0].(map[string]any)["type"] != "message" {
		t.Errorf("input[0] = %v, want the message", input[0])
	}
}

func TestWireRole(t *testing.T) {
	cases := map[provider.Role]string{
		provider.RoleUser:      "user",
		provider.RoleAssistant: "assistant",
		provider.RoleSystem:    "developer",
	}
	for in, want := range cases {
		if got := wireRole(in); got != want {
			t.Errorf("wireRole(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestReasoningEffortEnables is the issue #88 regression at the OpenAI wire: a
// named effort with Enabled left false — exactly the Params a Runner produces
// for an embedder that never constructs provider.Params — must still emit the
// reasoning config carrying that level, and opt into encrypted reasoning
// content. Before the fix the config was omitted entirely, so Runner.SetEffort
// could not reach the API.
func TestReasoningEffortEnables(t *testing.T) {
	for _, effort := range []string{provider.EffortLow, provider.EffortMedium, provider.EffortHigh} {
		t.Run(effort, func(t *testing.T) {
			req := provider.Request{Params: provider.Params{
				Thinking: provider.Thinking{Effort: effort},
			}}
			m := decodeReq(t, "gpt-5", req, true)

			rc, ok := m["reasoning"].(map[string]any)
			if !ok {
				t.Fatalf("reasoning config missing for effort %q — it never reached the wire", effort)
			}
			if rc["effort"] != effort {
				t.Errorf("reasoning effort = %v, want %q", rc["effort"], effort)
			}
			include, ok := m["include"].([]any)
			if !ok || len(include) != 1 || include[0] != "reasoning.encrypted_content" {
				t.Errorf("include = %v, want [reasoning.encrypted_content]", m["include"])
			}
		})
	}
}

// TestReasoningEffortOffWithoutRequest is the must-fire twin of the test above:
// with neither Enabled nor an effort no reasoning config may appear, so a change
// that enabled reasoning unconditionally cannot pass both tests. It also pins
// that a non-reasoning model still refuses an effort-only request — the model
// capability gate outranks the caller's level.
func TestReasoningEffortOffWithoutRequest(t *testing.T) {
	unrequested := decodeReq(t, "gpt-5", provider.Request{}, true)
	if _, present := unrequested["reasoning"]; present {
		t.Errorf("reasoning = %v, want omitted with reasoning unrequested", unrequested["reasoning"])
	}

	effortReq := provider.Request{Params: provider.Params{
		Thinking: provider.Thinking{Effort: provider.EffortHigh},
	}}
	nonReasoning := decodeReq(t, "some-chat-model", effortReq, false)
	if _, present := nonReasoning["reasoning"]; present {
		t.Errorf("reasoning = %v, want omitted for a non-reasoning model", nonReasoning["reasoning"])
	}
}
