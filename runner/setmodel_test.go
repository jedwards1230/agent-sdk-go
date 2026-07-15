package runner_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/runner"
)

// recordingModelProvider is a scripted provider.Provider (modeled on
// scriptedProvider) that additionally records req.Model on every Stream
// call, in order — proof of exactly which model id a turn actually ran
// with. Every call scripts the same trivial one-shot text turn; only the
// recorded model id matters to the SetModel tests.
type recordingModelProvider struct {
	mu     sync.Mutex
	models []string
}

func (p *recordingModelProvider) Stream(_ context.Context, req provider.Request) (provider.StreamHandle, error) {
	p.mu.Lock()
	p.models = append(p.models, req.Model)
	p.mu.Unlock()
	return provider.SliceStream(
		provider.StreamEvent{Type: provider.StreamTextDelta, Text: "hi"},
		provider.StreamEvent{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
	), nil
}

func (p *recordingModelProvider) Info() provider.ModelInfo {
	return provider.ModelInfo{ID: "claude-sonnet-5", Provider: "anthropic"}
}

// last returns the most recently recorded req.Model, or "" if Stream was
// never called.
func (p *recordingModelProvider) last() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.models) == 0 {
		return ""
	}
	return p.models[len(p.models)-1]
}

// TestRunner_SetModel_AppliesToNextPrompt is the core proof: SetModel to a
// same-provider model, then Prompt, and the provider must observe the NEW
// model on that turn's req.Model.
func TestRunner_SetModel_AppliesToNextPrompt(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()

	prov := &recordingModelProvider{}
	r, err := runner.New(context.Background(), runner.Options{
		Root: root, Cwd: cwd, Model: "claude-sonnet-5",
		Provider: prov,
		Tools:    oneToolRegistry{},
		IDGen:    seqIDGen(), Clock: seqClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = r.Close() }()

	if err := r.SetModel("claude-opus-4-8"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	if err := r.Prompt(context.Background(), "hello"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	if got, want := prov.last(), "claude-opus-4-8"; got != want {
		t.Fatalf("req.Model = %q, want %q (SetModel did not apply to the next turn)", got, want)
	}
}

// TestRunner_SetModel_UnknownModelRejected asserts an unregistered model id
// is rejected and leaves the runner's model unchanged — a subsequent Prompt
// still runs on the original model.
func TestRunner_SetModel_UnknownModelRejected(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()

	prov := &recordingModelProvider{}
	r, err := runner.New(context.Background(), runner.Options{
		Root: root, Cwd: cwd, Model: "claude-sonnet-5",
		Provider: prov,
		Tools:    oneToolRegistry{},
		IDGen:    seqIDGen(), Clock: seqClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = r.Close() }()

	err = r.SetModel("not-a-real-model")
	if err == nil {
		t.Fatal("SetModel: got nil error, want an unknown-model error")
	}
	if !strings.Contains(err.Error(), "unknown model") {
		t.Errorf("SetModel err = %q, want it to mention %q", err.Error(), "unknown model")
	}

	if err := r.Prompt(context.Background(), "hello"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got, want := prov.last(), "claude-sonnet-5"; got != want {
		t.Fatalf("req.Model = %q, want %q (rejected SetModel must not change the model)", got, want)
	}
}

// TestRunner_SetModel_CrossProviderRejected asserts SetModel refuses a model
// from a different provider family than the runner's current model, and
// leaves the runner's model unchanged.
func TestRunner_SetModel_CrossProviderRejected(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()

	prov := &recordingModelProvider{}
	r, err := runner.New(context.Background(), runner.Options{
		Root: root, Cwd: cwd, Model: "claude-sonnet-5",
		Provider: prov,
		Tools:    oneToolRegistry{},
		IDGen:    seqIDGen(), Clock: seqClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = r.Close() }()

	err = r.SetModel("gpt-5")
	if err == nil {
		t.Fatal("SetModel: got nil error, want a cross-provider rejection")
	}
	if !strings.Contains(err.Error(), "different provider") {
		t.Errorf("SetModel err = %q, want it to mention %q", err.Error(), "different provider")
	}

	if err := r.Prompt(context.Background(), "hello"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got, want := prov.last(), "claude-sonnet-5"; got != want {
		t.Fatalf("req.Model = %q, want %q (rejected SetModel must not change the model)", got, want)
	}
}

// TestRunner_SetModel_ConcurrentWithPrompt drives SetModel concurrently with
// a Prompt turn. It makes no claim about which turn first observes the
// change (that ordering is documented as unspecified) — it exists to prove
// the field access is race-free under `go test -race`, the point of routing
// every r.model read through the locked currentModel accessor.
func TestRunner_SetModel_ConcurrentWithPrompt(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()

	prov := &recordingModelProvider{}
	r, err := runner.New(context.Background(), runner.Options{
		Root: root, Cwd: cwd, Model: "claude-sonnet-5",
		Provider: prov,
		Tools:    oneToolRegistry{},
		IDGen:    seqIDGen(), Clock: seqClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = r.Close() }()

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
		for i := 0; i < 50; i++ {
			// Alternate between the two same-provider models so this stays a
			// same-provider swap (never rejected) throughout the run.
			model := "claude-opus-4-8"
			if i%2 == 0 {
				model = "claude-sonnet-5"
			}
			if err := r.SetModel(model); err != nil {
				t.Errorf("SetModel(%q): %v", model, err)
			}
		}
	}()
	wg.Wait()
}
