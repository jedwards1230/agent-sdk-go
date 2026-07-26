// Package runner is the SDK's composable session runner: it builds a
// provider and tool registry, drives the SDK's agent loop, and streams the
// loop's typed events into a durable session journal as each model-call turn
// settles. The SDK drives the loop and emits events; it does not persist
// anything on its own — a Runner is the piece that owns that persistence,
// folding a journal back into provider messages on resume, consuming the SDK
// only through its exported provider/auth/tool/loop/event/session APIs.
package runner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/loop"
	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/session"
	"github.com/jedwards1230/agent-sdk-go/tool"
)

// defaultSubBuffer is the channel buffer for the Runner's own event
// subscriptions — ample for one interactive session.
const defaultSubBuffer = 256

// defaultReplay is how many must-deliver events the broker retains so a
// subscriber attaching after construction still receives session.created /
// session.resumed.
const defaultReplay = 256

// Options configures a Runner. Model, Cwd, and (for a fresh session) Root are
// the only fields a caller normally sets; Provider, IDGen, and Clock are test
// seams.
type Options struct {
	// Root is the session store's root directory (holds sessions/ and, for a
	// real provider, auth.json). Required unless Store is set: the SDK invents
	// no directory name, so an empty Root with no injected Store surfaces the
	// underlying store's "no store root" error.
	Root string
	// Cwd is the working directory tools operate in, and the project the
	// session belongs to (via session.Slugify).
	Cwd string
	// SessionID, when non-empty, is the id [New] gives the freshly created
	// session verbatim, instead of a freshly generated UUIDv7 — the seam a caller
	// that must know a session's id BEFORE the session exists (e.g. a
	// process-isolated worker keyed by that id for its socket/lock filenames)
	// uses to pin it, replacing the fragile alternative of a stateful IDGen whose
	// first call happens to be the session id.
	//
	//   - The id becomes the journal filename, [Runner.ID] (and the
	//     [event.SessionCreated] a frontend derives from it), and the id every
	//     later [Resume] addresses.
	//   - Empty (the default) generates a fresh UUIDv7 — unchanged behavior.
	//   - Used only by New; [Resume] addresses an existing id and ignores it.
	//   - Does not affect entry-id generation (still IDGen / the store default).
	//   - Must be a safe single path component or New returns [session.ErrInvalidID];
	//     a disk store additionally rejects an id whose session already exists.
	SessionID string
	// Model is the model identifier passed to the provider and loop.
	Model string
	// System is the system prompt.
	System string
	// Params carries sampling and reasoning controls.
	Params provider.Params
	// MaxIters caps model-call rounds per Prompt; <= 0 uses the loop default.
	MaxIters int
	// Agent is the originating-agent id forwarded to loop.Config.Agent, stamped
	// onto every tool-call event the runner emits so a consumer can attribute
	// tool calls to the agent that ran them. Empty ⇒ un-attributed (the default).
	Agent string
	// Role classifies what KIND of thing this session is (e.g. "monitor"), for
	// an embedder that runs more than one. [New] persists it into the journal's
	// root session_meta entry (see [session.MetaPayload.Role]), so
	// [session.ReadEntries] can classify the session for a roster without
	// resuming or folding it, and [Resume] restores it.
	//
	//   - The SDK defines no vocabulary of roles and attaches no behavior to any
	//     value — it carries and surfaces the string, exactly as it does for
	//     Agent and the session title. The embedder owns its meaning.
	//   - Empty (the default) ⇒ unclassified; the field is omitted from the
	//     journal, so an existing journal reads back unchanged.
	//   - Used only by New. Resume reads the role from the journal it opens (the
	//     durable record wins); this field is only the fallback for a journal
	//     written before the field existed.
	Role string

	// Guard decides how each tool call is handled before execution
	// (run-contained / ask / deny). Nil ⇒ every tool runs uncontained, the
	// pre-M3 default. Passed straight through to the loop; the SDK ships the
	// seam, applications inject the policy (see loop.Guard / loop.RuleGuard).
	Guard loop.Guard
	// Approver awaits a human's reply when Guard returns an "ask" decision.
	// Required whenever Guard can return ask (a nil Approver fails closed —
	// the loop denies). Passed straight through to the loop.
	Approver loop.Approver

	// IDGen overrides the session/entry id generator. Test seam.
	IDGen func() string
	// Clock overrides the wall clock used to timestamp journal entries. Test
	// seam.
	Clock func() time.Time

	// Provider, when set, is used instead of building a real provider from
	// Model via auth + provider.Lookup. Test seam.
	Provider provider.Provider

	// Tools, when set, fully REPLACES the builtin tool set rooted at Cwd — the
	// runner registers exactly these tools and nothing else. Test seam, and
	// also an embedder's escape hatch for total control over the tool surface.
	// Mutually exclusive with ExtraTools (see resolveTools).
	Tools loop.ToolRegistry
	// ExtraTools, when set, is registered ADDITIVELY alongside the builtin
	// tool set rooted at Cwd — the front door for an embedder's own
	// domain-specific tools without giving up bash/read/edit/write/grep/glob/ls.
	// A name colliding with a builtin, or with another ExtraTools entry, is a
	// registration error surfaced by New/Resume. Mutually exclusive with Tools.
	ExtraTools []tool.Tool

	// Store, when set, is used instead of building a disk [session.FileStore]
	// from Root. This is the seam a multi-session owner (e.g. a supervisor)
	// uses to share one store across every Runner it drives: the Runner does
	// NOT close an injected Store in Close — the caller owns its lifecycle.
	// Tests use it too, to share a store across a Runner and out-of-band
	// assertions. An embedder wanting an ephemeral session that writes nothing
	// to disk passes [session.NewMemStore]().
	Store session.Store
}

// Runner drives one session: it owns the provider, tool registry, event
// broker, and session journal, and folds the journal back into provider
// messages so a Prompt after Resume continues with full prior context.
type Runner struct {
	model    string
	system   string
	params   provider.Params
	effort   string
	maxIters int
	agent    string
	// role is the session's opaque, embedder-owned classification, seeded from
	// Options.Role by New and restored from the journal's root meta entry by
	// Resume. It is immutable after construction (there is no setter), so
	// [Runner.Role] reads it without synchronization.
	role string

	provider provider.Provider
	tools    loop.ToolRegistry
	guard    loop.Guard
	approver loop.Approver

	broker  *event.Broker
	journal *session.Journal
	store   session.Store
	// ownsStore is true when this Runner built its own store (Options.Store
	// was nil) and so must close it in Close; false when the store was
	// injected and its lifecycle belongs to the caller.
	ownsStore bool

	journalDone chan struct{}
	// barrier hands the consume goroutine an ack channel: after a Prompt run
	// returns, Prompt sends one and blocks until consume has drained (journaled)
	// the run's events, so the next Prompt's user-message append cannot reorder
	// ahead of this run's assistant/tool entries. See awaitJournaled.
	barrier chan chan struct{}

	mu   sync.Mutex
	jerr error
}

// New builds a Runner around a freshly created journal for the project at
// opts.Cwd. The provider (and its credential) is resolved BEFORE the journal
// is created, so a missing-credential misconfiguration fails fast with no
// orphan journal on disk.
func New(ctx context.Context, opts Options) (*Runner, error) {
	prov, err := resolveProvider(ctx, opts)
	if err != nil {
		return nil, err
	}
	tools, err := resolveTools(opts)
	if err != nil {
		return nil, err
	}
	store, err := newStore(opts)
	if err != nil {
		return nil, err
	}
	// CreateWithID with an empty SessionID is exactly Create (a fresh UUIDv7);
	// a non-empty SessionID pins the session id verbatim (see Options.SessionID).
	journal, err := store.CreateWithID(ctx, session.Slugify(opts.Cwd), opts.SessionID)
	if err != nil {
		if opts.Store == nil {
			_ = store.Close()
		}
		return nil, fmt.Errorf("runner: create session: %w", err)
	}
	// Persist the cwd (and the optional role) as the journal's first (root)
	// entry so it survives a daemon restart — session/list on a disk-only
	// session needs it to cwd-filter and classify without resuming. Resume never
	// hits this path: it opens an existing journal that already has its meta
	// entry.
	var metaOpts []session.MetaOpt
	if opts.Role != "" {
		metaOpts = append(metaOpts, session.WithMetaRole(opts.Role))
	}
	if _, err := journal.Append(session.NewMetaEntry(opts.Cwd, metaOpts...)); err != nil {
		_ = journal.Close()
		if opts.Store == nil {
			_ = store.Close()
		}
		return nil, fmt.Errorf("runner: append session metadata: %w", err)
	}
	return build(opts, store, journal, prov, tools, false), nil
}

// Resume builds a Runner around the existing journal for id, publishing
// session.resumed once the runner is live. The provider is resolved before the
// journal is opened so a credential misconfiguration fails before session.resumed.
func Resume(ctx context.Context, id string, opts Options) (*Runner, error) {
	prov, err := resolveProvider(ctx, opts)
	if err != nil {
		return nil, err
	}
	tools, err := resolveTools(opts)
	if err != nil {
		return nil, err
	}
	store, err := newStore(opts)
	if err != nil {
		return nil, err
	}
	journal, err := store.Open(ctx, id)
	if err != nil {
		if opts.Store == nil {
			_ = store.Close()
		}
		return nil, fmt.Errorf("runner: open session %s: %w", id, err)
	}
	// The journal is the durable record of what this session IS, so its root
	// meta entry — not the caller's Options — decides the resumed session's
	// role. A journal written before the field existed (or by a New that set no
	// role) carries none, and the resumed runner falls back to opts.Role, which
	// is empty unless the caller supplied one.
	if role := journalRole(journal); role != "" {
		opts.Role = role
	}
	return build(opts, store, journal, prov, tools, true), nil
}

// journalRole returns the role recorded in the journal's root session_meta
// entry, or "" when the journal has no meta entry (an older journal, or one
// not created by [New]) or its payload does not decode. It scans for the first
// meta entry rather than indexing entry 0, so it holds even if a journal ever
// grows a preceding entry.
func journalRole(j *session.Journal) string {
	for _, e := range j.Entries() {
		if e.Type != session.EntryMeta {
			continue
		}
		p, err := e.Meta()
		if err != nil {
			return ""
		}
		return p.Role
	}
	return ""
}

// resolveProvider returns the test-injected provider when set, else builds the
// real one — which pre-flights its credential. It runs before any journal is
// created so a failure leaves no on-disk residue.
func resolveProvider(ctx context.Context, opts Options) (provider.Provider, error) {
	if opts.Provider != nil {
		return opts.Provider, nil
	}
	return newProvider(ctx, opts.Model, opts.Root)
}

// resolveTools builds the loop tool registry from opts: Tools (when set) is a
// full replacement; otherwise the builtins rooted at Cwd plus each ExtraTools,
// where a name colliding with a builtin (or another custom tool) is a
// registration error rather than a silent override. Tools and ExtraTools are
// mutually exclusive — a full replacement already includes whatever the caller
// wants, so pairing it with additive tools is a configuration error.
func resolveTools(opts Options) (loop.ToolRegistry, error) {
	if opts.Tools != nil {
		if len(opts.ExtraTools) > 0 {
			return nil, fmt.Errorf("runner: Options.Tools (full replacement) and Options.ExtraTools (additive) are mutually exclusive")
		}
		return opts.Tools, nil
	}
	reg := tool.NewRegistry(tool.Builtins(opts.Cwd)...)
	for _, t := range opts.ExtraTools {
		if err := reg.Register(t); err != nil {
			return nil, fmt.Errorf("runner: register custom tool: %w", err)
		}
	}
	return loop.FromRegistry(reg), nil
}

// newStore returns the injected store when opts.Store is set (the caller
// owns its lifecycle — see Options.Store), else builds a disk
// [session.FileStore] from opts, wiring the deterministic id generator /
// clock test seams when set.
func newStore(opts Options) (session.Store, error) {
	if opts.Store != nil {
		return opts.Store, nil
	}
	var storeOpts []session.StoreOption
	if opts.Root != "" {
		storeOpts = append(storeOpts, session.WithRoot(opts.Root))
	}
	if opts.IDGen != nil {
		storeOpts = append(storeOpts, session.WithStoreIDGen(opts.IDGen))
	}
	if opts.Clock != nil {
		storeOpts = append(storeOpts, session.WithStoreClock(opts.Clock))
	}
	store, err := session.NewFileStore(storeOpts...)
	if err != nil {
		return nil, fmt.Errorf("runner: open session store: %w", err)
	}
	return store, nil
}

// build assembles a Runner around an already-opened journal, a resolved
// provider, and a resolved tool registry: it starts the broker and its
// journaling consumer, and (when resumed) publishes session.resumed.
func build(opts Options, store session.Store, journal *session.Journal, prov provider.Provider, tools loop.ToolRegistry, resumed bool) *Runner {
	broker := event.NewBroker(event.WithReplay(defaultReplay))
	journalSub := broker.Subscribe(event.FilterMustDeliver, defaultSubBuffer)

	r := &Runner{
		model:       opts.Model,
		system:      opts.System,
		params:      opts.Params,
		effort:      opts.Params.Thinking.Effort,
		maxIters:    opts.MaxIters,
		agent:       opts.Agent,
		role:        opts.Role,
		provider:    prov,
		tools:       tools,
		guard:       opts.Guard,
		approver:    opts.Approver,
		broker:      broker,
		journal:     journal,
		store:       store,
		ownsStore:   opts.Store == nil,
		journalDone: make(chan struct{}),
		barrier:     make(chan chan struct{}),
	}
	go r.consume(journalSub)

	if resumed {
		broker.Publish(event.NewSessionResumed(journal.ID()))
	}
	return r
}

// ID returns the session's journal id.
func (r *Runner) ID() string { return r.journal.ID() }

// JournalPath returns the session journal's JSONL file path.
func (r *Runner) JournalPath() string { return r.journal.Path() }

// Role returns the session's opaque, embedder-owned classification (see
// [Options.Role]), or "" when unclassified. It is the same value the journal's
// root session_meta entry carries, so a live runner and an off-line
// [session.ReadEntries] read of the same session agree. The SDK attaches no
// behavior to any value.
func (r *Runner) Role() string { return r.role }

// Fold returns the session's current folded context as provider messages —
// the same context Prompt feeds the provider, exposed for read-only
// transcript views.
func (r *Runner) Fold() []provider.Message { return r.journal.Fold() }

// Events returns a subscription to every event the session emits, of both
// delivery tiers. The broker replays its retained must-deliver backlog into
// the subscription first (see [event.Broker.Subscribe]), so a mid-session
// attach recovers the lifecycle and terminal events it missed.
func (r *Runner) Events() *event.Subscription {
	return r.broker.Subscribe(event.FilterAll, defaultSubBuffer)
}

// EventsLive returns a subscription to events emitted AFTER the call, without
// the retained-backlog replay [Events] performs (see
// [event.Broker.SubscribeLive]). It is for a caller driving a new turn that
// wants only that turn's events — subscribe, [Prompt], then read to the
// turn's terminal event — where replaying a prior turn's retained terminal
// event would be mistaken for this turn finishing. Use [Events] for a
// mid-session attach that must recover missed events.
func (r *Runner) EventsLive() *event.Subscription {
	return r.broker.SubscribeLive(event.FilterAll, defaultSubBuffer)
}

// Prompt appends text as a user message, projects the journal's folded
// context into provider messages, and drives the agent loop. Loop events
// stream into the journal concurrently as each turn settles (see consume);
// a cancelled ctx interrupts the loop between or during model calls, leaving
// whatever prefix had already settled durably on disk.
//
// Before driving the loop, Prompt publishes the user's own turn onto the
// event stream as a MessageStarted/MessageFinished{MessageUser} pair (no
// delta — a prompt isn't streamed token-by-token), so every live observer
// (an attached TUI, a daemon forwarding to attached clients) can render the
// user's side of the transcript. Both kinds are must-deliver, so the broker
// retains and replays them like any other terminal event. This publish
// happens before loop.Run, so the user message always precedes that run's
// TurnStarted and agent reply in the stream.
func (r *Runner) Prompt(ctx context.Context, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := r.journal.Append(session.NewMessageEntry(provider.UserText(text))); err != nil {
		return fmt.Errorf("runner: append user message: %w", err)
	}
	sid := r.journal.ID()
	r.broker.Publish(event.NewMessageStarted(sid, event.MessageUser))
	r.broker.Publish(event.NewMessageFinishedMeta(sid, event.MessageUser, text, nil))

	spillDir, spillRelDir := r.spillCallsDir()
	// Overlay the live reasoning effort onto this turn's params. r.params is the
	// construction-time base (sampling controls, thinking budget); currentEffort
	// is the authoritative, hot-swappable effort level, read under the same lock
	// currentModel uses so a concurrent SetEffort can't race this turn's build.
	params := r.params
	params.Thinking.Effort = r.currentEffort()
	cfg := loop.Config{
		Provider:    r.provider,
		Model:       r.currentModel(),
		System:      r.system,
		Params:      params,
		Tools:       r.tools,
		Broker:      r.broker,
		SessionID:   sid,
		Agent:       r.agent,
		MaxIters:    r.maxIters,
		SpillDir:    spillDir,
		SpillRelDir: spillRelDir,
		Guard:       r.guard,
		Approver:    r.approver,
	}
	// The journal folds back to provider messages directly (verbatim content
	// blocks), so the loop's input is the folded context as-is.
	_, err := loop.Run(ctx, cfg, r.journal.Fold())
	// Block until consume has journaled this run's turns, so a subsequent
	// Prompt's user-message append cannot land ahead of this run's assistant and
	// tool entries (which consume writes asynchronously off the broker).
	r.awaitJournaled()
	// The barrier above guarantees this run's journaling is done, so any write
	// failure it hit is visible now — this is the turn boundary, and the only
	// point at which the caller can still act on it (retry the turn, abandon
	// the session, alert an operator). Surfacing it here matches the
	// user-message append at the top of Prompt, which has always returned its
	// error; the asymmetry where only the consumer goroutine's appends were
	// swallowed was the bug. Taking the error clears it, so a caller that
	// retries after a transient fault is not handed a stale failure forever;
	// a persistent fault simply sets it again on the next turn.
	jerr := r.takeJournalWriteErr()
	switch {
	case jerr == nil:
		return err
	case err == nil:
		return jerr
	default:
		return errors.Join(err, jerr)
	}
}

// spillCallsDir returns the absolute directory this session's per-call tool
// output spills to (<session-dir>/calls), and that directory relative to the
// store root (forward-slashed) for the portable spill_path on
// tool.call.finished. The directory is created lazily by the first spill.
func (r *Runner) spillCallsDir() (abs, rel string) {
	abs = filepath.Join(r.journal.Dir(), "calls")
	if r0, err := filepath.Rel(r.store.Root(), abs); err == nil {
		rel = filepath.ToSlash(r0)
	}
	return abs, rel
}

// awaitJournaled blocks until the consume goroutine has journaled every event
// published so far — the run that just completed. It returns immediately if the
// Runner has been closed (consume has exited).
//
// The ordering guarantee (this run's entries are durable before the next
// Prompt's user message is appended) holds as long as the journaling
// subscription is not force-dropped by the broker — i.e. consume never blocks a
// must-deliver event past the broker's bound. consume only stops receiving to
// service this barrier, which happens after the run's publishing is done, and
// the subscription is buffered to defaultSubBuffer (256) must-deliver events, so
// a force-drop is not reachable on the Prompt path in practice.
func (r *Runner) awaitJournaled() {
	ack := make(chan struct{})
	select {
	case r.barrier <- ack:
		select {
		case <-ack:
		case <-r.journalDone:
		}
	case <-r.journalDone:
	}
}

// Close shuts down the runner's broker (closing every subscription,
// including the journaling consumer's), waits for the journaling consumer to
// drain so no settled turn is lost, then closes the journal and — only when
// this Runner built its own store (Options.Store was nil) — the store. An
// injected store is never closed here; its lifecycle belongs to the caller
// (e.g. a supervisor sharing one store across many Runners). Close returns
// the first error encountered, if any, joined with any journal write error the
// consumer observed that no [Runner.Prompt] turn boundary already reported —
// the backstop for a failure in the final drain.
func (r *Runner) Close() error {
	r.broker.Close()
	<-r.journalDone

	var errs []error
	if err := r.takeJournalWriteErr(); err != nil {
		errs = append(errs, err)
	}
	if err := r.journal.Close(); err != nil {
		errs = append(errs, err)
	}
	if r.ownsStore {
		if err := r.store.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Emit publishes a lifecycle event (e.g. session.killed / session.archived)
// onto this session's stream so subscribers observe it. A caller that kills
// or archives a session out-of-band calls this before Close, which closes
// the broker and so ends delivery to every subscriber.
func (r *Runner) Emit(e event.Event) { r.broker.Publish(e) }

// Cost returns the session's token/cost tally across every journaled turn,
// priced against the embedded provider model registry. An unknown (or faux)
// model still has its tokens summed, with a zero priced cost.
func (r *Runner) Cost() session.CostReport { return r.journal.Cost(session.RegistryPricing{}) }

// Checkpoint appends a named marker at the journal's current HEAD and returns
// the marker entry's id — the id [Runner.Fork] takes, and the id
// [Runner.Rewind] resolves label to. The marker contributes nothing to the
// folded context, so naming a point never changes what the model sees.
//
// label must be non-blank: an unnamed checkpoint is indistinguishable from the
// fork points the journal already contains, and nothing could address it.
// The label is stored verbatim and is opaque to the SDK — duplicates are
// allowed (see Rewind for how they resolve).
//
// Checkpoint drains the journaling handshake first (see awaitJournaled), so a
// checkpoint taken right after a [Runner.Prompt] returns is parented on that
// turn's last entry rather than on a stale HEAD.
func (r *Runner) Checkpoint(label string) (string, error) {
	if strings.TrimSpace(label) == "" {
		return "", fmt.Errorf("runner: checkpoint label must not be blank")
	}
	r.awaitJournaled()
	e, err := r.journal.Append(session.NewCheckpointEntry(label))
	if err != nil {
		return "", fmt.Errorf("runner: append checkpoint %q: %w", label, err)
	}
	return e.ID, nil
}

// Checkpoints lists the session's checkpoints in append order, read off the
// journal's full log (every branch, not just the folded path).
//
// The same listing is available WITHOUT a live runner —
// session.Checkpoints(session.ReadEntries(path)) — which is the path a roster
// uses to show a persisted session's checkpoints without resuming it.
func (r *Runner) Checkpoints() []session.Checkpoint {
	return session.Checkpoints(r.journal.Entries())
}

// Fork branches the session at entry at and makes the branch HEAD, then
// publishes a must-deliver [event.SessionForked] carrying at. Subsequent turns
// chain onto the branch, so [Runner.Fold] walks the tree through at and the
// entries that followed at drop out of the model's context.
//
// Fork is ADDITIVE — nothing is deleted; see [Runner.Rewind] for the full
// semantics and the reasons. at must be an existing entry id (any entry, on
// any branch): an unknown id returns an error wrapping
// [session.ErrEntryNotFound] with HEAD untouched and no event published.
//
// Precondition: do not call Fork while a [Runner.Prompt] is in flight on
// another goroutine. Fork drains the journaling handshake first (see
// awaitJournaled), so it branches from a HEAD that already includes everything
// published so far — but a run still in progress goes on publishing, and those
// entries would land on the new branch. Fork between turns.
func (r *Runner) Fork(at string) error { return r.forkAt(at, "") }

// Rewind resolves ref — a checkpoint label, else a raw entry id — and forks
// there, publishing a must-deliver [event.SessionForked] carrying the resolved
// entry id and, when ref named a checkpoint, its label. It is the ergonomic
// spelling of [Runner.Fork] for undo; every caveat below is Fork's too.
//
// # Rewind is additive: nothing is ever deleted
//
// Rewind APPENDS a fork_point parented on the target entry. It does not
// truncate the journal, drop entries, or shorten the file:
//
//   - The abandoned tail REMAINS in the log. It is still in
//     [session.Journal.Entries], still visible to anything reading the JSONL
//     file, and still recoverable by forking back to it.
//   - It still counts toward [Runner.Cost], which aggregates every branch. A
//     rewind reclaims no spend — cost measures tokens the provider was already
//     paid for, and those calls happened.
//   - Only the FOLDED CONTEXT changes: the tail drops out of what the model
//     sees on the next turn.
//
// This is a ratified design decision, not an implementation shortcut. The
// journal is strictly append-only, and two properties rest on that: its
// auditability tenet (every model call the session ever made is reconstructable
// from the log) and its torn-write recovery, which repairs a crash by dropping
// a bad FINAL line — a model that cannot survive an in-place rewrite of earlier
// entries. A destructive rewind would trade both away for the intuition that
// undo reclaims cost. It does not.
//
// # Resolution and label collisions
//
// ref is matched against checkpoint labels first, and only falls back to being
// treated as a raw entry id when no checkpoint carries it. When SEVERAL
// checkpoints share a label, the MOST RECENT one in append order wins:
// "rewind to before-refactor" means the latest such mark, which is also what
// makes a label reusable as a moving bookmark.
func (r *Runner) Rewind(ref string) error {
	at, label := ref, ""
	// Last match wins: Checkpoints is in append order, so scanning forward and
	// overwriting lands on the most recent checkpoint carrying the label.
	for _, cp := range r.Checkpoints() {
		if cp.Label == ref {
			at, label = cp.ID, cp.Label
		}
	}
	return r.forkAt(at, label)
}

// forkAt is the shared body of Fork and Rewind: drain the journaling handshake
// so the branch is parented against an up-to-date HEAD, append the fork point,
// and announce it. The event publishes only after the fork is durable, so a
// client never sees a fork the journal does not have.
func (r *Runner) forkAt(at, label string) error {
	r.awaitJournaled()
	if _, err := r.journal.Fork(at); err != nil {
		return fmt.Errorf("runner: fork session at %s: %w", at, err)
	}
	r.broker.Publish(event.NewSessionForked(r.journal.ID(), at, label))
	return nil
}

// currentModel returns the model this runner currently uses, synchronized
// against [SetModel]. Every read of the model — Prompt's loop.Config and
// consume's journaled entries — goes through this accessor rather than the
// field directly, so a concurrent SetModel can never race with a Prompt in
// flight or with the consume goroutine journaling that turn's entries.
func (r *Runner) currentModel() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.model
}

// SetModel changes the model this runner uses for its next (and subsequent)
// Prompt turns, without rebuilding the session. It is the mechanism a caller
// uses to switch models mid-session — e.g. in response to a user command —
// while keeping the same journal, provider client, and conversation history.
//
// The swap is same-provider only. The Runner's provider client
// ([providers.Build]) is bound to one backend family (anthropic, openai, …)
// at construction; the per-call model id flows through as [provider.Request]
// .Model, so switching to another model in that same family works with the
// existing client. Switching across families would hand the bound client a
// model id it cannot serve, so SetModel rejects it: model must resolve
// ([provider.Resolve]) to the same Provider as the runner's current model.
// The id need NOT be in the registry — an unregistered model whose backend is
// inferable switches fine, it simply carries no pricing or limits.
// A caller that needs a different provider family
// starts a new session (a new Runner, built with the new model, which
// resolves its own provider and credential) instead of mutating this one.
//
// Concurrency: the field write lands under the same lock [currentModel]
// reads through, so this is race-free to call concurrently with Prompt. A
// turn already in flight when SetModel is called completes on the model it
// started with (Prompt reads the model once, at the top of the turn); only
// the NEXT Prompt observes the change. Calling SetModel while a turn is in
// flight is safe but the exact turn boundary at which the new model first
// applies is unspecified from the caller's point of view — a caller wanting
// deterministic behavior calls SetModel between turns (i.e. after a Prompt
// call returns).
//
// SetModel does NOT re-validate the standing reasoning effort. [SetEffort]
// rejects an effort when the CURRENT model is registry-known-non-reasoning,
// but the reverse is unguarded: setting an effort and then switching to a
// non-reasoning model leaves the effort in place. What happens next is
// provider-dependent — the OpenAI adapter's reasoning-model gate drops it,
// while the Anthropic adapter's [provider.Thinking.Active] gate does not. A
// caller moving to a model that cannot reason should clear the effort itself
// (SetEffort("")).
func (r *Runner) SetModel(model string) error {
	next, err := provider.Resolve(model)
	if err != nil {
		return fmt.Errorf("runner: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, err := provider.Resolve(r.model); err == nil && cur.Provider != next.Provider {
		return fmt.Errorf("runner: cannot change model from %q (%s) to %q (%s): different provider; start a new session for a different provider instead", r.model, cur.Provider, model, next.Provider)
	}
	r.model = model
	return nil
}

// currentEffort returns the reasoning effort this runner currently uses,
// synchronized against [SetEffort] and mirroring [currentModel]. Prompt reads
// the effort through this accessor when it overlays the per-turn params, so a
// concurrent SetEffort can never race with a Prompt in flight.
func (r *Runner) currentEffort() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.effort
}

// SetEffort changes the reasoning effort this runner uses for its next (and
// subsequent) Prompt turns, without rebuilding the session. It is the
// effort-axis parallel to [SetModel]: a consuming TUI uses it to raise or lower
// reasoning effort mid-session — orthogonal to model choice — while keeping the
// same journal, provider client, and conversation history.
//
// effort must be a level the unified vocabulary recognizes
// ([provider.ValidEffort]): "low", "medium", "high", or "" to clear the level
// back to the provider's default. A provider projects the level down to its own
// wire format (OpenAI's effort string, Anthropic's thinking budget) and ignores
// what it cannot use, so the same call is meaningful across provider families —
// unlike SetModel, effort carries no same-provider constraint.
//
// Setting a level TURNS REASONING ON: a non-empty effort satisfies
// [provider.Thinking.Active], so a caller need not also have set
// Params.Thinking.Enabled at construction. Clearing (effort == "") drops back to
// whatever the construction-time Params.Thinking said — reasoning stays on at
// the provider's default level if Enabled was set, and off if it was not.
//
// Three consequences are worth knowing. Turning reasoning on via a level DROPS
// any Params.Temperature on Anthropic, whose API rejects the two together — so
// an embedder that pinned a temperature for determinism loses it the moment a
// user picks a level. And in two cases the level reaches the request but does
// not move the wire: a construction-time Params.Thinking.BudgetTokens pins the
// Anthropic budget and outranks any level set here (see [provider.Thinking]),
// and the OpenAI adapter emits reasoning config only for a model its registry
// knows reasons, so an unregistered OpenAI model — which this setter admits
// (below) — still runs without it.
//
// Capability: a non-empty effort is rejected when the runner's CURRENT model is
// one the registry KNOWS does not support reasoning. This mirrors SetModel's
// permissiveness toward unregistered ids — an unregistered model's reasoning
// support is UNKNOWN, not "no", so it is allowed; only positive registry
// evidence of a non-reasoning model rejects. Setting effort does not itself
// switch models, so pair it with SetModel when moving to a reasoning model.
//
// Concurrency: the field write lands under the same lock [currentEffort] reads
// through, so this is race-free to call concurrently with Prompt. As with
// SetModel, a turn already in flight completes on the effort it started with
// (Prompt reads the effort once, at the top of the turn); only the NEXT Prompt
// observes the change, and the exact turn boundary is unspecified when called
// mid-turn.
func (r *Runner) SetEffort(effort string) error {
	if !provider.ValidEffort(effort) {
		return fmt.Errorf("runner: unknown reasoning effort %q: want %q, %q, %q, or \"\" to clear", effort, provider.EffortLow, provider.EffortMedium, provider.EffortHigh)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	// Reject only on positive evidence the current model cannot reason. Lookup
	// (registered-only) never reports on an unregistered id, so those pass —
	// the same UNKNOWN-is-allowed rule SetModel applies. Clearing (effort == "")
	// is always allowed: it asks for no reasoning, so model capability is moot.
	if effort != "" {
		if info, ok := provider.Lookup(r.model); ok && !info.Reasoning {
			return fmt.Errorf("runner: model %q does not support reasoning effort; switch to a reasoning model first", r.model)
		}
	}
	r.effort = effort
	return nil
}

// setJournalWriteErr records the first journal write failure the consumer
// goroutine observes since the last [Runner.takeJournalWriteErr]; later
// failures in the same window are dropped (the first is the one that matters
// for diagnosis).
func (r *Runner) setJournalWriteErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.jerr == nil {
		r.jerr = err
	}
}

// takeJournalWriteErr returns the journal write failure the consumer goroutine
// observed since the last take, and clears it. [Runner.Prompt] takes it at each
// turn boundary; [Runner.Close] takes whatever is left as a backstop, covering
// a failure in the final drain that no later Prompt boundary could report.
func (r *Runner) takeJournalWriteErr() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	err := r.jerr
	r.jerr = nil
	return err
}
