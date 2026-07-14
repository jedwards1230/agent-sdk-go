package loop_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/loop"
	"github.com/jedwards1230/agent-sdk-go/provider"
)

func TestGateAwaitCancelledReturnsPromptly(t *testing.T) {
	g := loop.NewGate()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err := g.Await(ctx, "id-1")
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed > time.Second {
		t.Errorf("Await took %v, want prompt return on cancellation", elapsed)
	}

	// The waiter was cleaned up on return: a reply for the same id now has no
	// live waiter, so it must be a silent no-op (not delivered anywhere, no
	// panic, no block).
	g.Reply(event.PermissionReply{ID: "id-1", Verdict: event.VerdictAllow})
}

func TestGateReplyUnknownIDIsNoop(t *testing.T) {
	g := loop.NewGate()
	// Must not panic or block for an id with no live Await waiter.
	g.Reply(event.PermissionReply{ID: "never-awaited", Verdict: event.VerdictAllow})
}

// TestGateReplyBeforeAwait covers the emit-then-await window: the loop always
// publishes permission.requested before calling Await, so a fast client can
// Reply before the waiter is registered. That early reply must be delivered to
// the subsequent Await for the same id, not lost (a deadlock the loop would
// otherwise hit).
func TestGateReplyBeforeAwait(t *testing.T) {
	g := loop.NewGate()

	// Reply arrives first, no waiter yet.
	g.Reply(event.PermissionReply{ID: "early", Verdict: event.VerdictAllow, Remember: true})

	reply, err := g.Await(context.Background(), "early")
	if err != nil {
		t.Fatalf("Await after early Reply: %v", err)
	}
	if reply.Verdict != event.VerdictAllow || !reply.Remember {
		t.Errorf("reply = %+v, want {allow, remember}", reply)
	}

	// The stashed reply is consumed once: a second Await for the same id must
	// block (no lingering pending entry), so a short-deadline ctx times out.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := g.Await(ctx, "early"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("second Await err = %v, want DeadlineExceeded (pending consumed once)", err)
	}
}

func TestGateEmitAwaitReplyAllow(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()
	sub := b.Subscribe(event.FilterAll, 256)

	tool := &fakeTool{name: "echo", result: loop.ToolResult{Content: "ok"}}
	cfg := gatedToolConfig(b, tool)
	cfg.Guard = &stubGuard{decision: loop.DecisionAsk, rule: "ask echo"}
	gate := loop.NewGate()
	cfg.Approver = gate

	id := runGatedAndReply(t, sub, cfg, event.VerdictAllow, gate)

	if id != "t1" {
		t.Errorf("permission.requested id = %q, want t1", id)
	}
	if tool.runs != 1 {
		t.Errorf("tool.runs = %d, want 1 (approved)", tool.runs)
	}
}

func TestGateEmitAwaitReplyDeny(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()
	sub := b.Subscribe(event.FilterAll, 256)

	tool := &fakeTool{name: "echo", result: loop.ToolResult{Content: "ok"}}
	cfg := gatedToolConfig(b, tool)
	cfg.Guard = &stubGuard{decision: loop.DecisionAsk, rule: "ask echo"}
	gate := loop.NewGate()
	cfg.Approver = gate

	runGatedAndReply(t, sub, cfg, event.VerdictDeny, gate)

	if tool.runs != 0 {
		t.Errorf("tool.runs = %d, want 0 (denied)", tool.runs)
	}
}

// runGatedAndReply drives loop.Run in a goroutine, waits for the
// permission.requested event on sub, replies through gate, and waits for Run
// to complete. It returns the requested call's id.
func runGatedAndReply(t *testing.T, sub *event.Subscription, cfg loop.Config, verdict event.Verdict, gate *loop.Gate) string {
	t.Helper()

	errCh := make(chan error, 1)
	go func() {
		_, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("go")})
		errCh <- err
	}()

	var id string
	deadline := time.After(2 * time.Second)
waitReq:
	for {
		select {
		case e := <-sub.C:
			if pr, ok := e.(event.PermissionRequested); ok {
				id = pr.ID
				break waitReq
			}
		case <-deadline:
			t.Fatal("timed out waiting for permission.requested")
		}
	}

	gate.Reply(event.PermissionReply{ID: id, Verdict: verdict})

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not complete after Reply")
	}

	return id
}

// TestGateNoGoroutineLeak drives N cancelled Await cycles concurrently and
// asserts the goroutine count returns to baseline once they all join — proof
// that Await itself spawns nothing that could survive a cancelled turn.
// Stdlib only: runtime.NumGoroutine(), no goleak dependency.
func TestGateNoGoroutineLeak(t *testing.T) {
	g := loop.NewGate()

	runtime.GC()
	base := runtime.NumGoroutine()

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		wg.Add(1)
		go func(id string, ctx context.Context) {
			defer wg.Done()
			_, _ = g.Await(ctx, id)
		}(fmt.Sprintf("call-%d", i), ctx)
		cancel()
	}
	wg.Wait()

	// Let the runtime settle before sampling.
	time.Sleep(50 * time.Millisecond)
	runtime.GC()
	after := runtime.NumGoroutine()
	if after > base+2 { // small slack for the Go runtime's own housekeeping
		t.Errorf("goroutine count grew from %d to %d after %d cancelled awaits", base, after, n)
	}
}
