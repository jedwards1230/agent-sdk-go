package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLSMixedEntriesSorted(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "zdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bfile.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ls := NewLS(dir)
	res, err := ls.Run(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, content: %q", res.Content)
	}
	want := "adir/\nbfile.txt\nzdir/"
	if res.Content != want {
		t.Fatalf("Content = %q, want %q", res.Content, want)
	}
}

func TestLSNotFound(t *testing.T) {
	dir := t.TempDir()
	ls := NewLS(dir)
	res, err := ls.Run(context.Background(), json.RawMessage(`{"path":"missing"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IsError {
		t.Fatalf("IsError = false, want true")
	}
}

func TestLSNotADirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ls := NewLS(dir)
	res, err := ls.Run(context.Background(), json.RawMessage(`{"path":"f.txt"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IsError {
		t.Fatalf("IsError = false, want true (not a directory)")
	}
}

func TestLSEmptyDir(t *testing.T) {
	dir := t.TempDir()
	ls := NewLS(dir)
	res, err := ls.Run(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, want false for empty dir")
	}
	if res.Content != "" {
		t.Fatalf("Content = %q, want empty", res.Content)
	}
}
