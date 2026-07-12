package session_test

import (
	"context"
	"testing"
	"time"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/provider/faux"
	"github.com/jedwards1230/agent-sdk-go/session"
)

func fixedClock() time.Time { return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC) }

func newTestSession(t *testing.T) *session.Session {
	t.Helper()
	return session.New(faux.New(faux.Default()),
		session.WithIDGen(func() string { return "sess-test" }),
		session.WithClock(fixedClock),
		session.WithModel("faux-1"),
	)
}

// drain reads events from a subscription until turn.finished, returning them.
func drain(t *testing.T, sub *event.Subscription) []event.Event {
	t.Helper()
	var got []event.Event
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev := <-sub.C:
			got = append(got, ev)
			if ev.Kind() == event.KindTurnFinished {
				return got
			}
		case <-timeout:
			t.Fatal("timed out waiting for turn.finished")
		}
	}
}

// TestPromptEmitsOrderedTurn asserts one Prompt produces the full ordered event
// sequence, with session.created replayed to the late subscriber.
func TestPromptEmitsOrderedTurn(t *testing.T) {
	sess := newTestSession(t)
	if sess.ID() != "sess-test" {
		t.Fatalf("ID() = %q, want sess-test", sess.ID())
	}

	sub := sess.Subscribe(event.FilterAll)
	defer sub.Close()
	if err := sess.Prompt(context.Background(), "hello"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	got := drain(t, sub)
	wantKinds := []string{
		event.KindSessionCreated,
		event.KindTurnStarted,
		event.KindMessageStarted, // reasoning
		event.KindMessageDelta,
		event.KindMessageDelta,
		event.KindMessageFinished,
		event.KindMessageStarted, // text
		event.KindMessageDelta,
		event.KindMessageDelta,
		event.KindMessageDelta,
		event.KindMessageFinished,
		event.KindTurnFinished,
	}
	if len(got) != len(wantKinds) {
		t.Fatalf("got %d events, want %d: %v", len(got), len(wantKinds), kinds(got))
	}
	for i, ev := range got {
		if ev.Kind() != wantKinds[i] {
			t.Errorf("event %d kind = %q, want %q", i, ev.Kind(), wantKinds[i])
		}
		if want := uint64(i + 1); ev.Seq() != want {
			t.Errorf("event %d seq = %d, want %d", i, ev.Seq(), want)
		}
	}
}

// TestFinishedReconcilesDeltas asserts each message.finished carries the full
// concatenation of its deltas, and turn.finished carries the scripted usage.
func TestFinishedReconcilesDeltas(t *testing.T) {
	sess := newTestSession(t)
	sub := sess.Subscribe(event.FilterAll)
	defer sub.Close()
	if err := sess.Prompt(context.Background(), "hello"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	var finished []event.MessageFinished
	var turn event.TurnFinished
	for _, ev := range drain(t, sub) {
		switch e := ev.(type) {
		case event.MessageFinished:
			finished = append(finished, e)
		case event.TurnFinished:
			turn = e
		}
	}

	if len(finished) != 2 {
		t.Fatalf("got %d message.finished events, want 2", len(finished))
	}
	if finished[0].MessageKind != event.MessageReasoning ||
		finished[0].Content != "The user said hello. I'll greet them back." {
		t.Errorf("reasoning finished = %+v", finished[0])
	}
	if finished[1].MessageKind != event.MessageText ||
		finished[1].Content != "Hello! How can I help you today?" {
		t.Errorf("text finished = %+v", finished[1])
	}
	if turn.StopReason != "end_turn" || turn.Usage != (provider.Usage{InputTokens: 9, OutputTokens: 7}) {
		t.Errorf("turn.finished = %+v", turn)
	}
}

// TestPromptContextCanceled asserts a canceled context is reported before the
// turn starts.
func TestPromptContextCanceled(t *testing.T) {
	sess := newTestSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sess.Prompt(ctx, "hello"); err == nil {
		t.Fatal("Prompt with canceled context returned nil error")
	}
}

func kinds(evs []event.Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Kind()
	}
	return out
}
