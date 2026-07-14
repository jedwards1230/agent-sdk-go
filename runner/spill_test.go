package runner_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/loop"
	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/runner"
	"github.com/jedwards1230/agent-sdk-go/session"
	"github.com/jedwards1230/agent-sdk-go/tool"
)

// TestRunner_SpillsBashOutputToSessionDir is the end-to-end proof of the spill
// seam through the real runner→loop→bash→file path: a scripted provider requests
// a bash call whose multi-megabyte output must land in full in an append-only
// per-call file under the session directory, while the event stream and the
// journal carry only the bounded excerpt + reference.
func TestRunner_SpillsBashOutputToSessionDir(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()

	cmd, err := json.Marshal(map[string]string{
		"command": `head -c 3000000 /dev/zero | tr '\0' 'a'`,
	})
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}
	prov := &scriptedProvider{events: [][]provider.StreamEvent{
		{
			{Type: provider.StreamToolCallStart, Tool: &provider.ToolCall{ID: "call-1", Name: "bash"}},
			{Type: provider.StreamToolCallEnd, Tool: &provider.ToolCall{ID: "call-1", Name: "bash", Input: cmd}},
			{Type: provider.StreamFinished, StopReason: provider.StopToolUse, Usage: provider.Usage{InputTokens: 4, OutputTokens: 1}},
		},
		{
			{Type: provider.StreamTextDelta, Text: "done"},
			{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{InputTokens: 3, OutputTokens: 1}},
		},
	}}

	r, err := runner.New(context.Background(), runner.Options{
		Root: root, Cwd: cwd, Model: testModel,
		Provider: prov,
		Tools:    loop.FromRegistry(tool.NewRegistry(tool.NewBash(cwd))),
		IDGen:    seqIDGen(), Clock: seqClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id := r.ID()

	sub := r.Events()
	if err := r.Prompt(context.Background(), "make a lot of output"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	// The spill file sits under the session directory (a sibling of the journal
	// file) and holds the FULL, untruncated output.
	const wantBytes = 3_000_000
	spillPath := filepath.Join(root, "sessions", session.Slugify(cwd), id, "calls", "call-1.log")
	onDisk, err := os.ReadFile(spillPath)
	if err != nil {
		t.Fatalf("read spill file: %v", err)
	}
	if len(onDisk) != wantBytes {
		t.Fatalf("spill file len = %d, want %d", len(onDisk), wantBytes)
	}

	// The session dir coexists with the journal file; the journal still opens.
	if _, err := os.Stat(r.JournalPath()); err != nil {
		t.Errorf("journal file missing: %v", err)
	}

	// The tool.call.finished event carries a portable, root-relative reference
	// whose bytes/sha match the on-disk file, and a bounded excerpt.
	var ev event.ToolCallFinished
	for {
		e, ok := <-sub.C
		if !ok {
			t.Fatal("stream closed before tool.call.finished")
		}
		if tf, is := e.(event.ToolCallFinished); is {
			ev = tf
			break
		}
	}
	wantRel := filepath.ToSlash(filepath.Join("sessions", session.Slugify(cwd), id, "calls", "call-1.log"))
	if ev.SpillPath != wantRel {
		t.Errorf("SpillPath = %q, want %q", ev.SpillPath, wantRel)
	}
	if ev.SpillBytes != wantBytes {
		t.Errorf("SpillBytes = %d, want %d", ev.SpillBytes, wantBytes)
	}
	sum := sha256.Sum256(onDisk)
	if ev.SpillSHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("SpillSHA256 does not match the on-disk file")
	}
	if len(ev.Result) > 8<<10 {
		t.Errorf("event excerpt len = %d, want a bounded preview", len(ev.Result))
	}

	// The journal stores the bounded excerpt, not the 3 MB payload: Fold projects
	// the tool_result back with the excerpt only.
	var toolResult string
	var found bool
	for _, m := range r.Fold() {
		for _, blk := range m.Content {
			if blk.Type == provider.BlockToolResult && blk.ToolUseID == "call-1" {
				toolResult, found = blk.ToolResult, true
			}
		}
	}
	if !found {
		t.Fatal("no tool_result for call-1 in the folded journal")
	}
	if len(toolResult) > 8<<10 {
		t.Errorf("journaled tool_result len = %d, want the bounded excerpt (not the full output)", len(toolResult))
	}
	if toolResult != ev.Result {
		t.Errorf("journaled tool_result differs from the event excerpt")
	}

	if err := r.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// pathFromMarker extracts the spill path an elision marker names, from the
// "… full output at <path>] …" text — i.e. exactly the string the model would
// read out of the excerpt.
func pathFromMarker(t *testing.T, excerpt string) string {
	t.Helper()
	const lead = "full output at "
	i := strings.Index(excerpt, lead)
	if i < 0 {
		t.Fatalf("no elision marker in excerpt: %.140q", excerpt)
	}
	rest := excerpt[i+len(lead):]
	j := strings.Index(rest, "]")
	if j < 0 {
		t.Fatalf("malformed elision marker: %.140q", rest)
	}
	return strings.TrimSpace(rest[:j])
}

// TestRunner_SpillMarkerPathResolvesWhenCwdDiffersFromRoot is the real gofer
// scenario: the session store Root and the tool Cwd are DIFFERENT directories.
// A capped tool's excerpt marker must name a path the read tool can resolve from
// the tool cwd — which only works because the marker names the ABSOLUTE spill
// path (a root-relative path would resolve against Cwd and miss). It reads the
// path straight out of the marker text (what the model would do) and asserts the
// complete original output comes back.
func TestRunner_SpillMarkerPathResolvesWhenCwdDiffersFromRoot(t *testing.T) {
	root := t.TempDir() // session store lives here (e.g. ~/.gofer)
	cwd := t.TempDir()  // tools operate here (the project dir) — deliberately different
	if root == cwd {
		t.Fatal("root and cwd must differ for this test")
	}

	// 5000 bytes of 'a' — over the 4 KiB excerpt window, so the result elides and
	// carries a path-naming marker.
	cmd, err := json.Marshal(map[string]string{"command": `head -c 5000 /dev/zero | tr '\0' 'a'`})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	prov := &scriptedProvider{events: [][]provider.StreamEvent{
		{
			{Type: provider.StreamToolCallStart, Tool: &provider.ToolCall{ID: "call-1", Name: "bash"}},
			{Type: provider.StreamToolCallEnd, Tool: &provider.ToolCall{ID: "call-1", Name: "bash", Input: cmd}},
			{Type: provider.StreamFinished, StopReason: provider.StopToolUse, Usage: provider.Usage{InputTokens: 4, OutputTokens: 1}},
		},
		{
			{Type: provider.StreamTextDelta, Text: "done"},
			{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{InputTokens: 3, OutputTokens: 1}},
		},
	}}

	r, err := runner.New(context.Background(), runner.Options{
		Root: root, Cwd: cwd, Model: testModel,
		Provider: prov,
		Tools:    loop.FromRegistry(tool.NewRegistry(tool.NewBash(cwd))),
		IDGen:    seqIDGen(), Clock: seqClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = r.Close() }()

	sub := r.Events()
	if err := r.Prompt(context.Background(), "make output"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	var ev event.ToolCallFinished
	for {
		e, ok := <-sub.C
		if !ok {
			t.Fatal("stream closed before tool.call.finished")
		}
		if tf, is := e.(event.ToolCallFinished); is {
			ev = tf
			break
		}
	}

	// The marker names an ABSOLUTE path, under Root (the store), not under Cwd.
	markerPath := pathFromMarker(t, ev.Result)
	if !filepath.IsAbs(markerPath) {
		t.Fatalf("marker path %q is not absolute; read from a different cwd could not resolve it", markerPath)
	}
	if !strings.HasPrefix(markerPath, root) {
		t.Errorf("marker path %q is not under the store root %q", markerPath, root)
	}
	// The structured event field, by contrast, stays root-relative.
	if filepath.IsAbs(ev.SpillPath) || !strings.HasPrefix(ev.SpillPath, "sessions/") {
		t.Errorf("event spill_path should stay root-relative, got %q", ev.SpillPath)
	}

	// The model reads exactly the marker's path, through a read tool rooted at
	// Cwd (≠ Root), and gets the COMPLETE original output back.
	readInput, err := json.Marshal(map[string]string{"path": markerPath})
	if err != nil {
		t.Fatalf("marshal read input: %v", err)
	}
	readRes, err := tool.NewRead(cwd).Run(context.Background(), readInput)
	if err != nil {
		t.Fatalf("read spill via marker path: %v", err)
	}
	if readRes.IsError {
		t.Fatalf("read of marker path errored (cwd≠root not resolved): %q", readRes.Content)
	}
	if !readRes.FullResult {
		t.Errorf("read should return full uncapped content")
	}
	if got := strings.Count(readRes.Content, "a"); got != 5000 {
		t.Errorf("read via marker path returned %d 'a' bytes, want the complete 5000", got)
	}
}
