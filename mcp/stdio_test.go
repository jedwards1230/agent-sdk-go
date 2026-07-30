package mcp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jedwards1230/agent-sdk-go/mcp"
	"github.com/jedwards1230/agent-sdk-go/tool"
)

// Tests here build a duplex stream from an io.Pipe pair and drive a
// hand-scripted fake server against mcp.Client's real wire behavior —
// deliberately not reusing any of the package's own framing helpers, the
// same discipline lsp/client_test.go uses. Every fake-server goroutine
// reports outcomes over a channel rather than calling t.Fatal/t.Error
// directly, since those must only be called from the test's own goroutine.

const testTimeout = 2 * time.Second

// pipeStream turns a paired io.PipeReader/io.PipeWriter into an
// io.ReadWriteCloser.
type pipeStream struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (p *pipeStream) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *pipeStream) Write(b []byte) (int, error) { return p.w.Write(b) }

func (p *pipeStream) Close() error {
	rerr := p.r.Close()
	werr := p.w.Close()
	if rerr != nil {
		return rerr
	}
	return werr
}

// newPipedStreams returns two streams wired to each other: writes to one
// arrive as reads on the other.
func newPipedStreams() (client, server *pipeStream) {
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	client = &pipeStream{r: s2cR, w: c2sW}
	server = &pipeStream{r: c2sR, w: s2cW}
	return client, server
}

// wireMessage is a hand-rolled JSON-RPC 2.0 envelope for the fake server
// side of these tests, independent of the package's own rpc* types.
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

func writeLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", b)
	return err
}

func readLine(r *bufio.Reader) (wireMessage, error) {
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return wireMessage{}, err
	}
	var msg wireMessage
	if jsonErr := json.Unmarshal([]byte(strings.TrimRight(line, "\r\n")), &msg); jsonErr != nil {
		return wireMessage{}, fmt.Errorf("decode line %q: %w", line, jsonErr)
	}
	return msg, nil
}

// handshake reads the initialize request off r, replies with a minimal valid
// result, then reads and validates the required initialized notification.
func handshake(r *bufio.Reader, w io.Writer) error {
	req, err := readLine(r)
	if err != nil {
		return fmt.Errorf("read initialize: %w", err)
	}
	if req.Method != "initialize" {
		return fmt.Errorf("first request method = %q, want initialize", req.Method)
	}
	result, err := json.Marshal(map[string]any{
		"protocolVersion": "2025-06-18",
		"serverInfo":      map[string]string{"name": "fake", "version": "0.1.0"},
	})
	if err != nil {
		return err
	}
	if err := writeLine(w, wireMessage{JSONRPC: "2.0", ID: req.ID, Result: result}); err != nil {
		return fmt.Errorf("write initialize response: %w", err)
	}
	note, err := readLine(r)
	if err != nil {
		return fmt.Errorf("read initialized notification: %w", err)
	}
	if note.Method != "notifications/initialized" {
		return fmt.Errorf("notification method = %q, want notifications/initialized", note.Method)
	}
	return nil
}

func newTestClient(t *testing.T, opts ...mcp.Option) (c *mcp.Client, server *pipeStream) {
	t.Helper()
	clientStream, serverStream := newPipedStreams()
	c = mcp.NewStdio(clientStream, opts...)
	t.Cleanup(func() { _ = c.Close() })
	return c, serverStream
}

func ctxWithTimeout(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	t.Cleanup(cancel)
	return ctx
}

// TestInitializeHandshake covers the initialize round trip end to end: the
// request, the server's info coming back, and the required initialized
// notification landing on the wire afterward.
func TestInitializeHandshake(t *testing.T) {
	c, server := newTestClient(t)

	serverErrs := make(chan error, 1)
	go func() {
		r := bufio.NewReader(server)
		serverErrs <- handshake(r, server)
	}()

	info, err := c.Initialize(ctxWithTimeout(t), mcp.ClientInfo{Name: "test", Version: "0.0.0"})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if info.Name != "fake" || info.Version != "0.1.0" {
		t.Errorf("ServerInfo = %+v, want name=fake version=0.1.0", info)
	}

	select {
	case err := <-serverErrs:
		if err != nil {
			t.Fatalf("fake server: %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for fake server")
	}
}

// TestListToolsProjection covers tools/list -> mcp.Project -> []tool.Tool:
// name qualification, description, and schema conversion all reach the
// returned tool.Tool.
func TestListToolsProjection(t *testing.T) {
	c, server := newTestClient(t)

	serverErrs := make(chan error, 1)
	go func() {
		r := bufio.NewReader(server)
		if err := handshake(r, server); err != nil {
			serverErrs <- err
			return
		}
		req, err := readLine(r)
		if err != nil {
			serverErrs <- fmt.Errorf("read tools/list: %w", err)
			return
		}
		if req.Method != "tools/list" {
			serverErrs <- fmt.Errorf("method = %q, want tools/list", req.Method)
			return
		}
		result, err := json.Marshal(map[string]any{
			"tools": []map[string]any{
				{
					"name":        "search",
					"description": "search the wiki",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"query": map[string]any{"type": "string", "description": "the query"},
						},
						"required": []string{"query"},
					},
				},
			},
		})
		if err != nil {
			serverErrs <- err
			return
		}
		serverErrs <- writeLine(server, wireMessage{JSONRPC: "2.0", ID: req.ID, Result: result})
	}()

	ctx := ctxWithTimeout(t)
	if _, err := c.Initialize(ctx, mcp.ClientInfo{Name: "test"}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	tools, err := mcp.Project(ctx, c, "wiki")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}
	got := tools[0]
	if want := "mcp__wiki__search"; got.Name() != want {
		t.Errorf("Name() = %q, want %q", got.Name(), want)
	}
	if got.Description() != "search the wiki" {
		t.Errorf("Description() = %q", got.Description())
	}
	spec := got.Spec()
	if spec.Type != "object" || len(spec.Required) != 1 || spec.Required[0] != "query" {
		t.Errorf("Spec() = %+v, want object schema requiring query", spec)
	}
	prop, ok := spec.Properties["query"]
	if !ok || prop.Type != "string" {
		t.Errorf("Spec().Properties[query] = %+v, ok=%v, want type=string", prop, ok)
	}

	select {
	case err := <-serverErrs:
		if err != nil {
			t.Fatalf("fake server: %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for fake server")
	}
}

// TestCallToolSuccess covers a projected tool's Run performing exactly one
// tools/call and turning the server's content blocks into tool.Result.
func TestCallToolSuccess(t *testing.T) {
	c, server := newTestClient(t)

	serverErrs := make(chan error, 1)
	go func() {
		r := bufio.NewReader(server)
		if err := handshake(r, server); err != nil {
			serverErrs <- err
			return
		}
		listReq, err := readLine(r)
		if err != nil {
			serverErrs <- err
			return
		}
		listResult, _ := json.Marshal(map[string]any{
			"tools": []map[string]any{{"name": "echo", "description": "echoes input", "inputSchema": map[string]any{"type": "object"}}},
		})
		if err := writeLine(server, wireMessage{JSONRPC: "2.0", ID: listReq.ID, Result: listResult}); err != nil {
			serverErrs <- err
			return
		}

		callReq, err := readLine(r)
		if err != nil {
			serverErrs <- err
			return
		}
		if callReq.Method != "tools/call" {
			serverErrs <- fmt.Errorf("method = %q, want tools/call", callReq.Method)
			return
		}
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(callReq.Params, &params); err != nil {
			serverErrs <- err
			return
		}
		if params.Name != "echo" {
			serverErrs <- fmt.Errorf("tools/call name = %q, want echo", params.Name)
			return
		}
		callResult, _ := json.Marshal(map[string]any{
			"content": []map[string]any{{"type": "text", "text": "hello back"}},
			"isError": false,
		})
		serverErrs <- writeLine(server, wireMessage{JSONRPC: "2.0", ID: callReq.ID, Result: callResult})
	}()

	ctx := ctxWithTimeout(t)
	if _, err := c.Initialize(ctx, mcp.ClientInfo{Name: "test"}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	tools, err := mcp.Project(ctx, c, "srv")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	result, err := tools[0].Run(ctx, json.RawMessage(`{"msg":"hi"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Errorf("Run result.IsError = true, want false")
	}
	if result.Content != "hello back" {
		t.Errorf("Run result.Content = %q, want %q", result.Content, "hello back")
	}

	select {
	case err := <-serverErrs:
		if err != nil {
			t.Fatalf("fake server: %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for fake server")
	}
}

// TestCallToolJSONRPCError covers a tools/call that gets a JSON-RPC-level
// error response (not a tool-level isError): Run must surface it as a
// tool.Result{IsError: true}, never as a Go error, since ctx was never
// cancelled by the caller.
func TestCallToolJSONRPCError(t *testing.T) {
	c, server := newTestClient(t)

	serverErrs := make(chan error, 1)
	go func() {
		r := bufio.NewReader(server)
		if err := handshake(r, server); err != nil {
			serverErrs <- err
			return
		}
		req, err := readLine(r)
		if err != nil {
			serverErrs <- err
			return
		}
		serverErrs <- writeLine(server, wireMessage{JSONRPC: "2.0", ID: req.ID, Error: &wireError{Code: -32601, Message: "unknown tool"}})
	}()

	ctx := ctxWithTimeout(t)
	if _, err := c.Initialize(ctx, mcp.ClientInfo{Name: "test"}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	res, err := c.CallTool(ctx, "missing", nil)
	if err == nil {
		t.Fatal("CallTool err = nil, want the JSON-RPC error to propagate")
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("CallTool err = %v, want it to mention the server error", err)
	}
	_ = res

	select {
	case err := <-serverErrs:
		if err != nil {
			t.Fatalf("fake server: %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for fake server")
	}
}

// TestRunSurfacesJSONRPCErrorAsResult covers the projectedTool.Run half of
// the previous test: a JSON-RPC error from CallTool becomes an IsError
// tool.Result, not a Go error, when the caller's ctx was never cancelled.
func TestRunSurfacesJSONRPCErrorAsResult(t *testing.T) {
	c, server := newTestClient(t)

	serverErrs := make(chan error, 1)
	go func() {
		r := bufio.NewReader(server)
		if err := handshake(r, server); err != nil {
			serverErrs <- err
			return
		}
		listReq, err := readLine(r)
		if err != nil {
			serverErrs <- err
			return
		}
		listResult, _ := json.Marshal(map[string]any{
			"tools": []map[string]any{{"name": "broken", "description": "", "inputSchema": map[string]any{"type": "object"}}},
		})
		if err := writeLine(server, wireMessage{JSONRPC: "2.0", ID: listReq.ID, Result: listResult}); err != nil {
			serverErrs <- err
			return
		}
		callReq, err := readLine(r)
		if err != nil {
			serverErrs <- err
			return
		}
		serverErrs <- writeLine(server, wireMessage{JSONRPC: "2.0", ID: callReq.ID, Error: &wireError{Code: -32602, Message: "bad params"}})
	}()

	ctx := ctxWithTimeout(t)
	if _, err := c.Initialize(ctx, mcp.ClientInfo{Name: "test"}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	tools, err := mcp.Project(ctx, c, "srv")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	result, err := tools[0].Run(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Run err = %v, want nil (a nil-error IsError result)", err)
	}
	if !result.IsError {
		t.Errorf("Run result.IsError = false, want true")
	}
	if !strings.Contains(result.Content, "bad params") {
		t.Errorf("Run result.Content = %q, want it to mention the failure", result.Content)
	}

	select {
	case err := <-serverErrs:
		if err != nil {
			t.Fatalf("fake server: %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for fake server")
	}
}

// TestRunTimeoutSurfacesAsResult covers a server that never responds: the
// Client's own per-call timeout must fire and Run must report it as
// IsError, never hang the turn or return a Go error, since the caller's own
// ctx (background, no deadline) was never itself cancelled.
func TestRunTimeoutSurfacesAsResult(t *testing.T) {
	c, server := newTestClient(t, mcp.WithCallTimeout(50*time.Millisecond))

	serverErrs := make(chan error, 1)
	go func() {
		r := bufio.NewReader(server)
		if err := handshake(r, server); err != nil {
			serverErrs <- err
			return
		}
		listReq, err := readLine(r)
		if err != nil {
			serverErrs <- err
			return
		}
		listResult, _ := json.Marshal(map[string]any{
			"tools": []map[string]any{{"name": "slow", "description": "", "inputSchema": map[string]any{"type": "object"}}},
		})
		if err := writeLine(server, wireMessage{JSONRPC: "2.0", ID: listReq.ID, Result: listResult}); err != nil {
			serverErrs <- err
			return
		}
		// Read the tools/call request but never reply — the server hangs.
		if _, err := readLine(r); err != nil {
			serverErrs <- err
			return
		}
		serverErrs <- nil
	}()

	initCtx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if _, err := c.Initialize(initCtx, mcp.ClientInfo{Name: "test"}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	tools, err := mcp.Project(initCtx, c, "srv")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	// The caller's own context has NO deadline: only the Client's internal
	// call timeout should fire.
	runCtx := context.Background()
	start := time.Now()
	result, err := tools[0].Run(runCtx, json.RawMessage(`{}`))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run err = %v, want nil (an IsError result)", err)
	}
	if !result.IsError {
		t.Errorf("Run result.IsError = false, want true")
	}
	if elapsed > testTimeout {
		t.Errorf("Run took %v, want it bounded by the ~50ms call timeout", elapsed)
	}

	select {
	case err := <-serverErrs:
		if err != nil {
			t.Fatalf("fake server: %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for fake server")
	}
}

// TestRunContextCancellationIsAnError covers the OTHER half of the ctx.Err()
// rule: when the CALLER's own ctx is cancelled mid-call, Run must return it
// as a real Go error (the loop aborts the turn), not as an IsError result.
func TestRunContextCancellationIsAnError(t *testing.T) {
	c, server := newTestClient(t)

	serverErrs := make(chan error, 1)
	reqRead := make(chan struct{})
	go func() {
		r := bufio.NewReader(server)
		if err := handshake(r, server); err != nil {
			serverErrs <- err
			return
		}
		listReq, err := readLine(r)
		if err != nil {
			serverErrs <- err
			return
		}
		listResult, _ := json.Marshal(map[string]any{
			"tools": []map[string]any{{"name": "slow", "description": "", "inputSchema": map[string]any{"type": "object"}}},
		})
		if err := writeLine(server, wireMessage{JSONRPC: "2.0", ID: listReq.ID, Result: listResult}); err != nil {
			serverErrs <- err
			return
		}
		if _, err := readLine(r); err != nil {
			serverErrs <- err
			return
		}
		close(reqRead)
		// Never reply: the test cancels its own ctx instead.
		serverErrs <- nil
	}()

	initCtx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if _, err := c.Initialize(initCtx, mcp.ClientInfo{Name: "test"}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	tools, err := mcp.Project(initCtx, c, "srv")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	runCtx, runCancel := context.WithCancel(context.Background())
	resultCh := make(chan struct {
		res tool.Result
		err error
	}, 1)
	go func() {
		res, err := tools[0].Run(runCtx, json.RawMessage(`{}`))
		resultCh <- struct {
			res tool.Result
			err error
		}{res, err}
	}()

	select {
	case <-reqRead:
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for the server to read the tools/call request")
	}
	runCancel()

	select {
	case got := <-resultCh:
		if got.err == nil {
			t.Fatalf("Run err = nil, want a real error from the cancelled ctx")
		}
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for Run to return after ctx cancellation")
	}

	select {
	case err := <-serverErrs:
		if err != nil {
			t.Fatalf("fake server: %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for fake server")
	}
}

// TestServerDiesMidSessionSurfacesAsResult covers a server that closes its
// connection mid-call (crash, kill): the call must fail, and Run must
// report it as IsError, never a Go error.
func TestServerDiesMidSessionSurfacesAsResult(t *testing.T) {
	c, server := newTestClient(t)

	serverErrs := make(chan error, 1)
	go func() {
		r := bufio.NewReader(server)
		if err := handshake(r, server); err != nil {
			serverErrs <- err
			return
		}
		listReq, err := readLine(r)
		if err != nil {
			serverErrs <- err
			return
		}
		listResult, _ := json.Marshal(map[string]any{
			"tools": []map[string]any{{"name": "doomed", "description": "", "inputSchema": map[string]any{"type": "object"}}},
		})
		if err := writeLine(server, wireMessage{JSONRPC: "2.0", ID: listReq.ID, Result: listResult}); err != nil {
			serverErrs <- err
			return
		}
		if _, err := readLine(r); err != nil {
			serverErrs <- err
			return
		}
		serverErrs <- server.Close() // simulate a crash: close instead of replying
	}()

	ctx := ctxWithTimeout(t)
	if _, err := c.Initialize(ctx, mcp.ClientInfo{Name: "test"}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	tools, err := mcp.Project(ctx, c, "srv")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	result, err := tools[0].Run(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Run err = %v, want nil (an IsError result)", err)
	}
	if !result.IsError {
		t.Errorf("Run result.IsError = false, want true for a dead server")
	}

	select {
	case err := <-serverErrs:
		if err != nil {
			t.Fatalf("fake server: %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for fake server")
	}
}

// TestMalformedFrameIsDropped covers a garbage line arriving before a real
// response: the read loop must drop it and keep routing subsequent frames
// correctly, exactly like lsp.Client's malformed-frame handling.
func TestMalformedFrameIsDropped(t *testing.T) {
	c, server := newTestClient(t)

	serverErrs := make(chan error, 1)
	go func() {
		r := bufio.NewReader(server)
		req, err := readLine(r)
		if err != nil {
			serverErrs <- err
			return
		}
		if req.Method != "initialize" {
			serverErrs <- fmt.Errorf("method = %q, want initialize", req.Method)
			return
		}
		if _, err := fmt.Fprintf(server, "not json at all\n"); err != nil {
			serverErrs <- err
			return
		}
		result, _ := json.Marshal(map[string]any{"serverInfo": map[string]string{"name": "fake"}})
		if err := writeLine(server, wireMessage{JSONRPC: "2.0", ID: req.ID, Result: result}); err != nil {
			serverErrs <- err
			return
		}
		note, err := readLine(r)
		if err != nil {
			serverErrs <- fmt.Errorf("read initialized notification: %w", err)
			return
		}
		if note.Method != "notifications/initialized" {
			serverErrs <- fmt.Errorf("notification method = %q, want notifications/initialized", note.Method)
			return
		}
		serverErrs <- nil
	}()

	ctx := ctxWithTimeout(t)
	info, err := c.Initialize(ctx, mcp.ClientInfo{Name: "test"})
	if err != nil {
		t.Fatalf("Initialize: %v (a preceding malformed line must not break the real response)", err)
	}
	if info.Name != "fake" {
		t.Errorf("ServerInfo.Name = %q, want fake", info.Name)
	}

	select {
	case err := <-serverErrs:
		if err != nil {
			t.Fatalf("fake server: %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for fake server")
	}
}

// TestConcurrentCalls drives many concurrent CallTool invocations against a
// single-threaded echo fake server and asserts every call gets back its own
// matching argument — proof the id-keyed pending map correctly correlates
// interleaved requests/responses. Run under -race.
func TestConcurrentCalls(t *testing.T) {
	c, server := newTestClient(t)

	const n = 50
	serverErrs := make(chan error, 1)
	go func() {
		r := bufio.NewReader(server)
		if err := handshake(r, server); err != nil {
			serverErrs <- err
			return
		}
		for i := 0; i < n; i++ {
			req, err := readLine(r)
			if err != nil {
				serverErrs <- err
				return
			}
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				serverErrs <- err
				return
			}
			result, _ := json.Marshal(map[string]any{
				"content": []map[string]any{{"type": "text", "text": string(params.Arguments)}},
			})
			if err := writeLine(server, wireMessage{JSONRPC: "2.0", ID: req.ID, Result: result}); err != nil {
				serverErrs <- err
				return
			}
		}
		serverErrs <- nil
	}()

	ctx := ctxWithTimeout(t)
	if _, err := c.Initialize(ctx, mcp.ClientInfo{Name: "test"}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args := json.RawMessage(fmt.Sprintf(`{"i":%d}`, i))
			res, err := c.CallTool(ctx, "echo", args)
			if err != nil {
				t.Errorf("CallTool(%d): %v", i, err)
				return
			}
			if res.Text != string(args) {
				t.Errorf("CallTool(%d) result = %q, want %q", i, res.Text, string(args))
			}
		}(i)
	}
	wg.Wait()

	select {
	case err := <-serverErrs:
		if err != nil {
			t.Fatalf("fake server: %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for fake server")
	}
}
