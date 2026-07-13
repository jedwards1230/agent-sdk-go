package runner_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/loop"
	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/session"

	"github.com/jedwards1230/agent-sdk-go/runner"
)

// streamThenBlockProvider yields a fixed prefix of stream events, then blocks
// the final Next on ctx.Done() and returns ctx.Err() — modeling a model call
// killed mid-stream, specifically while a tool call is still streaming its
// input (announced via tool.call.started, with no end event and no result).
type streamThenBlockProvider struct {
	prefix []provider.StreamEvent
}

func (p *streamThenBlockProvider) Stream(ctx context.Context, _ provider.Request) (provider.StreamHandle, error) {
	return &blockingStream{events: p.prefix, ctx: ctx}, nil
}

func (p *streamThenBlockProvider) Info() provider.ModelInfo {
	return provider.ModelInfo{ID: testModel, Provider: "test"}
}

type blockingStream struct {
	events []provider.StreamEvent
	i      int
	ctx    context.Context
}

func (s *blockingStream) Next() (provider.StreamEvent, error) {
	if s.i < len(s.events) {
		e := s.events[s.i]
		s.i++
		return e, nil
	}
	<-s.ctx.Done()
	return provider.StreamEvent{}, s.ctx.Err()
}

func (s *blockingStream) Close() error { return nil }

// unusedTool satisfies loop.Tool for a registry whose tool is never executed
// (the run is killed before the loop reaches tool execution).
type unusedTool struct{}

func (unusedTool) Run(context.Context, json.RawMessage) (loop.ToolResult, error) {
	return loop.ToolResult{}, nil
}

// erroringTool always fails, so the loop publishes tool.call.finished with
// IsError true — the fixture for TestConsume_ToolCallFinishedIsError.
type erroringTool struct{}

func (erroringTool) Run(context.Context, json.RawMessage) (loop.ToolResult, error) {
	return loop.ToolResult{Content: "boom: file not found", IsError: true}, nil
}

// waitForKind drains sub until an event of kind arrives, failing the test on
// timeout or an early channel close.
func waitForKind(t *testing.T, sub *event.Subscription, kind string) {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case e, ok := <-sub.C:
			if !ok {
				t.Fatalf("subscription closed before observing %q", kind)
			}
			if e.Kind() == kind {
				return
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %q", kind)
		}
	}
}

// TestRunner_KillDuringToolStreaming is the regression for a kill AFTER a
// turn's assistant text/reasoning has settled but BEFORE a just-announced
// tool call streams to completion (started, but no end event and no
// result): the settled text must still be journaled rather than stranded, and
// the accumulator must not wedge for the Runner's life.
func TestRunner_KillDuringToolStreaming(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()

	toolInput, err := json.Marshal(map[string]string{"path": "notes.txt"})
	if err != nil {
		t.Fatalf("marshal tool input: %v", err)
	}
	prov := &streamThenBlockProvider{prefix: []provider.StreamEvent{
		{Type: provider.StreamReasoningDelta, Text: "planning the read"},
		{Type: provider.StreamTextDelta, Text: "I will read the notes."},
		{Type: provider.StreamToolCallStart, Tool: &provider.ToolCall{ID: "t1", Name: "read", Input: toolInput}},
		// No end/finished: the next Next blocks until the test cancels ctx.
	}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r, err := runner.New(context.Background(), runner.Options{
		Root: root, Cwd: cwd, Model: testModel, System: "test system",
		Provider: prov,
		Tools:    oneToolRegistry{name: "read", tool: unusedTool{}},
		IDGen:    seqIDGen(), Clock: seqClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id := r.ID()

	sub := r.Events()
	promptDone := make(chan error, 1)
	go func() { promptDone <- r.Prompt(ctx, "read the notes") }()

	// Kill the moment the tool call is announced — i.e. after the assistant
	// text has settled (message.finished precedes tool.call.started) but before
	// any tool result.
	waitForKind(t, sub, event.KindToolCallStarted)
	cancel()

	if err := <-promptDone; err == nil {
		t.Fatal("Prompt returned nil, want a cancellation error")
	}
	sub.Close()

	// Close drains the journaling goroutine; assertions on disk come after it.
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen from a fresh store to prove durability on disk.
	store, err := session.NewFileStore(session.WithRoot(root))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer func() { _ = store.Close() }()
	j, err := store.Open(context.Background(), id)
	if err != nil {
		t.Fatalf("Open(%s): %v", id, err)
	}

	entries := skipMeta(j.Entries())
	if len(entries) != 2 {
		t.Fatalf("Entries after kill mid tool-streaming: got %d, want 2 (user message + settled assistant message): %+v", len(entries), entries)
	}
	if entries[0].Type != session.EntryMessage {
		t.Fatalf("entries[0].Type = %s, want %s", entries[0].Type, session.EntryMessage)
	}
	if userMsg, err := entries[0].Message(); err != nil || msgText(userMsg) != "read the notes" {
		t.Fatalf("entries[0].Message() = %+v, %v", userMsg, err)
	}
	if entries[1].Type != session.EntryMessage {
		t.Fatalf("entries[1].Type = %s, want %s (the settled assistant text, NOT dropped, and NOT a dangling tool_round)", entries[1].Type, session.EntryMessage)
	}
	asst, err := entries[1].Message()
	if err != nil {
		t.Fatalf("entries[1].Message(): %v", err)
	}
	if asst.Role != provider.RoleAssistant || msgText(asst) != "I will read the notes." {
		t.Errorf("assistant entry = %+v, want role assistant text %q", asst, "I will read the notes.")
	}
	if msgReasoning(asst) != "planning the read" {
		t.Errorf("assistant reasoning = %q, want %q", msgReasoning(asst), "planning the read")
	}
	if uses := blocksOfType(asst, provider.BlockToolUse); len(uses) != 0 {
		t.Errorf("assistant tool_use blocks = %+v, want none (orphaned call dropped, no dangling tool_use)", uses)
	}

	// The fold must round-trip cleanly: two messages, the assistant one carrying
	// the settled text/reasoning and NO orphaned tool_use (a dangling tool_use
	// would corrupt the provider projection on resume).
	fold := j.Fold()
	if len(fold) != 2 {
		t.Fatalf("Fold: got %d, want 2: %+v", len(fold), fold)
	}
	if uses := blocksOfType(fold[1], provider.BlockToolUse); len(uses) != 0 {
		t.Errorf("fold[1] tool_use blocks = %+v, want none (orphaned call dropped)", uses)
	}
	if msgText(fold[1]) != "I will read the notes." {
		t.Errorf("fold[1] text = %q, want the settled assistant text", msgText(fold[1]))
	}
}

// TestConsume_ToolCallFinishedIsError asserts a failed tool call's error flag
// (event.ToolCallFinished.IsError) is journaled verbatim on the tool_result
// block, not silently discarded as a false-positive success.
func TestConsume_ToolCallFinishedIsError(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()

	toolInput, err := json.Marshal(map[string]string{"path": "missing.txt"})
	if err != nil {
		t.Fatalf("marshal tool input: %v", err)
	}
	prov := &scriptedProvider{events: [][]provider.StreamEvent{
		{
			{Type: provider.StreamToolCallStart, Tool: &provider.ToolCall{ID: "t1", Name: "read"}},
			{Type: provider.StreamToolCallEnd, Tool: &provider.ToolCall{ID: "t1", Name: "read", Input: toolInput}},
			{Type: provider.StreamFinished, StopReason: provider.StopToolUse, Usage: provider.Usage{InputTokens: 5, OutputTokens: 2}},
		},
		// The loop calls the provider again after the tool round settles.
		{
			{Type: provider.StreamTextDelta, Text: "the file is missing"},
			{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
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
	id := r.ID()

	if err := r.Prompt(context.Background(), "read missing.txt"); err != nil {
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

	entries := skipMeta(j.Entries())
	if len(entries) != 4 {
		t.Fatalf("Entries: got %d, want 4 (user + assistant tool_use + tool_result round + assistant reply): %+v", len(entries), entries)
	}
	if entries[2].Type != session.EntryToolRound {
		t.Fatalf("entries[2].Type = %s, want %s", entries[2].Type, session.EntryToolRound)
	}
	round, err := entries[2].ToolRound()
	if err != nil {
		t.Fatalf("entries[2].ToolRound(): %v", err)
	}
	if len(round.Blocks) != 1 {
		t.Fatalf("ToolRound.Blocks = %+v, want one tool_result block", round.Blocks)
	}
	res := round.Blocks[0]
	if !res.IsError {
		t.Errorf("tool_result IsError = false, want true (the tool failed)")
	}
	if res.ToolResult != "boom: file not found" {
		t.Errorf("tool_result content = %q, want the tool's error content", res.ToolResult)
	}

	// Fold must project the same IsError flag back into the provider message.
	fold := j.Fold()
	results := blocksOfType(fold[2], provider.BlockToolResult)
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("fold tool_result blocks = %+v, want one with IsError true", results)
	}
}

// TestConsume_ReasoningMetaPreserved asserts a reasoning block's per-block
// Meta (e.g. an Anthropic reasoning signature, carried on
// event.MessageFinished.Meta) round-trips into the journaled reasoning
// content block, so it survives a resume boundary.
func TestConsume_ReasoningMetaPreserved(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()

	prov := &scriptedProvider{events: [][]provider.StreamEvent{
		{
			{Type: provider.StreamReasoningDelta, Text: "let me think this through", Meta: map[string]string{"anthropic.signature": "sig-abc123"}},
			{Type: provider.StreamTextDelta, Text: "here's my answer"},
			{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{InputTokens: 4, OutputTokens: 3}},
		},
	}}

	r, err := runner.New(context.Background(), runner.Options{
		Root: root, Cwd: cwd, Model: testModel, System: "test system",
		Provider: prov,
		Tools:    oneToolRegistry{},
		IDGen:    seqIDGen(), Clock: seqClock(),
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

	entries := skipMeta(j.Entries())
	if len(entries) != 2 {
		t.Fatalf("Entries: got %d, want 2 (user + assistant): %+v", len(entries), entries)
	}
	asst, err := entries[1].Message()
	if err != nil {
		t.Fatalf("entries[1].Message(): %v", err)
	}
	reasoningBlocks := blocksOfType(asst, provider.BlockReasoning)
	if len(reasoningBlocks) != 1 {
		t.Fatalf("reasoning blocks = %+v, want exactly one", reasoningBlocks)
	}
	if got := reasoningBlocks[0].Meta["anthropic.signature"]; got != "sig-abc123" {
		t.Errorf("reasoning block Meta[anthropic.signature] = %q, want %q", got, "sig-abc123")
	}

	// The text block must NOT pick up the reasoning block's Meta.
	textBlocks := blocksOfType(asst, provider.BlockText)
	if len(textBlocks) != 1 || len(textBlocks[0].Meta) != 0 {
		t.Errorf("text blocks = %+v, want one with no Meta", textBlocks)
	}

	// Fold must carry the same Meta forward for a later turn to replay.
	fold := j.Fold()
	foldReasoning := blocksOfType(fold[1], provider.BlockReasoning)
	if len(foldReasoning) != 1 || foldReasoning[0].Meta["anthropic.signature"] != "sig-abc123" {
		t.Fatalf("fold reasoning blocks = %+v, want Meta[anthropic.signature] = sig-abc123", foldReasoning)
	}
}
