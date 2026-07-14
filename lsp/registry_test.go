package lsp

import (
	"errors"
	"os/exec"
	"testing"
)

func TestLookup(t *testing.T) {
	r := NewRegistry()
	r.Register(Server{Language: "go", Command: "gopls"})

	t.Run("hit", func(t *testing.T) {
		s, ok := r.Lookup("go")
		if !ok {
			t.Fatal("Lookup(go) ok = false, want true")
		}
		if s.Command != "gopls" {
			t.Errorf("Command = %q, want gopls", s.Command)
		}
	})

	t.Run("miss", func(t *testing.T) {
		if _, ok := r.Lookup("cobol"); ok {
			t.Fatal("Lookup(cobol) ok = true, want false")
		}
	})
}

func TestResolveSuccess(t *testing.T) {
	r := NewRegistry()
	r.Register(Server{Language: "go", Command: "gopls", RootMarkers: []string{"go.mod"}})
	r.lookPath = func(cmd string) (string, error) {
		if cmd != "gopls" {
			t.Fatalf("lookPath called with %q, want gopls", cmd)
		}
		return "/usr/local/bin/gopls", nil
	}

	got, err := r.Resolve("go")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := Resolved{
		Server: Server{Language: "go", Command: "gopls", RootMarkers: []string{"go.mod"}},
		Path:   "/usr/local/bin/gopls",
	}
	if got.Path != want.Path || got.Language != want.Language || got.Command != want.Command {
		t.Errorf("Resolve = %+v, want %+v", got, want)
	}
}

func TestResolveNotRegistered(t *testing.T) {
	r := NewRegistry()
	r.lookPath = func(string) (string, error) { return "", errors.New("should not be called") }

	_, err := r.Resolve("cobol")
	if !errors.Is(err, ErrNotRegistered) {
		t.Fatalf("Resolve error = %v, want wrapping ErrNotRegistered", err)
	}
	if errors.Is(err, ErrNotOnPath) {
		t.Errorf("Resolve error unexpectedly also wraps ErrNotOnPath")
	}
}

func TestResolveNotOnPath(t *testing.T) {
	r := NewRegistry()
	r.Register(Server{Language: "go", Command: "gopls"})
	r.lookPath = func(string) (string, error) { return "", exec.ErrNotFound }

	_, err := r.Resolve("go")
	if !errors.Is(err, ErrNotOnPath) {
		t.Fatalf("Resolve error = %v, want wrapping ErrNotOnPath", err)
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("Resolve error = %v, want also wrapping exec.ErrNotFound", err)
	}
	if errors.Is(err, ErrNotRegistered) {
		t.Errorf("Resolve error unexpectedly also wraps ErrNotRegistered")
	}
}

func TestAvailable(t *testing.T) {
	r := NewRegistry()
	r.Register(Server{Language: "python", Command: "pyright-langserver"})
	r.Register(Server{Language: "go", Command: "gopls"})
	r.Register(Server{Language: "rust", Command: "rust-analyzer"})

	installed := map[string]string{
		"gopls":         "/bin/gopls",
		"rust-analyzer": "/bin/rust-analyzer",
	}
	r.lookPath = func(cmd string) (string, error) {
		if p, ok := installed[cmd]; ok {
			return p, nil
		}
		return "", exec.ErrNotFound
	}

	got := r.Available()
	if len(got) != 2 {
		t.Fatalf("Available() len = %d, want 2 (%+v)", len(got), got)
	}
	if got[0].Language != "go" || got[1].Language != "rust" {
		t.Errorf("Available() languages = [%s, %s], want [go, rust] (sorted)", got[0].Language, got[1].Language)
	}
	if got[0].Path != "/bin/gopls" || got[1].Path != "/bin/rust-analyzer" {
		t.Errorf("Available() paths = [%s, %s]", got[0].Path, got[1].Path)
	}
}

func TestDefaultRegistrySeed(t *testing.T) {
	r := DefaultRegistry()
	want := []string{"go", "typescript", "javascript", "python", "rust", "c", "cpp"}
	for _, lang := range want {
		s, ok := r.Lookup(lang)
		if !ok {
			t.Errorf("DefaultRegistry missing seed language %q", lang)
			continue
		}
		if s.Command == "" {
			t.Errorf("DefaultRegistry seed %q has empty Command", lang)
		}
	}
}
