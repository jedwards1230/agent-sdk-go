package provider_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

func TestUsageAdd(t *testing.T) {
	a := provider.Usage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 2, Raw: map[string]int{"x": 1}}
	b := provider.Usage{InputTokens: 3, OutputTokens: 7, CacheWriteTokens: 4, Raw: map[string]int{"y": 2}}
	got := a.Add(b)
	want := provider.Usage{
		InputTokens: 13, OutputTokens: 12, CacheReadTokens: 2, CacheWriteTokens: 4,
		Raw: map[string]int{"x": 1, "y": 2},
	}
	if !got.Equal(want) {
		t.Errorf("Add = %+v, want %+v", got, want)
	}
}

func TestUsageEqualAndZero(t *testing.T) {
	if !(provider.Usage{}).IsZero() {
		t.Error("zero Usage should report IsZero")
	}
	if (provider.Usage{InputTokens: 1}).IsZero() {
		t.Error("non-zero Usage should not report IsZero")
	}
	a := provider.Usage{InputTokens: 1, Raw: map[string]int{"k": 1}}
	if !a.Equal(provider.Usage{InputTokens: 1, Raw: map[string]int{"k": 1}}) {
		t.Error("equal usages should compare equal")
	}
	if a.Equal(provider.Usage{InputTokens: 1, Raw: map[string]int{"k": 2}}) {
		t.Error("differing raw maps should compare unequal")
	}
}

func TestLookupAndCost(t *testing.T) {
	m, ok := provider.Lookup("claude-opus-4-8")
	if !ok {
		t.Fatal("claude-opus-4-8 should be registered")
	}
	if m.Provider != "anthropic" || m.ContextWindow != 1_000_000 || !m.Reasoning {
		t.Errorf("opus info = %+v", m)
	}

	// 1M input + 1M output at $5/$25 per 1M = $30.
	cost, ok := provider.CostOf("claude-opus-4-8", provider.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000})
	if !ok {
		t.Fatal("CostOf should find claude-opus-4-8")
	}
	if cost.USD != 30 || cost.InputUSD != 5 || cost.OutputUSD != 25 {
		t.Errorf("cost = %+v, want total 30 (5 input + 25 output)", cost)
	}

	if _, ok := provider.CostOf("nonexistent-model", provider.Usage{InputTokens: 100}); ok {
		t.Error("CostOf should not find an unregistered model")
	}
}

func TestCostCacheTiers(t *testing.T) {
	// sonnet-5: CacheRead 0.3, CacheWrite 3.75 per 1M.
	c := provider.Pricing{CacheRead: 0.3, CacheWrite: 3.75}.Cost(
		provider.Usage{CacheReadTokens: 1_000_000, CacheWriteTokens: 1_000_000})
	if c.CacheReadUSD != 0.3 || c.CacheWriteUSD != 3.75 {
		t.Errorf("cache cost = %+v", c)
	}
}

func TestEnvCredentialSource(t *testing.T) {
	t.Setenv("TEST_PROVIDER_KEY", "sk-test-123")
	src := provider.EnvCredentialSource{Vars: map[string]string{"acme": "TEST_PROVIDER_KEY"}}

	cred, err := src.Credential(context.Background(), "acme")
	if err != nil {
		t.Fatalf("Credential: %v", err)
	}
	if cred.Kind != provider.CredAPIKey || cred.Token != "sk-test-123" {
		t.Errorf("cred = %+v", cred)
	}

	if _, err := src.Credential(context.Background(), "unknown"); err == nil {
		t.Error("unconfigured provider should error")
	}

	t.Setenv("TEST_PROVIDER_KEY", "")
	if _, err := src.Credential(context.Background(), "acme"); err == nil {
		t.Error("empty env var should error")
	}
}

func TestStaticEnvDefaults(t *testing.T) {
	src := provider.StaticEnv()
	if src.Vars["anthropic"] != "ANTHROPIC_API_KEY" || src.Vars["openai"] != "OPENAI_API_KEY" {
		t.Errorf("StaticEnv vars = %+v", src.Vars)
	}
}

func TestStaticCredentialSource(t *testing.T) {
	src := provider.StaticCredentialSource{Cred: provider.Credential{Kind: provider.CredOAuth, Token: "bearer", Account: "acct-123"}}
	cred, err := src.Credential(context.Background(), "anything")
	if err != nil {
		t.Fatalf("Credential: %v", err)
	}
	if cred.Kind != provider.CredOAuth || cred.Token != "bearer" || cred.Account != "acct-123" {
		t.Errorf("cred = %+v (Account must round-trip for ChatGPT-subscription OAuth)", cred)
	}
	if _, err := (provider.StaticCredentialSource{}).Credential(context.Background(), "x"); err == nil {
		t.Error("empty static credential should error")
	}
}

func TestSliceStreamAndIter(t *testing.T) {
	events := []provider.StreamEvent{
		{Type: provider.StreamTextDelta, Text: "hi"},
		{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{OutputTokens: 1}},
	}
	h := provider.SliceStream(events...)
	defer func() { _ = h.Close() }()

	var got []provider.StreamEvent
	for ev, err := range provider.Iter(h) {
		if err != nil {
			t.Fatalf("Iter err: %v", err)
		}
		got = append(got, ev)
	}
	if len(got) != 2 || got[0].Text != "hi" || got[1].StopReason != provider.StopEndTurn {
		t.Errorf("got %+v", got)
	}
}

func TestIterPropagatesError(t *testing.T) {
	h := errStream{err: errors.New("boom")}
	var gotErr error
	for _, err := range provider.Iter(h) {
		gotErr = err
	}
	if gotErr == nil || gotErr.Error() != "boom" {
		t.Errorf("gotErr = %v, want boom", gotErr)
	}
}

type errStream struct{ err error }

func (e errStream) Next() (provider.StreamEvent, error) { return provider.StreamEvent{}, e.err }

func (e errStream) Close() error { return nil }

func TestContentBlockConstructors(t *testing.T) {
	m := provider.UserText("hello")
	if m.Role != provider.RoleUser || m.Text() != "hello" {
		t.Errorf("UserText = %+v", m)
	}
	tu := provider.ToolUseBlock("id1", "bash", []byte(`{"cmd":"ls"}`))
	if tu.Type != provider.BlockToolUse || tu.ToolName != "bash" {
		t.Errorf("ToolUseBlock = %+v", tu)
	}
	tr := provider.ToolResultBlock("id1", "output", true)
	if tr.Type != provider.BlockToolResult || !tr.IsError || tr.ToolUseID != "id1" {
		t.Errorf("ToolResultBlock = %+v", tr)
	}
}

// TestThinkingActive pins the enablement rule the adapters key on: a named
// effort level is self-enabling, so a caller that only set Effort — the state
// every embedder that never constructs provider.Params lands in — still gets
// reasoning. Requiring Enabled alongside Effort is what made Runner.SetEffort
// a no-op on the wire through v0.17.0 (issue #88).
func TestThinkingActive(t *testing.T) {
	tests := []struct {
		name     string
		thinking provider.Thinking
		want     bool
	}{
		{"zero value is off", provider.Thinking{}, false},
		{"enabled alone is on", provider.Thinking{Enabled: true}, true},
		{"effort alone is on", provider.Thinking{Effort: provider.EffortHigh}, true},
		{"low effort alone is on", provider.Thinking{Effort: provider.EffortLow}, true},
		{"both is on", provider.Thinking{Enabled: true, Effort: provider.EffortMedium}, true},
		{"budget alone does not enable", provider.Thinking{BudgetTokens: 8000}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.thinking.Active(); got != tc.want {
				t.Errorf("Thinking%+v.Active() = %v, want %v", tc.thinking, got, tc.want)
			}
		})
	}
}
