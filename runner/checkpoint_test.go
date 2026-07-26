package runner_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/runner"
	"github.com/jedwards1230/agent-sdk-go/session"
)

// textTurns scripts n plain (no tool call) turns, the i-th replying with
// replies[i], each billing one input and one output token so a cost assertion
// has something to count.
func textTurns(replies ...string) *scriptedProvider {
	evs := make([][]provider.StreamEvent, 0, len(replies))
	for _, reply := range replies {
		evs = append(evs, []provider.StreamEvent{
			{Type: provider.StreamTextDelta, Text: reply},
			{Type: provider.StreamFinished, StopReason: provider.StopEndTurn, Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
		})
	}
	return &scriptedProvider{events: evs}
}

// newCheckpointRunner builds a hermetic Runner over the scripted provider, with
// deterministic entry ids and timestamps, closed at test end.
func newCheckpointRunner(t *testing.T, prov provider.Provider, opts ...func(*runner.Options)) *runner.Runner {
	t.Helper()
	o := runner.Options{
		Root: t.TempDir(), Cwd: t.TempDir(), Model: testModel, System: "test system",
		Provider: prov,
		Tools:    oneToolRegistry{},
		IDGen:    seqIDGen(),
		Clock:    seqClock(),
	}
	for _, apply := range opts {
		apply(&o)
	}
	r, err := runner.New(context.Background(), o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// foldText renders a runner's folded context as "role:text" lines, so a test
// can assert on exactly what the next model call would see.
func foldText(msgs []provider.Message) string {
	var b strings.Builder
	for i, m := range msgs {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(string(m.Role))
		b.WriteString(":")
		b.WriteString(msgText(m))
	}
	return b.String()
}

// entryIDs lists a journal's entry ids in append order.
func entryIDs(entries []session.Entry) []string {
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.ID)
	}
	return ids
}

// readEntries reads a runner's journal file straight off disk — the on-disk
// truth, independent of the in-memory journal.
func readEntries(t *testing.T, r *runner.Runner) []session.Entry {
	t.Helper()
	entries, err := session.ReadEntries(r.JournalPath())
	if err != nil {
		t.Fatalf("ReadEntries: %v", err)
	}
	return entries
}

// TestRunnerCheckpoint asserts Checkpoint appends an addressable marker at
// HEAD, that Checkpoints lists them in append order, and that a blank label is
// rejected (an unnamed checkpoint is indistinguishable from a fork point).
func TestRunnerCheckpoint(t *testing.T) {
	ctx := context.Background()
	r := newCheckpointRunner(t, textTurns("one"))

	if err := r.Prompt(ctx, "first"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	id, err := r.Checkpoint("before-refactor")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if id == "" {
		t.Fatal("Checkpoint returned an empty entry id")
	}

	// The returned id addresses a real checkpoint entry at what was HEAD.
	entries := readEntries(t, r)
	last := entries[len(entries)-1]
	if last.ID != id {
		t.Errorf("last entry id = %q, want the returned checkpoint id %q", last.ID, id)
	}
	if last.Type != session.EntryCheckpoint {
		t.Errorf("last entry type = %q, want %q", last.Type, session.EntryCheckpoint)
	}
	payload, err := last.Checkpoint()
	if err != nil {
		t.Fatalf("Checkpoint(): %v", err)
	}
	if payload.Label != "before-refactor" {
		t.Errorf("label = %q, want before-refactor", payload.Label)
	}

	// A checkpoint changes nothing about the model's view.
	if got, want := foldText(r.Fold()), "user:first\nassistant:one"; got != want {
		t.Errorf("Fold() after Checkpoint = %q, want %q", got, want)
	}

	second, err := r.Checkpoint("second")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	cps := r.Checkpoints()
	if len(cps) != 2 {
		t.Fatalf("Checkpoints() = %+v, want 2", cps)
	}
	if cps[0].ID != id || cps[0].Label != "before-refactor" {
		t.Errorf("checkpoint 0 = %+v, want %q/before-refactor", cps[0], id)
	}
	if cps[1].ID != second || cps[1].Label != "second" {
		t.Errorf("checkpoint 1 = %+v, want %q/second", cps[1], second)
	}
}

// TestRunnerCheckpointBlankLabel asserts a label that is empty or only
// whitespace is rejected and journals nothing.
func TestRunnerCheckpointBlankLabel(t *testing.T) {
	r := newCheckpointRunner(t, textTurns())

	before := len(readEntries(t, r))
	for _, label := range []string{"", " ", "\t\n"} {
		id, err := r.Checkpoint(label)
		if err == nil {
			t.Errorf("Checkpoint(%q) = %q, want an error", label, id)
		}
		if id != "" {
			t.Errorf("Checkpoint(%q) returned id %q, want empty", label, id)
		}
	}
	if after := len(readEntries(t, r)); after != before {
		t.Errorf("journal grew from %d to %d entries on rejected checkpoints", before, after)
	}
}

// TestRunnerForkDropsAbandonedTailFromContext asserts Fork(at) moves HEAD so a
// later Fold walks the branch through at — the abandoned tail leaves the
// model's context.
func TestRunnerForkDropsAbandonedTailFromContext(t *testing.T) {
	ctx := context.Background()
	r := newCheckpointRunner(t, textTurns("one", "two"))

	if err := r.Prompt(ctx, "first"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	at := readEntries(t, r)
	forkTarget := at[len(at)-1].ID // the settled assistant reply to "first"

	if err := r.Prompt(ctx, "second"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got, want := foldText(r.Fold()), "user:first\nassistant:one\nuser:second\nassistant:two"; got != want {
		t.Fatalf("Fold() before fork = %q, want %q", got, want)
	}

	if err := r.Fork(forkTarget); err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if got, want := foldText(r.Fold()), "user:first\nassistant:one"; got != want {
		t.Errorf("Fold() after fork = %q, want %q", got, want)
	}
}

// TestRunnerRewindMatchesForkAtCheckpoint asserts Rewind(label) resolves the
// label to its checkpoint entry and produces exactly the context forking at
// that entry produces — the two are the same operation, one addressed by name.
func TestRunnerRewindMatchesForkAtCheckpoint(t *testing.T) {
	ctx := context.Background()

	drive := func(t *testing.T, rewind func(r *runner.Runner, label, id string) error) string {
		t.Helper()
		r := newCheckpointRunner(t, textTurns("one", "two"))
		if err := r.Prompt(ctx, "first"); err != nil {
			t.Fatalf("Prompt: %v", err)
		}
		id, err := r.Checkpoint("before-refactor")
		if err != nil {
			t.Fatalf("Checkpoint: %v", err)
		}
		if err := r.Prompt(ctx, "second"); err != nil {
			t.Fatalf("Prompt: %v", err)
		}
		if err := rewind(r, "before-refactor", id); err != nil {
			t.Fatalf("rewind: %v", err)
		}
		return foldText(r.Fold())
	}

	byLabel := drive(t, func(r *runner.Runner, label, _ string) error { return r.Rewind(label) })
	byEntryID := drive(t, func(r *runner.Runner, _, id string) error { return r.Fork(id) })
	byRawRef := drive(t, func(r *runner.Runner, _, id string) error { return r.Rewind(id) })

	if want := "user:first\nassistant:one"; byLabel != want {
		t.Errorf("Rewind(label) fold = %q, want %q", byLabel, want)
	}
	if byEntryID != byLabel {
		t.Errorf("Fork(id) fold = %q, want the same as Rewind(label) %q", byEntryID, byLabel)
	}
	if byRawRef != byLabel {
		t.Errorf("Rewind(raw entry id) fold = %q, want the same as Rewind(label) %q", byRawRef, byLabel)
	}
}

// TestRewindNeverTruncatesTheJournal is the append-only invariant, and the one
// a consumer is most likely to assume away: a rewind APPENDS a fork point and
// deletes nothing. Every entry the journal held before the rewind is still on
// disk afterward, with the same ids in the same order, and the abandoned
// branch still counts toward Cost.
func TestRewindNeverTruncatesTheJournal(t *testing.T) {
	ctx := context.Background()
	r := newCheckpointRunner(t, textTurns("one", "two"))

	if err := r.Prompt(ctx, "first"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if _, err := r.Checkpoint("before-refactor"); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if err := r.Prompt(ctx, "second"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	before := entryIDs(readEntries(t, r))
	costBefore := r.Cost()
	if costBefore.Usage.OutputTokens != 2 {
		t.Fatalf("precondition: Cost().Usage.OutputTokens = %d, want 2 (two scripted turns)", costBefore.Usage.OutputTokens)
	}

	if err := r.Rewind("before-refactor"); err != nil {
		t.Fatalf("Rewind: %v", err)
	}

	after := entryIDs(readEntries(t, r))
	if len(after) != len(before)+1 {
		t.Fatalf("journal has %d entries after rewind, want %d (every original entry plus one fork_point)", len(after), len(before)+1)
	}
	for i, id := range before {
		if after[i] != id {
			t.Errorf("entry %d id = %q after rewind, want %q — a rewind must never remove or reorder entries", i, after[i], id)
		}
	}
	if last := readEntries(t, r)[len(after)-1]; last.Type != session.EntryForkPoint {
		t.Errorf("appended entry type = %q, want %q", last.Type, session.EntryForkPoint)
	}

	// The abandoned branch's spend is not reclaimed.
	costAfter := r.Cost()
	if !costAfter.Usage.Equal(costBefore.Usage) {
		t.Errorf("Cost().Usage = %+v after rewind, want %+v — the abandoned branch still counts", costAfter.Usage, costBefore.Usage)
	}
}

// TestRunnerForkUnknownEntry asserts forking at an id the journal does not
// hold fails with ErrEntryNotFound, leaves HEAD (and so the folded context)
// untouched, and publishes no session.forked.
func TestRunnerForkUnknownEntry(t *testing.T) {
	ctx := context.Background()
	r := newCheckpointRunner(t, textTurns("one"))

	if err := r.Prompt(ctx, "first"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	before := entryIDs(readEntries(t, r))
	wantFold := foldText(r.Fold())

	sub := r.EventsLive()
	defer sub.Close()

	if err := r.Fork("no-such-entry"); !errors.Is(err, session.ErrEntryNotFound) {
		t.Fatalf("Fork(unknown) err = %v, want ErrEntryNotFound", err)
	}
	// Rewind with an unresolvable ref falls through to the same entry-id path.
	if err := r.Rewind("no-such-label"); !errors.Is(err, session.ErrEntryNotFound) {
		t.Fatalf("Rewind(unknown) err = %v, want ErrEntryNotFound", err)
	}

	if got := entryIDs(readEntries(t, r)); len(got) != len(before) {
		t.Errorf("journal has %d entries after a failed fork, want %d unchanged", len(got), len(before))
	}
	if got := foldText(r.Fold()); got != wantFold {
		t.Errorf("Fold() = %q after a failed fork, want %q unchanged", got, wantFold)
	}

	// Nothing was published. Drive one more publish and assert it is the first
	// event to arrive — a session.forked would be ahead of it.
	r.Emit(event.NewSessionKilled(r.ID()))
	select {
	case ev := <-sub.C:
		if _, ok := ev.(event.SessionForked); ok {
			t.Errorf("a failed fork published %T", ev)
		}
	default:
		t.Error("expected the sentinel event on the stream")
	}
}

// TestRunnerForkPublishesSessionForked asserts a fork and a rewind each reach
// the session's own event stream as a must-deliver session.forked carrying the
// branch point — and that a rewind additionally carries the label it resolved.
func TestRunnerForkPublishesSessionForked(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name      string
		rewind    bool
		wantLabel string
	}{
		{name: "Fork carries at, no label"},
		{name: "Rewind carries at and label", rewind: true, wantLabel: "before-refactor"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newCheckpointRunner(t, textTurns("one"))
			if err := r.Prompt(ctx, "first"); err != nil {
				t.Fatalf("Prompt: %v", err)
			}
			id, err := r.Checkpoint("before-refactor")
			if err != nil {
				t.Fatalf("Checkpoint: %v", err)
			}

			sub := r.EventsLive()
			defer sub.Close()

			if tc.rewind {
				err = r.Rewind("before-refactor")
			} else {
				err = r.Fork(id)
			}
			if err != nil {
				t.Fatalf("fork: %v", err)
			}

			ev, ok := (<-sub.C).(event.SessionForked)
			if !ok {
				t.Fatalf("first event is not a session.forked")
			}
			if ev.SessionID() != r.ID() {
				t.Errorf("session_id = %q, want %q", ev.SessionID(), r.ID())
			}
			if ev.At != id {
				t.Errorf("At = %q, want the checkpoint entry id %q", ev.At, id)
			}
			if ev.Label != tc.wantLabel {
				t.Errorf("Label = %q, want %q", ev.Label, tc.wantLabel)
			}
			if ev.Tier() != event.TierMustDeliver {
				t.Errorf("Tier() = %v, want must-deliver", ev.Tier())
			}
		})
	}
}

// TestRunnerRewindDuplicateLabelPicksMostRecent pins the documented
// collision rule: with two checkpoints sharing a label, Rewind resolves to the
// LATER one in append order.
func TestRunnerRewindDuplicateLabelPicksMostRecent(t *testing.T) {
	ctx := context.Background()
	r := newCheckpointRunner(t, textTurns("one", "two"))

	if err := r.Prompt(ctx, "first"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if _, err := r.Checkpoint("mark"); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if err := r.Prompt(ctx, "second"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	later, err := r.Checkpoint("mark")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	sub := r.EventsLive()
	defer sub.Close()

	if err := r.Rewind("mark"); err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	ev, ok := (<-sub.C).(event.SessionForked)
	if !ok {
		t.Fatal("first event is not a session.forked")
	}
	if ev.At != later {
		t.Errorf("Rewind resolved to %q, want the most recent checkpoint %q", ev.At, later)
	}
	// The later checkpoint sits after the second turn, so both turns stay in
	// context — proof the earlier duplicate did not win.
	if got, want := foldText(r.Fold()), "user:first\nassistant:one\nuser:second\nassistant:two"; got != want {
		t.Errorf("Fold() = %q, want %q", got, want)
	}
}

// TestRunnerRoleSurvivesResume asserts Options.Role is persisted into the
// journal's root meta entry, readable off disk without resuming (the roster
// read path), and restored by Resume.
func TestRunnerRoleSurvivesResume(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cwd := t.TempDir()

	r, err := runner.New(ctx, runner.Options{
		Root: root, Cwd: cwd, Model: testModel, System: "test system",
		Provider: textTurns("one"),
		Tools:    oneToolRegistry{},
		IDGen:    seqIDGen(),
		Clock:    seqClock(),
		Role:     "monitor",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := r.Role(); got != "monitor" {
		t.Errorf("Role() = %q, want monitor", got)
	}
	id, path := r.ID(), r.JournalPath()
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Classifiable off disk, without resuming.
	entries, err := session.ReadEntries(path)
	if err != nil {
		t.Fatalf("ReadEntries: %v", err)
	}
	if len(entries) == 0 || entries[0].Type != session.EntryMeta {
		t.Fatalf("first entry = %+v, want a meta entry", entries)
	}
	mp, err := entries[0].Meta()
	if err != nil {
		t.Fatalf("Meta(): %v", err)
	}
	if mp.Role != "monitor" {
		t.Errorf("meta role on disk = %q, want monitor", mp.Role)
	}

	// Resume restores it from the journal even though Options names no role.
	r2, err := runner.Resume(ctx, id, runner.Options{
		Root: root, Cwd: cwd, Model: testModel,
		Provider: textTurns(),
		Tools:    oneToolRegistry{},
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer func() { _ = r2.Close() }()
	if got := r2.Role(); got != "monitor" {
		t.Errorf("resumed Role() = %q, want monitor (restored from the journal)", got)
	}
}

// TestRunnerRoleUnsetIsBackwardCompatible asserts the default: no role means
// no role field in the journal (so an existing journal is byte-identical to
// one written before the field existed), and a Resume of such a session
// reports an empty role rather than failing.
func TestRunnerRoleUnsetIsBackwardCompatible(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cwd := t.TempDir()

	r, err := runner.New(ctx, runner.Options{
		Root: root, Cwd: cwd, Model: testModel,
		Provider: textTurns(),
		Tools:    oneToolRegistry{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := r.Role(); got != "" {
		t.Errorf("Role() = %q, want empty by default", got)
	}
	id, path := r.ID(), r.JournalPath()
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	entries, err := session.ReadEntries(path)
	if err != nil {
		t.Fatalf("ReadEntries: %v", err)
	}
	if got := string(entries[0].Payload); strings.Contains(got, "role") {
		t.Errorf("meta payload = %s, want no role field", got)
	}

	r2, err := runner.Resume(ctx, id, runner.Options{
		Root: root, Cwd: cwd, Model: testModel,
		Provider: textTurns(),
		Tools:    oneToolRegistry{},
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer func() { _ = r2.Close() }()
	if got := r2.Role(); got != "" {
		t.Errorf("resumed Role() = %q, want empty", got)
	}
}
