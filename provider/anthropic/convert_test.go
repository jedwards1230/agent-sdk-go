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
