package loop_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/loop"
	"github.com/jedwards1230/agent-sdk-go/provider"
)

// stubGuard is a fixed-decision Guard for verdict-routing tests. It also
// implements Granter, recording every call it is asked to remember.
type stubGuard struct {
	decision loop.Decision
	rule     string
	granted  []loop.ToolCall
}

func (g *stubGuard) Evaluate(_ context.Context, _ loop.ToolCall) loop.Guarding {
	return loop.Guarding{Decision: g.decision, Rule: g.rule}
}

func (g *stubGuard) Grant(call loop.ToolCall) { g.granted = append(g.granted, call) }

// stubApprover answers every Await with a fixed reply or error.
type stubApprover struct {
	reply loop.Reply
	err   error
}

func (a stubApprover) Await(_ context.Context, _ string) (loop.Reply, error) {
	return a.reply, a.err
}

func gatedToolConfig(b *event.Broker, tool *fakeTool) loop.Config {
	cfg := baseConfig(b, scripted(
		toolTurn("t1", tool.name, `{"a":1}`),
		textTurn("done", provider.StopEndTurn),
	))
	cfg.Tools = tool
	return cfg
}

// drained is every event drained from a subscription, alongside a Kind()
// tally and the typed permission events for assertions on Verdict/Rule.
type drained struct {
	kinds     []string
	requested []event.PermissionRequested
	resolved  []event.PermissionResolved
}

func drainSub(sub *event.Subscription) drained {
	var d drained
	for {
		select {
		case e, ok := <-sub.C:
			if !ok {
				return d
			}
			d.kinds = append(d.kinds, e.Kind())
			switch ev := e.(type) {
			case event.PermissionRequested:
				d.requested = append(d.requested, ev)
			case event.PermissionResolved:
				d.resolved = append(d.resolved, ev)
			}
		default:
			return d
		}
	}
}

func TestGuardRunContainedRunsToolNoPermissionEvents(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()
	sub := b.Subscribe(event.FilterAll, 256)

	tool := &fakeTool{name: "echo", result: loop.ToolResult{Content: "ok"}}
	cfg := gatedToolConfig(b, tool)
	cfg.Guard = &stubGuard{decision: loop.DecisionRunContained}

	if _, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("go")}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tool.runs != 1 {
		t.Errorf("tool.runs = %d, want 1", tool.runs)
	}
	d := drainSub(sub)
	if countKind(d.kinds, event.KindPermissionRequested) != 0 || countKind(d.kinds, event.KindPermissionResolved) != 0 {
		t.Errorf("run-contained must emit no permission events, got %v", d.kinds)
	}
}

func TestGuardDenyBlocksToolNoRequested(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()
	sub := b.Subscribe(event.FilterAll, 256)

	tool := &fakeTool{name: "echo", result: loop.ToolResult{Content: "ok"}}
	cfg := gatedToolConfig(b, tool)
	cfg.Guard = &stubGuard{decision: loop.DecisionDeny, rule: "deny echo"}

	res, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("go")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tool.runs != 0 {
		t.Errorf("tool.runs = %d, want 0 (denied)", tool.runs)
	}
	trBlock := res.Messages[2].Content[0]
	if !trBlock.IsError {
		t.Errorf("expected an error tool_result for the denied call, got %+v", trBlock)
	}

	d := drainSub(sub)
	if countKind(d.kinds, event.KindPermissionRequested) != 0 {
		t.Errorf("a static deny must not emit permission.requested (no human asked): %v", d.kinds)
	}
	if countKind(d.kinds, event.KindPermissionResolved) != 1 {
		t.Errorf("want exactly 1 permission.resolved, got %v", d.kinds)
	}
	if countKind(d.kinds, event.KindToolCallFinished) != 1 {
		t.Errorf("want exactly 1 tool.call.finished for the blocked call, got %v", d.kinds)
	}
	if len(d.resolved) != 1 || d.resolved[0].Verdict != event.VerdictDeny || d.resolved[0].Rule != "deny echo" {
		t.Errorf("resolved = %+v, want verdict=deny rule=deny echo", d.resolved)
	}
}

func TestGuardAskApprovedRunsTool(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()
	sub := b.Subscribe(event.FilterAll, 256)

	tool := &fakeTool{name: "echo", result: loop.ToolResult{Content: "ok"}}
	cfg := gatedToolConfig(b, tool)
	cfg.Guard = &stubGuard{decision: loop.DecisionAsk, rule: "ask echo"}
	cfg.Approver = stubApprover{reply: loop.Reply{Verdict: event.VerdictAllow}}

	if _, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("go")}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tool.runs != 1 {
		t.Errorf("tool.runs = %d, want 1 (approved)", tool.runs)
	}
	d := drainSub(sub)
	if countKind(d.kinds, event.KindPermissionRequested) != 1 {
		t.Errorf("want exactly 1 permission.requested, got %v", d.kinds)
	}
	if len(d.resolved) != 1 || d.resolved[0].Verdict != event.VerdictAllow {
		t.Errorf("resolved = %+v, want verdict=allow", d.resolved)
	}
}

func TestGuardAskDeniedBlocksTool(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()
	sub := b.Subscribe(event.FilterAll, 256)

	tool := &fakeTool{name: "echo", result: loop.ToolResult{Content: "ok"}}
	cfg := gatedToolConfig(b, tool)
	cfg.Guard = &stubGuard{decision: loop.DecisionAsk, rule: "ask echo"}
	cfg.Approver = stubApprover{reply: loop.Reply{Verdict: event.VerdictDeny}}

	res, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("go")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tool.runs != 0 {
		t.Errorf("tool.runs = %d, want 0 (denied by user)", tool.runs)
	}
	trBlock := res.Messages[2].Content[0]
	if !trBlock.IsError {
		t.Errorf("expected an error tool_result for the denied call, got %+v", trBlock)
	}
	d := drainSub(sub)
	if countKind(d.kinds, event.KindPermissionRequested) != 1 || len(d.resolved) != 1 {
		t.Errorf("want exactly 1 requested + 1 resolved, got %v", d.kinds)
	}
	if len(d.resolved) == 1 && d.resolved[0].Verdict != event.VerdictDeny {
		t.Errorf("resolved.Verdict = %v, want deny", d.resolved[0].Verdict)
	}
}

func TestGuardAskWithNilApproverFailsClosed(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()
	sub := b.Subscribe(event.FilterAll, 256)

	tool := &fakeTool{name: "echo", result: loop.ToolResult{Content: "ok"}}
	cfg := gatedToolConfig(b, tool)
	cfg.Guard = &stubGuard{decision: loop.DecisionAsk, rule: "ask echo"}
	// cfg.Approver left nil.

	if _, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("go")}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tool.runs != 0 {
		t.Errorf("tool.runs = %d, want 0 (no approver ⇒ fail closed)", tool.runs)
	}
	d := drainSub(sub)
	// A request is still emitted (a human would have been asked, in
	// principle) — only the resolution fails closed because nothing can
	// await a reply.
	if countKind(d.kinds, event.KindPermissionRequested) != 1 {
		t.Errorf("want exactly 1 permission.requested, got %v", d.kinds)
	}
	if len(d.resolved) != 1 || d.resolved[0].Verdict != event.VerdictDeny {
		t.Errorf("resolved = %+v, want verdict=deny", d.resolved)
	}
}

func TestGuardAskApproverErrorFailsClosed(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()
	sub := b.Subscribe(event.FilterAll, 256)

	tool := &fakeTool{name: "echo", result: loop.ToolResult{Content: "ok"}}
	cfg := gatedToolConfig(b, tool)
	cfg.Guard = &stubGuard{decision: loop.DecisionAsk, rule: "ask echo"}
	cfg.Approver = stubApprover{err: context.Canceled}

	if _, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("go")}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tool.runs != 0 {
		t.Errorf("tool.runs = %d, want 0 (approver error ⇒ fail closed)", tool.runs)
	}
	d := drainSub(sub)
	if len(d.resolved) != 1 || d.resolved[0].Verdict != event.VerdictDeny {
		t.Errorf("resolved = %+v, want verdict=deny", d.resolved)
	}
}

func TestGuardAskApprovedWithRememberGrants(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()
	defer b.Subscribe(event.FilterAll, 256).Close()

	tool := &fakeTool{name: "echo", result: loop.ToolResult{Content: "ok"}}
	guard := &stubGuard{decision: loop.DecisionAsk, rule: "ask echo"}
	cfg := gatedToolConfig(b, tool)
	cfg.Guard = guard
	cfg.Approver = stubApprover{reply: loop.Reply{Verdict: event.VerdictAllow, Remember: true}}

	if _, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("go")}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(guard.granted) != 1 || guard.granted[0].ID != "t1" {
		t.Errorf("granted = %+v, want exactly the approved call remembered", guard.granted)
	}
}

func TestGuardAskAmendedRunsToolWithReplacementInput(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()
	defer b.Subscribe(event.FilterAll, 256).Close()

	tool := &fakeTool{name: "echo", result: loop.ToolResult{Content: "ok"}}
	cfg := gatedToolConfig(b, tool) // model's original input is {"a":1}
	cfg.Guard = &stubGuard{decision: loop.DecisionAsk, rule: "ask echo"}
	amended := json.RawMessage(`{"a":2}`)
	cfg.Approver = stubApprover{reply: loop.Reply{Verdict: event.VerdictAllow, Input: amended}}

	if _, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("go")}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tool.runs != 1 {
		t.Fatalf("tool.runs = %d, want 1 (amended allow runs the tool)", tool.runs)
	}
	if string(tool.gotIn) != string(amended) {
		t.Errorf("tool input = %s, want the amended input %s", tool.gotIn, amended)
	}
}

func TestGuardAskAmendedRememberGrantsAmendedCall(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()
	defer b.Subscribe(event.FilterAll, 256).Close()

	tool := &fakeTool{name: "echo", result: loop.ToolResult{Content: "ok"}}
	guard := &stubGuard{decision: loop.DecisionAsk, rule: "ask echo"}
	cfg := gatedToolConfig(b, tool)
	cfg.Guard = guard
	amended := json.RawMessage(`{"a":2}`)
	cfg.Approver = stubApprover{reply: loop.Reply{Verdict: event.VerdictAllow, Remember: true, Input: amended}}

	if _, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("go")}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// A remembered amend grants the call the human approved (amended input),
	// not the model's original.
	if len(guard.granted) != 1 {
		t.Fatalf("granted = %+v, want exactly one grant", guard.granted)
	}
	if string(guard.granted[0].Input) != string(amended) {
		t.Errorf("granted input = %s, want the amended input %s", guard.granted[0].Input, amended)
	}
}

func TestGuardNilRunsUncontainedLegacyBehavior(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()
	sub := b.Subscribe(event.FilterAll, 256)

	tool := &fakeTool{name: "echo", result: loop.ToolResult{Content: "ok"}}
	cfg := gatedToolConfig(b, tool) // cfg.Guard left nil

	if _, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("go")}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tool.runs != 1 {
		t.Errorf("tool.runs = %d, want 1 (nil Guard ⇒ uncontained)", tool.runs)
	}
	d := drainSub(sub)
	if countKind(d.kinds, event.KindPermissionRequested) != 0 || countKind(d.kinds, event.KindPermissionResolved) != 0 {
		t.Errorf("nil Guard must emit zero permission events, got %v", d.kinds)
	}
}
