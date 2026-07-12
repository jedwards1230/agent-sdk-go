package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadLineNumbering(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRead(dir)
	res, err := r.Run(context.Background(), json.RawMessage(`{"path":"f.txt"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, content: %q", res.Content)
	}
	want := "     1\tone\n     2\ttwo\n     3\tthree\n"
	if res.Content != want {
		t.Fatalf("Content = %q, want %q", res.Content, want)
	}
}

func TestReadOffsetLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	lines := []string{"a", "b", "c", "d", "e"}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRead(dir)
	res, err := r.Run(context.Background(), json.RawMessage(`{"path":"f.txt","offset":2,"limit":2}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := "     2\tb\n     3\tc\n"
	if res.Content != want {
		t.Fatalf("Content = %q, want %q", res.Content, want)
	}
}

func TestReadOffsetPastEOF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRead(dir)
	res, err := r.Run(context.Background(), json.RawMessage(`{"path":"f.txt","offset":10}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IsError {
		t.Fatalf("IsError = false, want true")
	}
	if !strings.Contains(res.Content, "past end of file") {
		t.Fatalf("Content = %q, want past-EOF message", res.Content)
	}
}

func TestReadNotFound(t *testing.T) {
	dir := t.TempDir()
	r := NewRead(dir)
	res, err := r.Run(context.Background(), json.RawMessage(`{"path":"missing.txt"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IsError {
		t.Fatalf("IsError = false, want true")
	}
	if !strings.Contains(res.Content, "file not found") {
		t.Fatalf("Content = %q, want not-found message", res.Content)
	}
}

func TestReadDirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	r := NewRead(dir)
	res, err := r.Run(context.Background(), json.RawMessage(`{"path":"sub"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IsError {
		t.Fatalf("IsError = false, want true")
	}
	if !strings.Contains(res.Content, "is a directory") {
		t.Fatalf("Content = %q, want directory message", res.Content)
	}
}

func TestReadEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRead(dir)
	res, err := r.Run(context.Background(), json.RawMessage(`{"path":"empty.txt"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, want false for empty file")
	}
	if res.Content != "" {
		t.Fatalf("Content = %q, want empty", res.Content)
	}
}

func TestReadMissingPath(t *testing.T) {
	dir := t.TempDir()
	r := NewRead(dir)
	_, err := r.Run(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("Run err = nil, want error for missing path")
	}
}
