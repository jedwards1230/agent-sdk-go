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

// TestBuildBodyThinkingDerivedBudgetClamped covers the cases where the
// effort-derived budget does NOT fit the output cap, and must shrink to fit
// rather than inflating that cap. Without the clamp, a "high" level sends
// max_tokens 36864 for any model whose cap the registry does not know — past
// what several real Anthropic models allow, turning a request that was valid
// before the effort projection existed into a 400.
func TestBuildBodyThinkingDerivedBudgetClamped(t *testing.T) {
	tests := []struct {
		name          string
		model         string
		params        provider.Params
		wantBudget    int
		wantMaxTokens int
	}{
		{
			// The cap falls back to defaultMaxTokens, leaving no headroom at all.
			name:          "unregistered model falls back to the floor",
			model:         "claude-3-5-haiku-latest",
			params:        provider.Params{Thinking: provider.Thinking{Effort: provider.EffortHigh}},
			wantBudget:    minThinkingBudget,
			wantMaxTokens: defaultMaxTokens,
		},
		{
			// The caller's explicit cap is their most specific statement; a level
			// is an approximation and must not override it.
			name:          "caller max_tokens outranks the level",
			model:         "claude-sonnet-5",
			params:        provider.Params{MaxTokens: 4000, Thinking: provider.Thinking{Effort: provider.EffortHigh}},
			wantBudget:    minThinkingBudget,
			wantMaxTokens: 4000,
		},
		{
			// Enough headroom to seat the level's full budget under the cap.
			name:          "cap with headroom keeps the full level budget",
			model:         "claude-sonnet-5",
			params:        provider.Params{MaxTokens: 40000, Thinking: provider.Thinking{Effort: provider.EffortHigh}},
			wantBudget:    highThinkingBudget,
			wantMaxTokens: 40000,
		},
		{
			// An EXPLICIT budget keeps the historical raise-the-cap behavior, so
			// embedders that set one see no change from the clamp.
			name:          "explicit budget still raises the cap",
			model:         "claude-3-5-haiku-latest",
			params:        provider.Params{Thinking: provider.Thinking{Enabled: true, BudgetTokens: 20000}},
			wantBudget:    20000,
			wantMaxTokens: 20000 + defaultMaxTokens,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := New(tc.model, provider.StaticCredentialSource{})
			r, err := p.buildBody(provider.Request{
				Messages: []provider.Message{provider.UserText("hi")},
				Params:   tc.params,
			}, provider.CredAPIKey)
			if err != nil {
				t.Fatalf("buildBody: %v", err)
			}
			body := decodeBody(t, r)
			if body.Thinking == nil {
				t.Fatalf("thinking block missing")
			}
			if body.Thinking.BudgetTokens != tc.wantBudget {
				t.Errorf("budget = %d, want %d", body.Thinking.BudgetTokens, tc.wantBudget)
			}
			if body.MaxTokens != tc.wantMaxTokens {
				t.Errorf("max_tokens = %d, want %d", body.MaxTokens, tc.wantMaxTokens)
			}
			// The invariant the whole clamp exists to protect.
			if body.MaxTokens <= body.Thinking.BudgetTokens {
				t.Errorf("max_tokens %d <= budget %d: the API rejects this", body.MaxTokens, body.Thinking.BudgetTokens)
			}
		})
	}
}

// --- prompt caching (cache_control breakpoints) ---

// countCacheMarkers totals cache_control markers across the whole request:
// system blocks, tools, and every message content block. The Anthropic limit is
// four; the builder must never exceed it.
func countCacheMarkers(body messagesRequest) int {
	n := 0
	for _, s := range body.System {
		if s.CacheControl != nil {
			n++
		}
	}
	for _, t := range body.Tools {
		if t.CacheControl != nil {
			n++
		}
	}
	for _, m := range body.Messages {
		for _, b := range m.Content {
			if b.CacheControl != nil {
				n++
			}
		}
	}
	return n
}

// lastBlockMarked reports whether the final content block of the message at idx
// carries a cache_control marker.
func lastBlockMarked(body messagesRequest, idx int) bool {
	blocks := body.Messages[idx].Content
	if len(blocks) == 0 {
		return false
	}
	return blocks[len(blocks)-1].CacheControl != nil
}

// anyBlockMarked reports whether any block of the message at idx is marked.
func anyBlockMarked(body messagesRequest, idx int) bool {
	for _, b := range body.Messages[idx].Content {
		if b.CacheControl != nil {
			return true
		}
	}
	return false
}

func TestPromptCacheSystemMarkerAPIKey(t *testing.T) {
	p := New("claude-sonnet-5", provider.StaticCredentialSource{})
	r, err := p.buildBody(provider.Request{
		System:   "be terse",
		Messages: []provider.Message{provider.UserText("hi")},
	}, provider.CredAPIKey)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	body := decodeBody(t, r)

	if len(body.System) != 1 {
		t.Fatalf("system = %+v, want single block", body.System)
	}
	if body.System[0].CacheControl == nil || body.System[0].CacheControl.Type != "ephemeral" {
		t.Errorf("system[0] cache_control = %+v, want ephemeral", body.System[0].CacheControl)
	}
}

func TestPromptCacheSystemMarkerOAuthOnlyLastBlock(t *testing.T) {
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
	if body.System[0].CacheControl != nil {
		t.Errorf("system[0] (identity) must NOT be marked; only the last block is")
	}
	if body.System[1].CacheControl == nil {
		t.Errorf("system[1] (last block) must be marked")
	}
}

func TestPromptCacheToolMarkerOnlyLast(t *testing.T) {
	p := New("claude-sonnet-5", provider.StaticCredentialSource{})
	r, err := p.buildBody(provider.Request{
		Messages: []provider.Message{provider.UserText("go")},
		Tools: []provider.ToolSpec{
			{Name: "a"}, {Name: "b"}, {Name: "c"},
		},
	}, provider.CredAPIKey)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	body := decodeBody(t, r)

	if len(body.Tools) != 3 {
		t.Fatalf("tools = %+v", body.Tools)
	}
	for i := 0; i < 2; i++ {
		if body.Tools[i].CacheControl != nil {
			t.Errorf("tool[%d] must NOT be marked; only the last tool is", i)
		}
	}
	if body.Tools[2].CacheControl == nil || body.Tools[2].CacheControl.Type != "ephemeral" {
		t.Errorf("tool[2] (last) cache_control = %+v, want ephemeral", body.Tools[2].CacheControl)
	}
}

func TestPromptCacheNoToolsNoToolMarker(t *testing.T) {
	p := New("claude-sonnet-5", provider.StaticCredentialSource{})
	r, err := p.buildBody(provider.Request{
		Messages: []provider.Message{provider.UserText("hi")},
	}, provider.CredAPIKey)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	body := decodeBody(t, r)
	if len(body.Tools) != 0 {
		t.Fatalf("expected no tools, got %+v", body.Tools)
	}
}

func TestPromptCacheRollingBoundarySecondToLast(t *testing.T) {
	p := New("claude-sonnet-5", provider.StaticCredentialSource{})
	msgs := []provider.Message{
		provider.UserText("u1"),
		provider.AssistantText("a1"),
		provider.UserText("u2"), // newest turn — must stay unmarked
	}
	r, err := p.buildBody(provider.Request{Messages: msgs}, provider.CredAPIKey)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	body := decodeBody(t, r)

	if len(body.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(body.Messages))
	}
	if !lastBlockMarked(body, 1) {
		t.Errorf("second-to-last message (idx 1) last block must be marked")
	}
	if anyBlockMarked(body, 0) {
		t.Errorf("message idx 0 must not be marked (only the rolling boundary is)")
	}
	if anyBlockMarked(body, 2) {
		t.Errorf("newest message (idx 2) must NOT be marked — it mutates every request")
	}
}

func TestPromptCacheSingleMessageNoConversationMarker(t *testing.T) {
	p := New("claude-sonnet-5", provider.StaticCredentialSource{})
	r, err := p.buildBody(provider.Request{
		Messages: []provider.Message{provider.UserText("only turn")},
	}, provider.CredAPIKey)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	body := decodeBody(t, r)
	if anyBlockMarked(body, 0) {
		t.Errorf("a single (newest) message must carry no conversation marker")
	}
}

// TestPromptCacheMarkerCapNeverExceedsFour walks representative request shapes
// and asserts the builder never places more than four cache_control markers
// (the Anthropic maximum). The fixed strategy tops out at three.
func TestPromptCacheMarkerCapNeverExceedsFour(t *testing.T) {
	longHistory := make([]provider.Message, 0, 12)
	for i := 0; i < 6; i++ {
		longHistory = append(longHistory, provider.UserText("u"), provider.AssistantText("a"))
	}
	cases := []struct {
		name string
		req  provider.Request
		cred provider.CredKind
		want int
	}{
		{"bare user", provider.Request{Messages: []provider.Message{provider.UserText("hi")}}, provider.CredAPIKey, 0},
		{"system only", provider.Request{System: "s", Messages: []provider.Message{provider.UserText("hi")}}, provider.CredAPIKey, 1},
		{"tools+system", provider.Request{System: "s", Tools: []provider.ToolSpec{{Name: "a"}, {Name: "b"}}, Messages: []provider.Message{provider.UserText("hi")}}, provider.CredAPIKey, 2},
		{"tools+system+oauth+long history", provider.Request{System: "s", Tools: []provider.ToolSpec{{Name: "a"}, {Name: "b"}}, Messages: longHistory}, provider.CredOAuth, 3},
	}
	p := New("claude-sonnet-5", provider.StaticCredentialSource{})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := p.buildBody(tc.req, tc.cred)
			if err != nil {
				t.Fatalf("buildBody: %v", err)
			}
			body := decodeBody(t, r)
			got := countCacheMarkers(body)
			if got > 4 {
				t.Fatalf("markers = %d, exceeds the Anthropic limit of 4", got)
			}
			if got != tc.want {
				t.Errorf("markers = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestPromptCacheMultiTurnInvariant proves the rolling boundary yields
// turn-over-turn reuse: as the conversation grows, the boundary marker advances
// to the new second-to-last message, the previously-marked boundary content
// remains in the (now longer) stable prefix, and the newest turn is never
// marked. So turn N+1's cached prefix is a superset of turn N's.
func TestPromptCacheMultiTurnInvariant(t *testing.T) {
	p := New("claude-sonnet-5", provider.StaticCredentialSource{})

	turnN := []provider.Message{
		provider.UserText("u1"),
		provider.AssistantText("a1"), // boundary this turn (idx 1)
		provider.UserText("u2"),      // newest
	}
	turnN1 := []provider.Message{
		provider.UserText("u1"),
		provider.AssistantText("a1"),
		provider.UserText("u2"),
		provider.AssistantText("a2"), // boundary next turn (idx 3)
		provider.UserText("u3"),      // newest
	}

	decode := func(msgs []provider.Message) messagesRequest {
		r, err := p.buildBody(provider.Request{Messages: msgs}, provider.CredAPIKey)
		if err != nil {
			t.Fatalf("buildBody: %v", err)
		}
		return decodeBody(t, r)
	}
	bodyN := decode(turnN)
	bodyN1 := decode(turnN1)

	// Turn N: boundary at second-to-last (a1), newest (u2) unmarked.
	if !lastBlockMarked(bodyN, len(bodyN.Messages)-2) {
		t.Fatalf("turn N: expected boundary marker on second-to-last message")
	}
	if anyBlockMarked(bodyN, len(bodyN.Messages)-1) {
		t.Fatalf("turn N: newest message must be unmarked")
	}
	if bodyN.Messages[1].Content[0].Text != "a1" {
		t.Fatalf("turn N: expected boundary message text a1, got %q", bodyN.Messages[1].Content[0].Text)
	}

	// Turn N+1: boundary advanced to the new second-to-last (a2), newest (u3) unmarked.
	if !lastBlockMarked(bodyN1, len(bodyN1.Messages)-2) {
		t.Fatalf("turn N+1: expected boundary marker on second-to-last message")
	}
	if anyBlockMarked(bodyN1, len(bodyN1.Messages)-1) {
		t.Fatalf("turn N+1: newest message must be unmarked")
	}
	if bodyN1.Messages[3].Content[0].Text != "a2" {
		t.Fatalf("turn N+1: expected boundary message text a2, got %q", bodyN1.Messages[3].Content[0].Text)
	}

	// The prior boundary content (a1) is still present in turn N+1's prefix,
	// now an interior stable element (unmarked) — so N+1's cached prefix ⊇ N's.
	if bodyN1.Messages[1].Content[0].Text != "a1" {
		t.Fatalf("turn N+1: prior boundary content a1 must persist in the prefix")
	}
	if anyBlockMarked(bodyN1, 1) {
		t.Fatalf("turn N+1: the old boundary (a1) must now be an unmarked cached-prefix element")
	}
	// Boundary advanced strictly forward: 1 -> 3.
	if got, want := len(bodyN1.Messages)-2, 3; got != want {
		t.Fatalf("turn N+1 boundary index = %d, want %d", got, want)
	}
}

func TestPromptCacheDisabledEmitsNoMarkers(t *testing.T) {
	p := New("claude-sonnet-5", provider.StaticCredentialSource{}, WithPromptCaching(false))
	r, err := p.buildBody(provider.Request{
		System: "be terse",
		Tools:  []provider.ToolSpec{{Name: "a"}, {Name: "b"}},
		Messages: []provider.Message{
			provider.UserText("u1"), provider.AssistantText("a1"), provider.UserText("u2"),
		},
	}, provider.CredOAuth)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	body := decodeBody(t, r)
	if got := countCacheMarkers(body); got != 0 {
		t.Errorf("WithPromptCaching(false): markers = %d, want 0", got)
	}
}
