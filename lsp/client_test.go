package lsp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jedwards1230/agent-sdk-go/lsp"
)

// Tests here build a Transport from an io.Pipe pair and drive a hand-scripted
// fake server against lsp.Client's real wire behavior — deliberately not
// reusing any of the package's own framing helpers, so a bug in
// protocol.go's writeFrame/readFrame can't hide a matching bug in the fake
// server too. Every fake-server goroutine reports outcomes over a channel
// rather than calling t.Fatal/t.Error directly — those must only be called
// from the test's own goroutine.

// pipeTransport turns a paired io.PipeReader/io.PipeWriter into an
// io.ReadWriteCloser (an lsp.Transport). Closing it closes both ends: the
// reader side unblocks any in-flight Read on the peer with io.ErrClosedPipe,
// and the writer side sends io.EOF to the peer's Read.
type pipeTransport struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (p *pipeTransport) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *pipeTransport) Write(b []byte) (int, error) { return p.w.Write(b) }

func (p *pipeTransport) Close() error {
	rerr := p.r.Close()
	werr := p.w.Close()
	if rerr != nil {
		return rerr
	}
	return werr
}

// newPipedTransports returns two Transports wired to each other: writes to
// one arrive as reads on the other.
func newPipedTransports() (client, server *pipeTransport) {
	c2sR, c2sW := io.Pipe() // client writes -> server reads
	s2cR, s2cW := io.Pipe() // server writes -> client reads
	client = &pipeTransport{r: s2cR, w: c2sW}
	server = &pipeTransport{r: c2sR, w: s2cW}
	return client, server
}

// testWriteFrame and testReadFrame independently re-implement the LSP base
// protocol's Content-Length framing for the fake server side of these tests.

func testWriteFrame(w io.Writer, payload []byte) error {
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(payload)); err != nil {
		return fmt.Errorf("write frame header: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("write frame body: %w", err)
	}
	return nil
}

func testReadFrame(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(name), "content-length") {
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return nil, err
			}
			length = n
		}
	}
	if length < 0 {
		return nil, errors.New("fake server: missing content-length")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

type wireMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *wireError      `json:"error,omitempty"`
}

type wireError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// recordingPublisher collects every Batch it is handed, on a buffered
// channel a test can select on without a sleep.
type recordingPublisher struct {
	batches chan recordedBatch
}

type recordedBatch struct {
	session string
	batch   lsp.Batch
}

func newRecordingPublisher() *recordingPublisher {
	return &recordingPublisher{batches: make(chan recordedBatch, 4)}
}

func (p *recordingPublisher) Publish(_ context.Context, session string, batch lsp.Batch) {
	p.batches <- recordedBatch{session: session, batch: batch}
}

const testTimeout = 2 * time.Second

// TestClientDiagnosticsRoundTrip is the scripted diagnostics round-trip: a
// fake server over an in-memory io.Pipe Transport replies to initialize,
// reads the initialized/didOpen notifications, then publishes two
// diagnostics, and the test asserts the configured Publisher receives the
// expected normalized Batch.
func TestClientDiagnosticsRoundTrip(t *testing.T) {
	clientTransport, serverTransport := newPipedTransports()
	pub := newRecordingPublisher()

	serverErrs := make(chan error, 1)
	go func() { serverErrs <- runFakeServer(serverTransport) }()

	c := lsp.NewClient(clientTransport, pub, "sess-1")

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	if err := c.Initialize(ctx, "file:///repo"); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := c.DidOpen(ctx, "file:///repo/foo.go", "go", "package foo\n"); err != nil {
		t.Fatalf("DidOpen: %v", err)
	}

	select {
	case got := <-pub.batches:
		if got.session != "sess-1" {
			t.Errorf("session = %q, want sess-1", got.session)
		}
		wantBatch := lsp.Batch{
			URI:     "file:///repo/foo.go",
			Version: 3,
			Items: []lsp.Diagnostic{
				{
					Range:    lsp.Range{Start: lsp.Position{Line: 11, Character: 2}, End: lsp.Position{Line: 11, Character: 5}},
					Severity: lsp.SeverityError,
					Code:     "undefined",
					Source:   "gopls",
					Message:  "undefined: x",
				},
				{
					Range:    lsp.Range{Start: lsp.Position{Line: 20, Character: 0}, End: lsp.Position{Line: 20, Character: 1}},
					Severity: lsp.SeverityWarning,
					Source:   "gopls",
					Message:  "unused variable y",
				},
			},
		}
		if got.batch.URI != wantBatch.URI || got.batch.Version != wantBatch.Version {
			t.Fatalf("batch = %+v, want URI/Version %+v", got.batch, wantBatch)
		}
		if len(got.batch.Items) != len(wantBatch.Items) {
			t.Fatalf("batch.Items len = %d, want %d (%+v)", len(got.batch.Items), len(wantBatch.Items), got.batch.Items)
		}
		for i, want := range wantBatch.Items {
			gotItem := got.batch.Items[i]
			if gotItem != want {
				t.Errorf("Items[%d] = %+v, want %+v", i, gotItem, want)
			}
		}
		strs := got.batch.Strings()
		if len(strs) != 2 || !strings.Contains(strs[0], "error") || !strings.Contains(strs[0], "undefined: x") {
			t.Errorf("Batch.Strings() = %v, want a rendered error line for the first diagnostic", strs)
		}
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for published diagnostics")
	}

	select {
	case err := <-serverErrs:
		if err != nil {
			t.Fatalf("fake server: %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for fake server to finish its script")
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// runFakeServer plays the scripted server side of the round trip: it reads
// the client's initialize request and replies, reads the initialized and
// didOpen notifications, then sends one publishDiagnostics notification
// carrying two diagnostics. It runs on its own goroutine, so it reports
// failure by return value rather than by calling any *testing.T method.
func runFakeServer(transport io.ReadWriteCloser) error {
	r := bufio.NewReader(transport)

	frame, err := testReadFrame(r)
	if err != nil {
		return fmt.Errorf("read initialize request: %w", err)
	}
	var initReq wireMessage
	if err := json.Unmarshal(frame, &initReq); err != nil {
		return fmt.Errorf("decode initialize request: %w", err)
	}
	if initReq.Method != "initialize" {
		return fmt.Errorf("first request method = %q, want initialize", initReq.Method)
	}
	resultJSON, err := json.Marshal(map[string]any{"capabilities": map[string]any{}})
	if err != nil {
		return fmt.Errorf("marshal initialize result: %w", err)
	}
	respJSON, err := json.Marshal(wireMessage{JSONRPC: "2.0", ID: initReq.ID, Result: resultJSON})
	if err != nil {
		return fmt.Errorf("marshal initialize response: %w", err)
	}
	if err := testWriteFrame(transport, respJSON); err != nil {
		return fmt.Errorf("write initialize response: %w", err)
	}

	frame, err = testReadFrame(r)
	if err != nil {
		return fmt.Errorf("read initialized notification: %w", err)
	}
	var initialized wireMessage
	if err := json.Unmarshal(frame, &initialized); err != nil {
		return fmt.Errorf("decode initialized notification: %w", err)
	}
	if initialized.Method != "initialized" {
		return fmt.Errorf("notification method = %q, want initialized", initialized.Method)
	}

	frame, err = testReadFrame(r)
	if err != nil {
		return fmt.Errorf("read didOpen notification: %w", err)
	}
	var didOpen wireMessage
	if err := json.Unmarshal(frame, &didOpen); err != nil {
		return fmt.Errorf("decode didOpen notification: %w", err)
	}
	if didOpen.Method != "textDocument/didOpen" {
		return fmt.Errorf("notification method = %q, want textDocument/didOpen", didOpen.Method)
	}

	diagParams, err := json.Marshal(map[string]any{
		"uri":     "file:///repo/foo.go",
		"version": 3,
		"diagnostics": []map[string]any{
			{
				"range": map[string]any{
					"start": map[string]any{"line": 11, "character": 2},
					"end":   map[string]any{"line": 11, "character": 5},
				},
				"severity": 1,
				"code":     "undefined",
				"source":   "gopls",
				"message":  "undefined: x",
			},
			{
				"range": map[string]any{
					"start": map[string]any{"line": 20, "character": 0},
					"end":   map[string]any{"line": 20, "character": 1},
				},
				"severity": 2,
				"source":   "gopls",
				"message":  "unused variable y",
			},
		},
	})
	if err != nil {
		return fmt.Errorf("marshal publishDiagnostics params: %w", err)
	}
	noteJSON, err := json.Marshal(wireMessage{JSONRPC: "2.0", Method: "textDocument/publishDiagnostics", Params: diagParams})
	if err != nil {
		return fmt.Errorf("marshal publishDiagnostics notification: %w", err)
	}
	if err := testWriteFrame(transport, noteJSON); err != nil {
		return fmt.Errorf("write publishDiagnostics notification: %w", err)
	}

	// The script is complete: the test drives no further client writes
	// before it closes the Client, so there is nothing left to read.
	return nil
}

// TestClientErrorResponsePropagates covers a request that gets a JSON-RPC
// error response: the error must propagate to the caller as a Go error.
func TestClientErrorResponsePropagates(t *testing.T) {
	clientTransport, serverTransport := newPipedTransports()
	pub := newRecordingPublisher()

	serverErrs := make(chan error, 1)
	go func() { serverErrs <- runErrorFakeServer(serverTransport) }()

	c := lsp.NewClient(clientTransport, pub, "sess-err")
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	err := c.Initialize(ctx, "file:///repo")
	if err == nil {
		t.Fatal("Initialize err = nil, want the server's JSON-RPC error to propagate")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("Initialize err = %v, want it to mention the server error message", err)
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case err := <-serverErrs:
		if err != nil {
			t.Fatalf("fake server: %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for fake server goroutine to exit")
	}
}

// runErrorFakeServer replies to the client's first request with a JSON-RPC
// error, then drains frames until the client closes the transport.
func runErrorFakeServer(transport io.ReadWriteCloser) error {
	r := bufio.NewReader(transport)
	frame, err := testReadFrame(r)
	if err != nil {
		return fmt.Errorf("read request: %w", err)
	}
	var req wireMessage
	if err := json.Unmarshal(frame, &req); err != nil {
		return fmt.Errorf("decode request: %w", err)
	}
	respJSON, err := json.Marshal(wireMessage{JSONRPC: "2.0", ID: req.ID, Error: &wireError{Code: -32601, Message: "boom"}})
	if err != nil {
		return fmt.Errorf("marshal error response: %w", err)
	}
	if err := testWriteFrame(transport, respJSON); err != nil {
		return fmt.Errorf("write error response: %w", err)
	}
	for {
		if _, err := testReadFrame(r); err != nil {
			return nil // the client closing the transport ends the script successfully
		}
	}
}

// TestClientCloseUnblocksPendingCall covers Close racing a pending call:
// Close must unblock it immediately with ErrClosed, and the read loop must
// exit with no goroutine left running.
func TestClientCloseUnblocksPendingCall(t *testing.T) {
	clientTransport, serverTransport := newPipedTransports()
	pub := newRecordingPublisher()

	// The fake server reads the request but never replies, so the pending
	// Initialize call can only be unblocked by Close. serverReadReq closes
	// once the request frame has actually been read off the wire — proof
	// that Client.call already registered the pending entry and wrote the
	// request (registration happens before the write) — so waiting on that
	// signal, not a sleep, guarantees Close races a genuinely pending call.
	serverReadReq := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		r := bufio.NewReader(serverTransport)
		_, _ = testReadFrame(r)
		close(serverReadReq)
		// Drain until the pipe is torn down by Close.
		for {
			if _, err := testReadFrame(r); err != nil {
				return
			}
		}
	}()

	c := lsp.NewClient(clientTransport, pub, "sess-block")

	initErrCh := make(chan error, 1)
	go func() {
		initErrCh <- c.Initialize(context.Background(), "file:///repo")
	}()

	select {
	case <-serverReadReq:
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for the fake server to read the initialize request")
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case err := <-initErrCh:
		if !errors.Is(err, lsp.ErrClosed) {
			t.Errorf("Initialize err = %v, want it to wrap lsp.ErrClosed", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Close did not unblock the pending Initialize call")
	}

	select {
	case <-serverDone:
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for fake server goroutine to exit after Close")
	}

	// Close must be idempotent.
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
