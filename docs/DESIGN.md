# agent-sdk-go — design notes

Normative spec beyond [`PRD.md`](PRD.md): the loop seam, permission grammar,
manifest schema, sourcing decisions, and engineering constraints. A cold-start
implementer should need no other context.

## Loop seam (M1)

- **Injectable model call** (`StreamFn`-shaped): the loop never imports a
  vendor SDK; providers are quarantined in adapter packages behind a
  normalized streaming event union.
- **Hooks**: `beforeTool` / `afterTool` / `transformContext` /
  `prepareNextTurn` — one orthogonal seam covering permissions, context
  shaping, and steering. Hook callbacks are **never-throw**: plain
  `(T, error)` returns, and a hook error never panics the loop.
- Iteration cap and interruption (context cancellation) are loop features,
  not caller conventions.
- **Internal message type ⊃ LLM wire type**: the session owns a richer
  message than any provider speaks; project down at the call boundary
  (`convertToLLM`), never up.

**Implementation (M1, `loop/`).** `loop.Run(ctx, Config, messages) (Result,
error)` drives the loop: each iteration is one model call (a `turn.*` pair
carrying that call's usage + priced cost), the provider stream is converted to
contract events (`message.*`, `tool.call.*`), and on a `tool_use` stop the loop
executes the requested tools and appends `tool_result` blocks before the next
call. Hooks are never-throw — a hook's error emits a non-fatal `session.error`
and the loop proceeds with the pre-hook value. The loop consumes a `ToolRegistry`
interface declared consumer-side in `loop/`; `loop.FromRegistry(*tool.Registry)`
adapts the builtin `tool.Registry` to it (building `provider.ToolSpec`s from each
tool's schema). `compose.LoopConfig(m, LoopDeps)` wires a
provider + model + tools + broker from a manifest; `compose.CredentialSource(m)`
resolves `provider.auth` (`env:VAR` today; `oauth:*` defers to an auth.Store).

## Broker & subscription semantics (M1)

The broker (`event/`) fans one session's typed stream out to N subscribers and
distinguishes *attach* from *drive*:

- **`Subscribe` / `Events`** replay the retained must-deliver backlog
  (lifecycle + terminal events, sized by `WithReplay(n)`) in seq order before
  live delivery — what a client attaching mid-session wants, so it recovers the
  events it missed.
- **`SubscribeLive` / `EventsLive`** deliver from-now only, never the backlog —
  what a driver starting a turn wants: subscribe, dispatch one new turn, wait
  for *that* turn's terminal event. Using replaying `Subscribe` here caused the
  M2 followup bug (the driver observed a prior turn's retained terminal event
  and mistook it for its own turn's completion).

Deltas ride the lossy tier (dropped under backpressure, drop counters exposed);
`*.finished` / `session.*` / `permission.*` are must-deliver. Fan-out is per
subscriber — every registered client sees every event, regardless of which
client's op started the turn.

## Provider parity & credentials (M1)

- **No flagship provider.** `provider.Provider` (`Stream(ctx, Request)
  (StreamHandle, error)` + `Info() ModelInfo`) is vendor-neutral; Anthropic and
  OpenAI are peers. `Request` carries the internal message model (`[]Message`,
  each `Content []ContentBlock`: text / reasoning / tool_use / tool_result /
  image), `[]ToolSpec`, `System`, and `Params` (max tokens, thinking
  budget/effort). The provider projects this down to its wire format; never up.
- **Normalized stream.** `StreamHandle.Next()` yields `StreamEvent`s
  (`TextDelta`, `ReasoningDelta`, `ToolCallStart/Delta/End`, and a terminal
  `Finished` carrying `StopReason` + normalized `Usage`). `Usage` has
  input/output/cache-read/cache-write counters plus a `Raw` map for audit.
  `provider.Iter` adapts a handle to `iter.Seq2`; `provider.SliceStream` builds
  a fake handle for tests.
- **Model registry.** An embedded `id → ModelInfo` table (context window, max
  output, per-Mtok pricing, reasoning support) backs `Info()` and cost
  accounting; `CostOf(model, usage)` prices a turn. It is plain data — extend by
  adding rows.
- **Credentials.** `provider.CredentialSource.Credential(ctx, providerID)
  (Credential, error)` decouples providers from the auth package. Kinds are
  `api_key` and `oauth`; `EnvCredentialSource` (API keys from env vars) ships in
  provider core, and `auth.Store` (M1) implements the same interface over an
  on-disk `auth.json` (mode `0600`, per-provider entries, refresh handling).
- **`providers.Build(model, creds)`** is the construction seam: it maps a
  manifest model id to a concrete provider wired to a `CredentialSource`. It is
  the factory-template pattern future pluggable subsystems copy — sandbox
  backends and vendor settings loaders take the same `Build(config, deps)`
  shape.
- **Direct-wire APIs, no unified SDK.** Providers are built directly on each
  vendor's HTTP API rather than a cross-vendor aggregator SDK — for full control
  of the request/stream shape and end-to-end inspectability of everything that
  enters the model's context. The cost is one thin adapter per vendor; the
  payoff is that nothing in the wire path is opaque.

## Auth & credentials (M1)

`auth/` owns an on-disk `auth.json` (mode `0600`, atomic temp-file+rename,
store root configurable via `WithRoot`) and
implements `provider.CredentialSource`, so provider adapters resolve auth
without importing `auth`. It reuses the provider-core contract directly:
`auth.CredKind`/`Credential`/`CredentialSource` are aliases of the `provider.*`
types, and `KindAPIKey`/`KindOAuth` alias `provider.CredAPIKey`/`CredOAuth`. The
adapter maps `Kind` to the header convention (Anthropic: `x-api-key` vs
`Authorization: Bearer` + `anthropic-beta: oauth-2025-04-20`; OpenAI: platform
key vs the ChatGPT backend with a `ChatGPT-Account-ID` header — both exported
from `auth`).

Expired OAuth tokens refresh transparently inside `Credential`, single-flighted
by a ctx-aware in-process write semaphore plus a cross-process advisory flock on
an `auth.lock` beside the token store, with a double-check re-read — because refresh tokens rotate
and a concurrent double-refresh would invalidate the winner. The refresh holds
that lock across the token-endpoint call, so acquisition honors the caller's
context: a cancelled `Credential(ctx)` returns promptly rather than waiting out
an unrelated refresh.

**Login flows** (clean-roomed from MIT/Apache-2.0 references — opencode + pi for
Anthropic, openai/codex for OpenAI; PKCE S256 throughout). `Login(ctx,
providerID)` returns the authorize URL and a completion handle — the **SDK
never opens a browser**. Two shapes:

- **Anthropic** (subscription, code-paste): authorize at `claude.ai/oauth/
  authorize`, user pastes a `code#state` string, JSON token exchange at
  `platform.claude.com/v1/oauth/token`. `Login` returns `Redeem(code)`.
- **OpenAI** (ChatGPT subscription, loopback): authorize at `auth.openai.com`,
  browser redirects to `http://localhost:1455/auth/callback`, form-encoded
  exchange; the `chatgpt_account_id` is read from the `id_token`. `Login`
  returns `Wait()`, which blocks on the local listener. The listener's lifetime
  is tied to the login ctx (cancellation frees the port) and `Login.Close`
  releases it explicitly, so an abandoned login never leaks the fixed port.

Tests drive both flows against an `httptest` fake OAuth server (authorize →
callback/redeem → exchange → refresh → expiry); no live endpoint is ever
contacted in tests or at build time.

## Permission rule grammar (M3)

```
rule       := ToolName | ToolName "(" specifier ")"
specifier  := prefix ":*"          Bash(git status:*)      command prefix
            | glob                 Read(src/**) · Edit(*.env)
            | mcp tool             mcp__search__*(*)
lists      := deny[] > ask[] > allow[]     first match in that order wins
unmatched  ⇒ ask (fail-safe)
compound shell (&&, |, ;) ⇒ dangerous
dangerous  ⇒ grants force-downgraded to exact-match, TTL'd, audited
sources    := embedded defaults < global config < project config < session grants
              (deny from ANY source is un-overridable)
```

The engine consumes typed `[]Rule`; vendor formats are import adapters that
produce those rules. Claude Code `settings.json` allow/ask/deny is one such
loader among others (native YAML is another) — the adapter lands with the
vendor-format milestone (M5/M6), and which package hosts the CC loader is
undecided (home TBD at M5). Grants persist with TTL behind an
anti-escalation cache: a read grant never satisfies a write ask, and dangerous
specs never widen past exact-match.

**Implementation home (M3, `permission/`).** The engine itself — `permission.Rule`
+ `permission.Engine` (`New`, `Evaluate`, `Grant`) — ships in M3 as a thin,
format-agnostic slice: deny > ask > allow precedence, an unmatched request is
`ask` (fail-safe), and `Grant` appends a runtime session-scoped rule. It imports
only `event` + stdlib. The TTL / anti-escalation / dangerous-downgrade policy and
every vendor-format loader (`settings.json`, native manifest) are **not** here
yet — M5/M6, landing in the same `permission/` package per the decided
permission-format home (2026-07-13).

## Guard / decision seam (M3)

The loop's before-exec gate (`loop/guard.go`, `loop/gate.go`) is the sandbox
seam named in the [PRD](PRD.md) milestone table: M3's permission story is
deliberately binary — a tool call is either sandboxable (run contained) or it
raises an approval request. There is no third "run uncontained" outcome.

- **`Decision`** — `DecisionRunContained` / `DecisionAsk` / `DecisionDeny`. A
  **`Guard`** (`Evaluate(ctx, ToolCall) Guarding`) returns one plus the
  `Rule`/`Spec`/`Trace` the permission events carry. `Config.Guard` is nil by
  default: every call runs uncontained, exactly the pre-M3 behavior — zero new
  events, zero new goroutines, no golden-file changes on any existing path.
- **`Approver`** (`Await(ctx, id) (Reply, error)`) is how the loop's own
  goroutine blocks on a human's reply to a `DecisionAsk`. **`Gate`** is the
  reference implementation: `Await` selects on a per-id buffered reply channel
  and `ctx.Done()` — it spawns **no goroutine**, so a cancelled turn cannot leak
  one. The consuming application routes an inbound `event.PermissionReply` op
  into `Gate.Reply`, which never blocks (buffered chan) and silently drops a
  reply with no live waiter (already answered, or the turn was cancelled).
- **`Container`** (`CanContain(ctx, ToolCall) (bool, error)`) is the sandbox
  capability check. The SDK defines **only this interface** — concrete backends
  (Seatbelt on macOS, bwrap+seccomp on Linux) are an application/optional-package
  concern and must never land in the SDK.
- **`RuleGuard`** composes a `permission.Engine` with an optional `Container`
  into the M3 policy: a deny rule → `DecisionDeny`; an ask rule or an unmatched
  request → `DecisionAsk`; an allow rule → `DecisionRunContained` only if
  `Container.CanContain` says so, else `DecisionAsk` — an allow verdict never
  runs a call uncontained (decided 2026-07-13). `RuleGuard` also implements
  **`Granter`** (`Grant(call)`): a remembered "always allow" reply appends a
  session-scoped allow rule to the engine via `Engine.Grant`, so an identical
  future call skips the ask. TTL / anti-escalation live with the engine (M5/M6),
  not here.
- **Emit → await → reply flow** (`runner.gate` in `loop/loop.go`, called from
  `runOneTool` right after `beforeTool` and before the tool executes or spills
  any output):
  - `DecisionRunContained` — the call proceeds; no permission events at all.
  - `DecisionDeny` — a **static** policy block: the loop publishes
    `permission.resolved{deny}` **without** a preceding `permission.requested`
    (no human was asked, so nothing was requested), then a blocked
    `tool.call.finished{is_error}` so the tool_use/tool_result pairing the
    provider sees stays well-formed.
  - `DecisionAsk` — the loop publishes `permission.requested`, then (if
    `Config.Approver` is set) blocks on `Approver.Await(ctx, call.ID)` for the
    matching `permission.reply`, then publishes `permission.resolved{verdict}`.
    An allow reply lets the call proceed (and grants a remember, if asked); a
    deny reply blocks it the same way a static deny does.
- **Fail-closed everywhere permission is uncertain**: a nil `Config.Approver`
  under `DecisionAsk` denies immediately after emitting the request (nothing
  can await a reply); an `Approver.Await` error (including a cancelled ctx)
  denies; a `Container.CanContain` error on an otherwise-allowed call escalates
  to `DecisionAsk` rather than either running uncontained or silently blocking.
  An unrecognized `Decision` value also denies.
- Every gated-off call (deny or ask-then-deny) still emits `tool.call.finished`
  — nothing executed, so there is no spill — keeping the loop's tool-round
  invariant (one `tool_result` per `tool_use`) intact for every outcome.

## Agent manifest (compose/)

```yaml
# release-ops.yaml — an agent manifest
agent: release-ops
description: release automation and deployment checks
provider:
  model: anthropic/claude-sonnet-5     # provider/model id from the catalog
  auth: env:ANTHROPIC_API_KEY          # or op://…, or oauth:anthropic
  params: { max_tokens: 32000, thinking: auto }
prompt:
  base: ./prompts/ops.md               # authority-layered: operator > rules > base > memory
  context_files: [AGENTS.md]           # CLAUDE.md honored via import shim
tools:
  builtin: [bash, read, edit, write, grep, glob, ls]
  mcp:
    search: { url: https://mcp.example.com, auth: oauth }
  plugins:
    - module: github.com/someone/agent-plugin-k9s   # subprocess, own repo
lsp: { auto: true }                    # registry auto-detect; per-server overrides
skills: [./skills, ~/.config/agent-sdk/skills]
permissions:
  allow: ["Bash(kubectl get:*)", "Read(**)"]
  ask:   ["Bash(kubectl:*)", "Edit(**)"]
  deny:  ["Bash(rm -rf:*)", "Read(*.env)"]
sandbox: { mode: workspace-write, network: false }
auto_mode: { reviewer: same-provider, fail: closed }   # rails → sandbox → reviewer
session: { store: jsonl, compact_at: 0.8 }
hooks:
  pre_tool_use: [./hooks/audit]        # subprocess: JSON in/out, allow|deny|rewrite
```

## Headless exec adapter (M3)

The `exec/` package is the SDK half of an application's `exec` verb: a one-shot,
transport- and app-agnostic adapter that drives a drivable session with a single
prompt to completion. `exec.Run(ctx, sess, prompt, opts)` takes any session
satisfying the minimal `exec.Session` seam (`ID`/`Events`/`Prompt` — both
`*session.Session` and `*runner.Runner` qualify), subscribes before prompting,
and streams every emitted event as JSONL (one compact object per line, in seq
order) to an `io.Writer` (`os.Stdout` by default), draining on a separate
goroutine so a full must-deliver buffer never deadlocks the publisher. It stops
at the terminal `turn.finished` and returns a `Result` (session id, final text,
stop reason, event count). This is a pure projection of the standard event
contract — no new event kind.

`Options.OutputSchema` optionally validates the run's final text result against
a **documented subset of JSON Schema draft 2020-12** (`exec/schema.go`, stdlib
only: `type`, `properties`/`required`, `additionalProperties`, `items`, `enum`,
`minimum`/`maximum`, `minLength`/`maxLength`, `minItems`/`maxItems`). A mismatch
is reported out-of-band through the Go return value as a `*SchemaError` (with the
`Result` still populated), **never** as a new event kind.

## LSP (M3)

`lsp/` is a stdlib-only leaf shipping the registry + diagnostics seam;
everything below "Future" is a later consuming layer built on top of it, not
part of this package.

- **Registry** (`lsp.Registry`): a small, hand-curated language → launch-command
  table (gopls, typescript-language-server, pyright, rust-analyzer, clangd —
  not the ~370-server nvim-lspconfig dataset), resolved against PATH.
  `Resolve` distinguishes "no server registered for this language"
  (`ErrNotRegistered`) from "registered but not installed" (`ErrNotOnPath`)
  via `errors.Is`.
- **Client** (`lsp.Client`): a hand-rolled JSON-RPC-over-stdio client
  (Content-Length framing + JSON-RPC 2.0) — the LSP base protocol is a few
  dozen lines, so hand-rolling it keeps the package dependency-free rather
  than pulling in a jsonrpc library for a trivial amount of code. One
  background goroutine reads framed messages, routing responses to pending
  calls and `textDocument/publishDiagnostics` notifications to the
  diagnostics seam. That goroutine is the SDK's one otherwise-silent spot, so
  `NewClient`/`Start` accept an optional `lsp.WithLogger(*slog.Logger)` (nil ⇒
  discard) surfacing its three invisible paths (see "Instrumentation seams").
  `Start` spawns a real server via `os/exec`; that path
  isn't exercised in CI (no LSP servers installed there) — tests script a
  fake server over an in-memory `io.Pipe` Transport instead.
- **Diagnostics seam** (`lsp.Publisher`): the client hands every
  `publishDiagnostics` notification to a `Publisher` as a normalized `Batch`.
  The SDK defines ONLY this interface — deciding how (or whether) diagnostics
  reach a model or UI is the consuming application's job, exactly like the
  loop package's `Container` seam. `lsp` never imports `event/`;
  `Batch.Strings()` renders each diagnostic as a one-line string so a
  consumer can assign the result straight onto
  `event.ToolCallFinished.Diagnostics` / `loop.ToolResult.Diagnostics` (both
  already exist) without `lsp` taking a reverse dependency on either.

**Future (not shipped by `lsp/`)**: an embedded ~370-server registry
(nvim-lspconfig-shaped dataset) with lazy per-file-event startup, diagnostics
injected into tool results (current-file vs project split, errors first,
settle debounce), and `lsp_diagnostics` / `lsp_references` / `lsp_restart`
tools built on top of the `Registry` + `Publisher` seam above.

## Bulk-payload spill (M3)

Tool output is bulk ground truth, not event payload. Every tool execution
streams its raw output **append-only** to a per-call file under the session dir,
and `tool.call.finished` carries a reference plus a bounded excerpt instead of an
unbounded payload. This bounds memory, makes every level of a session tree
greppable on disk, and surfaces errors from the source. Events stay typed
structure; the files are the bulk ground truth the events point into.

**Streaming, never buffered.** The `spill.Writer` (`spill/`, stdlib-only leaf) is
an `io.Writer`: bash points its process stdout/stderr straight at one, so no code
path holds the full output in memory. As bytes stream through, the writer appends
them to the file (buffered, flushed+fsynced on close), folds them into a running
sha256 and byte count, and retains only a bounded head (2 KiB) + tail-ring
(2 KiB) for the excerpt. A tool that returns a small bounded string (read, grep,
…) has the loop write that string through the same writer post-hoc. The loop
hands the per-call writer to a tool via `context` (`spill.NewContext` /
`FromContext`). Because the writer is append-only and closed even on a
tool/process error, whatever streamed before a mid-run kill is already durable
and the reference is consistent with the bytes on disk.

**On-disk layout.** A session gains a directory sibling to its journal file:
`<root>/sessions/<slug>/<id>/calls/<call-id>.log` (the `<id>` dir coexists with
the `<id>.jsonl` journal and is invisible to the store's `<id>.jsonl` globs).
Created lazily, mode `0o700`.

**Model-facing rule.** Durability is universal — *every* tool call spills to
disk. What the model sees is the bounded excerpt **by default**, with one escape
hatch: a tool may set `FullResult` on its `Result` to hand the model the full
content instead (still spilled). The **read** tool sets it, so an explicit file
read is never truncated to head+tail — its output is bounded by the operation the
model asked for, which is not the memory-safety concern (that is only unbounded
streaming tools like bash, which must never set `FullResult`). Whichever text the
model sees is the text the runner journals, so every model call is reconstructable
from the journal in-run and on resume.

**Excerpt names the file.** When an excerpt elides, its marker names the spill
file — `… [N bytes elided — full output at <abs-path>] …` — so the model knows
the full output is on disk and can read it back. The marker names the **absolute**
path so the read tool resolves it from **any** working directory: read resolves a
relative path against its cwd, which need not match the store root, so a
root-relative path in the marker would silently miss. The structured `spill_path`
event field stays store-root-relative (for portability); only the model-facing
marker is absolute — the divergence is intentional. A file-less writer keeps the
pathless `… [N bytes elided] …`.

**Event shape** (`event.ToolCallFinished`). `result` carries whatever the model
sees (bounded excerpt by default; full content for a `FullResult` tool). The
`spill_path` / `spill_bytes` / `spill_sha256` fields reference the full file.
`spill_path` is **relative to the store root** (e.g.
`sessions/<slug>/<id>/calls/<call-id>.log`), never an absolute host path, so the
serialized event stays portable.

`input` carries the **complete, assembled** tool input the call ran with — the
authoritative payload a client reconciles against. It is deliberately distinct
from `tool.call.started`'s `input`, which is only the start-of-block **seed**: a
provider that streams a tool call's arguments as `tool.call.delta` fragments (the
Anthropic `input_json_delta` shape) announces `started` with an empty `{}` and
does not settle the real arguments until the stream ends. `tool.call.finished` is
the must-deliver terminal that carries them, so a consumer that needs the real
arguments — to journal the assistant's `tool_use` block, or to surface them in a
UI — reads `finished.input`, not `started.input`. The loop's assembly is
resilient to *how* a provider delivers the input: arguments that arrive only at
`content_block_start` (an inline seed with no deltas) fold into the same
accumulator streamed deltas write to, and an empty `{}` at the block's end never
masks a real seed or accumulated deltas.

**Root vs Cwd (why the marker is absolute).** The session store root
(`runner.Options.Root`, the embedder's app dir) and the tool working dir
(`runner.Options.Cwd`, the project dir) are commonly different, and the read tool
resolves a relative path against Cwd. So the elision marker names the **absolute**
spill path: a model that reads exactly the path the marker gives it gets the full
output back regardless of where Cwd sits. The structured `spill_path` field stays
root-relative and is not what the model reads — the two intentionally differ. This
keeps the read tool decoupled from the session store (it just resolves an absolute
path via its normal path resolution).

## ACP v1 projection surface (M4)

`acp/` is the SDK's modeling + projection half of the cross-repo ACP v1
featureset expansion. It owns message **types** and pure **mapping functions**
only — stdlib, no networking, no JSON-RPC framing, no goroutines. The WebSocket
transport and method dispatch live in the consuming application (gofer); an ACP
session is just another broker subscriber. This keeps the ACP work squarely
inside the tenets: it is a *projection* of the one Event/Op contract, and the
SDK still imports no application code. New capabilities are earned against the
two gates — a type or projection belongs here only if a second ACP client would
consume it unchanged, and only as a mapping, never a built-in behavior.

**The ACP↔Event/Op boundary lives here, both directions:**

- **Outbound** (`event.Event` → `session/update`): `ToSessionUpdate` projects
  message/tool-call/permission events to ACP notifications; content blocks
  (`content_block.go`) and tool-call content (`tool_call.go`) carry the payload.
- **Inbound** (JSON-RPC method + params → `event.Op`): `DecodeOp` routes the
  four op-bearing methods — `session/prompt`→`PromptSend`, `session/cancel`→
  `TurnInterrupt`, `session/new`→`SessionNew`, `session/load`→`SessionResume`
  (resume) — via the `From*` functions; `initialize`/`authenticate` are
  handshakes a transport answers itself.

**Shipped (v0.6.0, the projection-safe subset).** `usage_update` projection;
the `image`/`audio`/`resource` (`EmbeddedResource`) content blocks; and the
`diff`/`terminal` tool-call content variants. v0.6.0 was modeling + projection
only — the types round-trip and project, but no producer emitted the rich
blocks. The **`diff` producer now ships**: the edit and write tools attach a
structured before/after `event.FileEdit` to `tool.call.finished`, and
`ToSessionUpdate` projects it to a `diff` [ToolCallContent] on
`tool_call_update` (replacing the plain-text result for an edit; a creation
carries no `oldText`). `terminal`, and the `image`/`audio`/`resource` blocks,
stay **modeled-but-dormant** — no builtin tool naturally produces them (no tool
drives a terminal or yields image/audio bytes, and the read tool's authoritative
output is deliberately line-numbered text, not a faithful raw-bytes resource).
`usage_update` is still skipped for turns without real usage. The `session/list`
request/response types (`list.go`) and `SessionInfo` (already carrying `cwd` and
optional `title`) are modeled but not yet wired into dispatch.

**Promote-if-stable** governs what projects onto this standard surface vs stays
gofer-native — see the [PRD](PRD.md) settled decision. In short: a capability
lands in `acp/` only when a stable ACP v1 spec variant exists; unstable/absent
spec surfaces stay application-layer (`gofer/event`) and are never invented
here. `usage_update` is promoted; `set_model` and `gofer/event` stay native.

**SDK-side M4 roadmap** (modeling + projection, matrix-driven):

- **Session methods** — wire `session/list` dispatch/projection over the
  already-modeled types, model `set_config_option`, and confirm resume
  (`session/load`) coverage. `SessionInfo.cwd`/`title` already exist; no schema
  guess needed.
- **Producers for the rich blocks** — the `diff` producer ships (edit/write →
  `event.FileEdit` → `diff` content block). `terminal` (and `image`/`audio`/
  `resource`) stay dormant until a builtin tool naturally produces them; none
  does today, so no producer was invented.
- **Model discovery types** — the types backing gofer's native list-models
  endpoint that feeds the `session/new` model picker (migrate to `providers/
  list` only once that spec surface stabilizes).
- **Capability modeling for the stretch set** — `session_info_update` (needs
  session titles), `plan` (needs a plan concept), and the
  `available_commands_update`/`current_mode_update`/`config_option_update`
  registries — modeled as they acquire a stable spec surface and a producer.

## Session tree & spawn seam (design-ahead, M5)

A subagent is a real session, not a sub-object: its own UUIDv7 journal, linked
to its parent. The SDK ships the spawn seam and the linking events — the parent
journal records a must-deliver `session.spawned{child_id, agent}`, child
metadata carries `parent_id`, and depth (parent-chain length) is capped at 5 and
enforced at spawn. The application wires this to its supervisor/roster (tree
view, peek/attach into any child). Ships M5; recorded here so the session and
event contracts leave room for it now.

## Extension tiers

Three tiers, by trust and coupling:

1. **Core** — hot path, security, or contract; compiled in (loop, broker,
   permission engine, session).
2. **Optional SDK package** — opt-in at compile time; Go compiles only what you
   import (`mcp/`, vendor settings loaders). First-party and trusted, but not
   forced on every embedder.
3. **Subprocess plugin** — third-party, runtime-installed, untrusted; isolated
   over JSON-RPC (host lands M5). Nothing untrusted runs in-process.

The tier is set by the two-gate test: would a second app need it unchanged
(core vs optional package), and could a seam suffice instead of a built-in?

## Component sourcing (survey verdict, 2026-07-11)

| Need | Verdict | Source |
|---|---|---|
| MCP client | **adopt** | `modelcontextprotocol/go-sdk` (official) |
| ACP protocol | build | M2 verdict: clean-room the ACP **v1** wire shapes in `acp/` (stdlib-only, no dep) + a pure Event/Op projection; transport (WebSocket/JSON-RPC) lives in the application, not the SDK. Supersedes the earlier "adopt `coder/acp-go-sdk`" survey verdict — keeping the SDK dependency-free and the projection a first-class broker client won out. |
| WASM plugin tier | **adopt** | `knqyf263/go-plugin` (wazero, typed interfaces) |
| Provider + streaming | build | thin, with a cross-vendor content-block message model |
| Loop + hooks | build | clean-room the proven seams; **FSL-licensed prior art is read-only, never a dependency** |
| Sessions | build | event-sourced JSONL tree behind a pluggable `session.Service`-shaped store interface |
| Permission engine | build | CC-settings-compatible grammar (above) |
| Coding tools | build | confirmed ecosystem gap: nobody ships bash/read/edit/grep as an importable package |
| Skills | build | the cross-tool Agent Skills SKILL.md standard |
| Manifests | build | schema above |

The survey behind these verdicts (six agents read at source level) is kept in
internal design notes; this table is the settled, repo-facing summary.

## Engineering constraints

- **Platforms**: macOS + Linux first-class (including sandbox backends);
  Windows later, no sandbox v1. Single static binary; `go install` works.
- **Go 1.25** (matches `go.mod`); range-over-func iterators (available since
  Go 1.23) are load-bearing in the event stream and per-test stream fakes.
- **Streaming budget**: first provider token reaches an attached client with
  ≤ one frame of added latency; the lossy delta tier exists so a slow client
  can never back-pressure the loop.
- **Observability**: no phone-home, ever. Local structured logs; optional
  OTLP export, off by default.

## Instrumentation seams (SDK stays dependency-light)

The SDK takes **no OpenTelemetry dependency** and emits no telemetry on its own
initiative — instrumentation lives in the embedding app (the application owns
the otel dep + exporters). `go list -deps ./...` names no `otel` package; a new
import of one is a bug. What the SDK owes an embedder is *seams*, not an
implementation:

- **Context propagation is end-to-end.** Every call path — loop, provider,
  `session`, `runner`, tools, guard, approver — threads `context.Context`
  through unbroken (`runner.New`/`Resume`/`Prompt(ctx, …)` →
  `loop.Run(ctx, …)` → `callModel`/`runTools` → the provider `Stream(ctx, …)`,
  `Guard.Evaluate(ctx, …)`, `Approver.Await(ctx, …)`, and each tool's
  `Run(ctx, …)`). An app can open a span on a turn and have it flow through the
  provider call and every tool execution without the SDK knowing tracing
  exists. This is an invariant, not an aspiration: a new code path that drops
  `ctx` is a bug. It is proven by `runner/ctxprop_test.go`, which plants a value
  in the ctx handed to `Prompt` and asserts it is observed at all four seams
  (provider, guard, approver, tool).
- **Optional `*slog.Logger` injection where the SDK is otherwise silent.** The
  SDK is silent by default; where SDK-internal diagnostics earn their keep, the
  seam is an optional `*slog.Logger` the embedder passes in (nil ⇒ discard, as
  the daemon already does for its own logger). The SDK never logs unprompted and
  never phones home. Two such seams exist today: `session.WithLogger` (torn-write
  warnings) and `lsp.WithLogger` (the LSP read loop's three otherwise-invisible
  paths — a malformed frame or publishDiagnostics notification dropped, and the
  read loop exiting on a transport death no in-flight call observed; a deliberate
  `Close` logs at debug so intentional shutdown is not noise). The loop needs
  none — every error and degraded path it hits is already surfaced on the stream
  as a `session.error` event (stream failures, spill open/close failures, the
  iteration-cap stop), not swallowed; likewise broker drops are exposed as
  counters. Add a logger option only when a genuine diagnostic would otherwise
  vanish with no event and no counter — not as blanket instrumentation.

### The Event/Op stream is the span/metric source

The typed two-tier stream in `event/` is the natural span/metric source, and an
embedder maps it to spans **entirely in the app** — the SDK never sees a span.
The mapping and the ordering that makes open/close pairing safe:

| Span | Opened by | Closed by | Correlation key |
|---|---|---|---|
| run / prompt | app, around its own `Prompt(ctx)` call | the terminal `turn.finished` (stop ≠ `tool_use`, or a `max_turns` terminal) | app-owned — no event needed |
| turn = model/provider call | `turn.started` | `turn.finished` | "currently-open turn" (turns never overlap) |
| tool | `tool.call.started{id}` | `tool.call.finished{id}` | `id` |
| permission | `permission.requested{id}` | `permission.resolved{id}` | `id` (same as the tool call) |

- **A "turn" brackets exactly one model call.** Per loop iteration the emit
  order is `turn.started` → `message.*` / `tool.call.started` (during the
  provider stream) → **`turn.finished`** → (if tools were requested)
  `permission.*` → `tool.call.finished`. So `turn.started`/`turn.finished`
  bracket the provider stream and *nothing else* — the turn span **is** the
  provider-call span (there is no separate provider event to open a distinct
  child from). Tool execution runs **after** `turn.finished`, between turns.
- **Tool spans are siblings under the run span, not children of the turn span.**
  Because `tool.call.finished` (and the `permission.*` pair) publish *after* the
  enclosing `turn.finished`, a tool span cannot nest inside the turn span — it
  would outlive it. Nest tool spans under the app-owned run span, keyed by call
  `id`; the run span alternates turn(model-call) spans and tool spans. (Naively
  nesting a tool under "the currently-open turn" is the trap this ordering
  guards against.)
- **Pairing is safe without new wire fields.** Tool and permission spans pair on
  the call `id` they already carry. Turn spans pair by position: the loop makes
  one model call at a time (turns never overlap) and every lifecycle/terminal
  event is must-deliver and per-session `seq`-ordered, so "open the turn on
  `turn.started`, close it on the next `turn.finished`" is unambiguous. The one
  edge case is the iteration-cap terminal — a `turn.finished{max_turns}` with
  **no** matching `turn.started` (documented on `event.TurnFinished`); a pairing
  consumer must tolerate that unmatched terminal. No `turn_id` on tool events is
  needed: tools nest under the run span by `id`, not under a turn. (This was
  evaluated against the "does a span-source event lack a correlation field?"
  bar and found sufficient — no wire field was added.)
- **Metrics** come from the same stream: `turn.finished{usage, cost}` →
  token/cost counters, `turn.*`/`tool.call.*`/`session.*` counts →
  turn/tool/session/error metrics.
- **Redaction is the app's job at the mapping boundary.** The stream carries raw
  payloads an app must **not** copy into span attributes: `tool.call.started`
  and `tool.call.finished` both carry `input` (tool params — the seed on
  `started`, the authoritative assembled input on `finished`) and `message.*`
  carry model text. Instrument with ids, names, counts, durations, costs,
  verdicts — never prompt text, tool params, or tool results.

A second embedder instruments the same seam the same way — the mapping above is
the contract, not one app's convention.
