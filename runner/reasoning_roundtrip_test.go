package runner_test

import (
	"context"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/runner"
	"github.com/jedwards1230/agent-sdk-go/session"
)

// TestReasoningTextSurvivesJournalRoundTrip drives the REAL Anthropic stream shape — a
// thinking_delta carrying the thinking TEXT followed by a SEPARATE
// signature_delta carrying the signature on an EMPTY-text reasoning delta (see
// provider/anthropic/stream.go) — across a tool-using turn, then folds the
// journal and asserts each reasoning block keeps BOTH its text and signature.
func TestReasoningTextSurvivesJournalRoundTrip(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()

	prov := &scriptedProvider{events: [][]provider.StreamEvent{
		// Iteration 1: think, then a tool call.
		{
			{Type: provider.StreamReasoningDelta, Text: "planning the read"},
			{Type: provider.StreamReasoningDelta, Meta: map[string]string{"anthropic.signature": "sig-1"}},
			{Type: provider.StreamToolCallStart, Tool: &provider.ToolCall{ID: "t1", Name: "read"}},
			{Type: provider.StreamToolCallEnd, Tool: &provider.ToolCall{ID: "t1", Name: "read", Input: []byte(`{"path":"x"}`)}},
			{Type: provider.StreamFinished, StopReason: provider.StopToolUse, Usage: provider.Usage{InputTokens: 5, OutputTokens: 2}},
		},
		// Iteration 2: think again, then answer.
		{
			{Type: provider.StreamReasoningDelta, Text: "now answering"},
			{Type: provider.StreamReasoningDelta, Meta: map[string]string{"anthropic.signature": "sig-2"}},
			{Type: provider.StreamTextDelta, Text: "done"},
			{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{InputTokens: 6, OutputTokens: 1}},
		},
	}}

	r, err := runner.New(context.Background(), runner.Options{
		Root: root, Cwd: cwd, Model: testModel, System: "test system",
		Provider: prov,
		Tools:    oneToolRegistry{name: "read", tool: erroringTool{}},
		IDGen:    seqIDGen(), Clock: seqClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.Prompt(context.Background(), "read the file"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store, err := session.NewFileStore(session.WithRoot(root))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer func() { _ = store.Close() }()
	j, err := store.Open(context.Background(), r.ID())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	fold := j.Fold()
	var reasoning []provider.ContentBlock
	for _, m := range fold {
		reasoning = append(reasoning, blocksOfType(m, provider.BlockReasoning)...)
	}
	if len(reasoning) != 2 {
		t.Fatalf("reasoning blocks = %d, want 2", len(reasoning))
	}
	wantText := []string{"planning the read", "now answering"}
	wantSig := []string{"sig-1", "sig-2"}
	for i, b := range reasoning {
		if b.Text != wantText[i] {
			t.Errorf("reasoning[%d].Text = %q, want %q (TEXT LOST)", i, b.Text, wantText[i])
		}
		if b.Meta["anthropic.signature"] != wantSig[i] {
			t.Errorf("reasoning[%d] sig = %q, want %q", i, b.Meta["anthropic.signature"], wantSig[i])
		}
	}
}
