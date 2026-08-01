package lsp

import (
	"context"
	"errors"
	"io"
	"testing"
)

// Tests here reach inside the package on purpose: they register a pending
// entry directly rather than going through call, so the assertion observes
// exactly what readLoop delivered without call's three-way select choosing
// pseudo-randomly between the delivered result and c.closed. That makes them
// deterministic at -count=1 where the external race test only catches the bug
// a fraction of the time.

// internalPipeTransport is the minimal in-memory Transport these tests need:
// writes to one end arrive as reads on the other, and Close tears both ends
// down (the peer's blocked Read unblocks with io.EOF or io.ErrClosedPipe).
type internalPipeTransport struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (p *internalPipeTransport) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *internalPipeTransport) Write(b []byte) (int, error) { return p.w.Write(b) }

func (p *internalPipeTransport) Close() error {
	rerr := p.r.Close()
	werr := p.w.Close()
	if rerr != nil {
		return rerr
	}
	return werr
}

func newInternalPipedTransports() (client, server *internalPipeTransport) {
	c2sR, c2sW := io.Pipe() // client writes -> server reads
	s2cR, s2cW := io.Pipe() // server writes -> client reads
	client = &internalPipeTransport{r: s2cR, w: c2sW}
	server = &internalPipeTransport{r: c2sR, w: s2cW}
	return client, server
}

// newPendingClient returns a client over an in-memory pipe with one pending
// entry registered directly, bypassing call. The returned channel is the one
// readLoop's failPending will deliver into.
func newPendingClient(t *testing.T) (*Client, *internalPipeTransport, chan callResult) {
	t.Helper()
	clientTransport, serverTransport := newInternalPipedTransports()
	c := NewClient(clientTransport, PublisherFunc(func(context.Context, string, Batch) {}), "sess-internal")

	ch := make(chan callResult, 1)
	c.pendingMu.Lock()
	c.pending[1] = ch
	c.pendingMu.Unlock()

	return c, serverTransport, ch
}

// TestReadLoopFailsPendingWithErrClosedOnDeliberateClose is the regression
// guard for the ErrClosed misclassification: readLoop used to fail every
// pending call with the raw transport error even when a deliberate Close had
// caused that error, so a call racing Close returned "read/write on closed
// pipe" instead of the documented ErrClosed sentinel roughly one time in
// four hundred (whichever of call's select cases the scheduler picked).
//
// Close returns only after <-c.readDone, and readDone is closed by readLoop's
// defer strictly after failPending has run, so by the time Close returns the
// pending channel is guaranteed populated — no sleeps, no timeouts, no select
// ambiguity.
func TestReadLoopFailsPendingWithErrClosedOnDeliberateClose(t *testing.T) {
	c, serverTransport, ch := newPendingClient(t)
	defer func() { _ = serverTransport.Close() }()

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case res := <-ch:
		if !errors.Is(res.err, ErrClosed) {
			t.Errorf("pending call err = %v, want it to wrap ErrClosed", res.err)
		}
	default:
		t.Fatal("Close returned without failing the pending call")
	}
}

// TestReadLoopFailsPendingWithTransportErrorOnServerDeath is the companion
// assertion: when the transport dies on its own with no Close, the pending
// call must still receive the wrapped transport error and must NOT be
// misreported as ErrClosed — proof the fix classifies the two cases rather
// than collapsing them.
func TestReadLoopFailsPendingWithTransportErrorOnServerDeath(t *testing.T) {
	c, serverTransport, ch := newPendingClient(t)
	defer func() { _ = c.Close() }()

	// Kill only the server side: the client's next read sees EOF while
	// c.closed is still open.
	if err := serverTransport.Close(); err != nil {
		t.Fatalf("server transport Close: %v", err)
	}

	// readDone is closed by readLoop's defer, strictly after failPending.
	<-c.readDone

	select {
	case res := <-ch:
		if errors.Is(res.err, ErrClosed) {
			t.Errorf("pending call err = %v, want the transport error, not ErrClosed", res.err)
		}
		if !errors.Is(res.err, io.EOF) {
			t.Errorf("pending call err = %v, want it to wrap io.EOF", res.err)
		}
	default:
		t.Fatal("read loop exited without failing the pending call")
	}
}
