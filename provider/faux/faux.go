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

// Turn scripts a single model turn: reasoning chunks, then text chunks, then
// the turn's usage and stop reason.
type Turn struct {
	Reasoning  []string
	Text       []string
	Usage      provider.Usage
	StopReason string
}

// Default returns the canonical script used by the demo and the golden-file
// tests: one turn that reasons briefly, then greets the user.
func Default() Script {
	return Script{Turns: []Turn{{
		Reasoning:  []string{"The user said hello. ", "I'll greet them back."},
		Text:       []string{"Hello", "! How can ", "I help you today?"},
		Usage:      provider.Usage{InputTokens: 9, OutputTokens: 7},
		StopReason: "end_turn",
	}}}
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

// Stream returns the next scripted turn as a normalized stream. It errors once
// the script is exhausted. The request is ignored — output is fully scripted.
func (p *Provider) Stream(_ context.Context, _ provider.Request) (provider.Stream, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.turn >= len(p.script.Turns) {
		return nil, fmt.Errorf("faux: script exhausted after %d turn(s)", len(p.script.Turns))
	}
	t := p.script.Turns[p.turn]
	p.turn++
	return newStream(t), nil
}

// stream replays a single turn's events in order.
type stream struct {
	events []provider.StreamEvent
	i      int
}

func newStream(t Turn) *stream {
	events := make([]provider.StreamEvent, 0, len(t.Reasoning)+len(t.Text)+2)
	for _, r := range t.Reasoning {
		events = append(events, provider.StreamEvent{Type: provider.StreamReasoning, Text: r})
	}
	for _, x := range t.Text {
		events = append(events, provider.StreamEvent{Type: provider.StreamText, Text: x})
	}
	events = append(events,
		provider.StreamEvent{Type: provider.StreamUsage, Usage: t.Usage},
		provider.StreamEvent{Type: provider.StreamStop, StopReason: t.StopReason},
	)
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
