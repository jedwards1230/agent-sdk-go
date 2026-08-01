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

- **Live model listing is an optional capability.** `provider.ModelLister`
  (`ListModels(ctx) ([]ModelInfo, error)`) is a separate interface, not a method
  on `Provider`: listing is orthogonal to running a turn, and requiring it would
  break `faux` and every third-party adapter for no gain. Callers type-assert.
  `anthropic.Provider` and `openai.Provider` implement it; `faux` deliberately
  does not. It lives in the SDK because the adapters already own the HTTP
  client, credential source (incl. OAuth refresh), base URL, and vendor auth
  headers — reimplementing it upstack would duplicate auth handling and drift.

  It is **stateless**: one vendor call, returning what the vendor said. No
  caching, TTL, disk persistence, registry merging, or degradation policy —
  those are application policy. No vendor listing endpoint reports pricing
  (Anthropic returns id/display_name/created_at; OpenAI returns
  id/created/owned_by on the API-key route, and the Codex/OAuth route serves a
  differently shaped catalogue — `{"models":[{"slug"…}]}`, requiring a
  `client_version` query parameter — carrying display names and context windows
  but, being subscription-served, no price). Every returned record is therefore
  `Unregistered: true` — nothing here is registry-sourced — and the flag's rule
  is **per-field**: a metadata field at zero means **unknown**, while a non-zero
  one is vendor-supplied fact a caller may render. The adapters carry what the
  vendor actually reports (Anthropic's `display_name`; the Codex route's
  `display_name`, `context_window`/`max_context_window`, and `visibility`
  normalized to `Hidden`) and invent nothing else. `Pricing` is the strict
  exception: it is always zero, because no listing endpoint reports a price and
  a fabricated one is the dangerous failure. A caller wanting pricing enriches
  each id itself via `Lookup`. An empty listing is a success returning an empty
  slice, never an error.

  `Hidden` **fails open**: its zero value means "not known to be hidden", so a
  record from any source that says nothing about visibility still shows up. The
  inverse spelling (`Selectable bool`) would fail closed and silently hide every
  model an adapter forgot to mark.

  The registry is **metadata, not an allowlist**. `Resolve(id)` — not `Lookup` —
  is the admission check: an id the table does not carry still resolves, and
  runs, so long as its backend is inferable from its shape (`claude-*`, `gpt-*`,
  `o<n>-*`). Such a record has `Unregistered: true` and — `Resolve` having only
  the id to work from — every metadata field at its zero value **meaning
  unknown**, so a newly released model is usable the
  day it ships rather than waiting on an SDK release. Only an empty id
  (`ErrNoModel` — the caller never resolved a default) and an id belonging to no
  known family (`ErrUnknownProvider` — no adapter to route to) are refused.

  Degradation is explicit by design, because the failure mode it replaces is
  worse than a rejection: a consumer that renders an unknown cost as `$0.00`
  reports a paid model as free. `Lookup`/`CostOf` keep their `ok` result,
  `ModelInfo.Unregistered` marks a synthesized record, and
  `session.CostReport` carries `Unpriced` + `Complete()` so a partial total
  cannot be presented as the session's cost.

  **Anthropic prompt caching** is a wire-level cost optimization distinct from
  the "no caching" above (which concerns model *listings*): the Anthropic
  adapter stamps `cache_control: {"type":"ephemeral"}` breakpoints on the stable
  request prefix — the last tool, the last system block, and the last block of
  the second-to-last message — so Anthropic caches the prefix and later turns
  read it instead of re-billing the whole prompt. The rolling conversation
  boundary sits on the last message *before* the newly-appended turn, so each
  turn's cache write becomes the next turn's cache read (the marker never lands
  on the mutating newest message). At most three markers are placed, within the
  four-breakpoint API limit. On by default (a strict win when the prefix is
  stable); `anthropic.WithPromptCaching(false)` opts a provider out. Cache hits
  and writes surface via the existing `Usage.CacheReadTokens`/`CacheWriteTokens`
  counters. OpenAI caches automatically and needs no wire markers.
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
- **Runtime reasoning-effort is a live seam (M4).** `Params.Thinking.Effort`
  (`low`/`medium`/`high`, unifying Anthropic's thinking budget and OpenAI's
  effort) seeds a runner's effort at construction, and `Runner.SetEffort(effort)`
  hot-swaps it at a turn boundary — the effort-axis parallel to
  `Runner.SetModel`. The setter validates against the unified vocabulary
  (`provider.ValidEffort`; `""` clears to the provider default) and against
  provider capability (it rejects a non-empty effort when the current model is
  one the registry KNOWS does not reason, staying permissive toward
  unregistered ids like SetModel). A named level is self-enabling —
  `provider.Thinking.Active()` is `Enabled || Effort != ""`, so an embedder that
  never constructs `provider.Params` still gets reasoning on the wire from a
  bare `SetEffort`; requiring both fields is what made the setter inert through
  v0.17.0. Anthropic projects the level onto a thinking budget (an explicit
  `BudgetTokens` outranks it, and a level-derived budget shrinks to fit the
  resolved `max_tokens` rather than inflating it), OpenAI sends it as its effort
  string. `event.SessionSetEffort` is the wire op that carries the swap,
  parallel to `session.set_model`. So a consuming TUI can offer an effort axis
  orthogonal to model choice between turns. Still
  design-ahead: a declarative home — `compose.Load` parses only the manifest
  `model` today, so a `params.thinking` block should be parsed too, making
  effort expressible without code.

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
    A plain allow reply lets the call proceed (and grants a remember, if asked);
    a deny reply blocks it the same way a static deny does; an *amended* allow
    is re-evaluated through the guard first (see AMEND below).
- **Fail-closed everywhere permission is uncertain**: a nil `Config.Approver`
  under `DecisionAsk` denies immediately after emitting the request (nothing
  can await a reply); an `Approver.Await` error (including a cancelled ctx)
  denies; a `Container.CanContain` error on an otherwise-allowed call escalates
  to `DecisionAsk` rather than either running uncontained or silently blocking.
  An unrecognized `Decision` value also denies.
- Every gated-off call (deny or ask-then-deny) still emits `tool.call.finished`
  — nothing executed, so there is no spill — keeping the loop's tool-round
  invariant (one `tool_result` per `tool_use`) intact for every outcome.

**Permission outcomes grow AMEND / EXPLAIN.** Beyond the four selection kinds
(`allow_once`/`allow_always`/`reject_once`/`reject_always`) the ACP permission
surface carries two more affordances a consuming TUI renders as "Tab" / "explain
why":

- **Amend** — `acp.PermissionOutcomeAmended{OptionID, RawInput}` is a
  `request_permission` response variant: the human edits the tool input, then
  allows the *amended* call. It resolves like the allow option it names (the
  chosen kind still decides remember-ness), but the replacement input rides
  along: `acp.ToPermissionReply` projects it onto `event.PermissionReply.Input`
  → `loop.Reply.Input`. An amend is **not** an override of the guard: the loop
  re-evaluates the amended call through `Guard.Evaluate` as a *fresh* request
  before running it (`runner.approveAmended`).
  - `DecisionDeny` on re-evaluation **rejects** the call — no re-prompt. A deny
    rule is a policy floor the static-deny path never offers a human either, so
    re-prompting would hand the operator a decision the policy says is not
    theirs. The loop publishes `permission.resolved{deny, "amend-denied: <rule>"}`
    then a blocked `tool.call.finished`.
  - `DecisionAsk` / `DecisionRunContained` proceed — an ask is already satisfied
    (the operator authored *and* approved this exact input in one round trip).
    The loop publishes `permission.resolved{allow, "amended: <rule>"}`, carrying
    the **re-evaluated** rule, since that is what the executed call was assessed
    under. An unrecognized decision fails closed like `gate`'s default.
  - **`Remember` is not honored for an amend**: `Engine.Grant` is append-only,
    unattributed and untimed, and the remember affordance the operator saw was
    scoped to the prompt describing the *original* call. The dropped bit is made
    legible on the label (`"amended: <rule> (remember ignored: amended)"`) rather
    than silently ignored. A plain allow keeps `Remember` unchanged.
  - Re-evaluation happens **before** `permission.resolved` is published, so a
    client never sees `resolved{allow}` followed by a blocked call. A nil `Input`
    is the unchanged plain allow/deny path — the whole extension is additive.
- **Explain** — `session/explain_permission` is a read-only rationale query
  (`acp.ExplainPermissionRequest`/`ExplainPermissionResponse` +
  `PermissionRationale{Reason, Policy, Source, Trace}`) the client sends while a
  `request_permission` is still pending. It carries no op (like `session/list`):
  the daemon answers from the guard decision it holds (the matched-rule label
  and decision trace already on the permission events), and the client
  re-prompts with the original options. It never resolves the request.

**Structured question surface, distinct from permission (design-ahead, M5).**
Permission gating is the only interactive-choice primitive today (allow/deny on a
`permission.requested`). A consuming TUI also wants a first-class *question*
surface: a titled question with N labeled options, each carrying a rationale
sub-line, optional free-text, and a batched **multi-question** form (ordered
questions, a per-option reference payload, per-question notes). This is a new
client-prompt surface — a modeled `acp/` message type and/or a builtin `ask_user`
tool — separate from permission: it composes with the guard/approver await seam
(the loop blocks on a client reply the same way) but it is not a policy gate, so
it is *not* the permission engine. Recorded design-ahead so the Event/Op contract
leaves room for the question event + its reply op. The `acp/` message type is now
modeled (`acp/decision.go`): `session/request_decision` carries a
`RequestDecisionRequest` of `DecisionQuestion`/`DecisionOption` values, answered
by a `RequestDecisionResponse` of `DecisionAnswer`s whose tagged `DecisionOutcome`
union (selected/text/chat/cancelled) mirrors `PermissionOutcome`. The Event/Op
projection remains design-ahead — only the wire types exist so far.

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
  `loop.ToolResult.Diagnostics` (`[]string`) is the only path to the event, and
  a consumer reaches it from outside the tool through either seam that yields a
  `ToolResult`: a `loop.Hooks.AfterTool` hook, or a decorated
  `loop.ToolRegistry`. `tool.Result` carries no diagnostics — there is no
  tool-side slot for the SDK to fill, and `loop.FromRegistry` never sets one.

**Future (not shipped by `lsp/`)**: an embedded ~370-server registry
(nvim-lspconfig-shaped dataset) with lazy per-file-event startup, diagnostics
injected into tool results (current-file vs project split, errors first,
settle debounce), and `lsp_diagnostics` / `lsp_references` / `lsp_restart`
tools built on top of the `Registry` + `Publisher` seam above. "Injected into
tool results" means the consuming layer setting `loop.ToolResult.Diagnostics`
from a `loop.Hooks.AfterTool` hook or a decorated `loop.ToolRegistry`, not an
SDK-side slot on `tool.Result`.

## Skills (`skill/`, M5)

`skill/` loads the neutral, cross-tool `SKILL.md` standard (the only other
neutral standard the SDK builds to besides `AGENTS.md`) with progressive
disclosure: a skill's name and description enter the model's context up
front; the body loads only when the skill is invoked. Same context-cost
discipline as tool/MCP index-first discovery.

- **`skill.Load(dirs []string, opts skill.Options) (*skill.Set, []skill.Diagnostic)`**
  discovers skills across a caller-supplied directory list — the SDK reads no
  config and decides no default locations; that is the embedder's concern
  (gofer's `config.Skills`). Each directory is scanned one level deep for the
  standard `<dir>/<name>/SKILL.md` layout.
- **Precedence**: dirs are scanned in the order given; the first directory to
  define a name wins, and every later duplicate is dropped with a
  `Diagnostic`. PATH-style resolution — the package does not know which
  directory is "project" vs "user global", so the caller's list order is the
  only precedence signal.
- **Never silently truncated**: an oversized `SKILL.md` (`Options.MaxBodyBytes`,
  default 64KiB) is skipped with a `Diagnostic`, never truncated — half a
  skill can silently drop the instruction that made it correct. Malformed or
  incomplete frontmatter (missing `name`/`description`, unclosed fence,
  invalid YAML) is the same: skipped with a `Diagnostic`, not degraded to a
  guessed default. A `Diagnostic` is a value the caller must observe, never
  swallowed.
- **`Set.Index() []Meta`** is the discovery projection: `{Name, Description,
  Truncated}` only — no body field exists on `Meta`, so nothing in the type
  can leak one. `Description` is truncated to `Options.DescriptionBudget`
  (default 200 bytes) at a word boundary where one exists.
- **`Set.Body(name string) (string, error)`** is the one place a body is
  read, from disk, at invocation time — never cached from `Load`.
- **Invocation surface is the embedder's choice.** The package does not
  decide whether a skill surfaces as a tool, a system-prompt injection, or a
  slash command; `Set.Index`/`Set.Body` are the primitives. The optional
  `skill.NewTool(*skill.Set) tool.Tool` is one instantiation — a single
  dispatcher tool whose `Description()` projects the current index and whose
  `Run` resolves `Body` by name — but it is never registered implicitly; a
  caller that wants it constructs and `Registry.Register`s it itself.
- **Path safety**: a skill directory is untrusted input. A symlinked skill
  subdirectory or a symlinked `SKILL.md` is refused, not followed (reported
  as a `Diagnostic`); a frontmatter `name` containing a path separator or a
  `.`/`..` segment is rejected at load time, before it can become a landmine
  for any embedder-side code that later joins it onto a path.
## Web search providers (M7)

`search/` is an optional SDK package (extension tier 2 — see *Extension
tiers*): it performs real network I/O, so nothing in the SDK core imports it.
It ships a vendor-neutral `Provider` interface, two backend implementations,
and a name-keyed factory registry that lets a third backend be added without
touching this package.

- **Interface.** `Provider` is `Search(ctx, query string, opts Options)
  (*Results, error)` plus `Name() string`. `Options{MaxResults int}` (zero =
  provider default) configures one call. `Results{Query, Provider, Items
  []Result, Truncated bool}` and `Result{Rank, Title, URL, Snippet}` are
  designed to serve both a human-rendered view and a lossless projection into
  a model-facing tool result — nothing on `Results` requires a caller to go
  back to the provider for context.
- **Bounding (context transparency).** A search returning many full-text
  snippets is a context bomb if unbounded. `DefaultMaxResults` (10) is the
  per-call default absent config; `MaxResultsCeiling` (25) is a hard clamp
  `ClampMaxResults` applies regardless of what `Config.MaxResults` or
  `Options.MaxResults` request, so a misconfigured value cannot blow the
  budget — a caller needing more results pages via repeated calls instead.
  `DefaultSnippetLimit` (400 runes) bounds each `Result.Snippet` via
  `TruncateSnippet`, so one verbose backend description cannot dominate a
  payload the way an unbounded count would. Both implementations apply these
  before returning.
- **Credentials/endpoint are `Config`, never SDK-chosen.** `Config{APIKey,
  BaseURL, HTTPClient, MaxResults}` is the only way a backend learns its
  auth material and endpoint — no package reads an environment variable of
  its own choosing, and no backend defaults `BaseURL` to a real instance.
  `brave.New` requires `APIKey` (Brave rejects unauthenticated requests);
  `searxng.New` requires `BaseURL` (a self-hosted instance has no universal
  default) and treats `APIKey`, when set, as a bearer token for an
  auth-proxied deployment — SearXNG itself has no native key concept.
- **Registry.** `search.Register(name string, factory Factory)` adds a named
  constructor to a package-level map; `search.Build(name, cfg)` dispatches to
  it. Each backend calls `Register` from its own `init()`, so an embedder
  wanting `"brave"` blank-imports `search/brave` and never touches a switch
  statement inside `search` — the extension point `providers.Build`
  approximates with a hardcoded dispatch, made fully open here. Registering a
  duplicate name panics at init time (the `database/sql.Register` contract).
- **Errors.** `*Error{Provider, Kind, StatusCode, Err}` classifies a failure
  (`ErrKindConfig`/`Request`/`HTTP`/`Decode`) and unwraps to the underlying
  cause, so `errors.Is(err, context.Canceled)` reaches through a cancelled
  in-flight request the same way it does elsewhere in the SDK.
- **Not wired anywhere.** This package defines the interface, backends, and
  registry only — no `tool_search` tool, no loop/registry integration, no
  MCP or skill surface. That projection (turning `*Results` into a model-
  facing tool call) is a separate, not-yet-landed piece.

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

**Session id: generated or caller-assigned.** A new session's `<id>` is a fresh
UUIDv7 by default (globally unique, time-ordered — the property the fleet/roster
layer relies on). `Store.CreateWithID` (surfaced as `runner.Options.SessionID`)
lets an embedder that must know a session's id *before* it exists — e.g. a
process-isolated worker keyed by it — supply the id instead; it is then used
verbatim as the journal name and wire session id, so that embedder, not the SDK,
owns the uniqueness/ordering guarantee. The id must be a safe single path
component (no separators, not `.`/`..`); entry-id generation is unaffected.

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
  It also projects the two session-metadata events — `session.info`
  (`event.SessionInfoUpdated`) to a `session_info_update` carrying the session
  `title` (+ `updatedAt` from the event's publish timestamp), and
  `session.config` (`event.ConfigOptionsUpdated`) to a `config_option_update`
  carrying the embedder's full config-option snapshot — plus the `plan` event
  (`event.PlanUpdated`) to a `plan` update carrying the agent's full task-plan
  `entries`.
- **Inbound** (JSON-RPC method + params → `event.Op`): `DecodeOp` routes the
  four op-bearing methods — `session/prompt`→`PromptSend`, `session/cancel`→
  `TurnInterrupt`, `session/new`→`SessionNew`, `session/load`→`SessionResume`
  (resume) — via the `From*` functions. It also *recognizes* the
  request/response methods it does not pre-project to an op — `initialize`,
  `authenticate`, `session/list`, and `session/set_config_option` — returning
  no op so a transport answers them directly via their typed request decoders
  (`DecodeInitialize`, `DecodeListSessions`, `DecodeSetConfigOption`).
  `set_config_option` stays unprojected on purpose: its `configId`/value
  semantics are the application's business logic, not the SDK's.

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
`usage_update` is still skipped for turns without real usage.

**Shipped (session methods).** `session/list` is wired into `DecodeOp`
dispatch over its already-modeled request/response types (`list.go`) plus a
`DecodeListSessions` decoder; `session/set_config_option` is modeled
(`config.go`: `SetConfigOptionRequest`, `SetConfigOptionResponse`, and the
`ConfigOption` select/boolean value union) with a `DecodeSetConfigOption`
decoder. Both are recognized-but-unprojected in `DecodeOp` — a transport
decodes and answers them directly. Grouped select options and `_meta` are not
modeled yet (additive). `SessionInfo` carries `cwd`/`title`/`updatedAt`; its
optional `additionalDirectories` is not modeled yet (additive).

**Promote-if-stable** governs what projects onto this standard surface vs stays
gofer-native — see the [PRD](PRD.md) settled decision. In short: a capability
lands in `acp/` only when a stable ACP v1 spec variant exists; unstable/absent
spec surfaces stay application-layer (`gofer/event`) and are never invented
here. `usage_update` is promoted; `set_model` and `gofer/event` stay native.

**SDK-side M4 roadmap** (modeling + projection, matrix-driven):

- **Session methods** ✅ — `session/list` dispatch, `set_config_option`
  modeling, and resume (`session/load`) coverage are done (see *Shipped
  (session methods)* above). Remaining additive follow-ups: grouped select
  options, `SessionInfo.additionalDirectories`, and `_meta` passthrough.
- **Producers for the rich blocks** ✅ — the `diff` producer ships (edit/write →
  `event.FileEdit` → `diff` content block). `terminal` (and `image`/`audio`/
  `resource`) stay dormant until a builtin tool naturally produces them; none
  does today, so no producer was invented.
- **Model discovery types** ✅ — `provider.ModelLister` and the vendor metadata
  it carries on `ModelInfo` back gofer's native list-models endpoint, which
  feeds the `session/new` model picker (see *Live model listing* above). Migrate
  to `providers/list` only once that spec surface stabilizes.
- **Capability modeling for the stretch set** — `session_info_update` **ships**:
  `session.Session` carries an embedder-set `title` (`SetTitle`/`Title`), a
  `SetTitle` change emits the must-deliver `session.info` event, and
  `ToSessionUpdate` projects it to a `session_info_update`. Title *generation*
  stays in the embedder (gofer). `plan` **ships**: the `update_plan` builtin tool
  lets the model publish its full task plan; the loop bridges the tool result to
  a must-deliver `plan` event (`event.PlanUpdated`), and `ToSessionUpdate`
  projects it to a `plan` update. `config_option_update` **ships** (the outbound
  half): the embedder emits a must-deliver `session.config` event
  (`event.ConfigOptionsUpdated`) carrying its full config-option snapshot — WHICH
  options exist (that "model" is a selector, its values) is the embedder's
  business logic (gofer) — and `ToSessionUpdate` re-erects it into a
  `config_option_update` reusing the Slice-3 `ConfigOption` shape. Still stretch:
  the `available_commands_update`/`current_mode_update` registries — modeled as
  they acquire a stable spec surface and a producer.

## Session tree & spawn seam

A subagent is a real session, not a sub-object: its own UUIDv7 journal, linked
to its parent. **Shipped (M5).** `Runner.Spawn(ctx, Options) (*Runner, error)`
is the whole seam — the SDK links, caps, and announces; it does not supervise.

- **Linking.** A child's root meta entry carries `parent_id` and `depth`
  (`session.WithMetaParent`), both omitempty so a root session's metadata — and
  every journal written before the fields existed — is byte-identical to before.
  `session.MetaOf(entries)` decodes that entry off a `session.ReadEntries` slice,
  so a roster classifies a disk session (root or child, and how deep) **without
  resuming or folding it**. `Resume` restores both onto the Runner
  (`ParentID()`/`Depth()`), so lineage survives a daemon restart rather than
  resetting to depth 0.
- **Depth cap.** `runner.DefaultMaxDepth` = 5, overridable per-Runner via
  `Options.MaxDepth` and inherited by a spawned child, so one value governs a
  whole chain. Enforced *before* anything is created: an over-cap spawn returns
  an error matching `errors.Is(err, runner.ErrMaxDepth)` and leaves no orphan
  journal on disk. `Depth()` is exported so an embedder enforcing its own
  delegation policy reads the depth before it asks, instead of discovering the
  cap by attempting a spawn.
- **Announcement.** The parent's stream carries the must-deliver
  `session.spawned{child_id, agent?, depth}` (`event.SessionSpawned`, whose
  `session_id` is the **parent** — that is the stream a roster already watches).
  Must-deliver because nothing reconciles a dropped spawn: no later event
  re-announces the child, so the drop would leave it live and invisible. `agent`
  is omitempty; `depth` is always present, since 0 is a meaningful depth.
- **Authority.** `Spawn` overwrites `Options.ParentID`/`Depth` with its own id
  and depth+1 — the parent is authoritative, or a child could name its way
  around the cap.
- **Store ownership.** A child spawned without its own `Options.Store` shares the
  parent's store and does not own it, so `child.Close()` never closes a store the
  parent is still using.

The durable record of lineage is the **child's** meta entry, not a parent-journal
entry: the parent's journal is a turn-structured transcript whose assistant
`tool_use` blocks and their `tool_result` round must not be interleaved, and an
agent-initiated spawn lands squarely in that window. The must-deliver event
covers the live view; `ReadEntries` + `MetaOf` covers the after-restart view.

The application wires this to its supervisor/roster (tree view, peek/attach into
any child) and owns the child's lifecycle — the caller must `Close()` the
returned Runner. Closing a parent does not close its children.

**Tool-call agent attribution.** Tool events
(`tool.call.started`/`delta`/`finished`) nest under the run span by call `id`,
and the instrumentation seam (*The Event/Op stream is the span/metric source*)
adds no `turn_id` on purpose. Each carries an optional `agent` field (omitempty,
so an un-attributed call is wire-identical to before the field existed), stamped
at emit time from `loop.Config.Agent` — surfaced end-to-end through
`runner.Options.Agent` and `compose.LoopDeps.Agent`. With the spawn seam shipped
this composes: the spawner sets the child's `Options.Agent`, so every tool call
that child runs is already attributed, and the `session.spawned{agent}` notice
tells a consumer which agent the child is before its first tool call — no
out-of-band correlation across the tree. The SDK still invents no id: an
un-attributed spawn stays un-attributed, and absent id ⇒ un-attributed
rendering.

## Checkpoint & rewind seam (M5)

Checkpoint/rewind is **not** a new subsystem: it is the journal's existing
fork/fold machinery pointed at undo, surfaced on `*runner.Runner` so a consumer
never has to reach past the contract into `session.Journal`. The boundary
decision (no `Task`/`TaskHandle`/`TaskStore`, task id == session id) is settled
in the [PRD](PRD.md) and argued in
[`proposals/checkpoint-task-handle-seam.md`](proposals/checkpoint-task-handle-seam.md).

**Rewind is additive — the journal is never truncated.** `Runner.Fork(at)` and
`Runner.Rewind(ref)` append a `fork_point` parented on the target entry and move
HEAD there. Nothing is deleted, shortened, or rewritten: the abandoned tail
stays in the log, stays readable on disk, and **still counts toward
`Runner.Cost()`** — the provider was already paid for those calls. Only the
folded context changes. This falls out of the two properties the whole journal
design rests on: append-only auditability (every model call stays
reconstructable) and torn-write recovery, which repairs a crash by dropping a
bad *final* line and so cannot survive in-place edits of earlier entries. A
destructive `Truncate` was considered and rejected.

Parent links are only a *tree* because the writer made them one, and a session
loads entries this process did not write — a concatenated or hand-edited file can
close a link into a cycle. Every derived value (`Fold`, `LastUsage`) walks those
links, so `FileStore.Open` refuses a cyclic chain with `ErrCorruptJournal` rather
than letting each walker survive it independently. Refusing to open beats folding a
partial context, which would prompt the model with something the journal does not
say and signal nothing. `ReadEntries` stays permissive on purpose — it scans
metadata linearly and never follows a link, so it must not lose a session over
corruption it cannot reach. The check covers the chain *as loaded*, so both walkers
also cap at `len(entries)` steps, which is what keeps an in-process journal and a
`Fork` onto an unreachable cyclic branch bounded.

Both publish the must-deliver `session.forked{at?, label?}` — the event's first
producer — so every client sees a rewind the way it sees a resume, and knows
*where* the branch landed. Both drain the runner's `awaitJournaled` barrier
first, so a fork immediately after a turn is parented on that turn's last entry
rather than a stale HEAD; forking while a `Prompt` is still in flight remains a
caller-side precondition (the run would go on appending onto the new branch).

**A checkpoint is a named marker in the log, not a sidecar.** `EntryCheckpoint`
carries a label and contributes nothing to `Journal.Fold` — like `fork_point`
and `session_meta`, and explicitly cased in `renderContext` rather than left to
fall through, so a label can never enter the model's context. Keeping the name
in the append-only log is the point: a consumer-side sidecar can desync from the
entries it addresses. `Runner.Rewind` resolves a label to its entry id, falling
back to treating the ref as a raw entry id; with duplicate labels the **most
recent in append order wins**, which makes a label usable as a moving bookmark.

**Session role.** `MetaPayload.Role` is an optional, omitempty, embedder-owned
string on the root `session_meta` entry (`runner.Options.Role` → `Runner.Role()`,
restored by `Resume` from the journal). It lets `Store.List`/`ReadEntries`
classify a session — a monitor, say — for a roster **without folding or
resuming**. As with `Options.Agent` and the session title, the SDK defines no
vocabulary and attaches no behavior: it carries the value, the embedder owns its
meaning.

## Compaction seam (M7)

The journal already modeled compaction's *result* — `session.EntryCompaction`
and `event.KindSessionCompacted` shipped in v0.17.0, and `Journal.Fold` already
stopped walking ancestors at a compaction entry. What was missing was a way to
CAUSE one (agent-sdk-go#89): `NewSessionCompacted` had zero call sites. This
seam is exactly that trigger, plus the accounting an embedder needs to decide
when to pull it — nothing about `Fold` changed.

**`Runner.Compact(ctx, instructions) error`** is an explicit method, not a
threshold or a loop hook — the same shape as `Checkpoint`/`Fork`/`Rewind`: it
drains `awaitJournaled` first, then appends a `session.EntryCompaction`
parented on the current HEAD and publishes a must-deliver `session.compacted`.
It is additive, like `Fork` — nothing is deleted, and the compacted entries
still count toward `Runner.Cost`.

**The SDK ships no compaction policy.** There is no size threshold and no
automatic trigger wired into `Prompt` or the loop. An embedder decides WHEN —
reading `Runner.LastUsage` or a live `turn.finished` event to judge pressure,
or simply honoring an explicit user command — and calls `Compact` between
turns. Because `Compact` always operates on whatever HEAD is at the moment
it's called, compacting right before the next `Prompt` (sized against the last
known usage) and compacting right after one returns (sized against that turn's
just-settled usage) are equally correct; there is no separate code path for
either, so the choice costs the caller nothing either way.

**Rejected: compaction as a fifth loop hook.** The loop's `Hooks` family
(`BeforeTool`/`AfterTool`/`TransformContext`/`PrepareNextTurn`) was considered
as the home for this instead of a `Runner` method — `TransformContext` already
rewrites the message list before each model call, and folding compaction in
there would make it automatic and embedder-overridable by construction.
Rejected because `TransformContext` rewrites the in-flight list for ONE call
only: it is never journaled, does not survive `Resume`, and is not observable
as a `session.compacted` event without handing the loop direct journal/broker
access that today only `Runner`'s synchronous append path has (guarded by the
`awaitJournaled` barrier that keeps a `Prompt`'s own entries from reordering
against it). Wiring compaction through it would reintroduce exactly that
ordering hazard for a marginal composability gain the `Summarizer` interface
already buys another way (below). An explicit method needed no new machinery
and matches every other structural seam this package ships.

**Summarization is a seam, not a hardcoded prompt.** `instructions` (or
`DefaultCompactionInstructions`, an exported constant — never a string
buried in the loop) is threaded through `Options.Summarizer`
(`runner.Summarizer`), which an embedder replaces wholesale — a different
model, a custom prompt template, an external service, or a deterministic
non-LLM condenser for tests — not just the instructions text. The default
(`defaultSummarizer`) makes one non-tool completion call against the runner's
own bound provider and reports back `SummarizeResult{Summary, Model, Usage}`;
Compact journals `Usage` on the compaction entry itself (via
`WithEntryModel`/`WithEntryUsage`, now accepted by `NewCompactionEntry`) so a
compaction's own cost folds into `Runner.Cost` like any other turn instead of
being invisible, and republishes it on `event.SessionCompacted` for a live
renderer.

**Accounting reused, not duplicated.** `Runner.Cost`/`session.CostReport`
(cumulative, every branch) and a live `event.TurnFinished{Usage,
ContextWindow}` (per-turn, already `ContextWindow`-aware via
`provider.Lookup`) already covered the common case — an embedder that stays
subscribed already has everything a `/context` view or a pressure threshold
needs, the same figures its existing usage/stats panel already tracks. The one
gap: a session with no live turn yet in THIS process (freshly `Resume`d,
before any new `Prompt`) has nothing to read. `Journal.LastUsage` (and
`Runner.LastUsage`, its thin wrapper) close it: the model and usage of the
most recent turn-bearing entry in the CURRENT folded chain — the same chain
`Fold` walks (HEAD back to root, stopping at an inclusive compaction
boundary), so both agree on what "current context" means. `Fold` materializes
that chain through `chainFromHead`; `LastUsage` walks it in place under the
journal lock, over the journal's own id index — allocation-free, and
exiting at the first entry that carries usage rather than building the whole
chain to inspect its head. Pair the returned model with `provider.Lookup` for a
context-window size, exactly as a live `turn.finished`'s `ContextWindow` is
derived.

**`event.SessionCompacted`'s payload** is designed for a renderer, not just an
audit log: `ReplacesThrough` (the boundary), `MessagesCompacted` (how many
provider messages `Fold` held immediately before — "12 messages summarized"
without walking the journal), `Model`/`Usage` (the summarization call's own
footprint — `Usage.InputTokens` approximates the pre-compaction context's
size, since the provider tokenized exactly that content to answer the call;
`Usage.OutputTokens` approximates the summary's own size — a before/after pair
with no separate tokenizer dependency), and `Summary` itself, so a client
renders what happened without a second read of the journal. `Usage` is always
present on the wire (like `TurnFinished`'s); the rest are omitempty, so a
strategy that made no model call still marshals a valid, mostly-empty event.

`event.SessionCompact` (the `session.compact` op) gained an optional
`Instructions` field mirroring the method signature — additive, omitempty, so
an existing bare `{session_id}` request is unaffected.

## Provider error classification

**`provider.ErrContextOverflow` is a contract; provider message text is not.**
The sentinel reports one thing: a request was REJECTED because the prompt
exceeded the model's context window. Callers branch with `errors.Is`. It is
distinct from `StopMaxTokens` (a turn that ran and hit its *output* cap — a
success), from a rate limit, from Anthropic's `request_too_large` (a request
*byte size* limit, not remedied by shortening history), and from a transport
failure. None of those classify.

It exists because the compaction seam above ships **no automatic trigger**: an
embedder judges pressure from settled usage. That can't see a single-turn
overshoot — a rejected call generates nothing and therefore reports *no usage
at all*, so a usage threshold never fires and the session wedges
(`jedwards1230/gofer#279`). The rejection error is the only signal, so it has
to be classified rather than string-matched by every consumer.

**Each adapter normalizes its own vendor signal**, and vendor asymmetry is
absorbed there rather than exported. Every adapter error type implements
`Is(target error) bool` over its *exported fields* — a pure function, so a value
a consumer hand-builds in its own test classifies exactly as a live response
does — and the rule is structured-signal-first:

- **OpenAI** names the failure discretely. `openai.APIError` now parses the
  `{"error":{…}}` envelope into `Type`/`Code`/`Param` (`Body` still retained),
  and both it and `openai.StreamError` recognize `context_length_exceeded` plus
  the `context_window_exceeded` that gateways re-emitting the envelope
  (LiteLLM, OpenRouter — reachable through `WithBaseURL`) send instead. An
  *unrecognized* code is not a verdict: it may simply have been rewritten, so
  the prose fallback still runs.
- **Anthropic ships no discrete code**: overflow is a plain
  `invalid_request_error` with the detail only in the message. So
  `anthropic.Error` gates on type first, then matches a narrow phrase list
  (`prompt is too long`, `exceed context limit`, `context window`).

Both adapters check each structured field **only when it is present**, and as
separate conditions. Neither is guaranteed — status is 0 on a mid-stream SSE
error frame, and type is empty whenever the envelope was rewritten or truncated
past the read cap — so demanding either outright false-negatives a real
overflow, while collapsing the two into one `status != 400 && type != …`
condition passes whenever *either* matches and lets a 5xx quoting overflow
prose through. Both spellings are natural and both are wrong; the tests pin
each direction.

Text matching is a fallback confined to the adapter, never a caller's job.
Where the two directions conflict the adapters resolve toward the false
positive: a missed overflow wedges the session, while a spurious one costs a
single bounded compact-and-retry.

The error propagates **unwrapped** through `loop.Run` and `runner.Prompt`, so
`errors.Is` works on what a consumer holds; a `loop` regression test pins that.
`faux.Turn.Err` scripts a pre-stream rejection, so the whole branch is testable
with no network:

```go
res, err := loop.Run(ctx, cfg, msgs)
if errors.Is(err, provider.ErrContextOverflow) {
    msgs = shrink(msgs)               // embedder policy: summarize or drop history
    res, err = loop.Run(ctx, cfg, msgs)
}
```

On the `Runner` path the retry is not this symmetric: `Runner.Prompt` journals
the user message *before* the model call, so an overflow leaves that message
already in the journal and re-prompting the same text double-appends it —
compact, then continue from the existing HEAD rather than re-sending.

No new `StreamEventType`, `StopReason`, or `event/` field: the returned `error`
is what a compact-and-retry branch reads, and each of those is a separate
contract addition with its own cost. There is deliberately no
`provider.IsContextOverflow` helper either — `errors.Is` is already the ask.

## MCP (M7)

`mcp/` is an **optional SDK package**: a hand-rolled JSON-RPC 2.0 client
(stdio and streamable-HTTP transports) plus the projection of a connected
server's tools onto `[]tool.Tool`. **Ratified 2026-07-29: hand-write the
client, don't import one** — this overrides the 2026-07-11 sourcing survey's
"adopt `modelcontextprotocol/go-sdk`" verdict. `lsp/client.go` already proved
the pattern (a stdlib-only JSON-RPC-over-stdio client is a few hundred lines,
not a dependency-worthy amount of code), MCP is the same protocol family, and
because the optional-package tier controls *compilation* rather than the
*module graph*, an adopted dependency would land in every embedder's
`go.sum`/`go list -m all` even when `mcp/` is never imported. Scope is
deliberately narrow: the client and the tool projection, nothing else —
server configuration, credential resolution, the connection manager
(reconnect/backoff/readiness), and any tool-index decorator are the consuming
application's job.

**Transports.** Stdio (`mcp.Start`, `os/exec`) frames each JSON-RPC message as
one newline-delimited line — MCP's stdio framing, distinct from LSP's
Content-Length headers, so `mcp/`'s wire layer is a parallel hand-roll, not a
shared one. Streamable-HTTP (`mcp.NewHTTP`, `net/http`) POSTs each request to
a single endpoint and accepts either a plain JSON response or an SSE stream
(read until the JSON-RPC message whose id matches the request arrives;
anything else on the stream — a progress notification — is skipped, since
this package models no notification consumer). Both transports sit behind one
unexported `transport` interface so `Client`'s lifecycle logic (id assignment,
JSON-RPC envelope construction, error propagation) is written once.

**Lifecycle.** `Client.Initialize` performs the "initialize" handshake plus
the required `notifications/initialized` notification. `Client.ListTools`
paginates `tools/list`'s cursor internally, aggregating every page.
`Client.CallTool` performs one `tools/call`; its `CallToolResult.IsError`
mirrors the MCP result's own `isError` field — a tool that ran and reported
failure, still a successful round trip — while a non-nil `error` return means
the round trip itself failed (unreachable, timed out, malformed, or a
JSON-RPC-level error).

**`tools/list` already returns full schemas — index-first is a context
projection, not a network saving.** There is no MCP affordance for fetching a
tool's name without its schema: one response carries both. So an index-first
tool registry (schemas fetched only for a tool the model actually names) can
only ever be something an application builds AFTER `Project` returns, over
the tool set already fully loaded into memory — never a lazy per-tool fetch
against the wire. Do not build the latter; it does not exist to build against.

**Projection: `Project(ctx, *Client, server string) ([]tool.Tool, error)`.**
Each returned tool wraps the same `*Client` and its own original (unsanitized)
name; `Run` performs exactly one `tools/call`. This is the load-bearing
output: an MCP tool and a builtin tool become the same Go type in the same
`tool.Registry`, so permission gating, the tool-index decorator, and
diagnostics all apply to both structurally, never by convention. A tool's
input schema is converted from the server's JSON Schema to `tool.Schema` via
a best-effort recursive projection (object/array nesting, `enum`, `default`,
top-level `required`); JSON Schema constructs `tool.Schema` has no
representation for (`oneOf`/`anyOf`, `patternProperties`,
`additionalProperties`, per-nested-object `required`) are dropped, not
rejected — the same limitation every builtin tool's schema already lives
with, not a new one this package introduces.

**Naming and sanitization (satisfies the permission grammar above).** A
projected tool is named `mcp__<server>__<tool>`, matching the
`mcp__search__*(*)` form the permission rule grammar documents. Sanitization
is mandatory: providers cap tool names at 64 bytes of `[A-Za-z0-9_-]`, and a
real federated name can exceed that (`mcp__home-assistant__ha_config_list_dashboard_resources`
is already 54 characters). `qualifiedToolName`:

1. Sanitizes `server` and `tool` independently — every rune outside
   `[A-Za-z0-9_-]` becomes `_` — so the result is pure ASCII and a later
   byte-length truncation can never split a multi-byte rune.
2. Joins them as `mcp__<server>__<tool>`.
3. If that exceeds 64 bytes: truncates to the first 57 bytes, appends `_`,
   then the first 6 hex characters of the sha256 of the **full** (untruncated)
   sanitized name — not just the surviving prefix. Two names that agree on
   everything up to the cut but differ only past it therefore still land on
   distinct qualified names, deterministically and without a collision table.

The original, unsanitized name stays available (`projectedTool.OriginalName`)
for display/audit; it is also what `tools/call` is actually sent with — only
the model- and permission-facing name is sanitized. An unsanitized or
over-long name is a provider 400 that kills the entire request, not just the
offending tool, which is why sanitization is mandatory rather than defensive
polish.

**Resilience is this client's contract; the connection manager is gofer's
job.** A dead, slow, or unreachable server must never fail a whole turn:
`projectedTool.Run` maps a `CallTool` failure to a `tool.Result{IsError:
true}` carrying a message the model can react to (e.g. "mcp server X tool Y
failed: ..."), never to a Go `error` — *unless* the ctx `Run` itself was given
is what ended the call (checked via `ctx.Err()` on the ORIGINAL ctx, after
`CallTool` returns its own internally-timeout-derived error), in which case it
propagates as a real error and the loop aborts the turn, exactly like any
other tool's ctx cancellation. `Client`'s per-server connect timeout
(`WithConnectTimeout`, default `DefaultConnectTimeout` = 10s, applied to
`Initialize`) and per-call timeout (`WithCallTimeout`, default
`DefaultCallTimeout` = 60s, applied to `ListTools`/`CallTool`) are what
produce that internal, non-propagating failure for a hung server — both are
configurable, never hardcoded, and compose with a caller-supplied ctx
deadline via `context.WithTimeout` (whichever is sooner wins). Because a
`projectedTool` always returns SOME `tool.Result` — it never becomes
"invalid" — a caller never needs to unregister it when its server misbehaves:
the same `[]tool.Tool` a session started with stays the same array all
session long, which matters because mutating a session's tool array
mid-session silently breaks prompt caching.

**Hard invariant: a session's tool set is fixed at create — this package
never mutates one, and neither may the connection manager built on it.**
`Project` is a one-shot snapshot; nothing here watches a server or re-projects
on reconnect. The consuming application's connection manager must preserve
that all the way up: a server that finishes connecting *after* a session is
already running joins the NEXT session, never a live one, and a server that
dies mid-session keeps its already-projected tools registered (degrading to
`IsError` per above, and working again on reconnect with no re-registration).
This is load-bearing, not stylistic: a resident-tool-index decorator built
over a session's registry (`toolindex`, see `docs/milestones/M7-round-ab.md`)
snapshots that registry once at `Wrap` time and stakes a prompt-cache
byte-identity guarantee on the tool array never growing afterward —
hot-adding a late-connecting server's tools into a live session would break
that guarantee silently, with nothing in either package's test suite to catch
it.
## Index-first tool registry (M7, `toolindex/`)

Federating many tool sources (MCP servers, plugins, builtins) into one
`req.Tools = r.cfg.Tools.Specs()` call must not mean every schema rides every
model call — that is the "context transparency" tenet applied to tool
surfaces. `toolindex.Index` is the seam: a decorator satisfying `loop.
ToolRegistry` (`Get` + `Specs`), so it drops straight into `Config.Tools` with
zero loop changes. `tool.Tool` and `tool.Registry` are untouched; the decorator
never even sees a `tool.Tool`, only the `provider.ToolSpec`s the wrapped
registry's `Specs()` already returns — uniformity (builtin vs MCP vs plugin)
is structural, not conventional, because the decorator has no way to tell
them apart.

**Two-phase construction.** `toolindex.New(opts)` builds the Index; `Index.
SearchTool()` returns a `tool.Tool` for `tool_search` that the caller registers
into the base `tool.Registry` **before** `Index.Wrap(base)` snapshots that
registry's `Specs()` into the index's entries — otherwise `tool_search` itself
would be unknown to the base. `Wrap` also fixes the always-resident name set
(`Options.Resident`, plus `tool_search` itself forced resident — the discovery
mechanism can never go dark) and panics on a second call, matching `tool.
NewRegistry`'s construction-time-error contract.

**`Specs()` ordering is the cache-safety property.** It returns resident
(sorted) ++ promoted (promotion order) — appended, never merged into sorted
order — so indices `[0..len(resident)-1]` stay byte-identical across a
promotion; a provider's longest-cached-prefix match only has to reprice the
promoted tail. Residency is monotonic: `Options.Resident` is fixed at `Wrap`,
and the promoted set only grows.

**`Get(name)`** delegates to the base for any name it knows, auto-promoting a
non-resident hit — a model that already guessed a valid tool name is served,
not punished for skipping search. Promotion is otherwise driven by
`tool_search`'s `Run`, which batches its whole result set through one
`Promote` call (N discoveries, one `Specs()`-tail rewrite) and states in its
result text that those tools' schemas resolve starting next turn — never the
schemas themselves, which would double-bill the tokens this package exists to
save. `Rehydrate` gives a session-resume path the same monotonic promotion
without replaying a search.

**`Hint()` is two-tiered and returns a string, never injects.** At or below
`Options.InlineMax` entries it inlines the whole `name — summary` index; above
it, a per-`Source` roster (count + sample names) plus the `tool_search`
instruction — a flat index over hundreds of federated tools runs to
thousands of tokens on its own, so the roster tier keeps `Hint` itself small
regardless of federated surface size. The embedder composes, replaces, or
drops the returned string; that is how the "nothing enters context the
embedder can't see and override" tenet is upheld here.

**Deferred, by ratified scope.** No auditability layer (`turn.started.
Tools`/`ToolsetDigest`/`session.toolset`) yet — the toggle must work before the
public surface expands to support it; that is a follow-on PR.

## Device keys & sealed envelopes (device/)

`device` is types only: an X25519 `PublicKey`/`KeyPair`, the opaque `Sealed`
envelope, and the `Seal`/`Open` pair. It opens no socket, fetches no key, and
models no account, roster, or pairing flow. The private half is unexported and
never marshalled — persist `PrivateBytes()` and rebuild with
`KeyPairFromPrivate`.

**Construction: RFC 9180 HPKE in `mode_auth`.** Ciphersuite
DHKEM(X25519, HKDF-SHA256) / HKDF-SHA256 / ChaCha20Poly1305, single-shot at
sequence 0, implemented in `internal/hpke` and checked against the CFRG
vectors — that vector suite, not the envelope tests, is the standing
cryptographic gate, because a mutated KEM derivation leaves the wire bytes
identical. The sender mints a **fresh ephemeral X25519 keypair per envelope**
(`Sealed.Ephemeral` is its public half); DHKEM's `AuthEncap` derives the shared
secret from `DH(skE, pkR) || DH(skS, pkR)`. The long-term identity key
therefore *authenticates* rather than contributing the secret: the ephemeral is
bound to the identity by that second DH, not by a signature. That is what lets
the identity key stay X25519 — X25519 cannot sign — and it makes sender
authentication implicit and deniable.

**What is bound.** The HPKE `info` is `agent-sdk-go/device/sealed/v<version>`,
so a future v2 cannot key-collide with v1 on the same suite. The AEAD's
associated data is `I2OSP(version,1) || Sender || Ephemeral`; `Sender` and
`Ephemeral` are already implicit in the KEM (substituting either derives a
different secret), so the AAD's real work is covering the version byte, which
the KEM never sees. Nothing else about an envelope is authenticated because
there is nothing else — **metadata that must be authenticated belongs inside
the plaintext**. An attacker may still reorder, drop, replay, or duplicate
envelopes; only the caller's in-plaintext sequencing detects it.

**Forward secrecy is one-sided, deliberately.** Compromise of the *sender's*
long-term private key reveals nothing historical — the per-envelope ephemeral
private key never left the sealing process. Compromise of the *recipient's*
still does, since the recipient contributes a static key to both DHs. Closing
that half needs a recipient-contributed ephemeral — an interactive handshake or
published one-time prekeys — which needs transport and application state a
one-shot stateless envelope has nowhere to keep. Known and accepted, not an
oversight.

**Versioning fails CLOSED**, the deliberate opposite of `announce`'s open-set
enums. `Seal` stamps `SealedVersion`; `Open` and `UnmarshalJSON` reject
anything else with `ErrUnsupportedVersion`. An unrecognized `EndpointKind` is a
path you skip, but an unknown envelope version is an unknown *construction*,
and processing it under v1's rules — or relaying it as understood — is exactly
the downgrade this rejects. `MarshalJSON` emits `s.Version` verbatim rather
than substituting `SealedVersion`, so a hand-built envelope cannot be laundered
into looking current.

**`Open` is not a decryption oracle.** Every cryptographic failure — bad
encapsulation, wrong sender key, forged tag — returns exactly `ErrOpen`,
unwrapped and undifferentiated, so a caller learns one bit and nothing more.
Only structural problems checked *before* any decryption (unsupported version,
zero recipient, zero sender) are reported distinctly. `internal/hpke.OpenAuth`
draws the same line at input-length validation: `ErrKeySize` for a caller-side
length bug, bare `ErrOpen` for everything after it.

**Trust is TOFU, and the pin store is the embedder's.** A successful `Open`
proves the envelope was sealed by the holder of `Sealed.Sender`'s private key;
it proves nothing about whether that key is trusted, so the recipient must
check `Sender` against its own authorized set. `PublicKey.ID` — the first 8
bytes of SHA-256(key), hex — is the pin handle: derived from the key alone,
stable, carrying no key material, safe in a log line or a confirmation prompt.
It is an identifier, never the check; 64 bits collide on purpose, so
authentication compares full keys. `ErrPinnedKeyMismatch` is a sentinel this
package never returns — it exists so an embedder can report "this is not the
device you paired with" distinctly from a generic auth failure. Where pins
live, how long they last, and when a re-pair is permitted are application state
and fail the membership test: no pin store, roster, account type, or comparison
helper ships here.

## Announce vocabulary (announce/)

`announce.Payload` is the one type a server publishes to describe itself:
identity (server id, account id, display name), candidate endpoints, coarse
session summaries, an optional opaque credential, a **required**
`device.PublicKey`, and a capability `Scope`. Types only — the package imports
no `net`, opens no socket, and knows nothing about rosters or fleets. Per the two-gate test, describing yourself is
vocabulary a second SDK-based app needs unchanged; the mDNS browser, rendezvous
client, device-code flow, and candidate racing are plumbing that lives in a
separate module.

**Endpoints are a list, not an address** — that is the load-bearing choice. A
server is reachable by several paths at once and which works depends on where
the client stands, so reachability is an ordered candidate list (lowest
`Priority` first, SRV-style) with a string-backed **open** `EndpointKind`. A
relay path arrives later as one more element, not a schema change; an
unrecognized kind round-trips instead of failing to decode. `Endpoint.Address`
is opaque: never parsed, resolved, validated, or dialed here.

`Scope` has **no safe zero value** — the empty scope is rejected by `Validate`
and `CanDrive()` is true only for `ScopeDriver` exactly, so a dropped or
unrecognized scope can never read as drive access. The field ships now because
adding it later is breaking, even though enforcement is the application's and
gets refined over time.

**`DeviceKey` is required**; `Validate` rejects the zero key. End-to-end
encryption is a hard requirement here, so a keyless server is not a degraded
server but one that cannot take part — accepting it opens a silent downgrade
path where a client holds a validated payload for a peer it can never seal an
envelope to. This does not touch `device.PublicKey`: the zero key stays
representable and still parses (a fixed-size array has no "absent" state, and
`ParsePublicKey` must accept a well-formed all-zero encoding), it is simply no
longer valid *in a payload* — the same parse-versus-validity split
`PublicKey.Valid` documents, applied one level up. Test presence with `Valid`,
never against the encoded form, which is an all-zero base64 string rather than
an empty one.

`Credential` is a slot the SDK carries and never fetches, refreshes, validates,
or interprets — and it is a struct with an unexported `*string`, not a bare
string, because a bearer secret inside a `Payload` is one careless format verb
from a log line. `Reveal()` is the only read, so every legitimate use is
greppable. `Format` is what makes the redaction total rather than partial: fmt
consults a `fmt.Formatter` for *every* verb but a `fmt.Stringer` only for
`%v %s %q %x %X`, and before `Format` existed `%d` on a struct holding a
credential printed the secret in clear. The boundary, exactly:

- Reached through an **exported** path — the value itself, a pointer, a slice
  or map, an exported field, the verb-less `fmt.Print` family, the implicit
  `%v` in `fmt.Errorf` — no verb prints the secret.
- Reached through another struct's **unexported** field, fmt never calls
  `Format`/`String`/`GoString` at all (`CanInterface` is false) and falls
  through to reflection, which does not care that `Credential`'s own field is
  unexported either. That is why the field is a `*string`: fmt renders a
  pointer at depth > 0 as an address and never follows it. The redaction
  methods are bypassed on this path; the secret still does not print.
- **Serialization carries the real value, on purpose** — both codecs, because
  the credential's whole job is to ride the wire. That includes structured
  logging: `slog`'s JSON handler resolves through `MarshalJSON`, not fmt, so it
  emits the credential in clear. Log a `ServerID`, not a payload; treat an
  encoded payload as secret-bearing.

`Credential` is also deliberately **non-comparable** (a zero-width `[0]func()`
field): with a pointer inside, `==` would compare addresses rather than
secrets, and a compile error beats a silent wrong answer in a security type —
compare via `Reveal`, or `reflect.DeepEqual`. `NewCredential("")` returns the
zero `Credential`, so "" and "no credential" have exactly one representation
and a decoded payload stays `DeepEqual` to the one it was encoded from.

**Not in the compose manifest.** A manifest is static configuration an embedder
*declares*; an announce payload is runtime state a server *emits* (endpoints
depend on discovered interfaces, summaries change every turn, the credential is
minted). There is also no consumer to wire it to. If a server later needs to be
*told* its stable identity or advertised endpoints, that is a small manifest
block naming those inputs — added then, against a real consumer.

Additive: no new `event.Event` kind, no change to the Event/Op contract.

## Extension tiers

Three tiers, by trust and coupling:

1. **Core** — hot path, security, or contract; compiled in (loop, broker,
   permission engine, session).
2. **Optional SDK package** — opt-in at compile time; Go compiles only what you
   import (`search/` and `mcp/` — see "MCP (M7)" below — both ship M7; the
   vendor settings loaders are still planned). First-party and trusted, but
   not forced on every embedder.
3. **Subprocess plugin** — third-party, runtime-installed, untrusted; isolated
   over JSON-RPC (host lands M5). Nothing untrusted runs in-process.

The tier is set by the two-gate test: would a second app need it unchanged
(core vs optional package), and could a seam suffice instead of a built-in?

## Component sourcing (survey verdict, 2026-07-11)

| Need | Verdict | Source |
|---|---|---|
| MCP client | ~~adopt~~ **build (superseded 2026-07-29)** | Hand-rolled, following `lsp/client.go`'s stdlib-only JSON-RPC-over-stdio precedent, extended to also cover streamable-HTTP. Overrides this survey row: MCP is the same protocol family the SDK already hand-rolls for LSP, and `modelcontextprotocol/go-sdk` would land in every embedder's module graph (`go.sum`) even unimported — the optional-package tier controls compilation, not the module graph. See "MCP (M7)" below. |
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
