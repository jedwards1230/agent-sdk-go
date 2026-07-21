package runner_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/loop"
	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/runner"
	"github.com/jedwards1230/agent-sdk-go/tool"
)

// TestRunner_OptionsAgentReachesToolEvents proves the end-to-end wiring:
// Options.Agent forwards through loop.Config.Agent and lands stamped on the
// tool-call events the runner emits, so a consumer can attribute the call.
func TestRunner_OptionsAgentReachesToolEvents(t *testing.T) {
	cmd, err := json.Marshal(map[string]string{"command": "echo hi"})
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
			{Type: provider.StreamTextDelta, Text: "done"},
			{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{InputTokens: 3, OutputTokens: 1}},
		},
	}}

	cwd := t.TempDir()
	r, err := runner.New(context.Background(), runner.Options{
		Root: t.TempDir(), Cwd: cwd, Model: testModel,
		Agent:    "researcher",
		Provider: prov,
		Tools:    loop.FromRegistry(tool.NewRegistry(tool.NewBash(cwd))),
		IDGen:    seqIDGen(), Clock: seqClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = r.Close() }()

	sub := r.Events()
	if err := r.Prompt(context.Background(), "run echo"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	var ev event.ToolCallFinished
	for {
		e, ok := <-sub.C
		if !ok {
			t.Fatal("stream closed before tool.call.finished")
		}
		if tf, is := e.(event.ToolCallFinished); is {
			ev = tf
			break
		}
	}
	if ev.Agent != "researcher" {
		t.Errorf("tool.call.finished Agent = %q, want researcher", ev.Agent)
	}
}
