# agent-sdk-go

An importable, provider-agnostic **agent framework for Go**: an owned, auditable
agent loop with sessions, permissions, tools, skills, MCP, ACP, plugins, and
declarative agent manifests.

> **Status: v0.5.0, M3 shipped.** The typed Event/Op contract, the two-tier
> event broker, real Anthropic/OpenAI providers (API key + subscription
> OAuth), the agent loop, builtin tools, the `runner` package, a clean-room
> `acp` (Agent Client Protocol) adapter, and M3's guardrails (permission
> engine, guard/approval seam, spill files, headless exec, LSP) are all in
> place, plus two point releases (v0.4.0, v0.5.0) letting a client swap a
> session's model mid-flight. Next up is M4, the ecosystem milestone (see the
> [roadmap](#roadmap)).

## Why

Today's coding agents force a trade: genuinely hackable loops exist mostly in
TypeScript ecosystems, while the Go implementations are either unimportable
monoliths (`internal/` everything) or LLM-app graph frameworks — the wrong
shape for an interactive agent. `agent-sdk-go` is the missing piece: a small,
importable loop you can read end-to-end, embed in your own product, and trust
because everything in the model's context went through code you own.

## Design tenets

- **Own the loop** — small, injectable, never-throw callbacks. Trust in what's
  in context is the product.
- **Stream from day one** — the core loop emits incremental typed events;
  accumulate-then-send is a design bug, not a mode.
- **Everything is a client** — one typed Event/Op contract above the loop.
  TUIs, ACP, headless exec, and HTTP are projections; none is privileged.
- **Structural permissions** — deny lives in an engine, not a prompt.
- **Declarative agents, code as escape hatch** — an agent is a manifest;
  `compose.Load()` wires it.
- **Out-of-process extensibility** — plugins are subprocesses over JSON-RPC;
  nothing untrusted runs in your process.

## Quickstart

```go
sess, err := compose.Load(ctx, "agent.yaml") // manifest → wired session
if err != nil { ... }

sub := sess.Subscribe(event.FilterAll)
defer sub.Close()
go func() { _ = sess.Prompt(ctx, "hello") }()

for ev := range sub.C {
    switch e := ev.(type) {
    case event.MessageFinished:
        fmt.Println(e.Content)
    case event.TurnFinished:
        return
    }
}
```

With `provider: faux` in the manifest this runs entirely offline — the same
harness the golden-file tests use. Swap in `provider: anthropic` or
`provider: openai` (API key or subscription OAuth) for a real model; the
`runner` package is the batteries-included alternative to hand-wiring
loop + session yourself.

An embedder's own tools compose additively with the builtins via
`runner.Options.ExtraTools` — no need to replace bash/read/edit/etc. just to
add one domain-specific tool:

```go
r, err := runner.New(ctx, runner.Options{
    Cwd: cwd, Root: root, Model: "claude-sonnet-5",
    ExtraTools: []tool.Tool{myTool}, // builtins + myTool
})
```

## Packages

| Package | Role |
|---|---|
| `event/` | Typed Event/Op contract · two-tier broker (lossy deltas, must-deliver terminals, drop counters) |
| `provider/` | LLM provider interface + normalized stream · `faux` scripted provider |
| `providers/` | `providers.Build` — construct a real provider (Anthropic/OpenAI) from manifest config |
| `auth/` | OAuth flows + on-disk token store (`auth.json`, mode 0600) for subscription auth |
| `loop/` | The agent loop: model calls, tool execution, hooks |
| `tool/` | Builtin tool registry: bash/read/edit/write/grep/glob/ls |
| `session/` | Session identity (UUIDv7), turn execution, event emission · pluggable journal `Store`: `FileStore` (on-disk JSONL, default) and `MemStore` (in-memory, opt-in) |
| `compose/` | Agent manifest (YAML) → wired session |
| `runner/` | Batteries-included drivable session (`New`/`Resume`/`Prompt`/`Events`/`Fold`/`Cost`/`SetModel`) assembling provider + tools + broker + loop + journal; `Options.ExtraTools` adds custom tools alongside the builtins, `Options.Store` swaps the journal store |
| `acp/` | Clean-room Agent Client Protocol adapter (stdlib-only), a pure Event/Op projection; `session/new` accepts an optional `model` field |
| `permission/` | Format-agnostic rule engine (`Rule`/`Engine`): deny > ask > allow, unmatched ⇒ ask, runtime grants |
| `lsp/` | Server registry + JSON-RPC-over-stdio client + diagnostics seam |
| `spill/` | Streaming per-tool-call output sink (bounded excerpt + durable on-disk file) |
| `exec/` | Headless one-shot exec adapter (JSONL events + output-schema validation) |

Journals default to on-disk JSONL for auditability; `session.MemStore` is the
opt-in for an ephemeral session that leaves no trace.

Planned: `skill/`, `plugin/`, `mcp/` (M4).

## Roadmap

| Stage | Ships |
|---|---|
| **M0 · scaffold** ✅ | Event/Op types, broker, compose skeleton, faux provider, golden-file harness, CI |
| **M1 · one good session** ✅ | Loop + real provider (Anthropic + OpenAI) + builtin tools + JSONL session tree + usage/cost accounting |
| **M2 · the daemon** ✅ | (in the consuming application) supervisor + TUI + native ACP; SDK ships `acp/` + `runner/` |
| **M3 · guardrails** ✅ | Sandbox/approval seam, binary containment policy, tool-output spill files, headless exec, LSP diagnostics |
| M4 · ecosystem | MCP client (tool-search-first), SKILL.md skills, plugin subprocess host, session tree / subagent spawn seam, vendor settings adapters, provider breadth |
| M5 · auto + polish | Reviewer pipeline, WASM plugin tier, asset import |

Two point releases followed M3, ahead of M4: **v0.4.0** (ACP `session/new`
optional `model` field) and **v0.5.0** (`Runner.SetModel`, mid-session model
swap) — both support gofer's M4, not SDK milestones.

The SDK must always build and test green with **zero** application code
present — the embedder is a CI gate, not a hope.

## License

[Apache-2.0](LICENSE). See [NOTICE](NOTICE) for attribution requirements.
