package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// setupGlobTree builds:
//
//	root/main.go
//	root/README.md
//	root/sub/a.go
//	root/sub/deep/b.go
//	root/.git/ignored.go
func setupGlobTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite := func(rel string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("main.go")
	mustWrite("README.md")
	mustWrite("sub/a.go")
	mustWrite("sub/deep/b.go")
	mustWrite(".git/ignored.go")
	return dir
}

func TestGlobDoubleStarNested(t *testing.T) {
	dir := setupGlobTree(t)
	g := NewGlob(dir)
	res, err := g.Run(context.Background(), json.RawMessage(`{"pattern":"**/*.go"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, content: %q", res.Content)
	}
	got := strings.Split(res.Content, "\n")
	want := []string{"main.go", "sub/a.go", "sub/deep/b.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("results = %v, want %v", got, want)
	}
}

func TestGlobTopLevelOnly(t *testing.T) {
	dir := setupGlobTree(t)
	g := NewGlob(dir)
	res, err := g.Run(context.Background(), json.RawMessage(`{"pattern":"*.md"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Content != "README.md" {
		t.Fatalf("Content = %q, want %q", res.Content, "README.md")
	}
}

func TestGlobNoMatch(t *testing.T) {
	dir := setupGlobTree(t)
	g := NewGlob(dir)
	res, err := g.Run(context.Background(), json.RawMessage(`{"pattern":"*.zzz"}`))
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

func TestGlobSkipsGitDir(t *testing.T) {
	dir := setupGlobTree(t)
	g := NewGlob(dir)
	res, err := g.Run(context.Background(), json.RawMessage(`{"pattern":"**/*.go"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(res.Content, ".git") {
		t.Fatalf("Content = %q, .git directory must be skipped", res.Content)
	}
}

func TestGlobSortedOrder(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"z.go", "a.go", "m.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	g := NewGlob(dir)
	res, err := g.Run(context.Background(), json.RawMessage(`{"pattern":"*.go"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := "a.go\nm.go\nz.go"
	if res.Content != want {
		t.Fatalf("Content = %q, want lexicographically sorted %q", res.Content, want)
	}
}

func TestGlobMissingPattern(t *testing.T) {
	dir := t.TempDir()
	g := NewGlob(dir)
	_, err := g.Run(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("Run err = nil, want error for missing pattern")
	}
}
