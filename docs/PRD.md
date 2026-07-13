# agent-sdk-go — PRD & design

> Library half of the **gofer** platform. The application half (daemon,
> supervisor, TUI) is [`jedwards1230/gofer`](https://github.com/jedwards1230/gofer).
> This document is the repo-scoped cut of the platform design doc; decisions
> recorded here are settled unless a dated entry supersedes them.
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
- **The operator**: served by gofer, which consumes this SDK through the same
  contract as every other client.

## Design tenets

1. **Own the loop** — small, injectable model-call (StreamFn), hook seam,
   never-throw callbacks.
2. **Stream from day one** — the loop emits incremental typed events;
   accumulate-then-send walls off ACP and live UIs.
3. **Everything is a client** — one typed Event/Op contract; TUI, ACP,
   headless, HTTP are projections. None is privileged.
4. **Structural permissions** — rule engine with allow/ask/deny,
   Claude-Code-settings-compatible grammar; approvals are protocol messages.
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
auth/        OAuth flows · token store (~/.gofer/auth.json) (M1)
loop/        runAgentLoop · hooks · StreamFn          (M1)
session/     event-sourced JSONL tree · resume · cost   (M1: journal + resume)
runner/      batteries-included *Runner (provider+tools+broker+loop+journal)  (M2)
permission/  rules · grants · escalation cap          (M3)
tool/        registry + bash/read/edit/write/grep/glob/ls  (M1)
skill/       SKILL.md, two-tier disclosure            (M4)
plugin/      subprocess JSON-RPC host                 (M4)
lsp/         server registry · diagnostics            (M3)
mcp/         client (official go-sdk)                 (M4)
compose/     manifest → wired session
acp/         clean-room Agent Client Protocol adapter, stdlib-only  (M2)
```

Membership test for every addition: *would a second app need it unchanged?*
If not, it belongs in gofer.

## The Event/Op contract

Serializable, ordered per session, wire-agnostic (in-process channel, unix
socket, or network — same messages).

**Events** (agent → clients):

| Event | Delivery |
|---|---|
| `session.created / .resumed / .forked / .compacted / .killed / .archived` | must-deliver |
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

**Two-tier broker**: deltas ride a lossy tier (drop under backpressure, drop
counters exposed); terminal events are must-deliver with bounded blocking.
Settled `*.finished` payloads carry authoritative content, so a slow client
converges to the correct state regardless of drops.

## Milestones

| Stage | Ships | Proof |
|---|---|---|
| **M0 · scaffold** ✅ shipped 2026-07-12 | Two repos, Event/Op types, compose skeleton, CI + golden-file harness | `compose.Load()` returns a session that streams a faux provider |
| **M1 · one good session** ✅ shipped 2026-07-12 | Loop + real provider (Anthropic + OpenAI, API-key + subscription OAuth) + builtin tools + JSONL tree + usage/cost accounting | a real coding task end-to-end, streaming, resumable after kill |
| **M2 · the daemon** ✅ shipped 2026-07-13 (v0.2.0) | (gofer) supervisor + roster + native ACP; SDK ships `acp/` + `runner/` | an ACP client on a phone drives a session on a laptop |
| M3 · guardrails | Permission engine + approval messages + grants + sandbox + headless exec + LSP | Claude Code `settings.json` imported and honored |
| M4 · ecosystem | MCP client + skills + plugin-sdk + subprocess host | a plugin from a separate repo adds a tool |
| M5 · auto + polish | Reviewer pipeline, WASM tier, asset import, mDNS pairing | auto mode survives a week of real ops without a bad allow |

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
- **Token store**: `~/.gofer/auth.json`, mode `0600`, per-provider entries with
  refresh-token handling. Providers reach credentials through the
  `provider.CredentialSource` interface (a static env-var implementation ships
  in provider core); a keychain backend can layer behind the same interface
  later.
- **Fleet/multi-machine is an application concern.** The SDK's only
  accommodations are already tenets: serializable transport-agnostic contract,
  globally-unique session IDs.

## Non-goals

- No graph/DAG workflow engine — this is an interactive agent loop.
- No hosted service, no central registry, no telemetry.
- No UI in this repo; TUI and supervision live in gofer.
