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
  provider core, and `auth.Store` (M1) implements the same interface over
  `~/.gofer/auth.json` (mode `0600`, per-provider entries, refresh handling).

## Auth & credentials (M1)

`auth/` owns `~/.gofer/auth.json` (mode `0600`, atomic temp-file+rename) and
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
`~/.gofer/auth.lock`, with a double-check re-read — because refresh tokens rotate
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
            | mcp tool             mcp__contextforge__*(*)
lists      := deny[] > ask[] > allow[]     first match in that order wins
unmatched  ⇒ ask (fail-safe)
compound shell (&&, |, ;) ⇒ dangerous
dangerous  ⇒ grants force-downgraded to exact-match, TTL'd, audited
sources    := embedded defaults < global config < project config < session grants
              (deny from ANY source is un-overridable)
```

Compatible with Claude Code `settings.json` allow/ask/deny — that file
imports directly (the M3 acceptance gate). Grants persist with TTL behind an
anti-escalation cache: a read grant never satisfies a write ask, and
dangerous specs never widen past exact-match.

## Agent manifest (compose/)

```yaml
# ~/.gofer/agents/homelab-ops.yaml
agent: homelab-ops
description: infra work against the k8s homelab
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
    contextforge: { url: https://mcp.example.com, auth: oauth }
  plugins:
    - module: github.com/someone/gofer-plugin-k9s   # subprocess, own repo
lsp: { auto: true }                    # registry auto-detect; per-server overrides
skills: [./skills, ~/.gofer/skills]
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

## LSP (M3)

- **Manager**: embedded server registry (~370 servers, nvim-lspconfig-shaped
  dataset) with lazy per-file-event startup — filetype + root-marker + PATH
  gating, a generic-command blocklist, and failed-lookup retry damping.
- **Diagnostics injected into tool results** (edit/write/view): current-file
  vs project split, errors first, truncated at 10, after a two-phase settle
  debounce (bail-fast 1s, then a 300ms version-stability window).
- Tools: `lsp_diagnostics` · `lsp_references` (grep→LSP hybrid) ·
  `lsp_restart`. One generic prompt line, not per-tool coaching.

## Component sourcing (survey verdict, 2026-07-11)

| Need | Verdict | Source |
|---|---|---|
| MCP client | **adopt** | `modelcontextprotocol/go-sdk` (official) |
| ACP protocol | build | M2 verdict: clean-room the ACP **v1** wire shapes in `acp/` (stdlib-only, no dep) + a pure Event/Op projection; transport (WebSocket/JSON-RPC) lives in gofer, not the SDK. Supersedes the earlier "adopt `coder/acp-go-sdk`" survey verdict — keeping the SDK dependency-free and the projection a first-class broker client won out. |
| WASM plugin tier | **adopt** | `knqyf263/go-plugin` (wazero, typed interfaces) |
| Provider + streaming | build | thin, with a cross-vendor content-block message model |
| Loop + hooks | build | clean-room the proven seams; **FSL-licensed prior art is read-only, never a dependency** |
| Sessions | build | event-sourced JSONL tree behind a pluggable `session.Service`-shaped store interface |
| Permission engine | build | CC-settings-compatible grammar (above) |
| Coding tools | build | confirmed ecosystem gap: nobody ships bash/read/edit/grep as an importable package |
| Skills | build | the cross-tool Agent Skills SKILL.md standard |
| Manifests | build | schema above |

The survey behind these verdicts (six agents read at source level) lives on
the home wiki: *Agent Architecture Matrix*.

## Engineering constraints

- **Platforms**: macOS + Linux first-class (including sandbox backends);
  Windows later, no sandbox v1. Single static binary; `go install` works.
- **Go 1.26**; range-over-func iterators are load-bearing in the event
  stream and per-test stream fakes.
- **Streaming budget**: first provider token reaches an attached client with
  ≤ one frame of added latency; the lossy delta tier exists so a slow client
  can never back-pressure the loop.
- **Observability**: no phone-home, ever. Local structured logs; optional
  OTLP export, off by default.

## Observability seams (SDK stays dependency-light)

The SDK takes **no OpenTelemetry dependency** and emits no telemetry on its own
initiative — instrumentation lives in the embedding app (gofer owns the otel
dep + exporters). What the SDK owes an embedder is *seams*, not an
implementation:

- **Context propagation is already end-to-end.** Every call path — loop,
  provider, `session`, `runner`, tools — threads `context.Context` through
  unbroken (`runner.New`/`Resume`/`Prompt(ctx, …)`, `loop.Run(ctx, …)` down
  through each `callModel`/`runTools` call). An app can therefore open a span on
  a
  turn and have it flow through the provider call and every tool execution
  without the SDK knowing tracing exists. This is an invariant, not an
  aspiration: a new code path that drops `ctx` is a bug.
- **Optional `*slog.Logger` injection where the SDK is otherwise silent.** The
  SDK is silent by default; where SDK-internal diagnostics earn their keep, the
  seam is an optional `*slog.Logger` the embedder passes in (nil ⇒ discard, as
  the daemon already does for its own logger). The SDK never logs unprompted and
  never phones home.
- **The Event/Op stream is the instrumentation source.** The typed two-tier
  stream in `event/` is the natural span/metric source: `*.started`/`*.finished`
  events map to span open/close, `message`/`tool.call` deltas to span events,
  and settled usage/cost to metrics — all in the app, without SDK involvement.
  This is exactly how gofer wraps the stream with OTel spans (gofer `PRD.md`
  Observability); a second embedder would instrument the same seam the same
  way.
