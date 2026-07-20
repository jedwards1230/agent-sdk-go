# agent-sdk-go — PRD & design

> The importable library layer of an agent platform. The application layer
> (daemon, supervisor, TUI) lives in a separate repo and consumes this SDK
> through the typed Event/Op contract. This document is the repo-scoped cut of
> the platform design doc; decisions recorded here are settled unless a dated
> entry supersedes them.
> Companions: [`DESIGN.md`](DESIGN.md) (normative interfaces: loop seam,
> permission grammar, manifest schema, sourcing decisions) and
> [`TESTING.md`](TESTING.md) (test strategy + CI gates).

## Problem

A genuinely hackable agent loop exists mostly in TypeScript; Go implementations
are unimportable monoliths or LLM-app graph frameworks. Nothing importable
combines an owned, auditable loop with a typed multi-client protocol,
structural permissions, and ecosystem compatibility (MCP, SKILL.md, ACP).

## Personas

- **The embedder** (primary for this repo): a Go developer importing the SDK to
  put an agent loop inside their own product. *CI gate: the SDK builds and
  tests green with zero application code present.*
- **The plugin author**: ships a tool/hook from their own repo against a small
  published contract; never touches core.
- **The operator**: served by an application built on this SDK, which consumes
  it through the same contract as every other client.

## Design tenets

1. **Own the loop** — small, injectable model-call (StreamFn), hook seam,
   never-throw callbacks.
2. **Stream from day one** — the loop emits incremental typed events;
   accumulate-then-send walls off ACP and live UIs.
3. **Everything is a client** — one typed Event/Op contract; TUI, ACP,
   headless, HTTP are projections. None is privileged.
4. **Structural permissions** — approvals are protocol messages; enforcement is
   a format-agnostic rule engine over typed rules. Vendor formats (Claude Code
   `settings.json`, native YAML) are import adapters (M5/M6).
5. **Declarative agents, code as escape hatch** — an agent is a manifest
   (provider, tools, permissions, skills, hooks); `compose.Load()` wires it.
6. **Out-of-process extensibility** — plugins are subprocesses over JSON-RPC
   (WASM tier later). Nothing untrusted runs in-process.
7. **No central anything** — plugins install by module path; a marketplace is a
   git-hosted index file.
8. **Contain before you classify** — sandboxed execution auto-allows by
   containment; a reviewer judges only what escapes. Fail closed.
9. **No flagship provider** — Anthropic and OpenAI are peers with full parity
   (streaming, tool calls, reasoning, usage, OAuth + API key). Nothing is
   vendor-only unless the other API genuinely lacks the concept (then degrade
   gracefully, documented). Personal preference is config, never architecture.

## Package architecture

```
provider/    LLM iface · normalized stream · model registry · CredentialSource
providers/   providers.Build — construct a provider from manifest config     (M2)
auth/        OAuth flows · on-disk token store (auth.json, 0600) (M1)
loop/        loop.Run · hooks · StreamFn              (M1)
session/     event-sourced JSONL tree · resume · cost   (M1: journal + resume)
runner/      batteries-included *Runner (provider+tools+broker+loop+journal)  (M2)
permission/  rules · grants · escalation cap          (M3)
tool/        registry + bash/read/edit/write/grep/glob/ls  (M1)
skill/       SKILL.md, two-tier disclosure            (M5)
plugin/      subprocess JSON-RPC host                 (M5)
lsp/         server registry · diagnostics            (M3)
mcp/         client (official go-sdk)                 (M5)
compose/     manifest → wired session
acp/         clean-room Agent Client Protocol adapter, stdlib-only  (M2)
```

Membership test for every addition: *would a second app need it unchanged?*
If not, it belongs in an application built on the SDK.

## The Event/Op contract

Serializable, ordered per session, wire-agnostic (in-process channel, unix
socket, or network — same messages).

**Events** (agent → clients):

| Event | Delivery |
|---|---|
| `session.created / .resumed / .forked / .compacted / .killed / .archived` | must-deliver |
| `session.info{title}` (embedder-set title change) | must-deliver |
| `session.config{options}` (embedder config-option snapshot, e.g. current model) | must-deliver |
| `plan{entries}` (agent task-plan snapshot via `update_plan`) | must-deliver |
| `turn.started` · `turn.finished{stop_reason, usage}` | must-deliver |
| `message.started{kind: text\|reasoning}` | must-deliver |
| `message.delta{kind}` | **lossy tier** |
| `message.finished{content}` | must-deliver — settled text reconciles dropped deltas |
| `tool.call.started{id, name, input}` | must-deliver |
| `tool.call.delta{id}` | **lossy tier** |
| `tool.call.finished{id, result, diagnostics?}` | must-deliver |
| `permission.requested{id, tool, spec, trace}` | must-deliver |
| `permission.resolved{id, verdict, rule?}` | must-deliver |
| `session.error{fatal?}` | must-deliver |

**Ops** (clients → agent): `session.new{agent, cwd, worktree?}` ·
`session.resume` · `session.fork{at}` · `prompt.send{text, attachments?}`
(queues if busy) · `prompt.queue.list / .clear` ·
`permission.reply{id, verdict, remember?}` · `turn.interrupt` ·
`tool.cancel{id}` · `session.compact` · `session.set_model{model}` ·
`session.kill` · `session.archive`.

*Design-ahead (M4/M5):* a `session.set_effort{effort}` op should parallel
`session.set_model` — a turn-boundary reasoning-effort swap validated against
provider capability — and a manifest `params.thinking` block should make effort
declaratively expressible. See DESIGN *Provider parity & credentials*.

**Two-tier broker**: deltas ride a lossy tier (drop under backpressure, drop
counters exposed); terminal events are must-deliver with bounded blocking.
Settled `*.finished` payloads carry authoritative content, so a slow client
converges to the correct state regardless of drops.

## Milestones

| Stage | Ships | Proof |
|---|---|---|
| **M0 · scaffold** ✅ shipped 2026-07-12 | Two repos, Event/Op types, compose skeleton, CI + golden-file harness | `compose.Load()` returns a session that streams a faux provider |
| **M1 · one good session** ✅ shipped 2026-07-12 | Loop + real provider (Anthropic + OpenAI, API-key + subscription OAuth) + builtin tools + JSONL tree + usage/cost accounting | a real coding task end-to-end, streaming, resumable after kill |
| **M2 · the daemon** ✅ shipped 2026-07-13 (v0.2.0) | (application) supervisor + roster + native ACP; SDK ships `acp/` + `runner/` | an ACP client on a phone drives a session on a laptop |
| **M3 · guardrails** ✅ shipped 2026-07-14 (v0.3.0) | Sandbox/containment seam (concrete Seatbelt/bwrap+seccomp backends are an application concern) + approval protocol events + binary containment policy (sandboxable → run contained; else → ask a human) + tool-output spill files + headless exec + LSP | a non-sandboxable tool call raises `permission.requested` and a client's reply gates execution |
| **M4 · ACP v1 featureset expansion** | Cross-repo, matrix-driven ACP surface build-out — this repo owns the modeling + projection half. Session-method projection (`session/list` dispatch, resume, a modeled `set_config_option`) over the already-present `cwd`/`title` on `SessionInfo`; producers for the already-modeled rich blocks (emit `diff` from the edit tools, `terminal`) so a real tool call carries them; native list-models types feeding gofer's `session/new` model picker; capability modeling for the stretch set (`session_info_update`, `plan`, the `*_update` registries). Shipped so far: the projection-safe subset in **v0.6.0**; the `diff` producer, `set_config_option` modeling and `session/list` dispatch in **v0.7.0**; `session_info_update` in **v0.8.0**; `plan` in **v0.9.0**; `config_option_update` in **v0.10.0**; and the model-discovery types (`provider.ModelLister`) in **v0.13.0**. Still open: the `available_commands_update`/`current_mode_update` registries, and the additive follow-ups (grouped select options, `SessionInfo.additionalDirectories`, `_meta`). | gofer emits a `diff` tool-call block from an edit tool and an ACP client renders it |
| M5 · ecosystem | MCP client (tool-search-first index) + skills + plugin-sdk + subprocess host + session tree / subagent spawn seam (tool events gain originating-agent attribution) + vendor settings-import adapters (Claude Code `settings.json`; home TBD) + provider breadth (`openai-compat`, manifest `ModelInfo` overlay) | a plugin from a separate repo adds a tool |
| M6 · auto + polish | Reviewer pipeline, WASM tier, asset import, mDNS pairing | auto mode survives a week of real ops without a bad allow |

### Point releases (post-M3)

Incremental releases cut between milestones. v0.4.0/v0.5.0 supported gofer's M4
(application-layer command views); v0.6.0 opens this repo's M4 by landing its
projection-safe subset. **SDK milestone numbers are independent of gofer's** —
the same cross-repo ACP featureset is gofer's *next* milestone (it has already
shipped through its command-views milestone) but this repo's **M4**, because
M0–M3 are what shipped here.

- **v0.4.0** — ACP `session/new` gains an optional `model` field.
- **v0.5.0** — `Runner.SetModel(model)` swaps a live session's model between
  turns (same-provider only; a different provider starts a new session).
- **v0.6.0** — first M4 (ACP expansion) increment: `usage_update` projection +
  image/audio/resource content blocks + `diff`/`terminal` tool-call content
  modeled and projected (carries #52–#54). Modeling + projection only — no
  producer emits the rich blocks at this point; the `diff` producer lands in
  v0.7.0.
- **v0.7.0** — the `diff` producer (the edit and write tools attach a structured
  before/after `event.FileEdit` to `tool.call.finished`; `ToSessionUpdate`
  projects it to a `diff` tool-call content block, so an edit renders as a real
  diff instead of plain text — `terminal`/`image`/`audio`/`resource` stay
  modeled-but-dormant, since no builtin tool naturally produces them), plus
  `session/set_config_option` modeling and `session/list` dispatch.
- **v0.8.0** — session title seam (`Session.SetTitle`/`Title`, the must-deliver
  `session.info` event) + `session_info_update` projection.
- **v0.9.0** — `update_plan` builtin tool + `plan` session/update projection.
- **v0.10.0** — `config_option_update` outbound projection + `session.config`
  event.
- **v0.11.0** — canonical `event.Unmarshal` decoder; pre-assigned session-id
  seam (`runner.Options.SessionID`).
- **v0.12.0** — the model registry stops being an admission allowlist:
  `Resolve` admits an unregistered id whose backend is inferable from its shape,
  `Lookup`/`CostOf` stay strict and keep their `ok` result.
- **v0.13.0** — `provider.ModelLister`, the optional live model-listing
  capability, with Anthropic and OpenAI adapters; the Codex (OAuth) route needs
  a `client_version` query parameter and serves a differently shaped catalogue.
- **v0.13.1** — `ModelInfo` carries vendor-supplied metadata (`DisplayName`,
  `Hidden`) under the per-field rule on `Unregistered`: a zero-value field means
  unknown, a non-zero one is vendor fact. `Pricing` is the strict exception and
  stays unconditionally zero.
- **v0.14.0** — `Reasoning` is derived from the Codex listing's
  `supported_reasoning_levels`.
- **v0.14.1** — a journal `Append` failure fails closed instead of being
  swallowed: the consumer goroutine records it, `Runner.Prompt` takes-and-clears
  it at each turn boundary, and `Runner.Close` reports whatever residual failure
  no `Prompt` boundary already did.

## Settled decisions

- **License: Apache-2.0** (NOTICE-based attribution survives forks; patent
  grant; matches key dependencies).
- **Build our own loop** — clean-room the *seams* proven elsewhere (injectable
  StreamFn, event-sourced sessions, AsyncIterator-shaped streams,
  `session.Service`-shaped stores); never depend on FSL-licensed code.
- **Sessions are event-sourced JSONL trees** — UUIDv7 entries, fork/branch,
  compaction-as-entry, context = fold(root→head).
- **Usage/cost accounting is P0** (lands M1): normalized per-turn usage
  aggregated per session and per model, priced from a model metadata registry.
- **Subscription OAuth lands M1** (moved up from M3), both vendors: Anthropic
  (`claude setup-token`-style PKCE) and OpenAI (codex/ChatGPT-subscription
  login). API-key auth works day one for both. The ToS gray zone is accepted.
- **Token store**: an on-disk `auth.json` (mode `0600`, per-provider entries
  with refresh-token handling) under a configurable store root. Providers reach
  credentials through the
  `provider.CredentialSource` interface (a static env-var implementation ships
  in provider core); a keychain backend can layer behind the same interface
  later.
- **Fleet/multi-machine is an application concern.** The SDK's only
  accommodations are already tenets: serializable transport-agnostic contract,
  globally-unique session IDs.
- **Promote-if-stable, for the ACP surface (2026-07-17).** A capability projects
  onto the standard ACP surface in `acp/` only when a *stable* spec variant
  exists in the ACP v1 schema; where the spec surface is unstable or absent it
  stays gofer-native (an application-layer `gofer/event` extension), never
  invented into `acp/`. Consequences already settled: `usage_update` is promoted
  (stable → projected in v0.6.0); `set_model` stays gofer-native (its real spec
  surface, `providers/*`, is unstable); `gofer/event` stays native permanently.
  Model discovery uses a gofer-native list-models endpoint for the `session/new`
  picker, migrating to the unstable `providers/list` only once it stabilizes.
  This is the SDK reading of the cross-repo policy; the full conformance matrix
  is tracked internally (spec ↔ SDK ↔ gofer ↔ Agmente).
- **Journals default to on-disk JSONL** (the auditability tenet);
  `session.MemStore` is an explicit embedder opt-in for an ephemeral
  fire-and-forget session — same fold/resume within the process, nothing
  persisted. Code-level custom tools compose additively with builtins via
  `runner.Options.ExtraTools`.

## Non-goals

- No graph/DAG workflow engine — this is an interactive agent loop.
- No hosted service, no central registry, no telemetry.
- No UI in this repo; TUI and supervision live in the consuming application.

**Open question — in-process background-task handle.** A consuming app (gofer)
wants a first-class long-running background-task primitive: persistent,
task-id-keyed, re-attachable across turns. Whether the SDK offers a **task-handle
seam** (task ids + persistence layered atop resumable sessions) or leaves it
purely application-layer atop `runner.Resume` + the JSONL journal is undecided —
recorded, not committed. This is an *in-process* handle: it does **not** reopen
the "no hosted service / central registry" non-goal above, which forecloses a
hosted registry, not an in-process task id.
