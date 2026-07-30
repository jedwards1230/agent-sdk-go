package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// httpTransport speaks MCP's streamable-HTTP transport: every request or
// notification is one POST to a single endpoint. A response arrives either
// as a single JSON object ("Content-Type: application/json") or as an SSE
// stream ("Content-Type: text/event-stream") that this client reads until it
// sees the JSON-RPC message whose id matches the request — the streamable
// form exists so a server can interleave progress notifications ahead of the
// final response, though this client only ever looks for the matching
// response and ignores everything else on the stream, since it models no
// producer for the notification family (client + tool projection is this
// package's whole scope — see docs/DESIGN.md "MCP (M7)"). There is no
// persistent connection to correlate against, so — unlike stdio — no
// background goroutine and no pending-call table are needed: each call is
// its own bounded round trip.
type httpTransport struct {
	url    string
	client *http.Client
	header http.Header // extra headers (auth, etc.) applied to every request; caller-owned
}

func newHTTPTransport(url string, client *http.Client, header http.Header) *httpTransport {
	return &httpTransport{url: url, client: client, header: header}
}

func (t *httpTransport) roundTrip(ctx context.Context, id int64, frame []byte) (rpcResponse, error) {
	resp, err := t.post(ctx, frame, "application/json, text/event-stream")
	if err != nil {
		return rpcResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := checkStatus(resp); err != nil {
		return rpcResponse{}, err
	}
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		return readSSEResponse(resp.Body, id)
	}
	return decodeJSONResponse(resp.Body)
}

func (t *httpTransport) send(ctx context.Context, frame []byte) error {
	resp, err := t.post(ctx, frame, "application/json")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return checkStatus(resp)
}

func (t *httpTransport) post(ctx context.Context, frame []byte, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(frame))
	if err != nil {
		return nil, fmt.Errorf("mcp: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", accept)
	for k, vs := range t.header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp: http request: %w", err)
	}
	return resp, nil
}

func (t *httpTransport) close() error { return nil }

// checkStatus turns a non-2xx HTTP response into an error carrying a bounded
// excerpt of the body, since an MCP HTTP endpoint's error responses are not
// guaranteed to be JSON-RPC-shaped (they may be a plain-text gateway error).
func checkStatus(resp *http.Response) error {
	if resp.StatusCode/100 == 2 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("mcp: http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

func decodeJSONResponse(r io.Reader) (rpcResponse, error) {
	var resp rpcResponse
	if err := json.NewDecoder(r).Decode(&resp); err != nil {
		return rpcResponse{}, fmt.Errorf("mcp: decode response: %w", err)
	}
	return resp, nil
}

// readSSEResponse reads Server-Sent Events off r, decoding each event's
// accumulated "data:" payload as a JSON-RPC message, until it finds the
// response whose id matches want. Anything else on the stream (a progress
// notification, an unrelated id) is skipped. Minimal SSE parsing: "data:"
// lines are the only field this client understands; "event:"/"id:"/comment
// lines are read and ignored per the SSE spec's forward-compatibility rule.
func readSSEResponse(r io.Reader, want int64) (rpcResponse, error) {
	br := bufio.NewReader(r)
	var data strings.Builder
	for {
		line, err := br.ReadString('\n')
		trimmed := strings.TrimRight(line, "\r\n")
		switch {
		case trimmed == "":
			// Blank line: dispatches the accumulated event, if any.
			if data.Len() > 0 {
				payload := data.String()
				data.Reset()
				var msg rpcInbound
				if jsonErr := json.Unmarshal([]byte(payload), &msg); jsonErr == nil && msg.ID != nil && *msg.ID == want {
					return rpcResponse{JSONRPC: msg.JSONRPC, ID: msg.ID, Result: msg.Result, Error: msg.Error}, nil
				}
			}
		case strings.HasPrefix(trimmed, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(trimmed, "data:"), " "))
		default:
			// event:, id:, retry:, or a comment (":...") — not needed by this
			// client, ignored per SSE's forward-compatibility rule.
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return rpcResponse{}, fmt.Errorf("mcp: sse stream ended without a matching response (id=%d)", want)
			}
			return rpcResponse{}, fmt.Errorf("mcp: read sse stream: %w", err)
		}
	}
}

// httpConfig collects [HTTPOption] values before the transport is built.
type httpConfig struct {
	client *http.Client
	header http.Header
}

// HTTPOption configures [NewHTTP] at construction.
type HTTPOption func(*httpConfig)

// WithHTTPClient overrides the *http.Client [NewHTTP] uses, e.g. to supply a
// custom RoundTripper for mTLS or a non-bearer auth scheme. The default is a
// bare *http.Client{} (no custom transport, no timeout of its own — timeouts
// come from [WithConnectTimeout]/[WithCallTimeout] via ctx, not the HTTP
// client). This is the extension seam for whatever a server needs;
// credential resolution itself is the consuming application's job (see
// docs/DESIGN.md "MCP (M7)") — this package never resolves a secret on its
// own.
func WithHTTPClient(c *http.Client) HTTPOption {
	return func(cfg *httpConfig) { cfg.client = c }
}

// WithHTTPHeader adds a header sent on every request, e.g.
// WithHTTPHeader("Authorization", "Bearer "+token). Call it more than once to
// add several headers.
func WithHTTPHeader(key, value string) HTTPOption {
	return func(cfg *httpConfig) { cfg.header.Add(key, value) }
}

// NewHTTP builds a [Client] speaking MCP's streamable-HTTP transport against
// url. Call [Client.Initialize] before any other method.
func NewHTTP(url string, httpOpts []HTTPOption, opts ...Option) *Client {
	cfg := &httpConfig{header: make(http.Header)}
	for _, o := range httpOpts {
		o(cfg)
	}
	if cfg.client == nil {
		cfg.client = &http.Client{}
	}
	c := newClient(opts...)
	c.t = newHTTPTransport(url, cfg.client, cfg.header)
	return c
}
