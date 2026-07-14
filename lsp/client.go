package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
)

// Transport is the LSP server's stdio: writes go to the server's stdin, reads
// come from its stdout, and Close terminates the connection — killing the
// process for a real server ([Start]), or unblocking an in-memory pipe in
// tests.
type Transport = io.ReadWriteCloser

// ErrClosed is returned by any Client method invoked after (or racing) a call
// to [Client.Close], including a call already in flight when Close runs.
var ErrClosed = errors.New("lsp: client closed")

// callResult is what a pending request's waiter receives: either a decoded
// response or an error (a JSON-RPC error is instead surfaced via
// response.Error, checked by the caller — this err field is reserved for
// transport-level failure: connection loss, Close, or ctx cancellation seen
// by the read loop rather than by the caller's own select).
type callResult struct {
	resp responseMessage
	err  error
}

// Client is a minimal LSP client speaking JSON-RPC-over-stdio to one language
// server. A single background goroutine (started by [NewClient]) reads framed
// messages off Transport: responses are routed to the matching pending call,
// and textDocument/publishDiagnostics notifications are normalized into a
// [Batch] and handed to the configured [Publisher]. Close stops the read loop
// and is idempotent; it is safe to call from any goroutine, including
// concurrently with a pending call.
type Client struct {
	pub     Publisher
	session string

	writeMu sync.Mutex
	w       io.Writer

	transport io.Closer
	closeOnce sync.Once
	closed    chan struct{} // closed by Close; unblocks any pending call immediately
	readDone  chan struct{} // closed when the read loop returns

	nextID atomic.Int64

	pendingMu sync.Mutex
	pending   map[int64]chan callResult
}

// NewClient wraps t as an LSP connection, publishing every
// textDocument/publishDiagnostics notification it receives to pub tagged with
// session. The background read loop starts immediately; call [Client.Close]
// to stop it and release the Transport.
func NewClient(t Transport, pub Publisher, session string) *Client {
	c := &Client{
		pub:       pub,
		session:   session,
		w:         t,
		transport: t,
		closed:    make(chan struct{}),
		readDone:  make(chan struct{}),
		pending:   make(map[int64]chan callResult),
	}
	go c.readLoop(bufio.NewReader(t))
	return c
}

// Initialize sends the LSP "initialize" request with rootURI and, on success,
// the "initialized" notification the spec requires immediately after.
func (c *Client) Initialize(ctx context.Context, rootURI string) error {
	params := map[string]any{
		"processId":    nil,
		"rootUri":      rootURI,
		"capabilities": map[string]any{},
	}
	if _, err := c.call(ctx, "initialize", params); err != nil {
		return fmt.Errorf("lsp: initialize: %w", err)
	}
	if err := c.notify("initialized", map[string]any{}); err != nil {
		return fmt.Errorf("lsp: initialized: %w", err)
	}
	return nil
}

// DidOpen sends a textDocument/didOpen notification announcing uri as open
// with the given LSP languageID and full text (version 1).
func (c *Client) DidOpen(_ context.Context, uri, languageID, text string) error {
	params := map[string]any{
		"textDocument": map[string]any{
			"uri":        uri,
			"languageId": languageID,
			"version":    1,
			"text":       text,
		},
	}
	if err := c.notify("textDocument/didOpen", params); err != nil {
		return fmt.Errorf("lsp: didOpen: %w", err)
	}
	return nil
}

// DidClose sends a textDocument/didClose notification announcing uri as
// closed.
func (c *Client) DidClose(_ context.Context, uri string) error {
	params := map[string]any{
		"textDocument": map[string]any{"uri": uri},
	}
	if err := c.notify("textDocument/didClose", params); err != nil {
		return fmt.Errorf("lsp: didClose: %w", err)
	}
	return nil
}

// Shutdown sends the LSP "shutdown" request and, on success, the "exit"
// notification the spec requires immediately after. It does not close the
// Transport — call [Client.Close] once the server has had a chance to exit.
func (c *Client) Shutdown(ctx context.Context) error {
	if _, err := c.call(ctx, "shutdown", nil); err != nil {
		return fmt.Errorf("lsp: shutdown: %w", err)
	}
	if err := c.notify("exit", nil); err != nil {
		return fmt.Errorf("lsp: exit: %w", err)
	}
	return nil
}

// Close stops the read loop and closes the Transport. It is idempotent and
// safe to call concurrently with a pending call, which unblocks immediately
// with [ErrClosed] rather than waiting on the closed Transport.
func (c *Client) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.closed)
		err = c.transport.Close()
		<-c.readDone
	})
	return err
}

// call sends an LSP request and blocks for its response, ctx cancellation, or
// Close, whichever comes first.
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	select {
	case <-c.closed:
		return nil, ErrClosed
	default:
	}

	id := c.nextID.Add(1)
	ch := make(chan callResult, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()

	raw, err := marshalParams(params)
	if err != nil {
		c.dropPending(id)
		return nil, fmt.Errorf("lsp: marshal %s params: %w", method, err)
	}
	body, err := json.Marshal(requestMessage{JSONRPC: jsonrpcVersion, ID: id, Method: method, Params: raw})
	if err != nil {
		c.dropPending(id)
		return nil, fmt.Errorf("lsp: marshal %s request: %w", method, err)
	}
	if err := c.writeFrame(body); err != nil {
		c.dropPending(id)
		return nil, fmt.Errorf("lsp: send %s: %w", method, err)
	}

	select {
	case res := <-ch:
		if res.err != nil {
			return nil, res.err
		}
		if res.resp.Error != nil {
			return nil, res.resp.Error
		}
		return res.resp.Result, nil
	case <-ctx.Done():
		c.dropPending(id)
		return nil, ctx.Err()
	case <-c.closed:
		c.dropPending(id)
		return nil, ErrClosed
	}
}

// notify sends an LSP notification (no response expected).
func (c *Client) notify(method string, params any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return fmt.Errorf("lsp: marshal %s params: %w", method, err)
	}
	body, err := json.Marshal(notificationMessage{JSONRPC: jsonrpcVersion, Method: method, Params: raw})
	if err != nil {
		return fmt.Errorf("lsp: marshal %s notification: %w", method, err)
	}
	return c.writeFrame(body)
}

// marshalParams marshals params, or returns a nil RawMessage when params is
// nil so the request/notification's "params" field is omitted entirely
// (omitempty on a json.RawMessage checks length) rather than sent as a
// literal JSON null.
func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	return json.Marshal(params)
}

// dropPending removes id from the pending table without delivering a result
// — used when the caller is abandoning the call itself (ctx done, Close, or a
// send failure) rather than receiving one from the read loop.
func (c *Client) dropPending(id int64) {
	c.pendingMu.Lock()
	delete(c.pending, id)
	c.pendingMu.Unlock()
}

// writeFrame serializes writes to Transport: the read loop and every caller
// of call/notify may run concurrently, and the LSP base protocol requires
// each frame's header+body to be written without another frame interleaved.
func (c *Client) writeFrame(body []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeFrame(c.w, body)
}

// readLoop is the Client's single background goroutine: it reads framed
// messages off r until the Transport reports an error (EOF on a normal close,
// or any other read failure), routing each to either a pending call or
// [Client.handleDiagnostics]. It never exits except on a read error, so
// Close's transport.Close() is what always terminates it.
func (c *Client) readLoop(r *bufio.Reader) {
	defer close(c.readDone)
	for {
		frame, err := readFrame(r)
		if err != nil {
			c.failPending(fmt.Errorf("lsp: connection closed: %w", err))
			return
		}
		var msg inboundMessage
		if err := json.Unmarshal(frame, &msg); err != nil {
			// A malformed frame from a well-behaved server should not happen;
			// drop it rather than crash the read loop over one bad message.
			continue
		}
		switch {
		case msg.ID != nil && msg.Method == "":
			c.deliver(*msg.ID, responseMessage{JSONRPC: msg.JSONRPC, ID: *msg.ID, Result: msg.Result, Error: msg.Error})
		case msg.Method == "textDocument/publishDiagnostics":
			c.handleDiagnostics(msg.Params)
		default:
			// Unrecognized notification or a server-to-client request: ignore,
			// per LSP's well-behaved-client rule for methods it doesn't handle.
		}
	}
}

// deliver routes a decoded response to its pending caller, if still
// outstanding (it may have already been abandoned via ctx cancellation or
// Close, in which case this is a no-op).
func (c *Client) deliver(id int64, resp responseMessage) {
	c.pendingMu.Lock()
	ch, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.pendingMu.Unlock()
	if ok {
		ch <- callResult{resp: resp}
	}
}

// failPending unblocks every still-outstanding call with err — used once, when
// readLoop exits, so a call blocked on a connection that just died doesn't
// hang forever waiting on ctx or Close.
func (c *Client) failPending(err error) {
	c.pendingMu.Lock()
	pending := c.pending
	c.pending = make(map[int64]chan callResult)
	c.pendingMu.Unlock()
	for _, ch := range pending {
		ch <- callResult{err: err}
	}
}

// wirePosition, wireRange, and wireDiagnostic mirror the LSP
// textDocument/publishDiagnostics wire shapes; handleDiagnostics normalizes
// them into [Diagnostic].
type wirePosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type wireRange struct {
	Start wirePosition `json:"start"`
	End   wirePosition `json:"end"`
}

type wireDiagnostic struct {
	Range    wireRange       `json:"range"`
	Severity int             `json:"severity"`
	Code     json.RawMessage `json:"code,omitempty"` // LSP: integer | string
	Source   string          `json:"source"`
	Message  string          `json:"message"`
}

type publishDiagnosticsParams struct {
	URI         string           `json:"uri"`
	Version     int              `json:"version"`
	Diagnostics []wireDiagnostic `json:"diagnostics"`
}

// handleDiagnostics decodes a textDocument/publishDiagnostics notification's
// params and publishes the normalized Batch. context.Background() is used for
// the Publish call because the read loop is a long-lived background process
// with no per-request context of its own — there is no caller waiting on this
// particular notification.
func (c *Client) handleDiagnostics(raw json.RawMessage) {
	var p publishDiagnosticsParams
	if err := json.Unmarshal(raw, &p); err != nil {
		// A malformed notification from a well-behaved server should not
		// happen; drop it rather than crash the read loop.
		return
	}
	items := make([]Diagnostic, len(p.Diagnostics))
	for i, d := range p.Diagnostics {
		items[i] = Diagnostic{
			Range: Range{
				Start: Position{Line: d.Range.Start.Line, Character: d.Range.Start.Character},
				End:   Position{Line: d.Range.End.Line, Character: d.Range.End.Character},
			},
			Severity: Severity(d.Severity),
			Code:     decodeCode(d.Code),
			Source:   d.Source,
			Message:  d.Message,
		}
	}
	c.pub.Publish(context.Background(), c.session, Batch{URI: p.URI, Version: p.Version, Items: items})
}

// decodeCode normalizes an LSP diagnostic "code" (integer | string on the
// wire) to a string. Absent or unparseable input yields "".
func decodeCode(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}

// processTransport adapts a spawned language server's stdin/stdout pipes plus
// its *exec.Cmd into a Transport: writes go to stdin, reads come from stdout,
// and Close closes stdin (signalling EOF to a well-behaved server) then waits
// for the process to exit.
type processTransport struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
	cmd    *exec.Cmd
}

func (p *processTransport) Write(b []byte) (int, error) { return p.stdin.Write(b) }
func (p *processTransport) Read(b []byte) (int, error)  { return p.stdout.Read(b) }

func (p *processTransport) Close() error {
	closeErr := p.stdin.Close()
	_ = p.stdout.Close()
	_ = p.cmd.Wait()
	return closeErr
}

// Start spawns the language server named by r.Path with r.Args via os/exec,
// wires its stdin/stdout into a Transport, and returns a live Client
// publishing diagnostics to pub tagged with session. r.Path is the absolute
// path exec.LookPath already resolved (see [Registry.Resolve]) — not a shell
// interpolation and not model/user input — so it is safe to pass to
// exec.CommandContext despite gosec's generic G204 warning on dynamic
// commands. This path is not exercised in CI: no LSP servers are installed
// in the CI image, so it has no test coverage here; the faked-Transport tests
// exercise this package's actual logic (framing, routing, diagnostics
// normalization) via [NewClient] directly.
func Start(ctx context.Context, r Resolved, pub Publisher, session string) (*Client, error) {
	cmd := exec.CommandContext(ctx, r.Path, r.Args...) // #nosec G204 -- r.Path resolved by Registry.Resolve via exec.LookPath, not user/model input
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp: start %s: stdin pipe: %w", r.Command, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp: start %s: stdout pipe: %w", r.Command, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("lsp: start %s: %w", r.Command, err)
	}
	t := &processTransport{stdin: stdin, stdout: stdout, cmd: cmd}
	return NewClient(t, pub, session), nil
}
