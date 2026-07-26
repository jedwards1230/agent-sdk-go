package session_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/session"
)

// newTestJournal returns a live journal in a fresh temp store with
// deterministic entry ids ("e-000001", ...) and timestamps.
func newTestJournal(t *testing.T) *session.Journal {
	t.Helper()
	store, err := session.NewFileStore(
		session.WithRoot(t.TempDir()),
		session.WithStoreIDGen(newCounterIDGen("e")),
		session.WithStoreClock(newStepClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Second)),
	)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	j, err := store.Create(context.Background(), "proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return j
}

// foldText renders a folded context as "role:text" lines, for compact
// assertions about what a model would see.
func foldText(msgs []provider.Message) string {
	var b strings.Builder
	for i, m := range msgs {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(string(m.Role))
		b.WriteString(":")
		for _, blk := range m.Content {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// TestCheckpointEntryContributesNothingToFold is the contract that a
// checkpoint is a MARKER: a checkpoint appended between two messages must not
// change the folded context by so much as a block — its label never reaches
// the model.
func TestCheckpointEntryContributesNothingToFold(t *testing.T) {
	withoutJ := newTestJournal(t)
	for _, e := range []session.Entry{
		session.NewMessageEntry(provider.UserText("hello")),
		session.NewMessageEntry(provider.AssistantText("hi there")),
	} {
		if _, err := withoutJ.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	want := foldText(withoutJ.Fold())
	if want != "user:hello\nassistant:hi there" {
		t.Fatalf("baseline fold = %q, unexpected", want)
	}

	withJ := newTestJournal(t)
	for _, e := range []session.Entry{
		session.NewMessageEntry(provider.UserText("hello")),
		session.NewCheckpointEntry("before-refactor"),
		session.NewMessageEntry(provider.AssistantText("hi there")),
	} {
		if _, err := withJ.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if got := foldText(withJ.Fold()); got != want {
		t.Errorf("fold with a checkpoint = %q, want %q (a checkpoint must render nothing)", got, want)
	}
	// The marker is in the log even though it is not in the context.
	if got := withJ.Len(); got != 3 {
		t.Errorf("Len() = %d, want 3 (the checkpoint is journaled)", got)
	}

	// A checkpoint at HEAD is equally invisible.
	if _, err := withJ.Append(session.NewCheckpointEntry("tail")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if got := foldText(withJ.Fold()); got != want {
		t.Errorf("fold with a checkpoint at HEAD = %q, want %q", got, want)
	}
}

// TestCheckpoints asserts the extraction helper picks out exactly the
// checkpoint entries, in append order, with their id/label/time — the listing
// a roster builds over a plain entry slice.
func TestCheckpoints(t *testing.T) {
	cases := []struct {
		name    string
		entries []session.Entry
		want    []string // "<id>=<label>" in order
	}{
		{name: "no entries"},
		{
			name: "no checkpoints",
			entries: []session.Entry{
				{ID: "e-1", Type: session.EntryMessage},
				{ID: "e-2", Type: session.EntryForkPoint},
			},
		},
		{
			name: "mixed, append order",
			entries: []session.Entry{
				{ID: "e-1", Type: session.EntryMeta},
				withID("e-2", session.NewCheckpointEntry("first")),
				{ID: "e-3", Type: session.EntryMessage},
				withID("e-4", session.NewCheckpointEntry("second")),
			},
			want: []string{"e-2=first", "e-4=second"},
		},
		{
			name: "duplicate labels are all listed",
			entries: []session.Entry{
				withID("e-1", session.NewCheckpointEntry("mark")),
				withID("e-2", session.NewCheckpointEntry("mark")),
			},
			want: []string{"e-1=mark", "e-2=mark"},
		},
		{
			name: "malformed payload is skipped, not fatal",
			entries: []session.Entry{
				{ID: "e-1", Type: session.EntryCheckpoint, Payload: []byte(`"not an object"`)},
				withID("e-2", session.NewCheckpointEntry("ok")),
			},
			want: []string{"e-2=ok"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := session.Checkpoints(tc.entries)
			if len(got) != len(tc.want) {
				t.Fatalf("Checkpoints() = %+v, want %d entries", got, len(tc.want))
			}
			for i, w := range tc.want {
				if g := got[i].ID + "=" + got[i].Label; g != w {
					t.Errorf("checkpoint %d = %q, want %q", i, g, w)
				}
			}
		})
	}
}

// withID stamps an id on a constructed entry so a test can build an entry
// slice without a live journal (which is what assigns ids in production).
func withID(id string, e session.Entry) session.Entry {
	e.ID = id
	return e
}

// TestCheckpointsOffDiskWithoutResuming asserts the read path a roster needs:
// ReadEntries + Checkpoints lists a persisted session's checkpoints with no
// live journal, no resume, and no fold.
func TestCheckpointsOffDiskWithoutResuming(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := session.NewFileStore(session.WithRoot(root), session.WithStoreIDGen(newCounterIDGen("e")))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	j, err := store.Create(ctx, "proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	path := j.Path()
	if _, err := j.Append(session.NewMessageEntry(provider.UserText("hello"))); err != nil {
		t.Fatalf("Append: %v", err)
	}
	marker, err := j.Append(session.NewCheckpointEntry("before-refactor"))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	entries, err := session.ReadEntries(path)
	if err != nil {
		t.Fatalf("ReadEntries: %v", err)
	}
	got := session.Checkpoints(entries)
	if len(got) != 1 {
		t.Fatalf("Checkpoints() = %+v, want 1", got)
	}
	if got[0].ID != marker.ID || got[0].Label != "before-refactor" {
		t.Errorf("checkpoint = %+v, want id %q label %q", got[0], marker.ID, "before-refactor")
	}
	if got[0].Time.IsZero() {
		t.Error("checkpoint Time is zero, want the entry's append time")
	}
}

// TestLegacyJournalReadsBackUnchanged is the backward-compatibility contract:
// a journal written before EntryCheckpoint and MetaPayload.Role existed — no
// role field, no checkpoint entries — still reads back with every entry
// intact, folds to the same context, and reports no checkpoints. The fixture
// is raw JSONL written by hand, exactly as an older SDK would have left it.
func TestLegacyJournalReadsBackUnchanged(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions", "proj")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, "legacy.jsonl")
	legacy := strings.Join([]string{
		`{"id":"e-1","type":"session_meta","time":"2026-01-01T00:00:00Z","payload":{"cwd":"/home/user/project"}}`,
		`{"id":"e-2","parent":"e-1","type":"message","time":"2026-01-01T00:00:01Z","payload":{"role":"user","blocks":[{"type":"text","text":"hello"}]}}`,
		`{"id":"e-3","parent":"e-2","type":"message","time":"2026-01-01T00:00:02Z","model":"m1","payload":{"role":"assistant","blocks":[{"type":"text","text":"hi there"}]}}`,
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	entries, err := session.ReadEntries(path)
	if err != nil {
		t.Fatalf("ReadEntries: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("ReadEntries() returned %d entries, want 3", len(entries))
	}
	if entries[0].Type != session.EntryMeta {
		t.Fatalf("entry 0 type = %q, want %q", entries[0].Type, session.EntryMeta)
	}
	mp, err := entries[0].Meta()
	if err != nil {
		t.Fatalf("Meta(): %v", err)
	}
	if mp.Cwd != "/home/user/project" {
		t.Errorf("Meta().Cwd = %q, want the recorded cwd", mp.Cwd)
	}
	if mp.Role != "" {
		t.Errorf("Meta().Role = %q, want empty for a journal predating the field", mp.Role)
	}
	if got := session.Checkpoints(entries); got != nil {
		t.Errorf("Checkpoints() = %+v, want none", got)
	}

	// Resuming it folds to exactly the two conversation messages.
	store, err := session.NewFileStore(session.WithRoot(root))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer func() { _ = store.Close() }()
	j, err := store.Open(context.Background(), "legacy")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got, want := foldText(j.Fold()), "user:hello\nassistant:hi there"; got != want {
		t.Errorf("Fold() = %q, want %q", got, want)
	}
	if got := j.Head(); got != "e-3" {
		t.Errorf("Head() = %q, want e-3", got)
	}
}
