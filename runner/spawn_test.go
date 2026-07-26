package runner_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/runner"
	"github.com/jedwards1230/agent-sdk-go/session"
)

// newSpawnRunner builds a hermetic Runner over root/cwd with an unscripted
// provider — enough to exercise the spawn seam, which never drives the loop.
// Extra options (lineage, cap) are applied by mutate before construction.
func newSpawnRunner(t *testing.T, root, cwd string, mutate func(*runner.Options)) *runner.Runner {
	t.Helper()
	opts := runner.Options{
		Root:     root,
		Cwd:      cwd,
		Model:    testModel,
		Provider: &scriptedProvider{},
		Tools:    oneToolRegistry{},
	}
	if mutate != nil {
		mutate(&opts)
	}
	r, err := runner.New(context.Background(), opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

// TestSpawnLinksChild asserts Spawn parents the child on the runner that
// spawned it: the child reports the parent's id and depth+1, its journal
// records that lineage durably in the root meta entry, and the parent's stream
// carries a session.spawned naming the child, its agent, and its depth.
func TestSpawnLinksChild(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cwd := t.TempDir()

	parent := newSpawnRunner(t, root, cwd, nil)
	defer func() { _ = parent.Close() }()

	if parent.ParentID() != "" || parent.Depth() != 0 {
		t.Fatalf("root runner lineage = {%q, %d}, want {\"\", 0}", parent.ParentID(), parent.Depth())
	}
	if parent.MaxDepth() != runner.DefaultMaxDepth {
		t.Fatalf("MaxDepth() = %d, want DefaultMaxDepth (%d)", parent.MaxDepth(), runner.DefaultMaxDepth)
	}

	sub := parent.EventsLive()
	child, err := parent.Spawn(ctx, runner.Options{
		Cwd:      cwd,
		Model:    testModel,
		Agent:    "researcher",
		Provider: &scriptedProvider{},
		Tools:    oneToolRegistry{},
		// ParentID/Depth are deliberately wrong here: the parent is
		// authoritative and must overwrite both.
		ParentID: "someone-else",
		Depth:    99,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer func() { _ = child.Close() }()

	if child.ParentID() != parent.ID() {
		t.Errorf("child.ParentID() = %q, want the parent's id %q", child.ParentID(), parent.ID())
	}
	if child.Depth() != 1 {
		t.Errorf("child.Depth() = %d, want 1", child.Depth())
	}
	if child.MaxDepth() != runner.DefaultMaxDepth {
		t.Errorf("child.MaxDepth() = %d, want the parent's cap %d", child.MaxDepth(), runner.DefaultMaxDepth)
	}
	if child.ID() == parent.ID() {
		t.Fatal("child shares the parent's session id")
	}

	// The lineage is durable: it is in the child journal's root meta entry, so a
	// roster reads it back with ReadEntries+MetaOf without resuming.
	entries, err := session.ReadEntries(child.JournalPath())
	if err != nil {
		t.Fatalf("ReadEntries(child): %v", err)
	}
	meta, ok := session.MetaOf(entries)
	if !ok {
		t.Fatal("MetaOf(child journal): ok = false, want true")
	}
	if meta.ParentID != parent.ID() || meta.Depth != 1 {
		t.Errorf("child journal lineage = {%q, %d}, want {%q, 1}", meta.ParentID, meta.Depth, parent.ID())
	}

	// The parent's stream announces the child.
	spawned := awaitSpawned(t, sub)
	if spawned.SessionID() != parent.ID() {
		t.Errorf("session.spawned session_id = %q, want the parent %q", spawned.SessionID(), parent.ID())
	}
	if spawned.ChildID != child.ID() {
		t.Errorf("session.spawned child_id = %q, want %q", spawned.ChildID, child.ID())
	}
	if spawned.Agent != "researcher" {
		t.Errorf("session.spawned agent = %q, want researcher", spawned.Agent)
	}
	if spawned.Depth != 1 {
		t.Errorf("session.spawned depth = %d, want 1", spawned.Depth)
	}

	// The parent's journal is untouched by the spawn: lineage lives on the child
	// side, and the parent's journaling consumer ignores the notice rather than
	// appending an entry mid-turn.
	parentEntries, err := session.ReadEntries(parent.JournalPath())
	if err != nil {
		t.Fatalf("ReadEntries(parent): %v", err)
	}
	if len(parentEntries) != 1 || parentEntries[0].Type != session.EntryMeta {
		t.Errorf("parent journal = %+v, want just its root meta entry", parentEntries)
	}
}

// awaitSpawned reads sub until a session.spawned arrives, failing the test if
// the subscription closes first.
func awaitSpawned(t *testing.T, sub *event.Subscription) event.SessionSpawned {
	t.Helper()
	for e := range sub.C {
		if ev, ok := e.(event.SessionSpawned); ok {
			return ev
		}
	}
	t.Fatal("subscription closed before session.spawned arrived")
	return event.SessionSpawned{}
}

// TestSpawnDepthCap asserts the cap is enforced at spawn time — against
// DefaultMaxDepth by default and against Options.MaxDepth when set — that the
// refusal is matchable with errors.Is(err, ErrMaxDepth), and that a refused
// spawn creates no session at all (no orphan journal on disk).
func TestSpawnDepthCap(t *testing.T) {
	tests := []struct {
		name     string
		depth    int
		maxDepth int
		wantErr  bool
	}{
		{name: "root spawns under the default cap", depth: 0},
		{name: "one below the default cap", depth: runner.DefaultMaxDepth - 1},
		{name: "at the default cap", depth: runner.DefaultMaxDepth, wantErr: true},
		{name: "past the default cap", depth: runner.DefaultMaxDepth + 3, wantErr: true},
		{name: "custom cap admits", depth: 1, maxDepth: 2},
		{name: "custom cap refuses", depth: 2, maxDepth: 2, wantErr: true},
		{name: "custom cap of 0 falls back to the default", depth: 1, maxDepth: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			cwd := t.TempDir()

			parent := newSpawnRunner(t, root, cwd, func(o *runner.Options) {
				o.MaxDepth = tc.maxDepth
				if tc.depth > 0 {
					o.ParentID = "ancestor-session"
					o.Depth = tc.depth
				}
			})
			defer func() { _ = parent.Close() }()

			child, err := parent.Spawn(ctx, runner.Options{
				Cwd: cwd, Model: testModel,
				Provider: &scriptedProvider{},
				Tools:    oneToolRegistry{},
			})
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("Spawn: %v", err)
				}
				if got := child.Depth(); got != tc.depth+1 {
					t.Errorf("child.Depth() = %d, want %d", got, tc.depth+1)
				}
				_ = child.Close()
				return
			}

			if !errors.Is(err, runner.ErrMaxDepth) {
				t.Fatalf("Spawn err = %v, want one matching ErrMaxDepth", err)
			}
			if child != nil {
				t.Fatalf("Spawn returned a child alongside the cap error: %s", child.ID())
			}

			// Nothing was created: the project still holds exactly the parent's
			// session, so a refused spawn leaves no orphan journal.
			store, err := session.NewFileStore(session.WithRoot(root))
			if err != nil {
				t.Fatalf("NewFileStore: %v", err)
			}
			defer func() { _ = store.Close() }()
			ids, err := store.List(ctx, session.Slugify(cwd))
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(ids) != 1 || ids[0] != parent.ID() {
				t.Errorf("sessions after a refused spawn = %v, want just the parent %q", ids, parent.ID())
			}
		})
	}
}

// TestSpawnSharesParentStore asserts a child spawned without its own Store
// inherits the parent's and does NOT own it: closing the child leaves the
// parent's store live, so the parent keeps journaling.
func TestSpawnSharesParentStore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cwd := t.TempDir()

	// The parent is scripted for one turn, driven after the child closes.
	parent := newSpawnRunner(t, root, cwd, func(o *runner.Options) {
		o.Provider = &scriptedProvider{events: [][]provider.StreamEvent{{
			{Type: provider.StreamTextDelta, Text: "still here"},
			{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
		}}}
	})
	defer func() { _ = parent.Close() }()

	child, err := parent.Spawn(ctx, runner.Options{
		Cwd: cwd, Model: testModel,
		Provider: &scriptedProvider{},
		Tools:    oneToolRegistry{},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := child.Close(); err != nil {
		t.Fatalf("child.Close: %v", err)
	}

	// The parent is still fully usable after the child closed: it drives a turn
	// and journals it, which it could not do against a closed store.
	if err := parent.Prompt(ctx, "still alive?"); err != nil {
		t.Fatalf("parent.Prompt after child.Close: %v", err)
	}
	entries, err := session.ReadEntries(parent.JournalPath())
	if err != nil {
		t.Fatalf("ReadEntries(parent): %v", err)
	}
	if len(skipMeta(entries)) == 0 {
		t.Errorf("parent journal has no conversation entries after Prompt: %+v", entries)
	}
}

// TestSpawnedChildResumeRestoresLineage asserts Resume recovers a child's
// parent id and depth from its journal — without it a resumed child would
// report depth 0 and the spawn cap would silently stop holding across a daemon
// restart — while MaxDepth still comes from Options (it is policy, not journal
// state).
func TestSpawnedChildResumeRestoresLineage(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cwd := t.TempDir()

	parent := newSpawnRunner(t, root, cwd, func(o *runner.Options) {
		o.ParentID, o.Depth = "grandparent-session", 1
	})
	child, err := parent.Spawn(ctx, runner.Options{
		Cwd: cwd, Model: testModel, Agent: "researcher",
		Provider: &scriptedProvider{},
		Tools:    oneToolRegistry{},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	childID, parentID := child.ID(), parent.ID()
	if err := child.Close(); err != nil {
		t.Fatalf("child.Close: %v", err)
	}
	if err := parent.Close(); err != nil {
		t.Fatalf("parent.Close: %v", err)
	}

	resumed, err := runner.Resume(ctx, childID, runner.Options{
		Root: root, Cwd: cwd, Model: testModel,
		Provider: &scriptedProvider{},
		Tools:    oneToolRegistry{},
		MaxDepth: 9,
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer func() { _ = resumed.Close() }()

	if resumed.ParentID() != parentID {
		t.Errorf("resumed.ParentID() = %q, want %q", resumed.ParentID(), parentID)
	}
	if resumed.Depth() != 2 {
		t.Errorf("resumed.Depth() = %d, want 2", resumed.Depth())
	}
	if resumed.MaxDepth() != 9 {
		t.Errorf("resumed.MaxDepth() = %d, want 9 (the cap is policy from Options, not journal state)", resumed.MaxDepth())
	}
}

// TestResumeRootWithoutMetaEntry asserts Resume does not regress on a journal
// that has no meta entry at all (one written before runner.New recorded
// metadata): it resumes as a root session at depth 0.
func TestResumeRootWithoutMetaEntry(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cwd := t.TempDir()

	// Build a journal by hand with no meta entry — the pre-metadata shape.
	store, err := session.NewFileStore(session.WithRoot(root))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	j, err := store.Create(ctx, session.Slugify(cwd))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := j.Append(session.NewMessageEntry(provider.UserText("hi"))); err != nil {
		t.Fatalf("Append: %v", err)
	}
	id := j.ID()
	if err := store.Close(); err != nil {
		t.Fatalf("store.Close: %v", err)
	}

	r, err := runner.Resume(ctx, id, runner.Options{
		Root: root, Cwd: cwd, Model: testModel,
		Provider: &scriptedProvider{},
		Tools:    oneToolRegistry{},
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer func() { _ = r.Close() }()

	if r.ParentID() != "" || r.Depth() != 0 {
		t.Errorf("lineage of a meta-less journal = {%q, %d}, want a root session", r.ParentID(), r.Depth())
	}
	if r.MaxDepth() != runner.DefaultMaxDepth {
		t.Errorf("MaxDepth() = %d, want DefaultMaxDepth", r.MaxDepth())
	}
}
