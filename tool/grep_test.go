package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupGrepTree builds:
//
//	root/a.go       (contains "needle")
//	root/sub/b.go   (contains "Needle")
//	root/sub/c.txt  (contains "needle")
//	root/.git/d.go  (contains "needle", must be skipped)
func setupGrepTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("a.go", "package a\n// needle here\n")
	mustWrite("sub/b.go", "package sub\n// Needle here\n")
	mustWrite("sub/c.txt", "needle in text\n")
	mustWrite(".git/d.go", "// needle in git\n")
	return dir
}

func TestGrepBasicMatch(t *testing.T) {
	dir := setupGrepTree(t)
	g := NewGrep(dir)
	res, err := g.Run(context.Background(), json.RawMessage(`{"pattern":"needle"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, content: %q", res.Content)
	}
	if !strings.Contains(res.Content, "a.go:2:") {
		t.Fatalf("Content = %q, want a.go:2: line", res.Content)
	}
	if !strings.Contains(res.Content, "sub/c.txt:1:") {
		t.Fatalf("Content = %q, want sub/c.txt:1: line", res.Content)
	}
	if strings.Contains(res.Content, "sub/b.go") {
		t.Fatalf("Content = %q, should not match case-sensitive \"needle\" against \"Needle\"", res.Content)
	}
}

func TestGrepIgnoreCase(t *testing.T) {
	dir := setupGrepTree(t)
	g := NewGrep(dir)
	res, err := g.Run(context.Background(), json.RawMessage(`{"pattern":"needle","ignore_case":true}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Content, "sub/b.go:2:") {
		t.Fatalf("Content = %q, want sub/b.go:2: line with ignore_case", res.Content)
	}
}

func TestGrepGlobFilter(t *testing.T) {
	dir := setupGrepTree(t)
	g := NewGrep(dir)
	res, err := g.Run(context.Background(), json.RawMessage(`{"pattern":"needle","glob":"**/*.go"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(res.Content, "c.txt") {
		t.Fatalf("Content = %q, glob **/*.go should exclude c.txt", res.Content)
	}
	if !strings.Contains(res.Content, "a.go:2:") {
		t.Fatalf("Content = %q, want a.go:2: line", res.Content)
	}
}

func TestGrepNoMatch(t *testing.T) {
	dir := setupGrepTree(t)
	g := NewGrep(dir)
	res, err := g.Run(context.Background(), json.RawMessage(`{"pattern":"zzz_not_present"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, want false for no matches")
	}
	if res.Content != "no matches" {
		t.Fatalf("Content = %q, want \"no matches\"", res.Content)
	}
}

func TestGrepBadPattern(t *testing.T) {
	dir := setupGrepTree(t)
	g := NewGrep(dir)
	res, err := g.Run(context.Background(), json.RawMessage(`{"pattern":"("}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IsError {
		t.Fatalf("IsError = false, want true for bad pattern")
	}
	if !strings.Contains(res.Content, "invalid pattern") {
		t.Fatalf("Content = %q, want invalid-pattern message", res.Content)
	}
}

func TestGrepBadGlob(t *testing.T) {
	dir := setupGrepTree(t)
	g := NewGrep(dir)
	res, err := g.Run(context.Background(), json.RawMessage(`{"pattern":"needle","glob":"[bad"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IsError {
		t.Fatalf("IsError = false, want true for bad glob")
	}
	if !strings.Contains(res.Content, "invalid glob") {
		t.Fatalf("Content = %q, want invalid-glob message", res.Content)
	}
}

func TestGrepSkipsGitDir(t *testing.T) {
	dir := setupGrepTree(t)
	g := NewGrep(dir)
	res, err := g.Run(context.Background(), json.RawMessage(`{"pattern":"needle","ignore_case":true}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(res.Content, ".git") {
		t.Fatalf("Content = %q, .git directory must be skipped", res.Content)
	}
}

func TestGrepSkipsBinaryFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bin.dat"), []byte("needle\x00binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := NewGrep(dir)
	res, err := g.Run(context.Background(), json.RawMessage(`{"pattern":"needle"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Content != "no matches" {
		t.Fatalf("Content = %q, want \"no matches\" (binary file skipped)", res.Content)
	}
}

func TestGrepMissingPattern(t *testing.T) {
	dir := t.TempDir()
	g := NewGrep(dir)
	_, err := g.Run(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("Run err = nil, want error for missing pattern")
	}
}
