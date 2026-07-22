package runner_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/runner"
)

// recordingEffortProvider is the effort-axis analogue of recordingModelProvider:
// a scripted provider that records req.Params.Thinking on every Stream call, in
// order — proof of exactly which reasoning config a turn actually ran with.
// Every call scripts the same trivial one-shot text turn; only the recorded
// thinking config matters to the SetEffort tests.
type recordingEffortProvider struct {
	mu       sync.Mutex
	thinking []provider.Thinking
}

func (p *recordingEffortProvider) Stream(_ context.Context, req provider.Request) (provider.StreamHandle, error) {
	p.mu.Lock()
	p.thinking = append(p.thinking, req.Params.Thinking)
	p.mu.Unlock()
	return provider.SliceStream(
		provider.StreamEvent{Type: provider.StreamTextDelta, Text: "hi"},
		provider.StreamEvent{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
	), nil
}

func (p *recordingEffortProvider) Info() provider.ModelInfo {
	return provider.ModelInfo{ID: "claude-sonnet-5", Provider: "anthropic"}
}

// lastThinking returns the most recently recorded req.Params.Thinking, or the
// zero value if Stream was never called.
func (p *recordingEffortProvider) lastThinking() provider.Thinking {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.thinking) == 0 {
		return provider.Thinking{}
	}
	return p.thinking[len(p.thinking)-1]
}

// last returns the most recently recorded effort level, or "" if Stream was
// never called.
func (p *recordingEffortProvider) last() string { return p.lastThinking().Effort }

func newEffortRunner(t *testing.T, prov provider.Provider, params provider.Params) *runner.Runner {
	t.Helper()
	r, err := runner.New(context.Background(), runner.Options{
		Root: t.TempDir(), Cwd: t.TempDir(), Model: "claude-sonnet-5",
		Provider: prov,
		Params:   params,
		Tools:    oneToolRegistry{},
		IDGen:    seqIDGen(), Clock: seqClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// TestRunner_SetEffort_AppliesToNextPrompt is the core proof: SetEffort to a
// valid level, then Prompt, and the provider must observe the NEW effort on
// that turn's req.Params.Thinking.Effort.
func TestRunner_SetEffort_AppliesToNextPrompt(t *testing.T) {
	prov := &recordingEffortProvider{}
	r := newEffortRunner(t, prov, provider.Params{})

	if err := r.SetEffort(provider.EffortHigh); err != nil {
		t.Fatalf("SetEffort: %v", err)
	}
	if err := r.Prompt(context.Background(), "hello"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	if got, want := prov.last(), provider.EffortHigh; got != want {
		t.Fatalf("req effort = %q, want %q (SetEffort did not apply to the next turn)", got, want)
	}
}

// TestRunner_SetEffort_SeedsFromOptionsThenClears proves two things: the
// construction-time Params.Thinking.Effort seeds the runner's effort (so the
// first turn runs with it), and SetEffort("") clears it back to the provider
// default (empty) for subsequent turns.
func TestRunner_SetEffort_SeedsFromOptionsThenClears(t *testing.T) {
	prov := &recordingEffortProvider{}
	r := newEffortRunner(t, prov, provider.Params{
		Thinking: provider.Thinking{Enabled: true, Effort: provider.EffortMedium},
	})

	if err := r.Prompt(context.Background(), "first"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got, want := prov.last(), provider.EffortMedium; got != want {
		t.Fatalf("req effort = %q, want %q (construction-time effort did not seed the runner)", got, want)
	}

	if err := r.SetEffort(""); err != nil {
		t.Fatalf("SetEffort(\"\"): %v", err)
	}
	if err := r.Prompt(context.Background(), "second"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got := prov.last(); got != "" {
		t.Fatalf("req effort = %q, want \"\" (clearing effort did not restore the provider default)", got)
	}
}

// TestRunner_SetEffort_UnknownRejected asserts an effort outside the unified
// vocabulary is rejected, names the offending value, and leaves the runner's
// effort unchanged — a subsequent Prompt still runs on the prior level.
func TestRunner_SetEffort_UnknownRejected(t *testing.T) {
	prov := &recordingEffortProvider{}
	r := newEffortRunner(t, prov, provider.Params{})

	if err := r.SetEffort(provider.EffortLow); err != nil {
		t.Fatalf("SetEffort(low): %v", err)
	}

	err := r.SetEffort("ultra")
	if err == nil {
		t.Fatal("SetEffort(\"ultra\"): got nil error, want an unknown-effort rejection")
	}
	if !strings.Contains(err.Error(), "ultra") {
		t.Errorf("SetEffort err = %q, want it to name the offending value", err.Error())
	}

	if err := r.Prompt(context.Background(), "hello"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got, want := prov.last(), provider.EffortLow; got != want {
		t.Fatalf("req effort = %q, want %q (rejected SetEffort must not change the effort)", got, want)
	}
}

// TestRunner_SetEffort_UnregisteredModelAllowed is the capability-permissiveness
// parallel to TestRunner_SetModel_UnregisteredSameProviderAccepted: after
// switching to a model the registry does not carry, SetEffort must still be
// accepted (an unregistered model's reasoning support is UNKNOWN, not "no"), and
// the next turn must actually run with that effort.
func TestRunner_SetEffort_UnregisteredModelAllowed(t *testing.T) {
	const unregistered = "claude-opus-9-1"
	if _, ok := provider.Lookup(unregistered); ok {
		t.Fatalf("test premise broken: %q is registered", unregistered)
	}

	prov := &recordingEffortProvider{}
	r := newEffortRunner(t, prov, provider.Params{})

	if err := r.SetModel(unregistered); err != nil {
		t.Fatalf("SetModel(%q): %v", unregistered, err)
	}
	if err := r.SetEffort(provider.EffortHigh); err != nil {
		t.Fatalf("SetEffort on unregistered model = %v, want it accepted (reasoning support is UNKNOWN, not denied)", err)
	}
	if err := r.Prompt(context.Background(), "hello"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got, want := prov.last(), provider.EffortHigh; got != want {
		t.Fatalf("req effort = %q, want %q", got, want)
	}
}

// TestRunner_SetEffort_ConcurrentWithPrompt drives SetEffort concurrently with
// a Prompt turn. Like its SetModel sibling it makes no claim about which turn
// first observes the change — it exists to prove the field access is race-free
// under `go test -race`, the point of routing every r.effort read through the
// locked currentEffort accessor.
func TestRunner_SetEffort_ConcurrentWithPrompt(t *testing.T) {
	prov := &recordingEffortProvider{}
	r := newEffortRunner(t, prov, provider.Params{})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := r.Prompt(context.Background(), "hello"); err != nil {
			t.Errorf("Prompt: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		levels := []string{provider.EffortLow, provider.EffortMedium, provider.EffortHigh, ""}
		for i := 0; i < 50; i++ {
			effort := levels[i%len(levels)]
			if err := r.SetEffort(effort); err != nil {
				t.Errorf("SetEffort(%q): %v", effort, err)
			}
		}
	}()
	wg.Wait()
}

// TestRunner_SetEffort_ReachesProviderAsActive is the issue #88 regression at
// the runner seam, and the link that joins this file's proofs to the adapter
// ones. TestRunner_SetEffort_AppliesToNextPrompt already proved the LEVEL rides
// the request; it passed all the way through v0.17.0 while the feature was
// dead, because the adapters key on Thinking.Active(), not on Effort. So assert
// the property they actually read: after a bare SetEffort on a runner built
// with a zero-value Params — the state of every embedder that never constructs
// provider.Params — the request the provider receives must report Active.
//
// provider.TestThinkingActive pins Active's rule, and the adapter tests
// (anthropic.TestBuildBodyThinkingEffortEnables,
// openai.TestReasoningEffortEnables) pin that an Active-by-effort config emits
// reasoning config on each wire. Together the three cover runner -> API.
func TestRunner_SetEffort_ReachesProviderAsActive(t *testing.T) {
	prov := &recordingEffortProvider{}
	r := newEffortRunner(t, prov, provider.Params{})

	if err := r.SetEffort(provider.EffortHigh); err != nil {
		t.Fatalf("SetEffort: %v", err)
	}
	if err := r.Prompt(context.Background(), "hello"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	got := prov.lastThinking()
	if !got.Active() {
		t.Fatalf("req Thinking = %+v, Active() = false; the adapters gate reasoning on Active, so this turn ran with reasoning OFF despite SetEffort", got)
	}
	if got.Effort != provider.EffortHigh {
		t.Errorf("req effort = %q, want %q", got.Effort, provider.EffortHigh)
	}
}

// TestRunner_SetEffort_ClearedIsInactive is the must-fire twin: with no effort
// ever set and a zero-value construction Params, the request must NOT report
// Active — otherwise the assertion above would hold for a runner that turned
// reasoning on unconditionally, and would prove nothing. It also pins that
// clearing the level actually turns reasoning back off for such a runner.
func TestRunner_SetEffort_ClearedIsInactive(t *testing.T) {
	prov := &recordingEffortProvider{}
	r := newEffortRunner(t, prov, provider.Params{})

	if err := r.Prompt(context.Background(), "before any SetEffort"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got := prov.lastThinking(); got.Active() {
		t.Fatalf("req Thinking = %+v, want Active() false before any SetEffort", got)
	}

	if err := r.SetEffort(provider.EffortHigh); err != nil {
		t.Fatalf("SetEffort: %v", err)
	}
	if err := r.SetEffort(""); err != nil {
		t.Fatalf("SetEffort(\"\"): %v", err)
	}
	if err := r.Prompt(context.Background(), "after clearing"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got := prov.lastThinking(); got.Active() {
		t.Fatalf("req Thinking = %+v, want Active() false after clearing the level", got)
	}
}

// TestRunner_SetEffort_PreservesConstructionThinking is the backward-compat
// guard for embedders that already configure reasoning themselves: a runner
// built with an explicit Enabled + BudgetTokens must carry BOTH through onto
// every turn untouched. It also pins that clearing the level leaves their
// Enabled standing — the level is the only thing SetEffort owns.
//
// Scope, precisely: this covers the runner seam, and an explicit BudgetTokens
// is the combination that is also wire-stable, because it outranks any level
// downstream. It does NOT establish that the fix changes no embedder's wire.
// One combination does change — Enabled with NO BudgetTokens plus a level now
// sends that level's budget on Anthropic instead of the floor — which is the
// documented, intended behavior change (see provider.Thinking).
func TestRunner_SetEffort_PreservesConstructionThinking(t *testing.T) {
	prov := &recordingEffortProvider{}
	r := newEffortRunner(t, prov, provider.Params{
		Thinking: provider.Thinking{Enabled: true, BudgetTokens: 8000},
	})

	if err := r.SetEffort(provider.EffortLow); err != nil {
		t.Fatalf("SetEffort: %v", err)
	}
	if err := r.Prompt(context.Background(), "hello"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	got := prov.lastThinking()
	if !got.Enabled || got.BudgetTokens != 8000 {
		t.Errorf("req Thinking = %+v, want Enabled and BudgetTokens 8000 preserved", got)
	}
	if got.Effort != provider.EffortLow {
		t.Errorf("req effort = %q, want %q overlaid", got.Effort, provider.EffortLow)
	}

	if err := r.SetEffort(""); err != nil {
		t.Fatalf("SetEffort(\"\"): %v", err)
	}
	if err := r.Prompt(context.Background(), "second"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got := prov.lastThinking(); !got.Enabled || got.BudgetTokens != 8000 {
		t.Errorf("req Thinking = %+v, want the construction-time config to survive clearing the level", got)
	}
}
