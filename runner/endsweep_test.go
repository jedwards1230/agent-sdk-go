package runner_test

import (
	"context"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/runner"
	"github.com/jedwards1230/agent-sdk-go/session"
)

// TestPrompt_BackToBackJournalOrder drives two prompts on the SAME live Runner
// without a Close between them (the supervisor's path) and asserts the journal
// stays correctly ordered: user1, assistant1, user2, assistant2. Journaling
// runs on the async consume goroutine, so without an inter-turn drain barrier
// the second Prompt's user-message append could land ahead of the first run's
// assistant entry (userA, userB, assistantA) and corrupt the fold.
func TestPrompt_BackToBackJournalOrder(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()

	prov := &scriptedProvider{events: [][]provider.StreamEvent{
		{
			{Type: provider.StreamTextDelta, Text: "reply one"},
			{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
		{
			{Type: provider.StreamTextDelta, Text: "reply two"},
			{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
	}}

	r, err := runner.New(context.Background(), runner.Options{
		Root: root, Cwd: cwd, Model: testModel, System: "test system",
		Provider: prov, Tools: oneToolRegistry{},
		IDGen: seqIDGen(), Clock: seqClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id := r.ID()

	// Two back-to-back prompts on the live runner — no Close in between.
	if err := r.Prompt(context.Background(), "first"); err != nil {
		t.Fatalf("Prompt 1: %v", err)
	}
	if err := r.Prompt(context.Background(), "second"); err != nil {
		t.Fatalf("Prompt 2: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store, err := session.NewFileStore(session.WithRoot(root))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer func() { _ = store.Close() }()
	j, err := store.Open(context.Background(), id)
	if err != nil {
		t.Fatalf("Open(%s): %v", id, err)
	}

	want := []struct {
		role provider.Role
		text string
	}{
		{provider.RoleUser, "first"},
		{provider.RoleAssistant, "reply one"},
		{provider.RoleUser, "second"},
		{provider.RoleAssistant, "reply two"},
	}
	fold := j.Fold()
	if len(fold) != len(want) {
		t.Fatalf("Fold: got %d messages, want %d: %+v", len(fold), len(want), fold)
	}
	for i, w := range want {
		if fold[i].Role != w.role {
			t.Errorf("fold[%d].Role = %q, want %q", i, fold[i].Role, w.role)
		}
		if got := msgText(fold[i]); got != w.text {
			t.Errorf("fold[%d] text = %q, want %q", i, got, w.text)
		}
	}
}

// TestConsume_EmptyReasoningSummaryMetaPreserved asserts a reasoning item that
// streams NO summary text but does carry Meta (an OpenAI item id +
// encrypted_content) is still journaled as a reasoning block carrying that Meta.
// Gating the block on non-empty text would drop the encrypted content and defeat
// reasoning replay.
func TestConsume_EmptyReasoningSummaryMetaPreserved(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()

	meta := map[string]string{"openai.item_id": "rs_1", "openai.encrypted_content": "enc-xyz"}
	prov := &scriptedProvider{events: [][]provider.StreamEvent{
		{
			{Type: provider.StreamReasoningDelta, Text: "", Meta: meta},
			{Type: provider.StreamTextDelta, Text: "the answer"},
			{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{InputTokens: 2, OutputTokens: 2}},
		},
	}}

	r, err := runner.New(context.Background(), runner.Options{
		Root: root, Cwd: cwd, Model: testModel, System: "test system",
		Provider: prov, Tools: oneToolRegistry{},
		IDGen: seqIDGen(), Clock: seqClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id := r.ID()
	if err := r.Prompt(context.Background(), "explain"); err != nil {
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
	j, err := store.Open(context.Background(), id)
	if err != nil {
		t.Fatalf("Open(%s): %v", id, err)
	}

	fold := j.Fold()
	reasoning := blocksOfType(fold[1], provider.BlockReasoning)
	if len(reasoning) != 1 {
		t.Fatalf("reasoning blocks = %+v, want exactly one (empty summary, has Meta)", reasoning)
	}
	if got := reasoning[0].Meta["openai.encrypted_content"]; got != "enc-xyz" {
		t.Errorf("reasoning Meta[openai.encrypted_content] = %q, want %q", got, "enc-xyz")
	}
	if got := reasoning[0].Meta["openai.item_id"]; got != "rs_1" {
		t.Errorf("reasoning Meta[openai.item_id] = %q, want %q", got, "rs_1")
	}
}

// TestConsume_MultipleReasoningItemsPreserved asserts two distinct reasoning
// items in one turn (reasoning → text → reasoning, so the loop settles two
// reasoning MessageFinished events) are journaled as two separate reasoning
// blocks, each keeping its own Meta — rather than collapsing into one block
// that carries only the last item's encrypted_content.
func TestConsume_MultipleReasoningItemsPreserved(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()

	prov := &scriptedProvider{events: [][]provider.StreamEvent{
		{
			{Type: provider.StreamReasoningDelta, Text: "first thought", Meta: map[string]string{"openai.item_id": "rs_1", "openai.encrypted_content": "enc-1"}},
			{Type: provider.StreamTextDelta, Text: "partial"},
			{Type: provider.StreamReasoningDelta, Text: "second thought", Meta: map[string]string{"openai.item_id": "rs_2", "openai.encrypted_content": "enc-2"}},
			{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{InputTokens: 3, OutputTokens: 3}},
		},
	}}

	r, err := runner.New(context.Background(), runner.Options{
		Root: root, Cwd: cwd, Model: testModel, System: "test system",
		Provider: prov, Tools: oneToolRegistry{},
		IDGen: seqIDGen(), Clock: seqClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id := r.ID()
	if err := r.Prompt(context.Background(), "explain"); err != nil {
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
	j, err := store.Open(context.Background(), id)
	if err != nil {
		t.Fatalf("Open(%s): %v", id, err)
	}

	fold := j.Fold()
	reasoning := blocksOfType(fold[1], provider.BlockReasoning)
	if len(reasoning) != 2 {
		t.Fatalf("reasoning blocks = %+v, want two (one per distinct item)", reasoning)
	}
	if got := reasoning[0].Meta["openai.encrypted_content"]; got != "enc-1" {
		t.Errorf("reasoning[0] Meta[openai.encrypted_content] = %q, want enc-1", got)
	}
	if got := reasoning[1].Meta["openai.encrypted_content"]; got != "enc-2" {
		t.Errorf("reasoning[1] Meta[openai.encrypted_content] = %q, want enc-2", got)
	}
}
