package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/session"
	"github.com/jedwards1230/agent-sdk-go/tool"

	"github.com/jedwards1230/agent-sdk-go/runner"
)

// greetInput is greetTool's input shape.
type greetInput struct {
	Name string `json:"name"`
}

// greetTool is a minimal custom [tool.Tool]: it composes builtins +
// ExtraTools for TestRunner_ExtraToolsComposesWithBuiltins, proving a
// front-door custom tool actually executes (not just registers).
type greetTool struct{}

func (greetTool) Name() string        { return "greet" }
func (greetTool) Description() string { return "greets the named party" }
func (greetTool) Spec() tool.Schema {
	return tool.ObjectSchema([]string{"name"}, map[string]tool.Property{
		"name": {Type: "string"},
	})
}
func (greetTool) Run(_ context.Context, input json.RawMessage) (tool.Result, error) {
	var in greetInput
	if err := json.Unmarshal(input, &in); err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Content: "hello, " + in.Name}, nil
}

// namedTool is a [tool.Tool] stub whose Name is configurable, used to force a
// collision with a builtin.
type namedTool struct{ name string }

func (t namedTool) Name() string      { return t.name }
func (namedTool) Description() string { return "test tool" }
func (namedTool) Spec() tool.Schema   { return tool.Schema{Type: "object"} }
func (namedTool) Run(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.Result{}, nil
}

// TestRunner_ExtraToolsComposesWithBuiltins drives a tool call against a
// custom ExtraTools tool and asserts it actually executes alongside the
// builtin set, proving ExtraTools is additive, not a replacement.
func TestRunner_ExtraToolsComposesWithBuiltins(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()

	toolInput, err := json.Marshal(greetInput{Name: "world"})
	if err != nil {
		t.Fatalf("marshal tool input: %v", err)
	}
	prov := &scriptedProvider{events: [][]provider.StreamEvent{
		{
			{Type: provider.StreamToolCallStart, Tool: &provider.ToolCall{ID: "t1", Name: "greet"}},
			{Type: provider.StreamToolCallEnd, Tool: &provider.ToolCall{ID: "t1", Name: "greet", Input: toolInput}},
			{Type: provider.StreamFinished, StopReason: provider.StopToolUse, Usage: provider.Usage{}},
		},
		{
			{Type: provider.StreamTextDelta, Text: "done"},
			{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{}},
		},
	}}

	r, err := runner.New(context.Background(), runner.Options{
		Root: root, Cwd: cwd, Model: testModel, System: "test system",
		Provider:   prov,
		ExtraTools: []tool.Tool{greetTool{}},
		IDGen:      seqIDGen(),
		Clock:      seqClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := r.Prompt(context.Background(), "say hi"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	fold := r.Fold()
	// [user, assistant(tool_use), user(tool_result), assistant("done")].
	if len(fold) != 4 {
		t.Fatalf("Fold: got %d messages, want 4: %+v", len(fold), fold)
	}
	results := blocksOfType(fold[2], provider.BlockToolResult)
	if len(results) != 1 || results[0].ToolResult != "hello, world" {
		t.Fatalf("tool_result blocks = %+v, want one with content %q", results, "hello, world")
	}
}

// TestRunner_ExtraToolsAndToolsMutuallyExclusive asserts New rejects a
// caller that sets both the full-replacement Tools seam and the additive
// ExtraTools seam, rather than silently picking one.
func TestRunner_ExtraToolsAndToolsMutuallyExclusive(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()

	prov := &scriptedProvider{}
	_, err := runner.New(context.Background(), runner.Options{
		Root: root, Cwd: cwd, Model: testModel,
		Provider:   prov,
		Tools:      oneToolRegistry{},
		ExtraTools: []tool.Tool{greetTool{}},
	})
	if err == nil {
		t.Fatal("New: got nil error, want a mutual-exclusion error")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("New err = %q, want it to mention mutual exclusion", err.Error())
	}
}

// TestRunner_ExtraToolsCollisionWithBuiltin asserts an ExtraTools entry whose
// name collides with a builtin fails registration rather than silently
// overriding (or being shadowed by) the builtin.
func TestRunner_ExtraToolsCollisionWithBuiltin(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()

	prov := &scriptedProvider{}
	_, err := runner.New(context.Background(), runner.Options{
		Root: root, Cwd: cwd, Model: testModel,
		Provider:   prov,
		ExtraTools: []tool.Tool{namedTool{name: "bash"}},
	})
	if err == nil {
		t.Fatal("New: got nil error, want a registration error")
	}
	if !errors.Is(err, tool.ErrDuplicate) {
		t.Errorf("New err = %v, want it to wrap tool.ErrDuplicate", err)
	}
}

// TestRunner_MemStoreResume drives a Prompt against an injected
// [session.MemStore], resumes the same session id against the same store
// instance, and asserts the resumed runner's fold carries the first turn's
// context — resume works within the process even though nothing is written
// to disk (MemStore.Root() stays "").
func TestRunner_MemStoreResume(t *testing.T) {
	cwd := t.TempDir()
	mem := session.NewMemStore(session.WithStoreIDGen(seqIDGen()), session.WithStoreClock(seqClock()))
	defer func() { _ = mem.Close() }()

	prov1 := &scriptedProvider{events: [][]provider.StreamEvent{
		{
			{Type: provider.StreamTextDelta, Text: "hi"},
			{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{}},
		},
	}}
	r1, err := runner.New(context.Background(), runner.Options{
		Cwd: cwd, Model: testModel, System: "test system",
		Provider: prov1, Tools: oneToolRegistry{},
		Store: mem,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id := r1.ID()

	if err := r1.Prompt(context.Background(), "hello"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if err := r1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	prov2 := &scriptedProvider{events: [][]provider.StreamEvent{
		{
			{Type: provider.StreamTextDelta, Text: "again"},
			{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{}},
		},
	}}
	r2, err := runner.Resume(context.Background(), id, runner.Options{
		Cwd: cwd, Model: testModel, System: "test system",
		Provider: prov2, Tools: oneToolRegistry{},
		Store: mem,
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}

	preFold := r2.Fold()
	if len(preFold) != 2 {
		t.Fatalf("preFold: got %d messages, want 2 (the first Prompt's turn): %+v", len(preFold), preFold)
	}

	if err := r2.Prompt(context.Background(), "continue"); err != nil {
		t.Fatalf("Prompt (resumed): %v", err)
	}
	if err := r2.Close(); err != nil {
		t.Fatalf("Close (resumed): %v", err)
	}

	postFold := r2.Fold()
	if len(postFold) != 4 {
		t.Fatalf("postFold: got %d messages, want 4: %+v", len(postFold), postFold)
	}

	if got := mem.Root(); got != "" {
		t.Errorf("MemStore.Root() = %q, want \"\" (nothing persisted to disk)", got)
	}
}
