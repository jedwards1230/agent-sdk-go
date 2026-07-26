# Proposal: task-handle / checkpoint seam

Status: **ratified — Option C adopted; items 1–3 shipped**, item 4 (the spawn
seam) tracked separately. Resolves the open question stubbed in
[`../PRD.md`](../PRD.md) ("Open question — in-process background-task handle")
and tracked as [agent-sdk-go#80](https://github.com/jedwards1230/agent-sdk-go/issues/80).
Consumers: gofer [#182](https://github.com/jedwards1230/gofer/issues/182)
(persistent background tasks / monitors) and
[#183](https://github.com/jedwards1230/gofer/issues/183) (checkpoint / rewind).

The analysis below is preserved as written (it is the argument that produced the
decision); [**What shipped**](#what-shipped) at the end records the outcome.

## The question

Two gofer directions want a persistence-and-addressing primitive the SDK does
not name today:

- **Persistent background task** (#182) — a long-running task keyed by an id,
  surviving attach/detach and daemon restart, rendered as its own transcript
  block and listed in the roster alongside sessions. "A monitor may well *be* a
  session under the hood."
- **Checkpoint / rewind** (#183) — mark a point, keep working, then fold the
  session *and its context* back to that point out of the append-only journal.
  "The fork tree already makes a 'what if' branch free; a checkpoint is that
  machinery pointed at *undo* rather than explore."

Does the SDK grow a first-class **task-handle / checkpoint seam**, or do these
stay **gofer-native** atop the resumable session + JSONL journal it already
ships? This proposal answers with a hybrid that leans hard toward gofer-native,
and names the small, additive SDK gaps that make it clean.

## What already exists (the substrate)

The durable substrate a checkpoint/task primitive would need is already present
and unusually clean. Every claim below is in the current tree.

- **Append-only, fsync-per-entry, torn-write-safe JSONL journal.**
  `session.Journal.Append(Entry) (Entry, error)` (`session/journal.go:135`) sets
  `ID`/`Time`/`Parent` (= current HEAD), writes one JSON line, `Sync()`s, and
  only then mutates in-memory state — it fails closed on any write/marshal/sync
  error (`session/journal.go:158-183`). Read-back
  (`session/journal_read.go:26`) drops a torn final line and truncates the file
  to the last good entry (`journal_read.go:64`); an interior bad line is
  `ErrCorruptJournal`. HEAD, folded context, and cost are **all derived** from
  the log — "there is no separate HEAD state to lose or desync"
  (`session/journal.go:37-45`). The journal *is* an implicit per-entry
  checkpoint stream.

- **Context reconstruction by fold.** `Journal.Fold() []provider.Message`
  (`session/journal.go:206`) walks parent links HEAD→root, stopping at (and
  including) a compaction boundary, and renders provider-ready messages. This is
  the exact operation a "rewind the context" feature needs.

- **Pinnable identity before creation.** `Store.CreateWithID(ctx, slug, id)`
  (`session/store.go:30-40`) and `runner.Options.SessionID`
  (`runner/runner.go:61`) let a caller assign a session's id *before the session
  exists* — the seam "a process-isolated worker keyed by that id for its
  socket/lock filenames" uses. Ids are UUIDv7 (`session/session.go:119`),
  time-ordered, fleet-relied-upon.

- **Cross-restart resume + listing.** `FileStore.Open` (`session/store.go:313`)
  + `runner.Resume(ctx, id, opts)` (`runner/runner.go:189`) rebuild a live
  runner from a persisted id and fold prior context on the next `Prompt`.
  `Store.List` (`store.go:384`) and `ReadEntries(path)` (`store.go:354`)
  enumerate on-disk sessions and their metadata **without resuming** — the
  roster read path. `New` seeds a root `session_meta` entry carrying `Cwd`
  (`runner/runner.go:176`) so a session is classifiable on disk without a fold.

- **Rewind-as-branch, already.** `Journal.Fork(at) (Entry, error)`
  (`session/journal.go:146`) appends a `fork_point` parented on an existing
  entry and moves HEAD there; `Fold` then walks the branch through `at`, so a
  `Prompt` after `Fork` continues with context **truncated to `at`**. That is
  rewind — expressed additively, without deleting a line. Caveat: dropped
  entries stay in the log and **still count toward `Cost`**
  (`session/cost.go:73-77`), and the reserved `session.forked` event
  (`event/event.go:126`) was **defined but never emitted** — no code published
  it. (`Runner.Fork`/`Rewind` are now that producer; see
  [What shipped](#what-shipped).)

- **Extensibility already reserved.** `MetaPayload` is explicitly extensible
  (`session/entry.go:92-98`). The spawn seam — must-deliver
  `session.spawned{child_id, agent}`, child metadata carrying `parent_id`, depth
  capped at 5 — is already committed as design-ahead M5 work
  (`docs/DESIGN.md:574-591`).

### What is genuinely absent

No task id distinct from the session id; no parent/child session linkage and no
`session.spawned` type (docs-only); no pause/suspend; no destructive truncate
(the journal is strictly append-only); no detached/background execution
(`Prompt` is synchronous and ctx-bound); and — by architecture invariant #1 —
supervision, rosters, and detached running are explicitly **application**
concerns (`CLAUDE.md`, `docs/DESIGN.md:574-582`).

## The two boundary options

### Option A — SDK grows a first-class task-handle / checkpoint seam

The SDK introduces a `Task` concept distinct from a session: a `TaskID`, a
`TaskHandle` you can detach from and re-attach to, a task registry that survives
restart, and named `Checkpoint`/`Rewind` operations as first-class API.

Sketch of the surface this implies:

```go
// Illustrative only — no behavior, not committed.
type TaskID string

type TaskHandle interface {
    ID() TaskID
    Detach() error                       // keep running, drop this attachment
    Attach(ctx context.Context) (*Runner, error)
    Status() TaskStatus                  // running | idle | done | failed
}

type TaskStore interface {              // parallel to session.Store
    Spawn(ctx context.Context, opts TaskOptions) (TaskHandle, error)
    Open(ctx context.Context, id TaskID) (TaskHandle, error)
    List(ctx context.Context) ([]TaskInfo, error)
}

type Checkpoint struct { ID, Label string; Entry string /* journal entry id */ }
func (r *Runner) Checkpoint(label string) (Checkpoint, error)
func (r *Runner) Rewind(to Checkpoint) error
```

**How the consumers use it.** #182 spawns a `Task`, persists the `TaskID`,
re-attaches after restart. #183 calls `Runner.Checkpoint`/`Rewind` directly.

**Cost.** It fails **both** gates of the repo's own two-gate test. *Membership*:
a `Task` that survives restart and re-attaches is precisely the
supervisor/roster job invariant #1 assigns to the application — a second app
would not need the SDK's task registry unchanged; it would need its *own*
lifecycle policy. *Seam-suffices*: a `TaskHandle` is a session id plus a
supervision policy; the id is already pinnable and the journal already survives
restart, so a handle adds a wrapper, not a capability. `Rewind` as a distinct
verb duplicates `Fork`. A task registry re-opens the "no central registry"
non-goal in spirit even if kept in-process. Net: a new subsystem that mostly
re-expresses primitives the SDK already has, and annexes application
responsibility the invariants deliberately exclude.

### Option B — gofer-native atop resumable sessions + the JSONL journal

The SDK adds nothing task-shaped. gofer builds both features on what exists:

- **#182 monitor.** A monitor **is a session** with a pinned id
  (`Options.SessionID` — so the *task id is the session id*). gofer's daemon owns
  the detached goroutine and supervision; on restart it re-enumerates via
  `Store.List`/`ReadEntries` and `runner.Resume`s the live ones. The
  "persistent, greppable, on-disk" artifact #182 wants is the journal file
  itself — matching gofer's visible-artifacts-over-hidden-state tenet exactly.
- **#183 checkpoint/rewind.** A checkpoint is a journal entry id (optionally a
  named anchor gofer records in its own sidecar); rewind is `Journal.Fork(at)`
  followed by treating the new branch as canonical. Context follows for free via
  `Fold`. jj-style working-tree versioning is entirely gofer's — the SDK never
  models the working tree.

**Cost.** Two friction points push past the clean contract. First, `Journal` is
not reachable through the `Runner` public API — gofer cannot call `Fork`
without the SDK surfacing it, so "gofer-native" today means gofer reaching for a
primitive the runner hides. Second, classifying a background task in the roster
(`Store.List`) without folding needs a role/kind on the session metadata that
does not exist yet. Pure Option B therefore forces gofer to either fork the SDK
or reconstruct state the SDK already holds — both violate "one contract, no
client reaches past it."

### Option C (recommended) — gofer-native, with a small additive SDK seam

Keep persistence, addressing, supervision, rendering, and working-tree
versioning in gofer (Option B), and close exactly the gaps that otherwise force
gofer to reach past the contract. Each item is additive and backward-compatible,
and each passes both gates on its own.

1. ✅ **Shipped.** **Surface fork/rewind at the runner level, and emit the event.** Add
   `Runner.Fork(at string) error` (or `Rewind`) delegating to `Journal.Fork`,
   and make it publish the already-reserved `session.forked`
   (`event/event.go:126`) so every client sees a rewind the same way it sees a
   resume. This is the single change that makes #183 buildable without gofer
   touching `session.Journal` directly. *Membership*: any app offering undo needs
   it. *Seam*: it exposes an existing primitive, not a new one.

2. ✅ **Shipped.** **A named-anchor entry (optional checkpoint label).** Either a new
   `EntryType` ("checkpoint") or a label field so a fork point / anchor can carry
   a human name. Subsumes #183's "lightweight named timeline anchors, which fall
   out for free." Small, and it keeps the name *in the append-only log* rather
   than in a gofer sidecar that can desync from the journal.

3. ✅ **Shipped.** **A session role/kind in metadata.** Extend the already-extensible
   `MetaPayload` (`session/entry.go:92-98`) with an optional role (e.g.
   `"monitor"`) so `Store.List`/`ReadEntries` can classify a background task for
   the roster **without folding**. This is what lets #182's monitor "surface in
   the roster alongside sessions" through the read path the SDK already owns.

4. **Land the design-ahead spawn seam** (`docs/DESIGN.md:574-591`):
   `session.spawned{child_id, agent}` + `parent_id` in child metadata + depth-5.
   A monitor spawned by a session needs the parent/child link for the roster
   tree, and #182 explicitly lives alongside the session tree. This is already
   committed as M5 — this proposal only notes that #182 is a first hard consumer,
   not a new ask.

Everything else stays in gofer: the detached goroutine and its lifecycle, the
roster/fleet view, the `Monitor(...) → task <id> · persistent` transcript block,
and jj-style working-tree diffs.

## Recommendation

**Adopt Option C.** Do not grow a `Task`/`TaskHandle`/`TaskStore` subsystem or a
distinct task-id namespace. The SDK's own two-gate test and architecture
invariant #1 place persistence, supervision, and rosters in the application; the
journal already gives durable, restart-surviving, greppable state; and `Fork` +
`Fold` already *are* checkpoint/rewind. The task id should simply **be** the
pinned session id.

The SDK's job is to stop hiding primitives gofer needs and to record the two
linkage/classification fields the roster needs — four small additive changes,
each independently justified, none of which annexes application responsibility.
This keeps the boundary where every other capability in this SDK sits: the SDK
owns the durable, event-sourced substrate and the seams; gofer owns the policy.

### Risks

- **Option A risk (rejected path):** a task registry becomes a second source of
  truth beside the journal, and the "no central registry" non-goal erodes one
  in-process convenience at a time. Also the larger surface to keep
  backward-compatible forever.
- **Option C risk — rewind semantics (the open question below).** `Fork` is
  additive: the abandoned tail stays in the log and keeps counting toward `Cost`
  (`session/cost.go:73-77`), and the roster can show abandoned branches. If a
  user expects undo to *reclaim* cost and hide the tail, append-only Fork will
  surprise them — but a destructive truncate would break the append-only
  auditability tenet the whole design rests on.
- **Option C risk — metadata churn:** adding a role/kind and parent_id to
  metadata touches the `session_meta`/spawn contract; it must land as optional
  fields so existing journals read back unchanged (they will — `MetaPayload` is
  already extensible and JSON-omitempty).

## Open question — ratified

**Is rewind additive or destructive? → ADDITIVE.** Rewind appends a fork point;
it never truncates the journal, removes entries, or shortens the file. A
destructive `Truncate` was considered and **rejected**: it would trade away the
journal's append-only auditability (every model call the session ever made stays
reconstructable) and its torn-write recovery, which repairs a crash by dropping a
bad *final* line and cannot survive in-place rewrites of earlier entries. The
consequences are intended behavior, documented on `Runner.Rewind`: the abandoned
tail stays in the log, stays readable, and **keeps counting toward
`Runner.Cost()`** — a rewind reclaims no spend, because the provider was already
paid for those calls. Only the folded context changes.

**Secondary point — task id == session id: confirmed.** No distinct task-id
namespace; a monitor is a session with a pinned `Options.SessionID`.

## What shipped

Items 1–3, additive and backward-compatible. Item 4 (spawn seam) is unchanged
design-ahead work and is not part of this change.

**1. Runner-level fork/rewind, and a `session.forked` producer** (`runner/runner.go`).

```go
func (r *Runner) Fork(at string) error   // branch at an entry id
func (r *Runner) Rewind(ref string) error // checkpoint label, else raw entry id
```

Both delegate to `Journal.Fork` and then publish the (previously
defined-but-never-emitted) `session.forked`. The event grew two omitempty
fields — `at` (the entry the branch is parented on) and `label` (set when the
fork resolved from a checkpoint) — so a client learns *where* a session
branched, not just that it did; it stays `TierMustDeliver`. Changing
`NewSessionForked`'s signature broke no producer, because there was none.

Two behaviors worth naming: **label collisions resolve to the most recent
matching checkpoint in append order** (so a label works as a moving bookmark),
and both methods drain the runner's existing `awaitJournaled` barrier first, so a
fork right after a turn is parented on that turn's last entry rather than a stale
HEAD. Forking *while* a `Prompt` is in flight remains a caller-side precondition
— a run still publishing would land its remaining entries on the new branch —
and is documented as such.

**2. A checkpoint entry type** (`session/entry.go`, `session/checkpoint.go`).
`EntryCheckpoint` / `CheckpointPayload{Label}` / `NewCheckpointEntry` /
`Entry.Checkpoint()`, following the existing entry-type pattern. It is a marker:
`renderContext` skips it explicitly (not by falling through its default), so a
checkpoint label never enters the model's context. The name lives in the
append-only log rather than a consumer sidecar, exactly as argued above.

Runner surface: `Checkpoint(label) (string, error)` (blank label rejected) and
`Checkpoints() []session.Checkpoint`. The listing helper is
`session.Checkpoints(entries []Entry)`, so the roster read path is
`session.Checkpoints(session.ReadEntries(path))` — **no resume, no fold**.

**3. A session role in metadata** (`session/entry.go`, `runner/runner.go`).
`MetaPayload.Role` (omitempty, so existing journals read back unchanged) set via
the `WithMetaRole` `MetaOpt`, threaded through `runner.Options.Role` →
`New`'s root meta entry → `Runner.Role()`, and restored by `Resume` from the
journal's root meta entry (the durable record wins; a journal without one falls
back to `Options.Role`). The SDK defines no vocabulary of roles and attaches no
behavior to any value — same discipline as `Options.Agent` and the session
title.
