package tool

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBashEcho(t *testing.T) {
	dir := t.TempDir()
	b := NewBash(dir)
	res, err := b.Run(context.Background(), json.RawMessage(`{"command":"echo hi"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, want false; content: %q", res.Content)
	}
	if strings.TrimSpace(res.Content) != "hi" {
		t.Fatalf("Content = %q, want trimmed \"hi\"", res.Content)
	}
	if res.Metadata.ExitCode == nil || *res.Metadata.ExitCode != 0 {
		t.Fatalf("ExitCode = %v, want 0", res.Metadata.ExitCode)
	}
}

func TestBashNonZeroExit(t *testing.T) {
	dir := t.TempDir()
	b := NewBash(dir)
	res, err := b.Run(context.Background(), json.RawMessage(`{"command":"exit 3"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IsError {
		t.Fatalf("IsError = false, want true")
	}
	if res.Metadata.ExitCode == nil || *res.Metadata.ExitCode != 3 {
		t.Fatalf("ExitCode = %v, want 3", res.Metadata.ExitCode)
	}
}

func TestBashTruncatesOutput(t *testing.T) {
	dir := t.TempDir()
	b := NewBash(dir)
	res, err := b.Run(context.Background(), json.RawMessage(`{"command":"yes x | head -c 40000"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Metadata.Truncated {
		t.Fatalf("Truncated = false, want true")
	}
	if len(res.Content) > defaultMaxOutputBytes+200 {
		t.Fatalf("Content len = %d, want roughly capped at %d", len(res.Content), defaultMaxOutputBytes)
	}
}

func TestBashInternalTimeout(t *testing.T) {
	dir := t.TempDir()
	b := NewBash(dir)
	start := time.Now()
	res, err := b.Run(context.Background(), json.RawMessage(`{"command":"sleep 5","timeout_ms":50}`))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IsError {
		t.Fatalf("IsError = false, want true")
	}
	if !strings.Contains(res.Content, "timed out") {
		t.Fatalf("Content = %q, want a timeout message", res.Content)
	}
	if elapsed > 4*time.Second {
		t.Fatalf("elapsed = %s, want well under the 5s sleep", elapsed)
	}
}

func TestBashTimeoutCappedAtMax(t *testing.T) {
	dir := t.TempDir()
	b := NewBash(dir)
	res, err := b.Run(context.Background(), json.RawMessage(`{"command":"echo hi","timeout_ms":900000}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, want false (cap should not block a fast command)")
	}
}

func TestBashContextCancel(t *testing.T) {
	dir := t.TempDir()
	b := NewBash(dir)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	_, err := b.Run(ctx, json.RawMessage(`{"command":"sleep 5"}`))
	if err == nil {
		t.Fatal("Run err = nil, want context.Canceled")
	}
}

func TestBashMissingCommand(t *testing.T) {
	dir := t.TempDir()
	b := NewBash(dir)
	_, err := b.Run(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("Run err = nil, want error for missing command")
	}
}

func TestBashInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	b := NewBash(dir)
	_, err := b.Run(context.Background(), json.RawMessage(`{not json`))
	if err == nil {
		t.Fatal("Run err = nil, want error for malformed JSON")
	}
}

func TestBashNonexistentRootSurfacesOSReason(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "no", "such", "dir", "xyz")
	b := NewBash(root)
	res, err := b.Run(context.Background(), json.RawMessage(`{"command":"echo hi"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IsError {
		t.Fatalf("IsError = false, want true (root does not exist)")
	}
	if res.Content == "[exit -1]" || strings.TrimSpace(res.Content) == "[exit -1]" {
		t.Fatalf("Content = %q, want the underlying OS reason, not a bare [exit -1]", res.Content)
	}
	if !strings.Contains(res.Content, "no such file or directory") && !strings.Contains(res.Content, "does not exist") {
		t.Fatalf("Content = %q, want it to contain the OS reason (no such file or directory)", res.Content)
	}
	if res.Metadata.ExitCode == nil || *res.Metadata.ExitCode != -1 {
		t.Fatalf("ExitCode = %v, want -1", res.Metadata.ExitCode)
	}
}

func TestBashRunsInRoot(t *testing.T) {
	dir := t.TempDir()
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	b := NewBash(dir)
	res, err := b.Run(context.Background(), json.RawMessage(`{"command":"pwd"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := filepath.EvalSymlinks(strings.TrimSpace(res.Content))
	if err != nil {
		t.Fatalf("EvalSymlinks(output): %v", err)
	}
	if got != resolvedDir {
		t.Fatalf("pwd = %q, want %q", got, resolvedDir)
	}
}

func TestBashAlreadyCancelledContext(t *testing.T) {
	dir := t.TempDir()
	b := NewBash(dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := b.Run(ctx, json.RawMessage(`{"command":"echo hi"}`))
	if err == nil {
		t.Fatal("Run err = nil, want context.Canceled")
	}
}
