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
| ACP protocol | **adopt** | `coder/acp-go-sdk` (Apache-2.0; protocol layer only — the loop is ours) |
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
