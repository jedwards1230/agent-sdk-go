package session_test

import (
	"testing"
	"time"

	"github.com/jedwards1230/agent-sdk-go/event"
)

// nextInfo reads events until a session.info arrives, failing on timeout.
func nextInfo(t *testing.T, sub *event.Subscription) event.SessionInfoUpdated {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev := <-sub.C:
			if info, ok := ev.(event.SessionInfoUpdated); ok {
				return info
			}
		case <-timeout:
			t.Fatal("timed out waiting for session.info")
		}
	}
}

// assertNoInfo fails if any session.info arrives within d; other events are
// ignored.
func assertNoInfo(t *testing.T, sub *event.Subscription, d time.Duration) {
	t.Helper()
	timeout := time.After(d)
	for {
		select {
		case ev := <-sub.C:
			if _, ok := ev.(event.SessionInfoUpdated); ok {
				t.Fatalf("unexpected session.info event: %+v", ev)
			}
		case <-timeout:
			return
		}
	}
}

// TestSetTitleEmitsSessionInfo asserts SetTitle updates Title() and publishes a
// single session.info carrying the new title.
func TestSetTitleEmitsSessionInfo(t *testing.T) {
	sess := newTestSession(t)
	defer sess.Close()

	if got := sess.Title(); got != "" {
		t.Fatalf("Title() before SetTitle = %q, want empty", got)
	}

	sub := sess.Subscribe(event.FilterMustDeliver)
	defer sub.Close()

	sess.SetTitle("Debug auth timeout")

	info := nextInfo(t, sub)
	if info.Title != "Debug auth timeout" {
		t.Errorf("event title = %q, want %q", info.Title, "Debug auth timeout")
	}
	if info.SessionID() != sess.ID() {
		t.Errorf("event session id = %q, want %q", info.SessionID(), sess.ID())
	}
	if got := sess.Title(); got != "Debug auth timeout" {
		t.Errorf("Title() = %q, want %q", got, "Debug auth timeout")
	}
}

// TestSetTitleIdempotent asserts setting the title to its current value is a
// no-op: no second session.info is emitted. Setting it again to a new value
// then reaches the client, proving only the redundant set was suppressed.
func TestSetTitleIdempotent(t *testing.T) {
	sess := newTestSession(t)
	defer sess.Close()

	sub := sess.Subscribe(event.FilterMustDeliver)
	defer sub.Close()

	sess.SetTitle("A")
	if got := nextInfo(t, sub).Title; got != "A" {
		t.Fatalf("first title = %q, want A", got)
	}

	sess.SetTitle("A") // redundant: must not emit
	sess.SetTitle("B") // the next session.info must carry B, not a second A

	if got := nextInfo(t, sub).Title; got != "B" {
		t.Errorf("next title = %q, want B (redundant set leaked an event)", got)
	}
}

// TestNoSetTitleEmitsNothing asserts a session whose title is never set emits no
// session.info at all.
func TestNoSetTitleEmitsNothing(t *testing.T) {
	sess := newTestSession(t)
	defer sess.Close()

	sub := sess.Subscribe(event.FilterMustDeliver)
	defer sub.Close()

	assertNoInfo(t, sub, 100*time.Millisecond)
}
