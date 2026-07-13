# agent-sdk-go

An importable, provider-agnostic **agent framework for Go**: an owned, auditable
agent loop with sessions, permissions, tools, skills, MCP, ACP, plugins, and
declarative agent manifests.

> **Status: v0.2.0, M2 shipped.** The typed Event/Op contract, the two-tier
> event broker, real Anthropic/OpenAI providers (API key + subscription
> OAuth), the agent loop, builtin tools, the `runner` package, and a
> clean-room `acp` (Agent Client Protocol) adapter are all in place. Next up
> is M3, the permission/guardrails milestone (see the [roadmap](#roadmap)).

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

## Packages

| Package | Role |
|---|---|
| `event/` | Typed Event/Op contract · two-tier broker (lossy deltas, must-deliver terminals, drop counters) |
| `provider/` | LLM provider interface + normalized stream · `faux` scripted provider |
| `providers/` | `providers.Build` — construct a real provider (Anthropic/OpenAI) from manifest config |
| `auth/` | OAuth flows + on-disk token store (`auth.json`, mode 0600) for subscription auth |
| `loop/` | The agent loop: model calls, tool execution, hooks |
| `tool/` | Builtin tool registry: bash/read/edit/write/grep/glob/ls |
| `session/` | Session identity (UUIDv7), turn execution, event emission |
| `compose/` | Agent manifest (YAML) → wired session |
| `runner/` | Batteries-included drivable session (`New`/`Resume`/`Prompt`/`Events`/`Fold`/`Cost`) assembling provider + tools + broker + loop + journal |
| `acp/` | Clean-room Agent Client Protocol adapter (stdlib-only), a pure Event/Op projection |

Planned: `permission/`, `skill/`, `plugin/`, `lsp/`, `mcp/`.

## Roadmap

| Stage | Ships |
|---|---|
| **M0 · scaffold** ✅ | Event/Op types, broker, compose skeleton, faux provider, golden-file harness, CI |
| **M1 · one good session** ✅ | Loop + real provider (Anthropic + OpenAI) + builtin tools + JSONL session tree + usage/cost accounting |
| **M2 · the daemon** ✅ | (in the consuming application) supervisor + TUI + native ACP; SDK ships `acp/` + `runner/` |
| M3 · guardrails | Sandboxed/contained exec, approval messages, binary containment policy, tool-output spill files, headless exec, LSP diagnostics |
| M4 · ecosystem | MCP client (tool-search-first), SKILL.md skills, plugin subprocess host, session tree / subagent spawn seam, vendor settings adapters, provider breadth |
| M5 · auto + polish | Reviewer pipeline, WASM plugin tier, asset import |

The SDK must always build and test green with **zero** application code
present — the embedder is a CI gate, not a hope.

## License

[Apache-2.0](LICENSE). See [NOTICE](NOTICE) for attribution requirements.
