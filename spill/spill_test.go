package spill_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/spill"
)

// writeInChunks streams p through w in size-byte writes, mimicking a process
// pumping output through the sink incrementally.
func writeInChunks(t *testing.T, w *spill.Writer, p []byte, size int) {
	t.Helper()
	for off := 0; off < len(p); off += size {
		end := off + size
		if end > len(p) {
			end = len(p)
		}
		if _, err := w.Write(p[off:end]); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
}

func sha(p []byte) string {
	sum := sha256.Sum256(p)
	return hex.EncodeToString(sum[:])
}

func TestSmallOutputIsWholeExcerpt(t *testing.T) {
	dir := t.TempDir()
	w, err := spill.Create(dir, "sessions/p/s/calls", "c1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	content := []byte("hello, spill\n")
	writeInChunks(t, w, content, 3)
	ref, err := w.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	if ref.Path != "sessions/p/s/calls/c1.log" {
		t.Errorf("Path = %q, want forward-slashed root-relative", ref.Path)
	}
	if ref.Bytes != int64(len(content)) {
		t.Errorf("Bytes = %d, want %d", ref.Bytes, len(content))
	}
	if ref.SHA256 != sha(content) {
		t.Errorf("SHA256 = %s, want %s", ref.SHA256, sha(content))
	}
	if ref.Elided {
		t.Error("Elided = true, want false for a small output")
	}
	if ref.Excerpt != string(content) {
		t.Errorf("Excerpt = %q, want the whole content", ref.Excerpt)
	}

	// The on-disk file holds the full, untruncated content.
	onDisk, err := os.ReadFile(filepath.Join(dir, "c1.log"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(onDisk, content) {
		t.Errorf("file = %q, want %q", onDisk, content)
	}
}

func TestLargeOutputStreamsFullyAndExcerptsHeadTail(t *testing.T) {
	dir := t.TempDir()
	w, err := spill.Create(dir, "sessions/p/s/calls", "big")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 5 MiB — far larger than the excerpt window + any pipe buffer, so a naive
	// full-buffer implementation would be caught. Distinct bytes per position so
	// head/tail boundaries are verifiable.
	const n = 5 << 20
	content := make([]byte, n)
	for i := range content {
		content[i] = byte('A' + (i % 26))
	}
	writeInChunks(t, w, content, 4096)
	ref, err := w.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	if ref.Bytes != n {
		t.Errorf("Bytes = %d, want %d", ref.Bytes, n)
	}
	if ref.SHA256 != sha(content) {
		t.Errorf("SHA256 mismatch")
	}
	if !ref.Elided {
		t.Error("Elided = false, want true for a large output")
	}

	// The on-disk file is complete and matches the reference.
	onDisk, err := os.ReadFile(filepath.Join(dir, "big.log"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if int64(len(onDisk)) != ref.Bytes {
		t.Fatalf("file len = %d, want %d", len(onDisk), ref.Bytes)
	}
	if sha(onDisk) != ref.SHA256 {
		t.Errorf("on-disk sha != ref sha")
	}

	// Excerpt = head + elision marker + tail, bounded well under the full size.
	if len(ref.Excerpt) > 8<<10 {
		t.Errorf("excerpt len = %d, want a small bounded preview", len(ref.Excerpt))
	}
	if !strings.HasPrefix(ref.Excerpt, string(content[:2<<10])) {
		t.Error("excerpt does not start with the head of the content")
	}
	if !strings.HasSuffix(ref.Excerpt, string(content[n-(2<<10):])) {
		t.Error("excerpt does not end with the tail of the content")
	}
	omitted := n - (2 << 10) - (2 << 10)
	if !strings.Contains(ref.Excerpt, fmt.Sprintf("[%d bytes elided]", omitted)) {
		t.Errorf("excerpt missing elision marker for %d bytes: %.80q…", omitted, ref.Excerpt)
	}
}

func TestExcerptSeamAtWindowBoundary(t *testing.T) {
	// n between headBytes and headBytes+tailBytes must reconstruct the whole
	// content with no elision and no gap/overlap at the head/tail seam.
	const n = 3000 // 2048 head + 952 from tail, no elision
	content := make([]byte, n)
	for i := range content {
		content[i] = byte('a' + (i % 26))
	}
	w, err := spill.Create("", "", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	writeInChunks(t, w, content, 7)
	ref, err := w.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if ref.Elided {
		t.Errorf("Elided = true, want false when n (%d) ≤ head+tail", n)
	}
	if ref.Excerpt != string(content) {
		t.Errorf("excerpt did not reconstruct the whole content at the window boundary")
	}
}

func TestFilelessWriterHasNoPathButBoundedExcerpt(t *testing.T) {
	w, err := spill.Create("", "", "ignored")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	big := bytes.Repeat([]byte("x"), 1<<20)
	writeInChunks(t, w, big, 8192)
	ref, err := w.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if ref.Path != "" {
		t.Errorf("Path = %q, want empty for a file-less writer", ref.Path)
	}
	if ref.Bytes != int64(len(big)) || ref.SHA256 != sha(big) {
		t.Errorf("file-less writer still tracks bytes/sha: got %d/%s", ref.Bytes, ref.SHA256)
	}
	if !ref.Elided || len(ref.Excerpt) > 8<<10 {
		t.Errorf("file-less excerpt should be bounded+elided, got len %d elided %v", len(ref.Excerpt), ref.Elided)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	w, err := spill.Create(dir, "rel", "c")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := w.Write([]byte("data")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	ref1, err := w.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	ref2, err := w.Close()
	if err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if ref1 != ref2 {
		t.Errorf("Close not idempotent: %+v vs %+v", ref1, ref2)
	}
	if _, err := w.Write([]byte("more")); err == nil {
		t.Error("Write after Close should error")
	}
}

func TestAppendOnlyDoesNotTruncateExisting(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "c.log")
	if err := os.WriteFile(abs, []byte("PRIOR\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	w, err := spill.Create(dir, "rel", "c")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := w.Write([]byte("NEW\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "PRIOR\nNEW\n" {
		t.Errorf("append-only file = %q, want prior content preserved then appended", got)
	}
}

func TestCallIDSanitizedToSafeFilename(t *testing.T) {
	dir := t.TempDir()
	w, err := spill.Create(dir, "rel", "../../etc/passwd")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The file must land inside dir, not escape it.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].IsDir() {
		t.Fatalf("want a single spill file in dir, got %v", entries)
	}
	if strings.ContainsAny(entries[0].Name(), `/\`) || strings.Contains(entries[0].Name(), "..") {
		t.Errorf("unsafe spill filename %q", entries[0].Name())
	}
}
