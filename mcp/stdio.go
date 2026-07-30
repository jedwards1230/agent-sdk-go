package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
)

// stdioTransport speaks MCP's stdio transport framing: each JSON-RPC message
// is exactly one line of UTF-8 JSON with no embedded newline, terminated by
// "\n" — deliberately different from LSP's Content-Length header framing in
// [lsp.Client], because the two protocols specify different stdio framings.
// One background goroutine reads lines off the server's stdout, routing a
// response to its pending caller by id and dropping anything else (a
// malformed line, or a notification this client has no use for) rather than
// failing the connection over it.
type stdioTransport struct {
	w       io.Writer
	writeMu sync.Mutex

	closer    io.Closer
	closeOnce sync.Once
	closed    chan struct{} // closed by close(); unblocks any pending roundTrip immediately
	readDone  chan struct{} // closed when the read loop returns

	pendingMu sync.Mutex
	pending   map[int64]chan pendingResult

	log *slog.Logger
}

// pendingResult is what a pending roundTrip's waiter receives.
type pendingResult struct {
	resp rpcResponse
	err  error
}

// newStdioTransport wraps rw (an already-open duplex stream to the server —
// a subprocess's stdin/stdout for [Start], or an in-memory pipe in tests) and
// starts its background read loop.
func newStdioTransport(rw io.ReadWriteCloser, log *slog.Logger) *stdioTransport {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	t := &stdioTransport{
		w:        rw,
		closer:   rw,
		closed:   make(chan struct{}),
		readDone: make(chan struct{}),
		pending:  make(map[int64]chan pendingResult),
		log:      log,
	}
	go t.readLoop(bufio.NewReader(rw))
	return t
}

func (t *stdioTransport) roundTrip(ctx context.Context, id int64, frame []byte) (rpcResponse, error) {
	select {
	case <-t.closed:
		return rpcResponse{}, ErrClosed
	default:
	}

	ch := make(chan pendingResult, 1)
	t.pendingMu.Lock()
	t.pending[id] = ch
	t.pendingMu.Unlock()

	if err := t.writeFrame(frame); err != nil {
		t.dropPending(id)
		return rpcResponse{}, fmt.Errorf("mcp: write request: %w", err)
	}

	select {
	case res := <-ch:
		return res.resp, res.err
	case <-ctx.Done():
		t.dropPending(id)
		return rpcResponse{}, ctx.Err()
	case <-t.closed:
		t.dropPending(id)
		return rpcResponse{}, ErrClosed
	}
}

func (t *stdioTransport) send(_ context.Context, frame []byte) error {
	select {
	case <-t.closed:
		return ErrClosed
	default:
	}
	return t.writeFrame(frame)
}

func (t *stdioTransport) close() error {
	var err error
	t.closeOnce.Do(func() {
		close(t.closed)
		err = t.closer.Close()
		<-t.readDone
	})
	return err
}

// writeFrame serializes writes: the read loop's caller and every concurrent
// roundTrip/send may write at once, and a torn write would interleave two
// messages' bytes on the wire.
func (t *stdioTransport) writeFrame(frame []byte) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if _, err := t.w.Write(frame); err != nil {
		return err
	}
	_, err := t.w.Write([]byte("\n"))
	return err
}

func (t *stdioTransport) dropPending(id int64) {
	t.pendingMu.Lock()
	delete(t.pending, id)
	t.pendingMu.Unlock()
}

// readLoop reads newline-delimited frames off r until the transport reports
// an error (EOF on a normal close, or any other read failure), routing each
// decoded response to its pending caller. It never exits except on a read
// error, so close()'s Close() call is what always terminates it.
func (t *stdioTransport) readLoop(r *bufio.Reader) {
	defer close(t.readDone)
	for {
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			select {
			case <-t.closed:
				t.log.Debug("mcp: read loop stopped on close")
			default:
				t.log.Warn("mcp: read loop stopped on transport error", "error", err)
			}
			t.failPending(fmt.Errorf("mcp: connection closed: %w", err))
			return
		}
		// A final line with no trailing "\n" (err == io.EOF, line != "") is
		// still a complete frame — process it, then let the next iteration's
		// empty read end the loop.
		frame := trimNewline(line)
		if len(frame) == 0 {
			continue
		}
		var msg rpcInbound
		if jsonErr := json.Unmarshal(frame, &msg); jsonErr != nil {
			t.log.Warn("mcp: dropping malformed frame", "error", jsonErr)
			continue
		}
		if msg.ID != nil && msg.Method == "" {
			t.deliver(*msg.ID, rpcResponse{JSONRPC: msg.JSONRPC, ID: msg.ID, Result: msg.Result, Error: msg.Error})
		}
		// A notification (Method set, no ID) or anything else unrecognized is
		// ignored: this client has no notification consumer yet (M7 scope is
		// client + tool projection only).
	}
}

func trimNewline(line string) []byte {
	n := len(line)
	for n > 0 && (line[n-1] == '\n' || line[n-1] == '\r') {
		n--
	}
	return []byte(line[:n])
}

func (t *stdioTransport) deliver(id int64, resp rpcResponse) {
	t.pendingMu.Lock()
	ch, ok := t.pending[id]
	if ok {
		delete(t.pending, id)
	}
	t.pendingMu.Unlock()
	if ok {
		ch <- pendingResult{resp: resp}
	}
}

// failPending unblocks every still-outstanding call with err — used once,
// when readLoop exits, so a call blocked on a connection that just died
// doesn't hang forever waiting on ctx or close().
func (t *stdioTransport) failPending(err error) {
	t.pendingMu.Lock()
	pending := t.pending
	t.pending = make(map[int64]chan pendingResult)
	t.pendingMu.Unlock()
	for _, ch := range pending {
		ch <- pendingResult{err: err}
	}
}

// NewStdio builds a [Client] speaking MCP's stdio transport over an
// already-open duplex stream. It is the seam tests use (an in-memory
// io.Pipe); [Start] is the production constructor that spawns and wires a
// real subprocess.
func NewStdio(rw io.ReadWriteCloser, opts ...Option) *Client {
	c := newClient(opts...)
	c.t = newStdioTransport(rw, c.log)
	return c
}

// processStdio adapts a spawned server process's stdin/stdout pipes plus its
// *exec.Cmd into an io.ReadWriteCloser: writes go to stdin, reads come from
// stdout, and Close closes stdin (signalling EOF to a well-behaved server)
// then waits for the process to exit — the same shape as lsp.Client's
// processTransport.
type processStdio struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
	cmd    *exec.Cmd
}

func (p *processStdio) Write(b []byte) (int, error) { return p.stdin.Write(b) }
func (p *processStdio) Read(b []byte) (int, error)  { return p.stdout.Read(b) }

func (p *processStdio) Close() error {
	closeErr := p.stdin.Close()
	_ = p.stdout.Close()
	_ = p.cmd.Wait()
	return closeErr
}

// Start spawns an MCP server as a subprocess (command + args, PATH-resolved
// by os/exec like any exec.Command) and returns a [Client] wired to its
// stdio. command/args are operator-configured server launch settings (server
// definitions are the consuming application's job — see docs/DESIGN.md "MCP
// (M7)"), not user/model input, matching [lsp.Start]'s posture on the same
// gosec finding. The subprocess's lifetime is tied to ctx: cancelling it
// kills the process. Call [Client.Initialize] before any other method.
func Start(ctx context.Context, command string, args []string, opts ...Option) (*Client, error) {
	cmd := exec.CommandContext(ctx, command, args...) // #nosec G204 -- operator-configured server launch command, not user/model input
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: start %s: stdin pipe: %w", command, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: start %s: stdout pipe: %w", command, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp: start %s: %w", command, err)
	}
	rw := &processStdio{stdin: stdin, stdout: stdout, cmd: cmd}
	return NewStdio(rw, opts...), nil
}
