package anthropic

import (
	"encoding/json"
	"io"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// decodeBody reads a built body reader back into a messagesRequest.
func decodeBody(t *testing.T, r io.Reader) messagesRequest {
	t.Helper()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var req messagesRequest
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatalf("unmarshal body: %v\n%s", err, data)
	}
	return req
}

func TestBuildBodyAPIKeyNoIdentityBlock(t *testing.T) {
	p := New("claude-sonnet-5", provider.StaticCredentialSource{})
	r, err := p.buildBody(provider.Request{
		System:   "be terse",
		Messages: []provider.Message{provider.UserText("hi")},
	}, provider.CredAPIKey)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	body := decodeBody(t, r)

	if len(body.System) != 1 || body.System[0].Text != "be terse" {
		t.Fatalf("system = %+v, want single caller block", body.System)
	}
	if !body.Stream {
		t.Error("stream should be true")
	}
	if body.MaxTokens == 0 {
		t.Error("max_tokens must be set (API requires it)")
	}
}

func TestBuildBodyOAuthPrependsIdentity(t *testing.T) {
	p := New("claude-sonnet-5", provider.StaticCredentialSource{})
	r, err := p.buildBody(provider.Request{
		System:   "be terse",
		Messages: []provider.Message{provider.UserText("hi")},
	}, provider.CredOAuth)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	body := decodeBody(t, r)

	if len(body.System) != 2 {
		t.Fatalf("system = %+v, want identity + caller block", body.System)
	}
	if body.System[0].Text != systemIdentity {
		t.Errorf("system[0] = %q, want the Claude Code identity", body.System[0].Text)
	}
	if body.System[1].Text != "be terse" {
		t.Errorf("system[1] = %q, want caller prompt", body.System[1].Text)
	}
}

func TestBuildBodyOAuthIdentityWithoutCallerSystem(t *testing.T) {
	p := New("claude-sonnet-5", provider.StaticCredentialSource{})
	r, err := p.buildBody(provider.Request{
		Messages: []provider.Message{provider.UserText("hi")},
	}, provider.CredOAuth)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	body := decodeBody(t, r)

	if len(body.System) != 1 || body.System[0].Text != systemIdentity {
		t.Fatalf("system = %+v, want identity block only", body.System)
	}
}

func TestBuildBodyToolConversion(t *testing.T) {
	p := New("claude-sonnet-5", provider.StaticCredentialSource{})
	schema := json.RawMessage(`{"type":"object","properties":{"cmd":{"type":"string"}}}`)
	r, err := p.buildBody(provider.Request{
		Messages: []provider.Message{provider.UserText("run ls")},
		Tools: []provider.ToolSpec{
			{Name: "bash", Description: "run a command", InputSchema: schema},
			{Name: "noschema"},
		},
	}, provider.CredAPIKey)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	body := decodeBody(t, r)

	if len(body.Tools) != 2 {
		t.Fatalf("tools = %+v", body.Tools)
	}
	if body.Tools[0].Name != "bash" || string(body.Tools[0].InputSchema) != string(schema) {
		t.Errorf("tool[0] = %+v", body.Tools[0])
	}
	if string(body.Tools[1].InputSchema) != `{"type":"object"}` {
		t.Errorf("tool[1] schema = %s, want empty object fallback", body.Tools[1].InputSchema)
	}
}

func TestBuildBodyToolUseAndResultRoundTrip(t *testing.T) {
	p := New("claude-sonnet-5", provider.StaticCredentialSource{})
	msgs := []provider.Message{
		provider.UserText("run ls"),
		{Role: provider.RoleAssistant, Content: []provider.ContentBlock{
			provider.ReasoningBlock("thinking about it"),
			provider.ToolUseBlock("toolu_1", "bash", json.RawMessage(`{"cmd":"ls"}`)),
		}},
		{Role: provider.RoleUser, Content: []provider.ContentBlock{
			provider.ToolResultBlock("toolu_1", "file.txt", false),
		}},
	}
	r, err := p.buildBody(provider.Request{Messages: msgs}, provider.CredAPIKey)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	body := decodeBody(t, r)

	if len(body.Messages) != 3 {
		t.Fatalf("messages = %+v", body.Messages)
	}
	// Reasoning block dropped; only the tool_use survives in the assistant turn.
	asst := body.Messages[1]
	if len(asst.Content) != 1 || asst.Content[0].Type != "tool_use" {
		t.Fatalf("assistant content = %+v, want single tool_use (reasoning dropped)", asst.Content)
	}
	if asst.Content[0].ID != "toolu_1" || string(asst.Content[0].Input) != `{"cmd":"ls"}` {
		t.Errorf("tool_use = %+v", asst.Content[0])
	}
	tr := body.Messages[2].Content[0]
	if tr.Type != "tool_result" || tr.ToolUseID != "toolu_1" || tr.Content != "file.txt" {
		t.Errorf("tool_result = %+v", tr)
	}
}

func TestBuildBodyReplaysSignedReasoning(t *testing.T) {
	p := New("claude-sonnet-5", provider.StaticCredentialSource{})
	msgs := []provider.Message{
		provider.UserText("solve it"),
		{Role: provider.RoleAssistant, Content: []provider.ContentBlock{
			{Type: provider.BlockReasoning, Text: "step by step", Meta: map[string]string{metaSignatureKey: "sig-xyz"}},
			provider.ToolUseBlock("toolu_9", "bash", json.RawMessage(`{"cmd":"ls"}`)),
		}},
	}
	r, err := p.buildBody(provider.Request{Messages: msgs}, provider.CredAPIKey)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	body := decodeBody(t, r)

	asst := body.Messages[1]
	if len(asst.Content) != 2 {
		t.Fatalf("assistant content = %+v, want thinking + tool_use", asst.Content)
	}
	// Thinking block must come first, carrying its signature verbatim.
	th := asst.Content[0]
	if th.Type != "thinking" || th.Thinking != "step by step" || th.Signature != "sig-xyz" {
		t.Errorf("thinking block = %+v", th)
	}
	if asst.Content[1].Type != "tool_use" {
		t.Errorf("second block = %+v, want tool_use", asst.Content[1])
	}
}

// TestBuildBodyDropsEmptyTextSignedReasoning is the regression for the live
// Anthropic 400 "messages.N.content.M.thinking.thinking: Field required": a
// reasoning block that carries a signature but EMPTY text (Anthropic streamed a
// signature_delta with no thinking_delta text, folding to an empty-text signed
// reasoning block) must NOT be replayed as a thinking block — a thinking block
// with an empty `thinking` field serializes without the field (omitempty on the
// shared wire union) and the API rejects the whole request.
func TestBuildBodyDropsEmptyTextSignedReasoning(t *testing.T) {
	p := New("claude-sonnet-5", provider.StaticCredentialSource{})
	msgs := []provider.Message{
		provider.UserText("run it"),
		{Role: provider.RoleAssistant, Content: []provider.ContentBlock{
			{Type: provider.BlockReasoning, Text: "", Meta: map[string]string{metaSignatureKey: "sig-empty"}},
			provider.ToolUseBlock("toolu_1", "bash", json.RawMessage(`{"cmd":"ls"}`)),
		}},
	}
	r, err := p.buildBody(provider.Request{Messages: msgs}, provider.CredAPIKey)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}

	// The empty-text thinking block is dropped: only the tool_use survives.
	body := decodeBody(t, r)
	asst := body.Messages[1]
	if len(asst.Content) != 1 || asst.Content[0].Type != "tool_use" {
		t.Fatalf("assistant content = %+v, want only tool_use (empty-text thinking dropped)", asst.Content)
	}

	// Belt-and-suspenders: no thinking block anywhere in the wire body may carry
	// an empty `thinking` field (absent or "" both decode to ""), which is the
	// exact shape the API 400s on.
	for mi, m := range body.Messages {
		for ci, b := range m.Content {
			if b.Type == "thinking" && b.Thinking == "" {
				t.Errorf("messages[%d].content[%d] is a thinking block with an empty thinking field", mi, ci)
			}
		}
	}
}

func TestBuildBodyDropsUnsignedReasoning(t *testing.T) {
	p := New("claude-sonnet-5", provider.StaticCredentialSource{})
	msgs := []provider.Message{
		provider.UserText("hi"),
		{Role: provider.RoleAssistant, Content: []provider.ContentBlock{
			provider.ReasoningBlock("no signature here"), // Meta nil
			provider.AssistantText("the answer").Content[0],
		}},
	}
	r, err := p.buildBody(provider.Request{Messages: msgs}, provider.CredAPIKey)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	body := decodeBody(t, r)

	asst := body.Messages[1]
	if len(asst.Content) != 1 || asst.Content[0].Type != "text" {
		t.Fatalf("assistant content = %+v, want only the text block (unsigned reasoning dropped)", asst.Content)
	}
}

func TestBuildBodyDropsEmptyMessages(t *testing.T) {
	p := New("claude-sonnet-5", provider.StaticCredentialSource{})
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, Content: []provider.ContentBlock{provider.ReasoningBlock("only reasoning")}},
		provider.UserText("hi"),
	}
	r, err := p.buildBody(provider.Request{Messages: msgs}, provider.CredAPIKey)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	body := decodeBody(t, r)
	if len(body.Messages) != 1 || body.Messages[0].Role != "user" {
		t.Errorf("messages = %+v, want the reasoning-only message dropped", body.Messages)
	}
}

func TestBuildBodyThinkingConfig(t *testing.T) {
	p := New("claude-sonnet-5", provider.StaticCredentialSource{})
	temp := 0.7
	r, err := p.buildBody(provider.Request{
		Messages: []provider.Message{provider.UserText("hi")},
		Params: provider.Params{
			MaxTokens:   2000,
			Temperature: &temp,
			Thinking:    provider.Thinking{Enabled: true, BudgetTokens: 5000},
		},
	}, provider.CredAPIKey)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	body := decodeBody(t, r)

	if body.Thinking == nil || body.Thinking.Type != "enabled" || body.Thinking.BudgetTokens != 5000 {
		t.Fatalf("thinking = %+v", body.Thinking)
	}
	// max_tokens must exceed the budget.
	if body.MaxTokens <= 5000 {
		t.Errorf("max_tokens = %d, want > budget 5000", body.MaxTokens)
	}
	// Temperature must be omitted when thinking is on.
	if body.Temperature != nil {
		t.Errorf("temperature = %v, want nil with thinking enabled", *body.Temperature)
	}
}

func TestBuildBodyThinkingBudgetFloor(t *testing.T) {
	p := New("claude-sonnet-5", provider.StaticCredentialSource{})
	r, err := p.buildBody(provider.Request{
		Messages: []provider.Message{provider.UserText("hi")},
		Params:   provider.Params{Thinking: provider.Thinking{Enabled: true}},
	}, provider.CredAPIKey)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	body := decodeBody(t, r)
	if body.Thinking.BudgetTokens != minThinkingBudget {
		t.Errorf("budget = %d, want floor %d", body.Thinking.BudgetTokens, minThinkingBudget)
	}
}

func TestBuildBodyTemperaturePassThrough(t *testing.T) {
	p := New("claude-sonnet-5", provider.StaticCredentialSource{})
	temp := 0.3
	r, err := p.buildBody(provider.Request{
		Messages: []provider.Message{provider.UserText("hi")},
		Params:   provider.Params{Temperature: &temp},
	}, provider.CredAPIKey)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	body := decodeBody(t, r)
	if body.Temperature == nil || *body.Temperature != 0.3 {
		t.Errorf("temperature = %v, want 0.3", body.Temperature)
	}
}

func TestMaxTokensFromRegistry(t *testing.T) {
	p := New("claude-haiku-4-5", provider.StaticCredentialSource{})
	got := p.maxTokens("claude-haiku-4-5", provider.Params{})
	info, _ := provider.Lookup("claude-haiku-4-5")
	if got != info.MaxOutput {
		t.Errorf("maxTokens = %d, want registry max %d", got, info.MaxOutput)
	}
	if got := p.maxTokens("unknown-model", provider.Params{}); got != defaultMaxTokens {
		t.Errorf("maxTokens(unknown) = %d, want default %d", got, defaultMaxTokens)
	}
}

func TestInfoFallback(t *testing.T) {
	if got := New("claude-sonnet-5", provider.StaticCredentialSource{}).Info(); got.Provider != providerID {
		t.Errorf("Info().Provider = %q, want %q", got.Provider, providerID)
	}
	got := New("some-unregistered-model", provider.StaticCredentialSource{}).Info()
	if got.ID != "some-unregistered-model" || got.Provider != providerID {
		t.Errorf("Info() fallback = %+v", got)
	}
}

// TestInfoFallbackFlagsUnregistered pins the flag on the fallback record. It is
// the whole reason a consumer can tell the zero Pricing apart from a genuinely
// free model — without it, an unregistered model reads as costing $0.00.
func TestInfoFallbackFlagsUnregistered(t *testing.T) {
	got := New("some-unregistered-model", provider.StaticCredentialSource{}).Info()
	if !got.Unregistered {
		t.Errorf("Info() fallback Unregistered = false, want true: %+v", got)
	}
	if got.Pricing != (provider.Pricing{}) {
		t.Errorf("pricing invented for an unregistered model: %+v", got.Pricing)
	}
	// A registered model is the control: it comes from the registry, so the
	// flag must stay off there or it would mark every record unknown.
	if reg := New("claude-sonnet-5", provider.StaticCredentialSource{}).Info(); reg.Unregistered {
		t.Errorf("Info(claude-sonnet-5).Unregistered = true, want false: %+v", reg)
	}
}

// TestBuildBodyThinkingEffortEnables is the issue #88 regression at the
// Anthropic wire: a named effort with Enabled left false — exactly the Params a
// Runner produces for an embedder that never constructs provider.Params — must
// still turn extended thinking on, and each level must project onto its own
// budget. Before the fix every one of these cases emitted no thinking block at
// all, so Runner.SetEffort could not reach the API.
func TestBuildBodyThinkingEffortEnables(t *testing.T) {
	tests := []struct {
		name       string
		thinking   provider.Thinking
		wantBudget int
	}{
		{"low effort alone", provider.Thinking{Effort: provider.EffortLow}, lowThinkingBudget},
		{"medium effort alone", provider.Thinking{Effort: provider.EffortMedium}, mediumThinkingBudget},
		{"high effort alone", provider.Thinking{Effort: provider.EffortHigh}, highThinkingBudget},
		{
			"enabled plus effort agrees with effort alone",
			provider.Thinking{Enabled: true, Effort: provider.EffortHigh},
			highThinkingBudget,
		},
		{
			"explicit budget outranks the level",
			provider.Thinking{Effort: provider.EffortHigh, BudgetTokens: 5000},
			5000,
		},
		{
			"enabled with no level keeps the floor",
			provider.Thinking{Enabled: true},
			minThinkingBudget,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := New("claude-sonnet-5", provider.StaticCredentialSource{})
			temp := 0.7
			r, err := p.buildBody(provider.Request{
				Messages: []provider.Message{provider.UserText("hi")},
				Params:   provider.Params{Temperature: &temp, Thinking: tc.thinking},
			}, provider.CredAPIKey)
			if err != nil {
				t.Fatalf("buildBody: %v", err)
			}
			body := decodeBody(t, r)

			if body.Thinking == nil {
				t.Fatalf("thinking block missing for %+v — the effort never reached the wire", tc.thinking)
			}
			if body.Thinking.Type != "enabled" {
				t.Errorf("thinking type = %q, want %q", body.Thinking.Type, "enabled")
			}
			if body.Thinking.BudgetTokens != tc.wantBudget {
				t.Errorf("budget = %d, want %d", body.Thinking.BudgetTokens, tc.wantBudget)
			}
			// max_tokens must exceed the budget or the API rejects the request.
			if body.MaxTokens <= body.Thinking.BudgetTokens {
				t.Errorf("max_tokens = %d, want > budget %d", body.MaxTokens, body.Thinking.BudgetTokens)
			}
			// Anthropic forbids an explicit temperature alongside extended
			// thinking, so enabling via effort must drop it just as Enabled does.
			if body.Temperature != nil {
				t.Errorf("temperature = %v, want nil once thinking is on", *body.Temperature)
			}
		})
	}
}

// TestBuildBodyThinkingOffWithoutEffort is the must-fire twin of the test
// above: with neither Enabled nor an effort, no thinking block may appear and
// temperature must survive. Without it, a change that unconditionally enabled
// thinking would pass every assertion in TestBuildBodyThinkingEffortEnables.
func TestBuildBodyThinkingOffWithoutEffort(t *testing.T) {
	p := New("claude-sonnet-5", provider.StaticCredentialSource{})
	temp := 0.7
	r, err := p.buildBody(provider.Request{
		Messages: []provider.Message{provider.UserText("hi")},
		Params:   provider.Params{Temperature: &temp, Thinking: provider.Thinking{}},
	}, provider.CredAPIKey)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	body := decodeBody(t, r)
	if body.Thinking != nil {
		t.Errorf("thinking = %+v, want nil with reasoning unrequested", body.Thinking)
	}
	if body.Temperature == nil || *body.Temperature != temp {
		t.Errorf("temperature = %v, want %v preserved when thinking is off", body.Temperature, temp)
	}
}
