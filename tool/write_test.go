package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteNewFileWithNestedParents(t *testing.T) {
	dir := t.TempDir()
	w := NewWrite(dir)
	res, err := w.Run(context.Background(), json.RawMessage(`{"path":"a/b/c.txt","content":"hello"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, content: %q", res.Content)
	}
	if !strings.Contains(res.Content, "5 bytes") {
		t.Fatalf("Content = %q, want byte count", res.Content)
	}
	got, err := os.ReadFile(filepath.Join(dir, "a", "b", "c.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("file = %q, want %q", got, "hello")
	}
}

func TestWriteOverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("old content"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := NewWrite(dir)
	res, err := w.Run(context.Background(), json.RawMessage(`{"path":"f.txt","content":"new"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, content: %q", res.Content)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("file = %q, want %q", got, "new")
	}
}

func TestWriteNonexistentRootCreatesIt(t *testing.T) {
	// Write's contract is to create missing parent directories (see
	// Description), so a nonexistent root is not an error case for this tool
	// — unlike ls/glob/read/edit/bash, which require their root to already
	// exist. This test documents that intentional behavior.
	dir := t.TempDir()
	root := filepath.Join(dir, "no", "such", "dir")
	w := NewWrite(root)
	res, err := w.Run(context.Background(), json.RawMessage(`{"path":"f.txt","content":"hi"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, content: %q, want the missing root to be created", res.Content)
	}
	got, err := os.ReadFile(filepath.Join(root, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hi" {
		t.Fatalf("file = %q, want %q", got, "hi")
	}
}

func TestWriteMissingPath(t *testing.T) {
	dir := t.TempDir()
	w := NewWrite(dir)
	_, err := w.Run(context.Background(), json.RawMessage(`{"content":"x"}`))
	if err == nil {
		t.Fatal("Run err = nil, want error for missing path")
	}
}

func TestWriteEmptyContentIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	w := NewWrite(dir)
	res, err := w.Run(context.Background(), json.RawMessage(`{"path":"empty.txt","content":""}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, content: %q", res.Content)
	}
	got, err := os.ReadFile(filepath.Join(dir, "empty.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("file len = %d, want 0", len(got))
	}
}
