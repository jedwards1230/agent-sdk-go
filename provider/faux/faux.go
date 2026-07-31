// Package faux provides a deterministic, scripted [provider.Provider] for tests
// and demos. It emits a fixed sequence of stream events with zero randomness
// and zero dependence on wall-clock time, so a session driven by it produces
// byte-identical output every run.
package faux

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/jedwards1230/agent-sdk-go/provider"
)

// Script is a sequence of turns the provider replays, one per Stream call.
type Script struct {
	Turns []Turn
}

// Turn scripts a single model turn: either a successful turn — reasoning
// chunks, then text chunks, then the turn's usage and stop reason — or, when
// Err is set, a pre-stream failure instead.
type Turn struct {
	Reasoning  []string
	Text       []string
	Usage      provider.Usage
	StopReason provider.StopReason
	// Err, when non-nil, makes this turn fail from Stream before any event is
	// emitted, exactly as a provider that rejects a request pre-stream does.
	// It is the PRE-STREAM failure path only: a mid-stream error is not
	// scriptable, and the turn's other fields are ignored when it is set.
	//
	// It lets a test script a classified failure — say
	// provider.ErrContextOverflow, wrapped or bare — and assert on how the
	// caller reacts. The turn is still consumed, so a following turn replays
	// normally: turn 1 fails with an overflow, turn 2 succeeds, which is what
	// makes an end-to-end compact-and-retry test possible.
	Err error
}

// Default returns the canonical script used by the demo and the golden-file
// tests: one turn that reasons briefly, then greets the user.
func Default() Script {
	return Script{Turns: []Turn{{
		Reasoning:  []string{"The user said hello. ", "I'll greet them back."},
		Text:       []string{"Hello", "! How can ", "I help you today?"},
		Usage:      provider.Usage{InputTokens: 9, OutputTokens: 7},
		StopReason: provider.StopEndTurn,
	}}}
}

// info is the synthetic model metadata reported by the faux provider. It is
// unpriced — faux is not a real model.
var info = provider.ModelInfo{
	ID:            "faux",
	Provider:      "faux",
	ContextWindow: 200_000,
	MaxOutput:     8192,
	Reasoning:     true,
}

// Provider is a scripted provider. Each call to Stream consumes the next turn
// of the script. It is safe for concurrent use.
type Provider struct {
	mu     sync.Mutex
	script Script
	turn   int
}

// New returns a provider that replays s.
func New(s Script) *Provider { return &Provider{script: s} }

// Info returns the faux provider's synthetic model metadata.
func (p *Provider) Info() provider.ModelInfo { return info }

// Stream returns the next scripted turn as a normalized stream, or that turn's
// [Turn.Err] when it scripts a pre-stream failure. It errors once the script is
// exhausted. The request is ignored — output is fully scripted.
func (p *Provider) Stream(_ context.Context, _ provider.Request) (provider.StreamHandle, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.turn >= len(p.script.Turns) {
		return nil, fmt.Errorf("faux: script exhausted after %d turn(s)", len(p.script.Turns))
	}
	t := p.script.Turns[p.turn]
	// Consume the turn before reporting its failure, so the next Stream call
	// advances to the following turn — a scripted failure is one turn, not a
	// permanent wedge.
	p.turn++
	if t.Err != nil {
		return nil, t.Err
	}
	return newStream(t), nil
}

// stream replays a single turn's events in order.
type stream struct {
	events []provider.StreamEvent
	i      int
}

func newStream(t Turn) *stream {
	events := make([]provider.StreamEvent, 0, len(t.Reasoning)+len(t.Text)+1)
	for _, r := range t.Reasoning {
		events = append(events, provider.StreamEvent{Type: provider.StreamReasoningDelta, Text: r})
	}
	for _, x := range t.Text {
		events = append(events, provider.StreamEvent{Type: provider.StreamTextDelta, Text: x})
	}
	events = append(events, provider.StreamEvent{
		Type:       provider.StreamFinished,
		StopReason: t.StopReason,
		Usage:      t.Usage,
	})
	return &stream{events: events}
}

// Next returns the next scripted event, or io.EOF when the turn is exhausted.
func (s *stream) Next() (provider.StreamEvent, error) {
	if s.i >= len(s.events) {
		return provider.StreamEvent{}, io.EOF
	}
	e := s.events[s.i]
	s.i++
	return e, nil
}

// Close is a no-op; a scripted stream holds no resources.
func (s *stream) Close() error { return nil }
