package event_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/provider"
)

const sid = "sess-1"

func fixedClock() time.Time { return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC) }

// TestBrokerLossyDropAndMustDeliver asserts the two-tier contract: lossy events
// drop (and are counted) when a subscriber's buffer is full, while must-deliver
// events all arrive even through a tiny buffer.
func TestBrokerLossyDropAndMustDeliver(t *testing.T) {
	t.Run("lossy drops on full buffer", func(t *testing.T) {
		b := event.NewBroker(event.WithClock(fixedClock))
		defer b.Close()
		sub := b.Subscribe(event.FilterAll, 1)

		// Buffer holds 1; the first lossy event fits, the next two drop.
		b.Publish(event.NewMessageDelta(sid, event.MessageText, "a"))
		b.Publish(event.NewMessageDelta(sid, event.MessageText, "b"))
		b.Publish(event.NewMessageDelta(sid, event.MessageText, "c"))

		if got := sub.Dropped(); got != 2 {
			t.Fatalf("Dropped() = %d, want 2", got)
		}
		ev := <-sub.C
		if d, ok := ev.(event.MessageDelta); !ok || d.Text != "a" {
			t.Fatalf("first buffered event = %#v, want MessageDelta{Text:\"a\"}", ev)
		}
	})

	t.Run("must-deliver all arrive through a tiny buffer", func(t *testing.T) {
		b := event.NewBroker(event.WithClock(fixedClock))
		defer b.Close()
		sub := b.Subscribe(event.FilterAll, 1)

		const n = 5
		done := make(chan []event.Event, 1)
		go func() {
			var got []event.Event
			for ev := range sub.C {
				got = append(got, ev)
			}
			done <- got
		}()

		for range n {
			b.Publish(event.NewTurnStarted(sid)) // must-deliver
		}
		b.Close() // ends the subscriber's range once drained

		got := <-done
		if len(got) != n {
			t.Fatalf("received %d must-deliver events, want %d", len(got), n)
		}
		if sub.Dropped() != 0 {
			t.Fatalf("Dropped() = %d, want 0 for must-deliver", sub.Dropped())
		}
		for i, ev := range got {
			if want := uint64(i + 1); ev.Seq() != want {
				t.Errorf("event %d seq = %d, want %d", i, ev.Seq(), want)
			}
		}
	})
}

// TestBrokerForceUnsubscribe asserts a subscriber wedged past the block bound is
// force-unsubscribed rather than hanging the broker.
func TestBrokerForceUnsubscribe(t *testing.T) {
	b := event.NewBroker(event.WithClock(fixedClock), event.WithBlockBound(10*time.Millisecond))
	defer b.Close()
	sub := b.Subscribe(event.FilterAll, 1)

	// No reader: first fills the buffer, the second wedges then times out.
	b.Publish(event.NewTurnStarted(sid))
	b.Publish(event.NewTurnStarted(sid))

	if !sub.Forced() {
		t.Fatal("Forced() = false, want true after wedging past the block bound")
	}
	if _, ok := <-sub.C; !ok {
		t.Fatal("channel closed before yielding the buffered event")
	}
	if _, ok := <-sub.C; ok {
		t.Fatal("channel still open after force-unsubscribe")
	}
}

// TestBrokerFilterMustDeliver asserts a must-deliver-only subscriber never sees
// lossy deltas.
func TestBrokerFilterMustDeliver(t *testing.T) {
	b := event.NewBroker(event.WithClock(fixedClock))
	defer b.Close()
	sub := b.Subscribe(event.FilterMustDeliver, 8)

	b.Publish(event.NewMessageDelta(sid, event.MessageText, "lossy")) // filtered out
	b.Publish(event.NewTurnFinished(sid, "end_turn", provider.Usage{}))
	b.Close()

	var kinds []string
	for ev := range sub.C {
		kinds = append(kinds, ev.Kind())
	}
	if len(kinds) != 1 || kinds[0] != event.KindTurnFinished {
		t.Fatalf("kinds = %v, want [%s]", kinds, event.KindTurnFinished)
	}
	if sub.Dropped() != 0 {
		t.Fatalf("Dropped() = %d, want 0 (filtered, not dropped)", sub.Dropped())
	}
}

// TestBrokerReplay asserts retained must-deliver events replay to late
// subscribers in seq order while lossy events are not retained.
func TestBrokerReplay(t *testing.T) {
	b := event.NewBroker(event.WithClock(fixedClock), event.WithReplay(8))
	defer b.Close()

	b.Publish(event.NewSessionCreated(sid))                       // retained
	b.Publish(event.NewMessageDelta(sid, event.MessageText, "x")) // lossy, not retained
	b.Publish(event.NewTurnStarted(sid))                          // retained

	sub := b.Subscribe(event.FilterAll, 8)
	var kinds []string
	for range 2 {
		kinds = append(kinds, (<-sub.C).Kind())
	}
	if len(kinds) != 2 || kinds[0] != event.KindSessionCreated || kinds[1] != event.KindTurnStarted {
		t.Fatalf("replayed kinds = %v, want [%s %s]", kinds, event.KindSessionCreated, event.KindTurnStarted)
	}
}

// TestBrokerSubscribeLiveSkipsReplay asserts SubscribeLive receives only events
// published after it subscribes, never the retained must-deliver backlog that
// Subscribe replays — the guard a new-turn driver needs so a prior turn's
// retained terminal event is not mistaken for its own turn finishing.
func TestBrokerSubscribeLiveSkipsReplay(t *testing.T) {
	b := event.NewBroker(event.WithClock(fixedClock), event.WithReplay(8))
	defer b.Close()

	b.Publish(event.NewSessionCreated(sid))                             // retained
	b.Publish(event.NewTurnFinished(sid, "end_turn", provider.Usage{})) // retained (a prior turn's terminal)

	sub := b.SubscribeLive(event.FilterAll, 8)

	// No backlog is replayed: a non-blocking read finds the channel empty.
	select {
	case e := <-sub.C:
		t.Fatalf("SubscribeLive replayed a retained event: %s", e.Kind())
	default:
	}

	// Only events published AFTER SubscribeLive are delivered.
	b.Publish(event.NewTurnStarted(sid))
	if got := (<-sub.C).Kind(); got != event.KindTurnStarted {
		t.Fatalf("live event = %s, want %s", got, event.KindTurnStarted)
	}
}

// TestEventEnvelope asserts the JSON envelope shape: type/session_id/seq/time
// plus payload fields, with seq and time assigned at publish.
func TestEventEnvelope(t *testing.T) {
	b := event.NewBroker(event.WithClock(fixedClock))
	defer b.Close()
	sub := b.Subscribe(event.FilterAll, 4)

	b.Publish(event.NewMessageFinished(sid, event.MessageText, "hi"))
	ev := <-sub.C

	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for k, want := range map[string]any{
		"type":       event.KindMessageFinished,
		"session_id": sid,
		"seq":        float64(1),
		"time":       "2025-01-01T00:00:00Z",
		"kind":       string(event.MessageText),
		"content":    "hi",
	} {
		if m[k] != want {
			t.Errorf("envelope[%q] = %v, want %v", k, m[k], want)
		}
	}
}

// TestTierOf asserts only stream deltas ride the lossy tier.
func TestTierOf(t *testing.T) {
	tests := []struct {
		kind string
		want event.Tier
	}{
		{event.KindMessageDelta, event.TierLossy},
		{event.KindToolCallDelta, event.TierLossy},
		{event.KindMessageFinished, event.TierMustDeliver},
		{event.KindTurnStarted, event.TierMustDeliver},
		{event.KindSessionCreated, event.TierMustDeliver},
		{event.KindPermissionRequested, event.TierMustDeliver},
	}
	for _, tt := range tests {
		if got := event.TierOf(tt.kind); got != tt.want {
			t.Errorf("TierOf(%q) = %v, want %v", tt.kind, got, tt.want)
		}
	}
}
