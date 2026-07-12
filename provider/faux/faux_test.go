package faux_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/provider/faux"
)

// collect drains a stream into a slice of events.
func collect(t *testing.T, s provider.StreamHandle) []provider.StreamEvent {
	t.Helper()
	var out []provider.StreamEvent
	for {
		e, err := s.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		out = append(out, e)
	}
}

// TestDefaultScriptOrder asserts the default script streams reasoning, then
// text, then a terminal finished event carrying the stop reason and usage.
func TestDefaultScriptOrder(t *testing.T) {
	p := faux.New(faux.Default())
	s, err := p.Stream(context.Background(), provider.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer func() { _ = s.Close() }()

	got := collect(t, s)
	wantTypes := []provider.StreamEventType{
		provider.StreamReasoningDelta, provider.StreamReasoningDelta,
		provider.StreamTextDelta, provider.StreamTextDelta, provider.StreamTextDelta,
		provider.StreamFinished,
	}
	if len(got) != len(wantTypes) {
		t.Fatalf("got %d events, want %d", len(got), len(wantTypes))
	}
	for i, e := range got {
		if e.Type != wantTypes[i] {
			t.Errorf("event %d type = %d, want %d", i, e.Type, wantTypes[i])
		}
	}
	last := got[len(got)-1]
	if last.StopReason != provider.StopEndTurn {
		t.Errorf("stop reason = %q, want %q", last.StopReason, provider.StopEndTurn)
	}
	if !last.Usage.Equal(provider.Usage{InputTokens: 9, OutputTokens: 7}) {
		t.Errorf("usage = %+v, want {9, 7}", last.Usage)
	}
}

// TestInfo asserts the faux provider reports synthetic model metadata.
func TestInfo(t *testing.T) {
	if got := faux.New(faux.Default()).Info().Provider; got != "faux" {
		t.Errorf("Info().Provider = %q, want faux", got)
	}
}

// TestScriptExhausted asserts a second turn against a one-turn script errors.
func TestScriptExhausted(t *testing.T) {
	p := faux.New(faux.Script{Turns: []faux.Turn{{Text: []string{"hi"}, StopReason: provider.StopEndTurn}}})
	if _, err := p.Stream(context.Background(), provider.Request{}); err != nil {
		t.Fatalf("first Stream: %v", err)
	}
	if _, err := p.Stream(context.Background(), provider.Request{}); err == nil {
		t.Fatal("second Stream returned nil error, want script-exhausted error")
	}
}
