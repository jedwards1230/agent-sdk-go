package mcp_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jedwards1230/agent-sdk-go/mcp"
)

// TestListToolsPaginates covers tools/list pagination: [Client.ListTools]
// must keep following "nextCursor" until the server stops returning one,
// aggregating every page — necessary for a federated server with a large
// tool catalog.
func TestListToolsPaginates(t *testing.T) {
	c, server := newTestClient(t)

	serverErrs := make(chan error, 1)
	go func() {
		r := bufio.NewReader(server)
		if err := handshake(r, server); err != nil {
			serverErrs <- err
			return
		}

		req1, err := readLine(r)
		if err != nil {
			serverErrs <- err
			return
		}
		page1, _ := json.Marshal(map[string]any{
			"tools":      []map[string]any{{"name": "one", "inputSchema": map[string]any{"type": "object"}}},
			"nextCursor": "page-2",
		})
		if err := writeLine(server, wireMessage{JSONRPC: "2.0", ID: req1.ID, Result: page1}); err != nil {
			serverErrs <- err
			return
		}

		req2, err := readLine(r)
		if err != nil {
			serverErrs <- err
			return
		}
		var params2 struct {
			Cursor string `json:"cursor"`
		}
		if err := json.Unmarshal(req2.Params, &params2); err != nil {
			serverErrs <- err
			return
		}
		if params2.Cursor != "page-2" {
			serverErrs <- fmt.Errorf("cursor = %q, want page-2", params2.Cursor)
			return
		}
		page2, _ := json.Marshal(map[string]any{
			"tools": []map[string]any{{"name": "two", "inputSchema": map[string]any{"type": "object"}}},
		})
		serverErrs <- writeLine(server, wireMessage{JSONRPC: "2.0", ID: req2.ID, Result: page2})
	}()

	ctx := ctxWithTimeout(t)
	if _, err := c.Initialize(ctx, mcp.ClientInfo{Name: "test"}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	infos, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(infos) != 2 || infos[0].Name != "one" || infos[1].Name != "two" {
		t.Fatalf("ListTools() = %+v, want [one two]", infos)
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
