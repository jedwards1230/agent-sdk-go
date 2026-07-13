package event

import (
	"sync"
	"sync/atomic"
	"time"
)

// defaultBlockBound is how long a must-deliver publish blocks on a full
// subscriber buffer before force-unsubscribing the wedged subscriber.
const defaultBlockBound = 5 * time.Second

// Broker fans session events out to subscribers with two-tier delivery. It
// assigns each event a per-session sequence number and publish timestamp,
// retains a bounded backlog of must-deliver events for replay to late
// subscribers, and is safe for concurrent use.
//
// Delivery is serialized: Publish holds the broker's lock while assigning seq
// and fanning out, so subscribers observe events in seq order. A must-deliver
// send blocks up to the block bound; a subscriber still wedged past it is
// force-unsubscribed (its channel closed, [Subscription.Forced] set) rather
// than hanging the broker. Lossy sends never block — a full buffer drops the
// event and increments the subscriber's drop counter.
type Broker struct {
	mu        sync.Mutex
	now       func() time.Time
	blockFor  time.Duration
	replayCap int
	seqs      map[string]uint64
	replay    []Event
	subs      map[*Subscription]struct{}
	closed    bool
}

// BrokerOption configures a [Broker].
type BrokerOption func(*Broker)

// WithClock sets the clock used to timestamp events. Tests inject a fixed clock
// for deterministic output. A nil clock is ignored.
func WithClock(now func() time.Time) BrokerOption {
	return func(b *Broker) {
		if now != nil {
			b.now = now
		}
	}
}

// WithBlockBound sets how long a must-deliver publish blocks on a full buffer
// before force-unsubscribing the subscriber. Non-positive values are ignored.
func WithBlockBound(d time.Duration) BrokerOption {
	return func(b *Broker) {
		if d > 0 {
			b.blockFor = d
		}
	}
}

// WithReplay retains the last n must-deliver events and replays them, in seq
// order, to each new subscriber so a client that attaches mid-session still
// receives the lifecycle and terminal events it missed. Lossy deltas are never
// retained. n <= 0 disables replay.
func WithReplay(n int) BrokerOption {
	return func(b *Broker) {
		if n > 0 {
			b.replayCap = n
		}
	}
}

// NewBroker returns a broker configured by opts.
func NewBroker(opts ...BrokerOption) *Broker {
	b := &Broker{
		now:      time.Now,
		blockFor: defaultBlockBound,
		seqs:     make(map[string]uint64),
		subs:     make(map[*Subscription]struct{}),
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Publish assigns e a per-session seq and timestamp and delivers it to every
// matching subscriber. Publishing to a closed broker is a no-op.
func (b *Broker) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}

	sid := e.SessionID()
	b.seqs[sid]++
	e = e.withMeta(b.seqs[sid], b.now())

	if b.replayCap > 0 && e.Tier() == TierMustDeliver {
		b.replay = append(b.replay, e)
		if len(b.replay) > b.replayCap {
			b.replay = b.replay[len(b.replay)-b.replayCap:]
		}
	}

	for sub := range b.subs {
		if sub.filter.accepts(e.Tier()) {
			b.deliver(sub, e)
		}
	}
}

// deliver sends e to one subscriber under b.mu. Lossy events drop on a full
// buffer; must-deliver events block up to the bound then force-unsubscribe.
func (b *Broker) deliver(sub *Subscription, e Event) {
	if e.Tier() == TierLossy {
		select {
		case sub.ch <- e:
		default:
			sub.dropped.Add(1)
		}
		return
	}

	select {
	case sub.ch <- e:
		return
	default:
	}

	timer := time.NewTimer(b.blockFor)
	defer timer.Stop()
	select {
	case sub.ch <- e:
	case <-timer.C:
		delete(b.subs, sub)
		sub.forced.Store(true)
		sub.closeOnce()
	}
}

// Subscribe returns a subscription for events matching filter, buffered to hold
// buffer events. Retained must-deliver events (see [WithReplay]) are replayed
// into the new subscription in seq order before live delivery begins — the
// behavior a client attaching mid-session wants, so it recovers the lifecycle
// and terminal events it missed.
func (b *Broker) Subscribe(filter Filter, buffer int) *Subscription {
	return b.subscribe(filter, buffer, true)
}

// SubscribeLive is [Broker.Subscribe] without backlog replay: the subscription
// receives only events published after it is created, never the retained
// must-deliver backlog. Use it when the caller wants "live events from now"
// rather than a mid-session attach — e.g. a driver that subscribes, dispatches
// one new turn, and waits for that turn's terminal event. With plain Subscribe
// such a driver would immediately observe a PRIOR turn's retained
// terminal event and mistake it for its own turn's completion.
func (b *Broker) SubscribeLive(filter Filter, buffer int) *Subscription {
	return b.subscribe(filter, buffer, false)
}

// subscribe is the shared implementation of [Broker.Subscribe] (replay=true)
// and [Broker.SubscribeLive] (replay=false).
func (b *Broker) subscribe(filter Filter, buffer int, replay bool) *Subscription {
	if buffer < 0 {
		buffer = 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	var pending []Event
	if replay {
		for _, e := range b.replay {
			if filter.accepts(e.Tier()) {
				pending = append(pending, e)
			}
		}
	}

	capacity := buffer
	if len(pending) > capacity {
		capacity = len(pending)
	}
	ch := make(chan Event, capacity)
	sub := &Subscription{ch: ch, C: ch, filter: filter, broker: b}
	for _, e := range pending {
		ch <- e // fits: capacity >= len(pending)
	}

	if b.closed {
		sub.closeOnce()
		return sub
	}
	b.subs[sub] = struct{}{}
	return sub
}

// Close unsubscribes and closes every subscriber channel. It is idempotent.
func (b *Broker) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for sub := range b.subs {
		sub.closeOnce()
		delete(b.subs, sub)
	}
}

// Subscription is a receive stream of events from a [Broker]. Range over C to
// consume events; call Close to unsubscribe. The channel closes when the
// subscription is closed, the broker is closed, or the subscriber is
// force-unsubscribed after wedging past the block bound.
type Subscription struct {
	// C receives delivered events. It is closed when the subscription ends.
	C <-chan Event

	ch      chan Event
	filter  Filter
	broker  *Broker
	dropped atomic.Uint64
	forced  atomic.Bool
	once    sync.Once
}

// Dropped returns the number of lossy events dropped for this subscriber
// because its buffer was full.
func (s *Subscription) Dropped() uint64 { return s.dropped.Load() }

// Forced reports whether the subscription was force-closed by the broker
// because it stayed wedged on a full buffer past the block bound.
func (s *Subscription) Forced() bool { return s.forced.Load() }

// Close unsubscribes and closes C. It is idempotent and safe to call
// concurrently with delivery.
func (s *Subscription) Close() {
	s.broker.mu.Lock()
	delete(s.broker.subs, s)
	s.broker.mu.Unlock()
	s.closeOnce()
}

func (s *Subscription) closeOnce() { s.once.Do(func() { close(s.ch) }) }
