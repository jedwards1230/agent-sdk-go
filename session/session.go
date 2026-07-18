// Package session provides session identity and single-turn execution. A
// [Session] binds a provider to an [event.Broker], emits session.created on
// construction, and translates a provider's normalized stream into the typed
// event contract when Prompt runs a turn.
//
// Determinism seams — an injectable id generator and clock — let tests produce
// byte-identical event streams; the defaults use UUIDv7 and the wall clock.
package session

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/provider"
)

// defaultSubBuffer is the channel buffer for subscriptions created via
// [Session.Subscribe]. It is ample for a single interactive turn.
const defaultSubBuffer = 256

// defaultReplay is how many must-deliver events the session's broker retains so
// a subscriber attaching after construction still receives session.created.
const defaultReplay = 256

// Session is one agent conversation. It owns a broker and a bound provider and
// runs turns via Prompt. A Session is safe for one turn at a time; concurrent
// Prompt calls are not supported at M0.
type Session struct {
	id        string
	model     string
	provider  provider.Provider
	broker    *event.Broker
	subBuffer int

	// mu guards title, the session's only mutable metadata. SetTitle may be
	// called from a goroutine other than the one driving Prompt, so the field
	// is synchronized independently of the single-turn Prompt guarantee.
	mu    sync.Mutex
	title string
}

// config holds constructor options.
type config struct {
	idGen     func() string
	clock     func() time.Time
	model     string
	subBuffer int
	replay    int
}

// Option configures a [Session] at construction.
type Option func(*config)

// WithIDGen overrides the session id generator. A nil generator is ignored.
func WithIDGen(f func() string) Option {
	return func(c *config) {
		if f != nil {
			c.idGen = f
		}
	}
}

// WithClock overrides the clock used to timestamp events. A nil clock is
// ignored. Tests inject a fixed clock for deterministic output.
func WithClock(f func() time.Time) Option {
	return func(c *config) {
		if f != nil {
			c.clock = f
		}
	}
}

// WithModel sets the model bound to the session and passed to the provider.
func WithModel(model string) Option {
	return func(c *config) { c.model = model }
}

// New constructs a session bound to p and emits session.created. Options tune
// identity, clock, and model.
func New(p provider.Provider, opts ...Option) *Session {
	cfg := config{
		idGen:     newV7,
		clock:     time.Now,
		subBuffer: defaultSubBuffer,
		replay:    defaultReplay,
	}
	for _, o := range opts {
		o(&cfg)
	}

	s := &Session{
		id:        cfg.idGen(),
		model:     cfg.model,
		provider:  p,
		broker:    event.NewBroker(event.WithClock(cfg.clock), event.WithReplay(cfg.replay)),
		subBuffer: cfg.subBuffer,
	}
	s.broker.Publish(event.NewSessionCreated(s.id))
	return s
}

// newV7 returns a UUIDv7 string, falling back to UUIDv4 if the platform clock
// read fails.
func newV7() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}

// ID returns the session's globally unique, time-ordered identifier.
func (s *Session) ID() string { return s.id }

// Model returns the model bound to the session.
func (s *Session) Model() string { return s.model }

// Title returns the session's current human-readable title, or "" if none has
// been set. The title is embedder-supplied metadata (see [Session.SetTitle]);
// the SDK never generates it.
func (s *Session) Title() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.title
}

// SetTitle updates the session's human-readable title and, when the value
// actually changes, publishes a must-deliver session.info event so subscribers
// (e.g. an ACP client) observe the new title live. Setting the title to its
// current value is a no-op: no spurious event is emitted, and a session whose
// title is never set emits none at all.
//
// Title generation is application business logic. An embedder derives a title
// from the first user prompt, an LLM summary, or a user rename and calls
// SetTitle; the SDK only carries the value and broadcasts the change.
func (s *Session) SetTitle(title string) {
	s.mu.Lock()
	if title == s.title {
		s.mu.Unlock()
		return
	}
	s.title = title
	s.mu.Unlock()
	// Publish outside the lock: a must-deliver publish can block on
	// backpressure, and holding s.mu across it would serialize Title() readers
	// against that blocking.
	s.broker.Publish(event.NewSessionInfoUpdated(s.id, title))
}

// Subscribe returns a subscription for events matching filter. session.created
// and other retained must-deliver events are replayed to late subscribers.
func (s *Session) Subscribe(filter event.Filter) *event.Subscription {
	return s.broker.Subscribe(filter, s.subBuffer)
}

// Events returns a subscription to every event of the session, of both tiers.
func (s *Session) Events() *event.Subscription {
	return s.Subscribe(event.FilterAll)
}

// Close shuts down the session's broker, closing all subscriptions.
func (s *Session) Close() { s.broker.Close() }

// Prompt runs exactly one turn for text: it emits turn.started, streams the
// provider's reasoning and text as message.started/.delta/.finished pairs
// (each .finished carrying the full accumulated content), and closes with
// turn.finished carrying the stop reason and usage. It returns the provider's
// error, if any, after emitting session.error and a terminal turn.finished.
func (s *Session) Prompt(ctx context.Context, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.broker.Publish(event.NewTurnStarted(s.id))

	req := provider.Request{
		Model:    s.model,
		Messages: []provider.Message{provider.UserText(text)},
	}
	stream, err := s.provider.Stream(ctx, req)
	if err != nil {
		s.broker.Publish(event.NewSessionError(s.id, err.Error(), true))
		s.broker.Publish(event.NewTurnFinished(s.id, "error", provider.Usage{}))
		return err
	}
	defer func() { _ = stream.Close() }()

	var (
		open     bool
		kind     event.MessageKind
		buf      strings.Builder
		usage    provider.Usage
		stop     string
		finished bool
	)
	finish := func() {
		if open {
			s.broker.Publish(event.NewMessageFinished(s.id, kind, buf.String()))
			buf.Reset()
			open = false
		}
	}
	// ensure opens a message of kind k, closing any different open message.
	ensure := func(k event.MessageKind) {
		if open && kind == k {
			return
		}
		finish()
		kind = k
		open = true
		s.broker.Publish(event.NewMessageStarted(s.id, kind))
	}
	delta := func(k event.MessageKind, chunk string) {
		ensure(k)
		buf.WriteString(chunk)
		s.broker.Publish(event.NewMessageDelta(s.id, k, chunk))
	}

	for {
		if err := ctx.Err(); err != nil {
			finish()
			s.broker.Publish(event.NewSessionError(s.id, err.Error(), true))
			s.broker.Publish(event.NewTurnFinished(s.id, "cancelled", usage))
			return err
		}

		se, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			finish()
			s.broker.Publish(event.NewSessionError(s.id, err.Error(), true))
			s.broker.Publish(event.NewTurnFinished(s.id, "error", usage))
			return err
		}

		switch se.Type {
		case provider.StreamReasoningDelta:
			delta(event.MessageReasoning, se.Text)
		case provider.StreamTextDelta:
			delta(event.MessageText, se.Text)
		case provider.StreamFinished:
			usage = se.Usage
			stop = string(se.StopReason)
			finished = true
		case provider.StreamToolCallStart, provider.StreamToolCallDelta, provider.StreamToolCallEnd:
			// Tool calls are typed but not exercised by the M0 session path;
			// the M1 loop package drives tool execution.
		}
	}

	finish()
	// Fail closed if the provider ended the stream without a terminal
	// StreamFinished event (e.g. a dropped connection surfacing as a bare
	// io.EOF): surface a non-fatal session.error and mark the turn as errored
	// rather than silently reporting a clean turn with an empty stop reason
	// and zero usage. Mirrors loop.go's callModel guard.
	if !finished {
		s.broker.Publish(event.NewSessionError(s.id, "provider stream ended without a finished event", false))
		stop = string(provider.StopError)
	}
	tf := event.NewTurnFinished(s.id, stop, usage)
	if info, ok := provider.Lookup(s.model); ok {
		tf.ContextWindow = info.ContextWindow
	}
	s.broker.Publish(tf)
	return nil
}
