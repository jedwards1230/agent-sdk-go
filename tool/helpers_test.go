package tool

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestMatchGlob(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		path    string
		want    bool
		wantErr bool
	}{
		{name: "doublestar matches nested", pattern: "**/*.go", path: "a/b/c.go", want: true},
		{name: "doublestar matches top-level", pattern: "**/*.go", path: "c.go", want: true},
		{name: "doublestar prefix matches nested", pattern: "src/**", path: "src/x/y", want: true},
		{name: "doublestar prefix requires prefix", pattern: "src/**", path: "other/x/y", want: false},
		{name: "star does not cross segment", pattern: "*.go", path: "main.go", want: true},
		{name: "star does not cross segment neg", pattern: "*.go", path: "a/main.go", want: false},
		{name: "question mark", pattern: "fil?.go", path: "file.go", want: true},
		{name: "question mark neg", pattern: "fil?.go", path: "fileaa.go", want: false},
		{name: "exact segment match", pattern: "a/b/c.go", path: "a/b/c.go", want: true},
		{name: "bad pattern", pattern: "a[.go", path: "a[.go", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := matchGlob(tc.pattern, tc.path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("matchGlob(%q, %q) err = nil, want error", tc.pattern, tc.path)
				}
				return
			}
			if err != nil {
				t.Fatalf("matchGlob(%q, %q) unexpected err: %v", tc.pattern, tc.path, err)
			}
			if got != tc.want {
				t.Fatalf("matchGlob(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
			}
		})
	}
}

func TestTruncateBytes(t *testing.T) {
	t.Run("under cap", func(t *testing.T) {
		out, truncated := truncateBytes("hello", 10)
		if truncated || out != "hello" {
			t.Fatalf("got %q, %v", out, truncated)
		}
	})

	t.Run("over cap", func(t *testing.T) {
		s := strings.Repeat("a", 20)
		out, truncated := truncateBytes(s, 5)
		if !truncated {
			t.Fatalf("truncated = false, want true")
		}
		if !strings.HasPrefix(out, "aaaaa") {
			t.Fatalf("out = %q, want prefix of 5 a's", out)
		}
		if !strings.Contains(out, "truncated 15 bytes") {
			t.Fatalf("out = %q, want dropped-byte count", out)
		}
	})

	t.Run("utf8 boundary", func(t *testing.T) {
		// "é" is 2 bytes (0xC3 0xA9); cutting at max=1 must not split it.
		s := "é" + strings.Repeat("x", 10)
		out, truncated := truncateBytes(s, 1)
		if !truncated {
			t.Fatalf("truncated = false, want true")
		}
		body := strings.SplitN(out, "\n", 2)[0]
		if !isValidUTF8(body) {
			t.Fatalf("body %q is not valid UTF-8", body)
		}
		if body != "" {
			t.Fatalf("body = %q, want empty (rune boundary before max)", body)
		}
	})

	t.Run("max non-positive means no cap", func(t *testing.T) {
		s := strings.Repeat("a", 100)
		out, truncated := truncateBytes(s, 0)
		if truncated || out != s {
			t.Fatalf("got %q, %v, want unchanged", out, truncated)
		}
		out, truncated = truncateBytes(s, -1)
		if truncated || out != s {
			t.Fatalf("got %q, %v, want unchanged", out, truncated)
		}
	})
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

func TestResolvePath(t *testing.T) {
	t.Run("absolute", func(t *testing.T) {
		abs := filepath.FromSlash("/tmp/foo/../bar")
		got := resolvePath("/root", abs)
		want := filepath.Clean(abs)
		if got != want {
			t.Fatalf("resolvePath = %q, want %q", got, want)
		}
	})

	t.Run("relative", func(t *testing.T) {
		got := resolvePath("/root", "sub/file.go")
		want := filepath.Join("/root", "sub/file.go")
		if got != want {
			t.Fatalf("resolvePath = %q, want %q", got, want)
		}
	})
}

func TestDecodeInput(t *testing.T) {
	type in struct {
		Path string `json:"path"`
	}

	t.Run("empty raw yields zero value", func(t *testing.T) {
		var v in
		if err := decodeInput(nil, &v); err != nil {
			t.Fatalf("decodeInput(nil): %v", err)
		}
		if v.Path != "" {
			t.Fatalf("v.Path = %q, want empty", v.Path)
		}

		var v2 in
		if err := decodeInput(json.RawMessage{}, &v2); err != nil {
			t.Fatalf("decodeInput(empty): %v", err)
		}
		if v2.Path != "" {
			t.Fatalf("v2.Path = %q, want empty", v2.Path)
		}
	})

	t.Run("bad json errors", func(t *testing.T) {
		var v in
		err := decodeInput(json.RawMessage(`{not json`), &v)
		if err == nil {
			t.Fatal("decodeInput(bad json) = nil error, want error")
		}
	})

	t.Run("valid json decodes", func(t *testing.T) {
		var v in
		if err := decodeInput(json.RawMessage(`{"path":"a.go"}`), &v); err != nil {
			t.Fatalf("decodeInput: %v", err)
		}
		if v.Path != "a.go" {
			t.Fatalf("v.Path = %q, want a.go", v.Path)
		}
	})
}

func TestCtxErr(t *testing.T) {
	ctx := context.Background()
	if err := ctxErr(ctx); err != nil {
		t.Fatalf("ctxErr(live) = %v, want nil", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ctxErr(cancelled); err == nil {
		t.Fatal("ctxErr(cancelled) = nil, want error")
	}
}
