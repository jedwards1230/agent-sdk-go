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
		Messages: []provider.Message{{Role: "user", Content: text}},
	}
	stream, err := s.provider.Stream(ctx, req)
	if err != nil {
		s.broker.Publish(event.NewSessionError(s.id, err.Error(), true))
		s.broker.Publish(event.NewTurnFinished(s.id, "error", provider.Usage{}))
		return err
	}
	defer func() { _ = stream.Close() }()

	var (
		open  bool
		kind  event.MessageKind
		buf   strings.Builder
		usage provider.Usage
		stop  string
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
		case provider.StreamReasoning:
			delta(event.MessageReasoning, se.Text)
		case provider.StreamText:
			delta(event.MessageText, se.Text)
		case provider.StreamUsage:
			usage = se.Usage
		case provider.StreamStop:
			stop = se.StopReason
		case provider.StreamToolCall:
			// Tool calls are typed but not exercised at M0.
		}
	}

	finish()
	s.broker.Publish(event.NewTurnFinished(s.id, stop, usage))
	return nil
}
