package loop_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/loop"
	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/provider/faux"
)

// TestContextOverflowPropagatesUnwrapped guards the classification contract at
// the seam a consumer actually holds: whatever loop.Run returns must still
// satisfy errors.Is against provider.ErrContextOverflow. The loop needs no code
// to make that true today — it returns the provider error as-is — which is
// exactly why it needs a test: a future change that stringifies or re-wraps
// without %w would break every compact-and-retry branch downstream, silently
// and without failing anything else.
func TestContextOverflowPropagatesUnwrapped(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()
	sub := b.Subscribe(event.FilterAll, 256)

	p := faux.New(faux.Script{Turns: []faux.Turn{{
		Err: fmt.Errorf("faux: %w", provider.ErrContextOverflow),
	}}})
	cfg := baseConfig(b, p.Stream)

	res, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("a very long prompt")})
	if err == nil {
		t.Fatal("Run returned nil error, want the scripted overflow")
	}
	if !errors.Is(err, provider.ErrContextOverflow) {
		t.Fatalf("errors.Is(err, ErrContextOverflow) = false, err = %v", err)
	}
	if res.StopReason != provider.StopError {
		t.Errorf("stop = %q, want %q", res.StopReason, provider.StopError)
	}
	// A rejected call generated nothing, so it reports no usage — the reason a
	// usage-threshold trigger cannot see a single-turn overshoot on its own.
	if !res.Usage.Equal(provider.Usage{}) {
		t.Errorf("usage = %+v, want zero on a rejected call", res.Usage)
	}
	kinds := collectKinds(sub)
	if countKind(kinds, event.KindSessionError) != 1 {
		t.Errorf("want one session.error, got %v", kinds)
	}
}

// TestContextOverflowThenRetrySucceeds is the shape of a consumer's
// compact-and-retry: the first call is rejected as an overflow, the caller
// shortens its message list and re-runs, and the second call succeeds. It
// exercises the whole branch end to end without a network or an API key.
func TestContextOverflowThenRetrySucceeds(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()

	p := faux.New(faux.Script{Turns: []faux.Turn{
		{Err: fmt.Errorf("faux: %w", provider.ErrContextOverflow)},
		{Text: []string{"ok"}, StopReason: provider.StopEndTurn, Usage: provider.Usage{InputTokens: 5, OutputTokens: 1}},
	}})
	cfg := baseConfig(b, p.Stream)

	msgs := []provider.Message{provider.UserText("turn one"), provider.UserText("turn two")}
	_, err := loop.Run(context.Background(), cfg, msgs)
	if !errors.Is(err, provider.ErrContextOverflow) {
		t.Fatalf("first Run: errors.Is(err, ErrContextOverflow) = false, err = %v", err)
	}

	// The compact step, standing in for a real summarizer: drop history.
	res, err := loop.Run(context.Background(), cfg, msgs[len(msgs)-1:])
	if err != nil {
		t.Fatalf("retry Run: %v", err)
	}
	if res.StopReason != provider.StopEndTurn || res.Messages[len(res.Messages)-1].Text() != "ok" {
		t.Errorf("retry result = %+v, want a completed turn", res)
	}
}
