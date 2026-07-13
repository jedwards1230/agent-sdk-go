package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/loop"
	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/session"
	"github.com/jedwards1230/agent-sdk-go/tool"

	"github.com/jedwards1230/agent-sdk-go/runner"
)

const testModel = "test-model"

// scriptedProvider is a deterministic, hermetic provider.Provider: each call
// to Stream consumes the next scripted event sequence, in order. It never
// touches the network — the canonical fake for a hermetic loop.Run drive.
type scriptedProvider struct {
	calls  int
	events [][]provider.StreamEvent
}

func (p *scriptedProvider) Stream(_ context.Context, _ provider.Request) (provider.StreamHandle, error) {
	if p.calls >= len(p.events) {
		return nil, fmt.Errorf("scriptedProvider: unexpected call %d (scripted for %d)", p.calls+1, len(p.events))
	}
	evs := p.events[p.calls]
	p.calls++
	return provider.SliceStream(evs...), nil
}

func (p *scriptedProvider) Info() provider.ModelInfo {
	return provider.ModelInfo{ID: testModel, Provider: "test"}
}

// cancelAfterRead wraps a real *tool.Read: it runs the real tool (so the
// journaled result is genuine file content, not a fixture), then cancels the
// run synchronously — deterministically, in the same goroutine that drives
// loop.Run, before the tool round's result is even published — the moment
// the round settles, proving a kill mid-run only ever loses unsettled work.
type cancelAfterRead struct {
	read   *tool.Read
	cancel context.CancelFunc
	fired  atomic.Bool
}

func (c *cancelAfterRead) Run(ctx context.Context, input json.RawMessage) (loop.ToolResult, error) {
	res, err := c.read.Run(ctx, input)
	if err != nil {
		return loop.ToolResult{}, err
	}
	if !c.fired.Swap(true) {
		c.cancel()
	}
	return loop.ToolResult{Content: res.Content, IsError: res.IsError}, nil
}

// oneToolRegistry is a minimal loop.ToolRegistry offering a single named
// tool — enough to drive the loop without pulling in the full builtin set.
type oneToolRegistry struct {
	name string
	tool loop.Tool
}

func (r oneToolRegistry) Get(name string) (loop.Tool, bool) {
	if name != r.name {
		return nil, false
	}
	return r.tool, true
}

func (r oneToolRegistry) Specs() []provider.ToolSpec {
	return []provider.ToolSpec{{Name: r.name, Description: "test tool"}}
}

// seqClock and seqIDGen give tests deterministic, monotonic journal
// timestamps and ids without depending on wall-clock ordering.
func seqClock() func() time.Time {
	t := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return func() time.Time {
		t = t.Add(time.Second)
		return t
	}
}

func seqIDGen() func() string {
	n := 0
	return func() string {
		n++
		return fmt.Sprintf("id-%04d", n)
	}
}

// TestRunner_TextTurn drives a single plain (no tool call) turn and asserts
// the user prompt and the settled assistant reply both land as message
// entries, and that Fold projects them back losslessly.
func TestRunner_TextTurn(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()

	prov := &scriptedProvider{events: [][]provider.StreamEvent{
		{
			{Type: provider.StreamReasoningDelta, Text: "thinking"},
			{Type: provider.StreamTextDelta, Text: "hi "},
			{Type: provider.StreamTextDelta, Text: "there"},
			{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{InputTokens: 3, OutputTokens: 2}},
		},
	}}

	r, err := runner.New(context.Background(), runner.Options{
		Root: root, Cwd: cwd, Model: testModel, System: "test system",
		Provider: prov,
		Tools:    oneToolRegistry{}, // no tools needed; Get always misses
		IDGen:    seqIDGen(),
		Clock:    seqClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id := r.ID()

	if err := r.Prompt(context.Background(), "hello"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	// Journaling streams into the journal on its own goroutine as the turn
	// settles; Close is the documented synchronization point that waits for
	// it to drain, so assertions on journaled content must come after it.
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	fold := r.Fold()
	if len(fold) != 2 {
		t.Fatalf("Fold: got %d messages, want 2: %+v", len(fold), fold)
	}
	if fold[0].Role != provider.RoleUser || msgText(fold[0]) != "hello" {
		t.Errorf("fold[0] = %+v, want user %q", fold[0], "hello")
	}
	if fold[1].Role != provider.RoleAssistant || msgText(fold[1]) != "hi there" {
		t.Errorf("fold[1] = %+v, want assistant %q", fold[1], "hi there")
	}
	if msgReasoning(fold[1]) != "thinking" {
		t.Errorf("fold[1] reasoning = %q, want %q", msgReasoning(fold[1]), "thinking")
	}

	// Reopen from a fresh store (no in-process cache) to prove the turn is
	// durable on disk, not just held in memory.
	store, err := session.NewFileStore(session.WithRoot(root))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer func() { _ = store.Close() }()
	j, err := store.Open(context.Background(), id)
	if err != nil {
		t.Fatalf("Open(%s): %v", id, err)
	}
	rawEntries := j.Entries()
	if len(rawEntries) != 3 {
		t.Fatalf("raw Entries: got %d, want 3 (meta + user + assistant): %+v", len(rawEntries), rawEntries)
	}
	if rawEntries[0].Type != session.EntryMeta {
		t.Errorf("rawEntries[0].Type = %s, want %s", rawEntries[0].Type, session.EntryMeta)
	}
	if metaPayload, err := rawEntries[0].Meta(); err != nil || metaPayload.Cwd != cwd {
		t.Errorf("rawEntries[0].Meta() = %+v, %v, want Cwd = %q", metaPayload, err, cwd)
	}

	entries := skipMeta(rawEntries)
	if len(entries) != 2 {
		t.Fatalf("Entries: got %d, want 2: %+v", len(entries), entries)
	}
	if entries[0].Type != session.EntryMessage || entries[1].Type != session.EntryMessage {
		t.Errorf("entry types = %s, %s, want message, message", entries[0].Type, entries[1].Type)
	}
}

// TestRunner_PromptEmitsUserMessageBeforeTurnStarted asserts Prompt publishes
// the user's own turn (MessageStarted{MessageUser} then
// MessageFinished{MessageUser, text}) onto the event stream BEFORE the loop's
// TurnStarted, so a live observer (TUI, attached ACP client) renders the
// user's prompt ahead of the agent's reply. It also asserts the user-message
// events do NOT get journaled as a separate entry (consume only journals
// MessageFinished{MessageText/MessageReasoning}) — the journal still holds
// exactly the user message appended by session.NewMessageEntry, not a
// duplicate.
func TestRunner_PromptEmitsUserMessageBeforeTurnStarted(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()

	prov := &scriptedProvider{events: [][]provider.StreamEvent{
		{
			{Type: provider.StreamTextDelta, Text: "hi"},
			{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{}},
		},
	}}

	r, err := runner.New(context.Background(), runner.Options{
		Root: root, Cwd: cwd, Model: testModel, System: "test system",
		Provider: prov,
		Tools:    oneToolRegistry{},
		IDGen:    seqIDGen(),
		Clock:    seqClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sub := r.EventsLive()
	defer sub.Close()

	if err := r.Prompt(context.Background(), "hello there"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	// Collect events up to and including TurnFinished, so the assertions
	// below don't hang waiting past the settled turn.
	var kinds []string
	var msgStarted event.MessageStarted
	var msgFinished event.MessageFinished
collect:
	for {
		select {
		case e := <-sub.C:
			kinds = append(kinds, e.Kind())
			switch ev := e.(type) {
			case event.MessageStarted:
				if ev.MessageKind == event.MessageUser {
					msgStarted = ev
				}
			case event.MessageFinished:
				if ev.MessageKind == event.MessageUser {
					msgFinished = ev
				}
			}
			if e.Kind() == event.KindTurnFinished {
				break collect
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for turn.finished; collected so far: %v", kinds)
		}
	}

	if msgStarted.MessageKind != event.MessageUser {
		t.Fatalf("no MessageStarted{MessageUser} observed; kinds = %v", kinds)
	}
	if msgFinished.MessageKind != event.MessageUser || msgFinished.Content != "hello there" {
		t.Fatalf("MessageFinished{MessageUser} = %+v, want content %q", msgFinished, "hello there")
	}

	// Ordering: message.started, message.finished (both MessageUser) precede
	// turn.started.
	wantPrefix := []string{event.KindMessageStarted, event.KindMessageFinished, event.KindTurnStarted}
	if len(kinds) < len(wantPrefix) {
		t.Fatalf("kinds = %v, want at least %d events", kinds, len(wantPrefix))
	}
	for i, want := range wantPrefix {
		if kinds[i] != want {
			t.Fatalf("kinds[%d] = %s, want %s (full sequence: %v)", i, kinds[i], want, kinds)
		}
	}

	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The journal holds exactly the user + assistant message entries — the
	// user-message events above are NOT separately journaled.
	fold := r.Fold()
	if len(fold) != 2 {
		t.Fatalf("Fold: got %d messages, want 2 (no duplicate user entry): %+v", len(fold), fold)
	}
	if fold[0].Role != provider.RoleUser || msgText(fold[0]) != "hello there" {
		t.Errorf("fold[0] = %+v, want user %q", fold[0], "hello there")
	}
}

// TestRunner_EventsLiveSkipsRetainedBacklog asserts EventsLive omits the
// broker's retained must-deliver backlog that Events replays. It is the
// SDK-level guard against a new-turn driver mistaking a PRIOR turn's retained
// terminal event for its own turn finishing: Prompt's barrier guarantees the
// turn's events are published (and journaled) before it returns, so the
// backlog is populated when both subscriptions are opened below.
func TestRunner_EventsLiveSkipsRetainedBacklog(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()

	prov := &scriptedProvider{events: [][]provider.StreamEvent{
		{
			{Type: provider.StreamTextDelta, Text: "hi"},
			{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{}},
		},
	}}

	r, err := runner.New(context.Background(), runner.Options{
		Root: root, Cwd: cwd, Model: testModel, System: "test system",
		Provider: prov,
		Tools:    oneToolRegistry{},
		IDGen:    seqIDGen(),
		Clock:    seqClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = r.Close() }()

	if err := r.Prompt(context.Background(), "hello"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	// Events replays the retained backlog: at least one must-deliver event
	// from the settled turn is waiting on the fresh subscription.
	replaySub := r.Events()
	select {
	case _, ok := <-replaySub.C:
		if !ok {
			t.Fatal("Events subscription closed unexpectedly")
		}
	case <-time.After(time.Second):
		t.Fatal("Events did not replay the retained backlog")
	}
	replaySub.Close()

	// EventsLive does not: a subscription opened at the same point sees an
	// empty channel (non-blocking read finds nothing to replay).
	liveSub := r.EventsLive()
	select {
	case e := <-liveSub.C:
		t.Fatalf("EventsLive replayed a retained event: %s", e.Kind())
	default:
	}
	liveSub.Close()
}

// TestRunner_KillAndResume shows a tool actually executes, that the journal
// is durable at the moment a run is killed mid-flight (a settled tool round
// survives cancellation), and that Resume folds that prior context back into
// the provider's messages and continues the conversation.
func TestRunner_KillAndResume(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()
	notesPath := filepath.Join(cwd, "notes.txt")
	if err := os.WriteFile(notesPath, []byte("hello world"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	// --- Phase 1: run until the tool round settles, then kill. ---

	toolInput, err := json.Marshal(map[string]string{"path": "notes.txt"})
	if err != nil {
		t.Fatalf("marshal tool input: %v", err)
	}
	prov1 := &scriptedProvider{events: [][]provider.StreamEvent{
		{
			{Type: provider.StreamToolCallStart, Tool: &provider.ToolCall{ID: "t1", Name: "read"}},
			{Type: provider.StreamToolCallEnd, Tool: &provider.ToolCall{ID: "t1", Name: "read", Input: toolInput}},
			{Type: provider.StreamFinished, StopReason: provider.StopToolUse, Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}},
		},
		// A second call is scripted defensively; the cancellation below must
		// pre-empt loop.Run before it is ever reached.
		{
			{Type: provider.StreamTextDelta, Text: "should not run"},
			{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
	}}

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	tools1 := oneToolRegistry{name: "read", tool: &cancelAfterRead{read: tool.NewRead(cwd), cancel: cancel1}}

	r1, err := runner.New(context.Background(), runner.Options{
		Root: root, Cwd: cwd, Model: testModel, System: "test system",
		Provider: prov1, Tools: tools1,
		IDGen: seqIDGen(), Clock: seqClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id := r1.ID()

	promptErr := r1.Prompt(ctx1, "read notes.txt")
	if !errors.Is(promptErr, context.Canceled) {
		t.Fatalf("Prompt: got %v, want context.Canceled", promptErr)
	}
	if prov1.calls != 1 {
		t.Fatalf("scriptedProvider: got %d calls, want exactly 1 (the second iteration must not run)", prov1.calls)
	}

	// Close waits for the journaling goroutine to drain — required before any
	// assertion on-disk, since journaling happens on its own goroutine.
	if err := r1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen from a brand-new store (bypassing any in-process cache) to prove
	// the settled prefix is durable, not merely resident in r1's memory.
	verifyStore, err := session.NewFileStore(session.WithRoot(root))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	verifyJournal, err := verifyStore.Open(context.Background(), id)
	if err != nil {
		t.Fatalf("Open(%s): %v", id, err)
	}
	entries := skipMeta(verifyJournal.Entries())
	// The assistant message (carrying the tool_use block) and the tool_result
	// round are distinct entries, so a settled tool turn is user message +
	// assistant(tool_use) + tool_round(tool_result).
	if len(entries) != 3 {
		t.Fatalf("Entries after kill: got %d, want 3 (user + assistant tool_use + tool_result round): %+v", len(entries), entries)
	}
	if entries[0].Type != session.EntryMessage {
		t.Fatalf("entries[0].Type = %s, want %s", entries[0].Type, session.EntryMessage)
	}
	if msg, err := entries[0].Message(); err != nil || msgText(msg) != "read notes.txt" {
		t.Fatalf("entries[0].Message() = %+v, %v", msg, err)
	}
	if entries[1].Type != session.EntryMessage {
		t.Fatalf("entries[1].Type = %s, want %s (assistant tool_use)", entries[1].Type, session.EntryMessage)
	}
	asst, err := entries[1].Message()
	if err != nil {
		t.Fatalf("entries[1].Message(): %v", err)
	}
	uses := blocksOfType(asst, provider.BlockToolUse)
	if len(uses) != 1 || uses[0].ToolUseID != "t1" || uses[0].ToolName != "read" {
		t.Fatalf("assistant tool_use blocks = %+v, want one (t1, read)", uses)
	}
	if entries[2].Type != session.EntryToolRound {
		t.Fatalf("entries[2].Type = %s, want %s", entries[2].Type, session.EntryToolRound)
	}
	round, err := entries[2].ToolRound()
	if err != nil {
		t.Fatalf("entries[2].ToolRound(): %v", err)
	}
	if len(round.Blocks) != 1 || round.Blocks[0].Type != provider.BlockToolResult {
		t.Fatalf("ToolRound.Blocks = %+v, want one tool_result block", round.Blocks)
	}
	res := round.Blocks[0]
	if res.ToolUseID != "t1" {
		t.Errorf("tool_result ToolUseID = %q, want t1", res.ToolUseID)
	}
	if !strings.Contains(res.ToolResult, "hello world") {
		t.Errorf("tool_result = %q, want it to contain the real file contents %q", res.ToolResult, "hello world")
	}
	if err := verifyStore.Close(); err != nil {
		t.Fatalf("verifyStore.Close: %v", err)
	}

	// --- Phase 2: resume the same session id and continue. ---

	prov2 := &scriptedProvider{events: [][]provider.StreamEvent{
		{
			{Type: provider.StreamTextDelta, Text: "done"},
			{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{InputTokens: 20, OutputTokens: 3}},
		},
	}}

	r2, err := runner.Resume(context.Background(), id, runner.Options{
		Root: root, Cwd: cwd, Model: testModel, System: "test system",
		Provider: prov2, Tools: oneToolRegistry{},
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}

	// The resumed runner's fold must already carry the prior tool result —
	// proof the fold/project round-trip preserves it across a process
	// boundary (a fresh store reopened it from disk in Resume itself).
	preFold := r2.Fold()
	// [user, assistant(tool_use), user(tool_result)] — the tool exchange folds
	// back as three provider messages across a fresh process.
	if len(preFold) != 3 {
		t.Fatalf("preFold: got %d messages, want 3: %+v", len(preFold), preFold)
	}
	results := blocksOfType(preFold[2], provider.BlockToolResult)
	if len(results) != 1 || !strings.Contains(results[0].ToolResult, "hello world") {
		t.Fatalf("preFold tool_result blocks = %+v, want the prior read result", results)
	}
	if uses := blocksOfType(preFold[1], provider.BlockToolUse); len(uses) != 1 {
		t.Fatalf("preFold[1] tool_use blocks = %+v, want one (matches the tool_result)", uses)
	}

	// session.resumed is must-deliver, so the broker's replay buffer hands it
	// to this subscription immediately even though it was published before
	// Events was called.
	sub := r2.Events()
	select {
	case e, ok := <-sub.C:
		if !ok {
			t.Fatal("subscription closed before observing session.resumed")
		}
		if _, ok := e.(event.SessionResumed); !ok {
			t.Fatalf("first replayed event = %T, want event.SessionResumed", e)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for session.resumed")
	}
	sub.Close()

	if err := r2.Prompt(context.Background(), "continue"); err != nil {
		t.Fatalf("Prompt (resumed): %v", err)
	}

	// As above: wait for the journaling goroutine to drain before reading the
	// continuation's settled output.
	if err := r2.Close(); err != nil {
		t.Fatalf("Close (resumed): %v", err)
	}

	postFold := r2.Fold()
	// Prior 3 + the "continue" user message + the "done" assistant reply.
	if len(postFold) != 5 {
		t.Fatalf("postFold: got %d messages, want 5: %+v", len(postFold), postFold)
	}
	last := postFold[len(postFold)-1]
	if last.Role != provider.RoleAssistant || msgText(last) != "done" {
		t.Fatalf("postFold last = %+v, want assistant %q", last, "done")
	}

	finalStore, err := session.NewFileStore(session.WithRoot(root))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer func() { _ = finalStore.Close() }()
	finalJournal, err := finalStore.Open(context.Background(), id)
	if err != nil {
		t.Fatalf("Open(%s): %v", id, err)
	}
	if got := len(skipMeta(finalJournal.Entries())); got != 5 {
		t.Fatalf("final Entries: got %d, want 5 (the journal grew with the continuation)", got)
	}
}

// TestRunner_MetaEntryPersistsCwd asserts New writes an [session.EntryMeta]
// entry carrying opts.Cwd as the journal's very first (root) entry, that it
// survives a Close + reopen (proving durability across e.g. a daemon
// restart), and that Resume does NOT append a second one.
func TestRunner_MetaEntryPersistsCwd(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()

	prov := &scriptedProvider{events: [][]provider.StreamEvent{
		{
			{Type: provider.StreamTextDelta, Text: "hi"},
			{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{}},
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
	path := r.JournalPath()

	rawEntries := r.Fold() // sanity: no prompt yet, empty context
	if len(rawEntries) != 0 {
		t.Fatalf("Fold before any Prompt = %+v, want empty", rawEntries)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen via ReadEntries (no live journal, disk-only enumeration) to prove
	// the meta entry is durable without resuming.
	onDisk, err := session.ReadEntries(path)
	if err != nil {
		t.Fatalf("ReadEntries: %v", err)
	}
	if len(onDisk) != 1 {
		t.Fatalf("ReadEntries = %+v, want exactly 1 entry (the meta root)", onDisk)
	}
	if onDisk[0].Type != session.EntryMeta {
		t.Fatalf("onDisk[0].Type = %s, want %s", onDisk[0].Type, session.EntryMeta)
	}
	meta, err := onDisk[0].Meta()
	if err != nil {
		t.Fatalf("onDisk[0].Meta(): %v", err)
	}
	if meta.Cwd != cwd {
		t.Errorf("onDisk[0].Meta().Cwd = %q, want %q", meta.Cwd, cwd)
	}

	// Resume must NOT append a second meta entry.
	r2, err := runner.Resume(context.Background(), id, runner.Options{
		Root: root, Cwd: cwd, Model: testModel, System: "test system",
		Provider: prov, Tools: oneToolRegistry{},
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := r2.Close(); err != nil {
		t.Fatalf("Close (resumed): %v", err)
	}

	afterResume, err := session.ReadEntries(path)
	if err != nil {
		t.Fatalf("ReadEntries (after resume): %v", err)
	}
	metaCount := 0
	for _, e := range afterResume {
		if e.Type == session.EntryMeta {
			metaCount++
		}
	}
	if metaCount != 1 {
		t.Fatalf("meta entry count after create-then-resume = %d, want exactly 1: %+v", metaCount, afterResume)
	}
	if afterResume[0].Type != session.EntryMeta {
		t.Fatalf("afterResume[0].Type = %s, want %s (meta stays the root)", afterResume[0].Type, session.EntryMeta)
	}
}

// TestNew_MissingCredentialLeavesNoJournal is the pre-flight regression: a
// run whose provider has no configured credential must fail BEFORE any
// session journal is created, so a misconfiguration leaves no orphan .jsonl
// on disk. A credential that resolves but is rejected live is a different
// case (a real errored session that does journal) and is not exercised here.
func TestNew_MissingCredentialLeavesNoJournal(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	root := t.TempDir()
	cwd := t.TempDir()

	// No Provider injected → the real credential pre-flight runs.
	_, err := runner.New(context.Background(), runner.Options{
		Root: root, Cwd: cwd, Model: "claude-sonnet-5", System: "test system",
	})
	if err == nil {
		t.Fatal("New: got nil error, want a missing-credential error")
	}
	if !errors.Is(err, runner.ErrNoCredential) {
		t.Fatalf("New err = %v, want runner.ErrNoCredential", err)
	}

	matches, _ := filepath.Glob(filepath.Join(root, "sessions", "*", "*.jsonl"))
	if len(matches) != 0 {
		t.Errorf("found %d journal file(s) after a failed pre-flight, want 0 (no orphan): %v", len(matches), matches)
	}
}
