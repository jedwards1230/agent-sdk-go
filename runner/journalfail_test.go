package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/loop"
	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/session"

	"github.com/jedwards1230/agent-sdk-go/runner"
)

// errWriteFailed stands in for a durable-append fault (ENOSPC, EIO) that the
// journal surfaces from its sink.
var errWriteFailed = errors.New("simulated journal write failure")

// failOnMatch is a session.JournalWriter that fails the write of any entry
// whose JSON line contains match, and discards every other line. It is the
// fault-injection seam for exercising what the runner does when one specific
// journal Append fails — targeting an entry by its content rather than its
// ordinal, so the fixture does not silently retarget when the number of
// entries a turn writes changes.
type failOnMatch struct {
	match string
}

func (w *failOnMatch) Write(p []byte) (int, error) {
	if strings.Contains(string(p), w.match) {
		return 0, errWriteFailed
	}
	return len(p), nil
}

func (w *failOnMatch) Sync() error  { return nil }
func (w *failOnMatch) Close() error { return nil }

// echoTool succeeds with fixed content, so a tool turn settles normally and
// the only thing that can fail is the journal.
type echoTool struct{}

func (echoTool) Run(context.Context, json.RawMessage) (loop.ToolResult, error) {
	return loop.ToolResult{Content: "tool ran"}, nil
}

// toolTurnScript is a two-call script: the first model call announces one tool
// call and stops with StopToolUse (so the runner journals an assistant message
// carrying a tool_use block, then a tool_result round); the second replies and
// ends the turn.
func toolTurnScript(t *testing.T) *scriptedProvider {
	t.Helper()
	toolInput, err := json.Marshal(map[string]string{"path": "some.txt"})
	if err != nil {
		t.Fatalf("marshal tool input: %v", err)
	}
	return &scriptedProvider{events: [][]provider.StreamEvent{
		{
			{Type: provider.StreamTextDelta, Text: "let me read that"},
			{Type: provider.StreamToolCallStart, Tool: &provider.ToolCall{ID: "t1", Name: "read"}},
			{Type: provider.StreamToolCallEnd, Tool: &provider.ToolCall{ID: "t1", Name: "read", Input: toolInput}},
			{Type: provider.StreamFinished, StopReason: provider.StopToolUse, Usage: provider.Usage{InputTokens: 5, OutputTokens: 2}},
		},
		{
			{Type: provider.StreamTextDelta, Text: "here is what it said"},
			{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
	}}
}

// newFailingRunner builds a Runner over a MemStore whose journal sink fails
// every entry containing match, and returns the runner alongside the store so
// the caller can reopen the journal for out-of-band assertions.
func newFailingRunner(t *testing.T, match string, prov provider.Provider) (*runner.Runner, session.Store) {
	t.Helper()
	// A MemStore's journal path is synthetic and relative ("<id>.jsonl"), so
	// per-call tool-output spills land under the process's working directory.
	// Move it to a temp dir for the test's duration so the run leaves nothing
	// in the package directory.
	t.Chdir(t.TempDir())

	store := session.NewMemStore(session.WithMemJournalWriter(
		func(string) session.JournalWriter { return &failOnMatch{match: match} },
	))
	r, err := runner.New(context.Background(), runner.Options{
		Cwd:      t.TempDir(),
		Model:    testModel,
		System:   "test system",
		Provider: prov,
		Tools:    oneToolRegistry{name: "read", tool: echoTool{}},
		Store:    store,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r, store
}

// journalOf reopens id on store and returns its entries and folded context.
func journalOf(t *testing.T, store session.Store, id string) ([]session.Entry, []provider.Message) {
	t.Helper()
	j, err := store.Open(context.Background(), id)
	if err != nil {
		t.Fatalf("Open(%s): %v", id, err)
	}
	return j.Entries(), j.Fold()
}

// hasBlock reports whether any message in fold carries a block of type bt.
func hasBlock(fold []provider.Message, bt provider.BlockType) bool {
	for _, m := range fold {
		if len(blocksOfType(m, bt)) > 0 {
			return true
		}
	}
	return false
}

// TestPrompt_AssistantAppendFailure is the regression for issue #64: when the
// assistant message carrying a turn's tool_use blocks fails to append, the
// runner must NOT go on to write that turn's tool_result round — a
// tool_result with no matching tool_use is an orphan that breaks the provider
// projection on every subsequent resume, converting one lost turn into a
// permanently unusable session. The failure must also reach the caller at the
// Prompt turn boundary, matching the user-message append path, which already
// returns its error.
func TestPrompt_AssistantAppendFailure(t *testing.T) {
	// The assistant message is the only entry carrying a tool_use block, so
	// matching on it fails exactly that append and leaves the user message,
	// the tool_result round, and the follow-up reply writable.
	r, store := newFailingRunner(t, `"tool_use"`, toolTurnScript(t))
	id := r.ID()

	promptErr := r.Prompt(context.Background(), "read some.txt")
	closeErr := r.Close()

	entries, fold := journalOf(t, store, id)

	// The turn is gone from the journal and from the folded context: no
	// assistant entry carries the tool_use blocks that failed to append.
	if hasBlock(fold, provider.BlockToolUse) {
		t.Errorf("fold carries a tool_use block, but its assistant append failed: %+v", fold)
	}

	// The orphan: a tool_result whose tool_use was never journaled.
	for i, e := range entries {
		if e.Type == session.EntryToolRound {
			t.Errorf("entries[%d] is a tool_result round, but the assistant message holding its tool_use failed to append — orphan tool_result", i)
		}
	}
	if hasBlock(fold, provider.BlockToolResult) {
		t.Errorf("fold carries a tool_result block with no matching tool_use — orphan: %+v", fold)
	}

	// The caller must learn about it at the turn boundary.
	if promptErr == nil {
		t.Errorf("Prompt returned nil, want the journal write failure surfaced at the turn boundary")
	} else if !errors.Is(promptErr, errWriteFailed) {
		t.Errorf("Prompt error = %v, want it to wrap errWriteFailed", promptErr)
	}
	_ = closeErr // Close is a backstop; Prompt is the contract under test here.
}

// TestPrompt_JournalFailureDoesNotLeakIntoNextTurn asserts the accumulator is
// reset after a failed assistant append: the turn is dropped whole, so the
// NEXT turn journals cleanly rather than merging the failed turn's text into
// itself. A dropped turn is recoverable (the caller was told); a silently
// merged one is not.
func TestPrompt_JournalFailureDoesNotLeakIntoNextTurn(t *testing.T) {
	r, store := newFailingRunner(t, `"tool_use"`, toolTurnScript(t))
	id := r.ID()

	if err := r.Prompt(context.Background(), "read some.txt"); err == nil {
		t.Fatalf("Prompt returned nil, want the journal write failure")
	}
	if err := r.Close(); err != nil && !errors.Is(err, errWriteFailed) {
		t.Fatalf("Close: %v", err)
	}

	_, fold := journalOf(t, store, id)

	// The follow-up reply must be journaled, and must carry only its own text.
	var assistant []provider.Message
	for _, m := range fold {
		if m.Role == provider.RoleAssistant {
			assistant = append(assistant, m)
		}
	}
	if len(assistant) != 1 {
		t.Fatalf("assistant messages in fold = %d, want 1 (the follow-up reply; the failed turn is dropped): %+v", len(assistant), fold)
	}
	if got := msgText(assistant[0]); got != "here is what it said" {
		t.Errorf("assistant text = %q, want only the follow-up turn's own text (the failed turn's text must not leak in)", got)
	}
}
