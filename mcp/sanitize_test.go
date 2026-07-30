package mcp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/mcp"
)

// projectNames wires a minimal pipe server that answers tools/list with one
// tool (a trivial empty-object schema) and nothing else, then returns the
// resulting projected [tool.Tool]'s Name() — used to exercise
// [mcp.Project]'s naming behavior without repeating the full
// handshake+list scaffolding per test.
func projectNames(t *testing.T, server, tool string) string {
	t.Helper()
	clientStream, serverStream := newPipedStreams()
	c := mcp.NewStdio(clientStream)
	t.Cleanup(func() { _ = c.Close() })

	done := make(chan struct{})
	go func() {
		defer close(done)
		r := bufio.NewReader(serverStream)
		_ = handshake(r, serverStream)
		req, err := readLine(r)
		if err != nil {
			return
		}
		result, _ := json.Marshal(map[string]any{
			"tools": []map[string]any{{"name": tool, "description": "", "inputSchema": map[string]any{"type": "object"}}},
		})
		_ = writeLine(serverStream, wireMessage{JSONRPC: "2.0", ID: req.ID, Result: result})
	}()

	ctx := context.Background()
	if _, err := c.Initialize(ctx, mcp.ClientInfo{Name: "test"}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	tools, err := mcp.Project(ctx, c, server)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	<-done
	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}
	return tools[0].Name()
}

// TestProjectedNameFormat covers the base "mcp__<server>__<tool>" grammar
// for a short, already-legal name.
func TestProjectedNameFormat(t *testing.T) {
	got := projectNames(t, "wiki", "search")
	if want := "mcp__wiki__search"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
}

// TestProjectedNameIllegalRunes covers illegal-rune replacement: anything
// outside [A-Za-z0-9_-] becomes '_', independently per component.
func TestProjectedNameIllegalRunes(t *testing.T) {
	got := projectNames(t, "home assistant!", "ha.config/list")
	if want := "mcp__home_assistant___ha_config_list"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
	for _, r := range got {
		isLegal := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-'
		if !isLegal {
			t.Errorf("Name() = %q contains illegal rune %q", got, r)
		}
	}
}

// TestProjectedNameTruncation covers the >64-byte truncation rule: the
// result is exactly 64 bytes, keeps the first 57 bytes of the sanitized
// name, and appends '_' plus a 6-hex-char sha256 suffix of the FULL
// sanitized name (not just the surviving prefix).
func TestProjectedNameTruncation(t *testing.T) {
	got := projectNames(t, "home-assistant", "ha_config_list_dashboard_resources_and_more_stuff_here_too")
	if len(got) != 64 {
		t.Fatalf("len(Name()) = %d, want 64: %q", len(got), got)
	}
	full := "mcp__home-assistant__ha_config_list_dashboard_resources_and_more_stuff_here_too"
	if len(full) <= 64 {
		t.Fatalf("test fixture full name is only %d bytes, want >64 to exercise truncation", len(full))
	}
	wantPrefix := full[:57]
	if !strings.HasPrefix(got, wantPrefix) {
		t.Errorf("Name() = %q, want prefix %q", got, wantPrefix)
	}
	if got[57] != '_' {
		t.Errorf("Name()[57] = %q, want '_' separating the prefix from the hash suffix", string(got[57]))
	}
	suffix := got[58:]
	if len(suffix) != 6 {
		t.Fatalf("hash suffix len = %d, want 6", len(suffix))
	}
	for _, r := range suffix {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Errorf("hash suffix %q contains non-hex rune %q", suffix, r)
		}
	}
}

// TestProjectedNameTruncationDistinctness covers the collision-safety
// requirement: two names that agree on everything up to the 57-byte cut but
// differ only past it must still truncate to distinct results, because the
// hash is computed over the FULL sanitized name, not just the prefix that
// survives.
func TestProjectedNameTruncationDistinctness(t *testing.T) {
	base := "ha_config_list_dashboard_resources_and_more_stuff_here_too_"
	nameA := projectNames(t, "home-assistant", base+"aaaaaaaaaaaaaaaaaaaa")
	nameB := projectNames(t, "home-assistant", base+"bbbbbbbbbbbbbbbbbbbb")

	if len(nameA) != 64 || len(nameB) != 64 {
		t.Fatalf("both names must be truncated to 64 bytes, got %d and %d", len(nameA), len(nameB))
	}
	if nameA[:57] != nameB[:57] {
		t.Fatalf("test fixture bug: the two names' first 57 bytes already differ (%q vs %q), want them identical so only the hash suffix can distinguish them", nameA[:57], nameB[:57])
	}
	if nameA == nameB {
		t.Errorf("Name() collided: %q == %q, want distinct results for inputs differing only past the truncation cut", nameA, nameB)
	}
}
