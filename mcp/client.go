package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"
)

// ErrClosed is returned by any [Client] method invoked after (or racing) a
// call to [Client.Close], including a call already in flight when Close runs.
var ErrClosed = errors.New("mcp: client closed")

// DefaultConnectTimeout bounds how long [Client.Initialize] waits for the
// "initialize" handshake before giving up. It is a default, not a limit:
// override per-Client with [WithConnectTimeout].
const DefaultConnectTimeout = 10 * time.Second

// DefaultCallTimeout bounds how long a single "tools/list" or "tools/call"
// request waits for its response. It is a default, not a limit: override
// per-Client with [WithCallTimeout]. This is also the timeout a dead or
// hung server hits before a projected tool's Run reports it to the model as
// [tool.Result.IsError] rather than hanging the turn — see [Project].
const DefaultCallTimeout = 60 * time.Second

// transport is how a [Client] reaches an MCP server: a subprocess over stdio
// ([Start]) or an HTTP(S) endpoint ([NewHTTP]). It operates on already
// request-framed bytes (the [Client] owns id assignment and JSON-RPC
// envelope construction) so each transport only owns wire delivery: stdio
// correlates a response to its request by id over a persistent duplex byte
// stream; HTTP correlates by virtue of being a single request/response round
// trip (or one SSE stream scoped to that round trip).
type transport interface {
	// roundTrip sends a JSON-RPC request frame carrying id and returns its
	// matching response. It blocks until the response arrives, ctx is done,
	// or the transport is closed.
	roundTrip(ctx context.Context, id int64, frame []byte) (rpcResponse, error)
	// send writes a one-way notification frame; no response is read.
	send(ctx context.Context, frame []byte) error
	// close releases the transport's resources. Idempotent.
	close() error
}

// Client is an MCP client speaking JSON-RPC 2.0 to one server over a
// [transport]. It owns the MCP lifecycle (initialize handshake, tools/list,
// tools/call) and nothing else — server configuration, credential
// resolution, reconnection, and readiness windows are the consuming
// application's job (see docs/DESIGN.md "MCP (M7)"). Construct one with
// [Start] (subprocess/stdio) or [NewHTTP] (streamable HTTP); [NewStdio] wires
// an already-open [io.ReadWriteCloser], the seam tests use.
type Client struct {
	t              transport
	nextID         atomic.Int64
	connectTimeout time.Duration
	callTimeout    time.Duration
	log            *slog.Logger
}

// Option configures a [Client] at construction.
type Option func(*Client)

// WithConnectTimeout overrides [DefaultConnectTimeout] for one Client.
func WithConnectTimeout(d time.Duration) Option {
	return func(c *Client) { c.connectTimeout = d }
}

// WithCallTimeout overrides [DefaultCallTimeout] for one Client.
func WithCallTimeout(d time.Duration) Option {
	return func(c *Client) { c.callTimeout = d }
}

// WithLogger attaches an optional structured logger for otherwise-silent
// diagnostics (a malformed frame dropped, the read loop exiting on a
// transport death no in-flight call observed). nil (the default) keeps the
// client silent — the SDK's optional-*slog.Logger instrumentation seam (see
// docs/DESIGN.md "Instrumentation seams"), mirroring [lsp.WithLogger].
func WithLogger(l *slog.Logger) Option {
	return func(c *Client) { c.log = l }
}

// newClient builds a Client with opts applied over the package defaults, and
// a normalized non-nil logger, but no transport yet. It is unexported:
// callers reach it only through a transport constructor ([Start], [NewHTTP],
// [NewStdio]), each of which sets c.t once its transport is built — some
// transports (stdio) need the resolved logger to construct themselves, which
// is why transport assembly isn't folded into this function.
func newClient(opts ...Option) *Client {
	c := &Client{
		connectTimeout: DefaultConnectTimeout,
		callTimeout:    DefaultCallTimeout,
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	return c
}

// Close releases the underlying transport (kills the subprocess for stdio;
// a no-op for HTTP, which holds no persistent connection of its own).
func (c *Client) Close() error {
	return c.t.close()
}

// ClientInfo identifies this client to the server during [Client.Initialize].
type ClientInfo struct {
	Name    string
	Version string
}

// ServerInfo is what the server reported about itself in its "initialize"
// response.
type ServerInfo struct {
	Name         string
	Version      string
	Instructions string
}

// Initialize performs the MCP lifecycle handshake: an "initialize" request
// followed by the required "notifications/initialized" notification. It must
// be called once, before [Client.ListTools] or [Client.CallTool]. The whole
// handshake is bounded by the Client's connect timeout (see
// [WithConnectTimeout]), composed with ctx's own deadline if it has one —
// whichever is sooner wins.
func (c *Client) Initialize(ctx context.Context, info ClientInfo) (ServerInfo, error) {
	params := map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": info.Name, "version": info.Version},
	}
	raw, err := c.call(ctx, c.connectTimeout, "initialize", params)
	if err != nil {
		return ServerInfo{}, err
	}
	var result struct {
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return ServerInfo{}, fmt.Errorf("mcp: decode initialize result: %w", err)
	}
	if err := c.notify(ctx, "notifications/initialized", nil); err != nil {
		return ServerInfo{}, fmt.Errorf("mcp: initialized notification: %w", err)
	}
	return ServerInfo{
		Name:         result.ServerInfo.Name,
		Version:      result.ServerInfo.Version,
		Instructions: result.Instructions,
	}, nil
}

// ToolInfo is one tool as reported by the server's "tools/list" response.
type ToolInfo struct {
	// Name is the server's own tool name — never sanitized or qualified.
	// [Project] uses it to build the projected tool's public name.
	Name        string
	Description string
	// InputSchema is the tool's raw JSON Schema input object, exactly as the
	// server sent it. [Project] converts it to [tool.Schema] via
	// schemaFromJSON; ToolInfo keeps the original for a caller that wants it.
	InputSchema json.RawMessage
}

// ListTools returns every tool the server offers, paginating internally over
// "tools/list"'s cursor until the server reports no further page. Per the
// MCP spec, tools/list already returns each tool's full schema alongside its
// name in the same response — there is no cheaper "names only" call. That
// means an index-first tool registry (schemas fetched only when a specific
// tool is chosen) is a context-window projection performed after this call
// returns, not a network optimization: this method is always going to fetch
// every schema. See docs/DESIGN.md "MCP (M7)".
func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	var out []ToolInfo
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.call(ctx, c.callTimeout, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var page struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, fmt.Errorf("mcp: decode tools/list result: %w", err)
		}
		for _, t := range page.Tools {
			out = append(out, ToolInfo{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
		}
		if page.NextCursor == "" {
			return out, nil
		}
		cursor = page.NextCursor
	}
}

// CallToolResult is the outcome of a "tools/call" request that round-tripped
// successfully (a non-nil error from [Client.CallTool] means the round trip
// itself failed — see that method's doc). IsError mirrors the MCP result's
// own "isError" field: the call reached the tool and the tool reported a
// failure, which is still a successful protocol exchange.
type CallToolResult struct {
	IsError bool
	// Text is every text content block the server returned, joined by
	// newlines. Non-text blocks (image, audio, embedded resource — no MCP
	// server in daily use here emits them, and this client models no
	// producer for them) are counted, never dropped silently: their count is
	// appended as a bracketed note so a human or model reading Text knows
	// content was omitted rather than assuming the tool returned nothing.
	Text string
	// Blocks is the total content block count the server returned, text and
	// non-text alike.
	Blocks int
}

// CallTool invokes name on the server with args as its JSON arguments object
// (an empty/nil args is sent as "{}"). A non-nil error means the round trip
// itself failed — the server was unreachable, the call's timeout elapsed, the
// response was malformed, or the server returned a JSON-RPC-level error
// (unknown tool, invalid params). It does NOT mean the tool itself failed;
// that is [CallToolResult.IsError], a normal (nil-error) result. Callers
// building a [tool.Tool] on top of this (see [Project]) must map a non-nil
// error to [tool.Result.IsError], never to a Go error, unless the ctx they
// were given was itself cancelled — see docs/DESIGN.md "MCP (M7)" for the
// full rule.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (CallToolResult, error) {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	params := struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}{Name: name, Arguments: args}
	raw, err := c.call(ctx, c.callTimeout, "tools/call", params)
	if err != nil {
		return CallToolResult{}, err
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return CallToolResult{}, fmt.Errorf("mcp: decode tools/call result: %w", err)
	}
	text, nonText := "", 0
	for _, block := range result.Content {
		if block.Type != "text" {
			nonText++
			continue
		}
		if text != "" {
			text += "\n"
		}
		text += block.Text
	}
	if nonText > 0 {
		if text != "" {
			text += "\n"
		}
		text += fmt.Sprintf("[%d non-text content block(s) omitted]", nonText)
	}
	return CallToolResult{IsError: result.IsError, Text: text, Blocks: len(result.Content)}, nil
}

// call assigns a request id, marshals method/params to a JSON-RPC request
// frame, and round-trips it through the transport within timeout (composed
// with ctx's own deadline, whichever is sooner). A JSON-RPC error response is
// surfaced as a wrapped error (via *rpcError's Error method), same as
// [lsp.Client.call].
func (c *Client) call(ctx context.Context, timeout time.Duration, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	raw, err := marshalParams(params)
	if err != nil {
		return nil, fmt.Errorf("mcp: marshal %s params: %w", method, err)
	}
	body, err := json.Marshal(rpcRequest{JSONRPC: jsonrpcVersion, ID: id, Method: method, Params: raw})
	if err != nil {
		return nil, fmt.Errorf("mcp: marshal %s request: %w", method, err)
	}
	callCtx, cancel := withTimeout(ctx, timeout)
	defer cancel()
	resp, err := c.t.roundTrip(callCtx, id, body)
	if err != nil {
		return nil, fmt.Errorf("mcp: %s: %w", method, err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("mcp: %s: %w", method, resp.Error)
	}
	return resp.Result, nil
}

// notify marshals method/params to a JSON-RPC notification frame and sends
// it with no response expected.
func (c *Client) notify(ctx context.Context, method string, params any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return fmt.Errorf("mcp: marshal %s params: %w", method, err)
	}
	body, err := json.Marshal(rpcNotification{JSONRPC: jsonrpcVersion, Method: method, Params: raw})
	if err != nil {
		return fmt.Errorf("mcp: marshal %s notification: %w", method, err)
	}
	return c.t.send(ctx, body)
}

// withTimeout derives a child context bounded by d, or returns ctx unchanged
// (with a no-op cancel) when d <= 0 — never hardcode "no timeout" as a magic
// duration. Composing via context.WithTimeout means a caller-supplied
// deadline that is already sooner than d is left alone: the earlier of the
// two always wins.
func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}
