package runner_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/loop"
	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/runner"
)

// ctxKey is an unexported context-key type (never a basic type, per
// staticcheck SA1029) used to plant a sentinel value on the context passed to
// runner.Runner.Prompt.
type ctxKey struct{}

var sentinelKey ctxKey

// ctxRecorder observes ctx.Value(sentinelKey) at each of the four downstream
// seams a tracing embedder would instrument, proving the context handed to
// Prompt threads through loop.Run into the provider call, the guard, the
// approver, and the tool's execution without being dropped or replaced.
type ctxRecorder struct {
	mu               sync.Mutex
	provider         any
	guard            any
	approver         any
	tool             any
	providerObserved bool
	guardObserved    bool
	approverObserved bool
	toolObserved     bool
}

func (r *ctxRecorder) recordProvider(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.provider = ctx.Value(sentinelKey)
	r.providerObserved = true
}

func (r *ctxRecorder) recordGuard(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.guard = ctx.Value(sentinelKey)
	r.guardObserved = true
}

func (r *ctxRecorder) recordApprover(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.approver = ctx.Value(sentinelKey)
	r.approverObserved = true
}

func (r *ctxRecorder) recordTool(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tool = ctx.Value(sentinelKey)
	r.toolObserved = true
}

// ctxObservingProvider is a scripted provider.Provider (modeled on
// scriptedProvider) that additionally records ctx.Value(sentinelKey) on every
// Stream call, proving the context reaches the provider seam. It scripts
// exactly two calls: the first drives a "probe" tool_use to StopToolUse, the
// second finishes the turn with plain text after the tool result folds back
// in.
type ctxObservingProvider struct {
	rec   *ctxRecorder
	calls int
}

func (p *ctxObservingProvider) Stream(ctx context.Context, _ provider.Request) (provider.StreamHandle, error) {
	p.rec.recordProvider(ctx)
	p.calls++
	switch p.calls {
	case 1:
		return provider.SliceStream(
			provider.StreamEvent{Type: provider.StreamToolCallStart, Tool: &provider.ToolCall{ID: "call-1", Name: "probe"}},
			provider.StreamEvent{Type: provider.StreamToolCallEnd, Tool: &provider.ToolCall{ID: "call-1", Name: "probe", Input: json.RawMessage(`{}`)}},
			provider.StreamEvent{Type: provider.StreamFinished, StopReason: provider.StopToolUse, Usage: provider.Usage{InputTokens: 4, OutputTokens: 1}},
		), nil
	default:
		return provider.SliceStream(
			provider.StreamEvent{Type: provider.StreamTextDelta, Text: "done"},
			provider.StreamEvent{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{InputTokens: 3, OutputTokens: 1}},
		), nil
	}
}

func (p *ctxObservingProvider) Info() provider.ModelInfo {
	return provider.ModelInfo{ID: testModel, Provider: "test"}
}

// ctxObservingGuard records ctx.Value(sentinelKey) in Evaluate and always
// returns DecisionAsk, so the run must reach the Approver seam next.
type ctxObservingGuard struct {
	rec *ctxRecorder
}

func (g ctxObservingGuard) Evaluate(ctx context.Context, _ loop.ToolCall) loop.Guarding {
	g.rec.recordGuard(ctx)
	return loop.Guarding{Decision: loop.DecisionAsk, Rule: "test-ask"}
}

// ctxObservingApprover records ctx.Value(sentinelKey) in Await and allows the
// call, so the run must reach the tool's Run seam next.
type ctxObservingApprover struct {
	rec *ctxRecorder
}

func (a ctxObservingApprover) Await(ctx context.Context, _ string) (loop.Reply, error) {
	a.rec.recordApprover(ctx)
	return loop.Reply{Verdict: event.VerdictAllow}, nil
}

// ctxObservingTool records ctx.Value(sentinelKey) in Run — the final
// downstream seam a tracing embedder would instrument.
type ctxObservingTool struct {
	rec *ctxRecorder
}

func (t ctxObservingTool) Run(ctx context.Context, _ json.RawMessage) (loop.ToolResult, error) {
	t.rec.recordTool(ctx)
	return loop.ToolResult{Content: "ok"}, nil
}

// TestRunner_ContextPropagatesToAllSeams plants a sentinel value on the
// context passed to Runner.Prompt and asserts that same value is observed,
// unmodified, at every downstream seam an embedder would instrument for
// tracing: the provider's Stream call, the Guard's Evaluate, the Approver's
// Await, and the tool's Run. This proves ctx threads unbroken through
// runner.Prompt -> loop.Run -> provider call -> guard -> approver -> tool
// execution — the seam an OTel span (or any other context-borne
// instrumentation) would ride.
func TestRunner_ContextPropagatesToAllSeams(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()

	rec := &ctxRecorder{}
	prov := &ctxObservingProvider{rec: rec}
	probe := ctxObservingTool{rec: rec}
	guard := ctxObservingGuard{rec: rec}
	approver := ctxObservingApprover{rec: rec}

	r, err := runner.New(context.Background(), runner.Options{
		Root: root, Cwd: cwd, Model: testModel,
		Provider: prov,
		Tools:    oneToolRegistry{name: "probe", tool: probe},
		Guard:    guard,
		Approver: approver,
		IDGen:    seqIDGen(), Clock: seqClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = r.Close() }()

	ctx := context.WithValue(context.Background(), sentinelKey, "SENTINEL")
	if err := r.Prompt(ctx, "go"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()

	if !rec.providerObserved {
		t.Fatal("ctx did not reach the provider seam: Stream was never called")
	}
	if rec.provider != "SENTINEL" {
		t.Errorf("ctx dropped at the provider seam: got %#v, want %q", rec.provider, "SENTINEL")
	}
	if !rec.guardObserved {
		t.Fatal("ctx did not reach the guard seam: Evaluate was never called")
	}
	if rec.guard != "SENTINEL" {
		t.Errorf("ctx dropped at the guard seam: got %#v, want %q", rec.guard, "SENTINEL")
	}
	if !rec.approverObserved {
		t.Fatal("ctx did not reach the approver seam: Await was never called")
	}
	if rec.approver != "SENTINEL" {
		t.Errorf("ctx dropped at the approver seam: got %#v, want %q", rec.approver, "SENTINEL")
	}
	if !rec.toolObserved {
		t.Fatal("ctx did not reach the tool seam: Run was never called")
	}
	if rec.tool != "SENTINEL" {
		t.Errorf("ctx dropped at the tool seam: got %#v, want %q", rec.tool, "SENTINEL")
	}
}
