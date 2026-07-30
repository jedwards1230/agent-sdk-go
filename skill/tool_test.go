package skill

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestNewToolDescriptionListsIndexNotBody(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "review", skillMD("review", "Reviews a diff.", "the body must not appear here"))

	set, diags := Load([]string{dir}, Options{})
	if len(diags) != 0 {
		t.Fatalf("diags = %v", diags)
	}
	tl := NewTool(set)

	desc := tl.Description()
	if !strings.Contains(desc, "review") || !strings.Contains(desc, "Reviews a diff.") {
		t.Fatalf("Description() = %q, want it to list the skill", desc)
	}
	if strings.Contains(desc, "the body must not appear here") {
		t.Fatal("Description() leaked the skill body")
	}
}

func TestNewToolRunLoadsBody(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "review", skillMD("review", "Reviews a diff.", "do the review carefully"))
	set, _ := Load([]string{dir}, Options{})
	tl := NewTool(set)

	input, err := json.Marshal(map[string]string{"name": "review"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	res, err := tl.Run(context.Background(), input)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatalf("Run() IsError = true, Content = %q", res.Content)
	}
	if res.Content != "do the review carefully" {
		t.Fatalf("Run() Content = %q", res.Content)
	}
	if !res.FullResult {
		t.Fatal("Run() FullResult = false, want true (skill bodies are not spill-excerpted)")
	}
}

func TestNewToolRunUnknownSkillIsResultError(t *testing.T) {
	set, _ := Load([]string{t.TempDir()}, Options{})
	tl := NewTool(set)

	input, err := json.Marshal(map[string]string{"name": "nope"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	res, err := tl.Run(context.Background(), input)
	if err != nil {
		t.Fatalf("Run() returned an error for an unknown skill, want a Result with IsError set: %v", err)
	}
	if !res.IsError {
		t.Fatal("Run() IsError = false, want true for an unknown skill")
	}
}

func TestNewToolRunMissingNameIsError(t *testing.T) {
	set, _ := Load([]string{t.TempDir()}, Options{})
	tl := NewTool(set)

	if _, err := tl.Run(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("Run() with no name should return an error (malformed call), not a Result")
	}
}

func TestNewToolNotAutoRegistered(t *testing.T) {
	// NewTool returns a value; nothing in the skill package touches a
	// tool.Registry. This test documents the invariant rather than probing
	// for it mechanically — see the package/tool.go doc comments.
	dir := t.TempDir()
	writeSkill(t, dir, "s", skillMD("s", "d", "b"))
	set, _ := Load([]string{dir}, Options{})
	tl := NewTool(set)
	if tl.Name() != ToolName {
		t.Fatalf("Name() = %q, want %q", tl.Name(), ToolName)
	}
}
