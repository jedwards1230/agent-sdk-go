package loop_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/internal/goldenio"
	"github.com/jedwards1230/agent-sdk-go/loop"
	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/tool"
)

func spillClock() time.Time { return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC) }

// drainFinished collects every tool.call.finished event buffered on sub.
func drainFinished(sub *event.Subscription) []event.ToolCallFinished {
	var out []event.ToolCallFinished
	for {
		select {
		case e, ok := <-sub.C:
			if !ok {
				return out
			}
			if tf, is := e.(event.ToolCallFinished); is {
				out = append(out, tf)
			}
		default:
			return out
		}
	}
}

// TestToolCallFinishedSpillGolden drives a tool round with file spilling on and
// asserts the tool.call.finished event's shape (bounded excerpt + portable,
// root-relative spill_path + byte count + sha256) against a golden file. The
// spill dir is a temp dir but only the RELATIVE dir appears in the event, so the
// golden is stable.
func TestToolCallFinishedSpillGolden(t *testing.T) {
	b := event.NewBroker(event.WithClock(spillClock))
	defer b.Close()
	sub := b.Subscribe(event.FilterMustDeliver, 256)

	out := "spilled tool output\n"
	cfg := baseConfig(b, scripted(
		toolTurn("t1", "echo", `{"msg":"hi"}`),
		textTurn("done", provider.StopEndTurn),
	))
	cfg.Tools = &fakeTool{name: "echo", result: loop.ToolResult{Content: out}}
	cfg.SpillDir = t.TempDir()
	cfg.SpillRelDir = "sessions/proj/sess-1/calls"

	if _, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("go")}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	finished := drainFinished(sub)
	if len(finished) != 1 {
		t.Fatalf("want 1 tool.call.finished, got %d", len(finished))
	}
	line, err := json.Marshal(finished[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	goldenio.Assert(t, filepath.Join("testdata", "toolcall_spill.golden.jsonl"), append(line, '\n'))

	// The spill file on disk holds the full, untruncated output.
	onDisk, err := os.ReadFile(filepath.Join(cfg.SpillDir, "t1.log"))
	if err != nil {
		t.Fatalf("ReadFile spill: %v", err)
	}
	if string(onDisk) != out {
		t.Errorf("spill file = %q, want %q", onDisk, out)
	}
}

// TestBashStreamsMultiMegabyteWithoutBuffering runs the REAL bash tool through
// the loop with a multi-megabyte output and proves: the spill file receives the
// full untruncated output, the event ref (bytes/sha) matches the file, the
// excerpt is a bounded head+tail preview, and the model-facing tool_result is the
// same bounded excerpt (not the multi-MB payload).
func TestBashStreamsMultiMegabyteWithoutBuffering(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()
	sub := b.Subscribe(event.FilterMustDeliver, 256)

	callsDir := t.TempDir()
	reg := tool.NewRegistry(tool.NewBash(t.TempDir()))
	cfg := baseConfig(b, scripted(
		// 3 MB of 'a' — far larger than the excerpt window + pipe buffers.
		toolTurn("bash-1", "bash", `{"command":"head -c 3000000 /dev/zero | tr '\\0' 'a'"}`),
		textTurn("done", provider.StopEndTurn),
	))
	cfg.Tools = loop.FromRegistry(reg)
	cfg.SpillDir = callsDir
	cfg.SpillRelDir = "sessions/p/s/calls"

	res, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("go")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	finished := drainFinished(sub)
	if len(finished) != 1 {
		t.Fatalf("want 1 tool.call.finished, got %d", len(finished))
	}
	ev := finished[0]

	const wantBytes = 3_000_000
	if ev.SpillBytes != wantBytes {
		t.Errorf("SpillBytes = %d, want %d", ev.SpillBytes, wantBytes)
	}
	if ev.SpillPath != "sessions/p/s/calls/bash-1.log" {
		t.Errorf("SpillPath = %q, want portable root-relative", ev.SpillPath)
	}

	onDisk, err := os.ReadFile(filepath.Join(callsDir, "bash-1.log"))
	if err != nil {
		t.Fatalf("ReadFile spill: %v", err)
	}
	if len(onDisk) != wantBytes {
		t.Fatalf("spill file len = %d, want %d (full output must be on disk)", len(onDisk), wantBytes)
	}
	sum := sha256.Sum256(onDisk)
	if ev.SpillSHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("SpillSHA256 does not match the on-disk file")
	}

	// The event excerpt is a small bounded preview whose elision marker names the
	// spill_path, so the model can read the full output back.
	if len(ev.Result) > 8<<10 {
		t.Errorf("excerpt len = %d, want a small bounded preview", len(ev.Result))
	}
	if !strings.Contains(ev.Result, "bytes elided — full output at "+ev.SpillPath) {
		t.Errorf("excerpt marker should name the spill_path %q: %.120q…", ev.SpillPath, ev.Result)
	}

	// The model-facing tool_result carries the SAME bounded excerpt, not the 3 MB
	// payload — so the model context never sees the full output either.
	tr := res.Messages[2].Content[0]
	if tr.Type != provider.BlockToolResult {
		t.Fatalf("messages[2].content[0] = %+v, want a tool_result block", tr)
	}
	if tr.ToolResult != ev.Result {
		t.Errorf("model-facing tool_result differs from the event excerpt")
	}
	if len(tr.ToolResult) > 8<<10 {
		t.Errorf("model-facing tool_result len = %d, want the bounded excerpt", len(tr.ToolResult))
	}
}

// TestReadReturnsFullContentUncapped is the escape hatch: a read of a file far
// larger than the excerpt window must hand the model the FULL content (not a
// head+tail excerpt), while the output is still spilled to disk.
func TestReadReturnsFullContentUncapped(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()
	sub := b.Subscribe(event.FilterMustDeliver, 256)

	// A file well over the 4 KiB excerpt window, one line per row so read's
	// line-numbering is cheap.
	dir := t.TempDir()
	var sbld strings.Builder
	for i := 0; i < 4000; i++ {
		sbld.WriteString("line-of-content\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(sbld.String()), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	callsDir := t.TempDir()
	cfg := baseConfig(b, scripted(
		toolTurn("r1", "read", `{"path":"big.txt"}`),
		textTurn("done", provider.StopEndTurn),
	))
	cfg.Tools = loop.FromRegistry(tool.NewRegistry(tool.NewRead(dir)))
	cfg.SpillDir = callsDir
	cfg.SpillRelDir = "sessions/p/s/calls"

	res, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("go")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	finished := drainFinished(sub)
	if len(finished) != 1 {
		t.Fatalf("want 1 tool.call.finished, got %d", len(finished))
	}
	ev := finished[0]

	// The model-facing tool_result and the event result are the FULL content,
	// not a ~4 KiB excerpt, and carry no elision marker.
	full := res.Messages[2].Content[0].ToolResult
	if len(full) < 8<<10 {
		t.Fatalf("read result len = %d, want the full (uncapped) content", len(full))
	}
	if strings.Contains(full, "bytes elided") {
		t.Errorf("read result was excerpted; escape hatch not honored")
	}
	if ev.Result != full {
		t.Errorf("event result differs from the model-facing full content")
	}
	// Durability is universal: the output is still spilled, ref still emitted.
	if ev.SpillPath == "" || ev.SpillBytes != int64(len(full)) {
		t.Errorf("expected the full content spilled: path=%q bytes=%d (content %d)", ev.SpillPath, ev.SpillBytes, len(full))
	}
	onDisk, err := os.ReadFile(filepath.Join(callsDir, "r1.log"))
	if err != nil {
		t.Fatalf("read spill: %v", err)
	}
	if string(onDisk) != full {
		t.Errorf("spill file content != full read result")
	}
}

// TestReadingSpillFileReturnsCompleteOutput proves the escape hatch closes the
// loop: after a capped tool (bash) spills its full output, reading that spill
// file back returns the COMPLETE original output. It uses an ABSOLUTE path to
// the spill file — see the Root-vs-Cwd caveat in the PR/DESIGN.md: the event's
// spill_path is store-root-relative while read resolves relative paths against
// the tool cwd, so a bare read(<spill_path>) only resolves when Root and Cwd
// share a base; an absolute path always works.
func TestReadingSpillFileReturnsCompleteOutput(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()
	sub := b.Subscribe(event.FilterMustDeliver, 256)

	callsDir := t.TempDir()
	cfg := baseConfig(b, scripted(
		toolTurn("bash-1", "bash", `{"command":"head -c 3000000 /dev/zero | tr '\\0' 'a'"}`),
		textTurn("done", provider.StopEndTurn),
	))
	cfg.Tools = loop.FromRegistry(tool.NewRegistry(tool.NewBash(t.TempDir())))
	cfg.SpillDir = callsDir
	cfg.SpillRelDir = "sessions/p/s/calls"
	if _, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("go")}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ev := drainFinished(sub)
	if len(ev) != 1 || ev[0].SpillBytes != 3_000_000 {
		t.Fatalf("unexpected spill event: %+v", ev)
	}

	// Read the spill file back via the read tool, by absolute path.
	absSpill := filepath.Join(callsDir, "bash-1.log")
	readRes, err := tool.NewRead(t.TempDir()).Run(context.Background(), json.RawMessage(`{"path":`+jsonString(absSpill)+`}`))
	if err != nil {
		t.Fatalf("read spill: %v", err)
	}
	if readRes.IsError {
		t.Fatalf("read of spill file errored: %q", readRes.Content)
	}
	if !readRes.FullResult {
		t.Errorf("read should return full (uncapped) content")
	}
	if got := strings.Count(readRes.Content, "a"); got != 3_000_000 {
		t.Errorf("read returned %d 'a' bytes, want the complete 3000000", got)
	}
}

// jsonString quotes s as a JSON string literal.
func jsonString(s string) string {
	buf, _ := json.Marshal(s)
	return string(buf)
}

// TestBashNonZeroExitFooterInSpill confirms bash's exit-status footer is streamed
// into the spill file (and thus the excerpt) rather than dropped.
func TestBashNonZeroExitFooterInSpill(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()
	sub := b.Subscribe(event.FilterMustDeliver, 256)

	callsDir := t.TempDir()
	reg := tool.NewRegistry(tool.NewBash(t.TempDir()))
	cfg := baseConfig(b, scripted(
		toolTurn("x", "bash", `{"command":"echo oops; exit 7"}`),
		textTurn("done", provider.StopEndTurn),
	))
	cfg.Tools = loop.FromRegistry(reg)
	cfg.SpillDir = callsDir
	cfg.SpillRelDir = "rel/calls"

	if _, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("go")}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	finished := drainFinished(sub)
	if len(finished) != 1 || !finished[0].IsError {
		t.Fatalf("want 1 errored tool.call.finished, got %+v", finished)
	}
	onDisk, err := os.ReadFile(filepath.Join(callsDir, "x.log"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := string(onDisk)
	if !strings.Contains(got, "oops") || !strings.Contains(got, "[exit 7]") {
		t.Errorf("spill file = %q, want it to contain the output and the exit footer", got)
	}
}
