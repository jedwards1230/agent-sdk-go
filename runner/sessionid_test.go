package runner_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/runner"
	"github.com/jedwards1230/agent-sdk-go/session"
)

// TestRunnerSessionID_Pinned asserts Options.SessionID is used verbatim as the
// session's id — its Runner.ID, its journal filename, and the id a later Resume
// addresses — while entry-id generation still flows from Options.IDGen (pinning
// the session id must not consume the id generator).
func TestRunnerSessionID_Pinned(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()

	prov := &scriptedProvider{events: [][]provider.StreamEvent{
		{
			{Type: provider.StreamTextDelta, Text: "hi"},
			{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
	}}

	const pinned = "pinned-session"
	r, err := runner.New(context.Background(), runner.Options{
		Root: root, Cwd: cwd, Model: testModel, System: "test system",
		Provider:  prov,
		Tools:     oneToolRegistry{},
		SessionID: pinned,
		IDGen:     seqIDGen(),
		Clock:     seqClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if r.ID() != pinned {
		t.Errorf("ID() = %q, want %q", r.ID(), pinned)
	}
	if got := filepath.Base(r.JournalPath()); got != pinned+".jsonl" {
		t.Errorf("journal filename = %q, want %q", got, pinned+".jsonl")
	}

	if err := r.Prompt(context.Background(), "hello"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Entry-id generation is unaffected: the journal's root meta entry (appended
	// by New) got the id generator's first value, proving the pinned session id
	// did not consume it.
	store, err := session.NewFileStore(session.WithRoot(root))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer func() { _ = store.Close() }()
	j, err := store.Open(context.Background(), pinned)
	if err != nil {
		t.Fatalf("Open(%q): %v", pinned, err)
	}
	if j.ID() != pinned {
		t.Errorf("reopened ID() = %q, want %q", j.ID(), pinned)
	}
	entries := j.Entries()
	if len(entries) == 0 || entries[0].Type != session.EntryMeta {
		t.Fatalf("first entry = %+v, want a meta entry", entries)
	}
	if entries[0].ID != "id-0001" {
		t.Errorf("first (meta) entry id = %q, want id-0001 (pinning the session id must not consume IDGen)", entries[0].ID)
	}

	// Resume addresses the pinned id and recovers the prior turn.
	r2, err := runner.Resume(context.Background(), pinned, runner.Options{
		Root: root, Cwd: cwd, Model: testModel, System: "test system",
		Provider: &scriptedProvider{}, Tools: oneToolRegistry{},
	})
	if err != nil {
		t.Fatalf("Resume(%q): %v", pinned, err)
	}
	defer func() { _ = r2.Close() }()
	if r2.ID() != pinned {
		t.Errorf("resumed ID() = %q, want %q", r2.ID(), pinned)
	}
	if fold := r2.Fold(); len(fold) != 2 {
		t.Fatalf("resumed Fold() = %d messages, want 2 (prior user + assistant): %+v", len(fold), fold)
	}
}

// TestRunnerSessionID_EmptyDefault asserts an empty SessionID is unchanged
// behavior: the store generates the id from IDGen exactly as before.
func TestRunnerSessionID_EmptyDefault(t *testing.T) {
	r, err := runner.New(context.Background(), runner.Options{
		Root: t.TempDir(), Cwd: t.TempDir(), Model: testModel, System: "test system",
		Provider:  &scriptedProvider{},
		Tools:     oneToolRegistry{},
		SessionID: "", // default
		IDGen:     seqIDGen(),
		Clock:     seqClock(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = r.Close() }()

	// With no pinned id, the session id is the generator's first value, and the
	// journal filename follows it — identical to pre-SessionID behavior.
	if r.ID() != "id-0001" {
		t.Errorf("ID() = %q, want id-0001 (empty SessionID must fall back to IDGen)", r.ID())
	}
	if got := filepath.Base(r.JournalPath()); got != "id-0001.jsonl" {
		t.Errorf("journal filename = %q, want id-0001.jsonl", got)
	}
}

// TestRunnerSessionID_Invalid asserts a SessionID that is not a safe single
// path component fails New with session.ErrInvalidID (surfaced through the
// runner's create-session wrap) and leaves no session behind.
func TestRunnerSessionID_Invalid(t *testing.T) {
	for _, bad := range []string{"..", "a/b"} {
		_, err := runner.New(context.Background(), runner.Options{
			Root: t.TempDir(), Cwd: t.TempDir(), Model: testModel, System: "test system",
			Provider:  &scriptedProvider{},
			Tools:     oneToolRegistry{},
			SessionID: bad,
		})
		if !errors.Is(err, session.ErrInvalidID) {
			t.Errorf("New(SessionID=%q): err = %v, want session.ErrInvalidID", bad, err)
		}
	}
}
