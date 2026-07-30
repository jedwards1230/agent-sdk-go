package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jedwards1230/agent-sdk-go/mcp"
)

// httpWireMessage mirrors wireMessage for the HTTP-transport fixtures.
type httpWireMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *wireError      `json:"error,omitempty"`
}

func decodeHTTPRequest(r *http.Request) (httpWireMessage, error) {
	var msg httpWireMessage
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		return httpWireMessage{}, err
	}
	return msg, nil
}

// TestHTTPInitializeAndCallToolJSON covers the plain-JSON response shape of
// streamable-HTTP: initialize, tools/list, and a successful tools/call all
// round-tripping over one httptest.Server.
func TestHTTPInitializeAndCallToolJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeHTTPRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			result, _ := json.Marshal(map[string]any{"serverInfo": map[string]string{"name": "http-fake", "version": "9"}})
			_ = json.NewEncoder(w).Encode(httpWireMessage{JSONRPC: "2.0", ID: req.ID, Result: result})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			result, _ := json.Marshal(map[string]any{
				"tools": []map[string]any{{"name": "greet", "description": "says hi", "inputSchema": map[string]any{"type": "object"}}},
			})
			_ = json.NewEncoder(w).Encode(httpWireMessage{JSONRPC: "2.0", ID: req.ID, Result: result})
		case "tools/call":
			result, _ := json.Marshal(map[string]any{
				"content": []map[string]any{{"type": "text", "text": "hi there"}},
			})
			_ = json.NewEncoder(w).Encode(httpWireMessage{JSONRPC: "2.0", ID: req.ID, Result: result})
		default:
			http.Error(w, "unexpected method "+req.Method, http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	c := mcp.NewHTTP(srv.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	info, err := c.Initialize(ctx, mcp.ClientInfo{Name: "test"})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if info.Name != "http-fake" {
		t.Errorf("ServerInfo.Name = %q, want http-fake", info.Name)
	}

	tools, err := mcp.Project(ctx, c, "httpsrv")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(tools) != 1 || tools[0].Name() != "mcp__httpsrv__greet" {
		t.Fatalf("Project() = %+v, want one tool named mcp__httpsrv__greet", tools)
	}

	res, err := tools[0].Run(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Content != "hi there" {
		t.Errorf("Run.Content = %q, want %q", res.Content, "hi there")
	}
}

// TestHTTPSSEResponse covers the SSE/streamable-HTTP response shape: the
// server answers with "text/event-stream" instead of a plain JSON body.
func TestHTTPSSEResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeHTTPRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		result, _ := json.Marshal(map[string]any{
			"content": []map[string]any{{"type": "text", "text": "sse hello"}},
		})
		resp, _ := json.Marshal(httpWireMessage{JSONRPC: "2.0", ID: req.ID, Result: result})
		// An interleaved unrelated notification (no id) ahead of the real
		// response, exercising that the client skips anything that isn't the
		// matching response id.
		_, _ = fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", resp)
	}))
	defer srv.Close()

	c := mcp.NewHTTP(srv.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	res, err := c.CallTool(ctx, "anything", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.Text != "sse hello" {
		t.Errorf("CallTool.Text = %q, want %q", res.Text, "sse hello")
	}
}

// TestHTTPUnreachableSurfacesAsClientError covers a server that cannot be
// reached at all (closed listener): [Client.CallTool] returns a non-nil
// error for the failed round trip. The Go-error-vs-IsError-result mapping
// this feeds into ([projectedTool.Run]) is transport-agnostic and already
// covered exhaustively over stdio (TestRunTimeoutSurfacesAsResult,
// TestServerDiesMidSessionSurfacesAsResult, TestRunContextCancellationIsAnError);
// this test only needs to prove the HTTP transport itself reports the
// failure as an error rather than hanging or panicking.
func TestHTTPUnreachableSurfacesAsClientError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // the server is gone before any request is made

	c := mcp.NewHTTP(url, nil)
	if _, err := c.CallTool(context.Background(), "whatever", nil); err == nil {
		t.Fatal("CallTool err = nil, want an error for an unreachable server")
	}
}

// TestHTTPSlowServerTimesOut covers a server that never responds: the
// Client's own call timeout must fire well before the test's overall
// deadline.
func TestHTTPSlowServerTimesOut(t *testing.T) {
	// The handler sleeps deliberately longer than the client's call timeout
	// (below) but still bounded, so the test doesn't depend on the server
	// noticing the client's disconnect (r.Context() cancellation timing is
	// not guaranteed promptly by httptest) to let httptest.Server.Close()
	// return.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()

	c := mcp.NewHTTP(srv.URL, nil, mcp.WithCallTimeout(50*time.Millisecond))
	start := time.Now()
	_, err := c.CallTool(context.Background(), "slow", nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("CallTool err = nil, want the internal call timeout to fire")
	}
	if elapsed > 250*time.Millisecond {
		t.Errorf("CallTool took %v, want it bounded by the ~50ms call timeout", elapsed)
	}
}

// TestHTTPCustomHeader covers the WithHTTPHeader seam: a header set at
// construction must reach every request, e.g. for bearer-token auth.
func TestHTTPCustomHeader(t *testing.T) {
	var sawAuth atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer secret" {
			sawAuth.Store(true)
		}
		req, _ := decodeHTTPRequest(r)
		result, _ := json.Marshal(map[string]any{"serverInfo": map[string]string{"name": "authed"}})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(httpWireMessage{JSONRPC: "2.0", ID: req.ID, Result: result})
	}))
	defer srv.Close()

	c := mcp.NewHTTP(srv.URL, []mcp.HTTPOption{mcp.WithHTTPHeader("Authorization", "Bearer secret")})
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if _, err := c.Initialize(ctx, mcp.ClientInfo{Name: "test"}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if !sawAuth.Load() {
		t.Error("server never saw the configured Authorization header")
	}
}
