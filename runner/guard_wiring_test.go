package runner_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/loop"
	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/runner"
	"github.com/jedwards1230/agent-sdk-go/tool"
)

// denyGuard denies every tool call. It proves runner.Options.Guard is threaded
// into the loop: a denied call must never execute.
type denyGuard struct{}

func (denyGuard) Evaluate(context.Context, loop.ToolCall) loop.Guarding {
	return loop.Guarding{Decision: loop.DecisionDeny, Rule: "test-deny"}
}

// TestRunner_GuardDeniesToolCall is the end-to-end proof that a Guard injected
// via runner.Options reaches the loop through runner.Runner.Prompt: a denying
// guard blocks a bash call over the real runner→loop→guard path, so the tool's
// side-effect never happens and a permission.resolved{deny} is emitted. Without
// the Options.Guard/Approver wiring this test's sentinel file would be created.
func TestRunner_GuardDeniesToolCall(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()
	sentinel := filepath.Join(cwd, "ran.txt")

	cmd, err := json.Marshal(map[string]string{"command": "touch " + sentinel})
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}
	prov := &scriptedProvider{events: [][]provider.StreamEvent{
		{
			{Type: provider.StreamToolCallStart, Tool: &provider.ToolCall{ID: "call-1", Name: "bash"}},
			{Type: provider.StreamToolCallEnd, Tool: &provider.ToolCall{ID: "call-1", Name: "bash", Input: cmd}},
			{Type: provider.StreamFinished, StopReason: provider.StopToolUse, Usage: provider.Usage{InputTokens: 4, OutputTokens: 1}},
		},
		{
			{Type: provider.StreamTextDelta, Text: "blocked"},
			{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{InputTokens: 3, OutputTokens: 1}},
		},
	}}

	r, err := runner.New(context.Background(), runner.Options{
		Root: root, Cwd: cwd, Model: testModel,
		Provider: prov,
		Tools:    loop.FromRegistry(tool.NewRegistry(tool.NewBash(cwd))),
		Guard:    denyGuard{},
		IDGen:    seqIDGen(), Clock: seqClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sub := r.Events()
	if err := r.Prompt(context.Background(), "try to run"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	// The denied tool never executed: its side-effect file is absent.
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("guard denied the call but the bash side-effect ran (sentinel present): %v", err)
	}

	// A permission.resolved{deny} was emitted for the blocked call. Prompt has
	// returned, so every event for the turn is already published to this
	// subscription — drain the buffer without blocking.
	var sawDeny bool
drain:
	for {
		select {
		case e, ok := <-sub.C:
			if !ok {
				break drain
			}
			if pr, is := e.(event.PermissionResolved); is && pr.Verdict == event.VerdictDeny {
				sawDeny = true
				break drain
			}
		default:
			break drain
		}
	}
	if !sawDeny {
		t.Fatal("expected a permission.resolved{deny} event; none seen")
	}
}
