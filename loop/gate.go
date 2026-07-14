package loop

import (
	"context"
	"sync"

	"github.com/jedwards1230/agent-sdk-go/event"
)

// Gate is the reference Approver: it bridges an emitted permission.requested
// to a client's permission.reply op. The loop's guard calls Await; the
// consuming application routes an inbound event.PermissionReply into Reply.
// Await unblocks with the matching reply or ctx error.
//
// Await spawns no goroutine: it blocks the calling (loop) goroutine on a
// select between the buffered reply channel and ctx.Done, so a cancelled turn
// leaks nothing.
//
// Emit-then-await ordering is safe in either direction. The loop publishes
// permission.requested before it calls Await, so a fast client can Reply before
// the waiter is registered; such an early reply is stashed in pending and
// consumed by the imminent Await for the same id (rather than lost). A reply
// for an id that is never awaited (e.g. a request abandoned by a cancelled
// turn) lingers in pending until the Gate is discarded — bounded by the unique
// tool-call ids of a session; a TTL/GC lands with grant persistence in M4/M5.
type Gate struct {
	mu      sync.Mutex
	waiters map[string]chan Reply
	pending map[string]Reply
}

// NewGate returns a ready-to-use Gate.
func NewGate() *Gate {
	return &Gate{
		waiters: make(map[string]chan Reply),
		pending: make(map[string]Reply),
	}
}

// Await implements Approver. It blocks until a matching Reply call for id
// arrives or ctx is done. A reply that arrived before this Await (the
// emit-then-await window) is returned immediately.
func (g *Gate) Await(ctx context.Context, id string) (Reply, error) {
	g.mu.Lock()
	if r, ok := g.pending[id]; ok {
		delete(g.pending, id)
		g.mu.Unlock()
		return r, nil
	}
	ch := make(chan Reply, 1) // buffered so Reply never blocks
	g.waiters[id] = ch
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.waiters, id)
		g.mu.Unlock()
	}()
	select {
	case r := <-ch:
		return r, nil
	case <-ctx.Done():
		return Reply{}, ctx.Err()
	}
}

// Reply delivers a client's answer to the matching Await call; it never blocks.
// If no waiter is registered yet, the reply is stashed for the imminent Await
// (see the Gate doc) rather than dropped.
func (g *Gate) Reply(op event.PermissionReply) {
	reply := Reply{Verdict: op.Verdict, Remember: op.Remember}
	g.mu.Lock()
	ch, ok := g.waiters[op.ID]
	if ok {
		delete(g.waiters, op.ID)
	} else {
		g.pending[op.ID] = reply
	}
	g.mu.Unlock()
	if ok {
		ch <- reply
	}
}
