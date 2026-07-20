# CLAUDE.md

@CONTRIBUTING.md

Guidance for Claude Code when working in this repository.

## What this is

`agent-sdk-go` — an importable, provider-agnostic agent framework
(loop, sessions, permissions, tools, skills, MCP, ACP, plugins, declarative
manifests). It is an importable library; applications (daemon + TUI) consume it
through the same typed Event/Op contract every other client uses.

Full product requirements + design: [`docs/PRD.md`](docs/PRD.md). Read it
before structural changes — the contract, tenets, and milestone sequencing are
all there. [`docs/DESIGN.md`](docs/DESIGN.md) holds the normative interfaces
(loop seam, permission grammar, manifest schema); [`docs/TESTING.md`](docs/TESTING.md)
the test strategy.

## Architecture invariants (violations are bugs)

1. **Membership test**: code belongs in the SDK only if a second application
   would need it unchanged. Supervision, rosters, and TUI live in the consuming
   application.
2. **The SDK never imports application code.** CI builds the SDK standalone.
3. **One contract**: every frontend (TUI, ACP, headless, HTTP) consumes the
   typed Event/Op stream defined in `event/`. No client reaches past it.
4. **Two-tier delivery**: stream deltas (`message.delta`, `tool.call.delta`)
   ride the lossy tier; terminal events (`*.finished`, `session.*`,
   `permission.*`) are must-deliver with bounded blocking. Drop counters are
   exposed, never hidden.
5. **Settled payloads reconcile**: `message.finished{content}` carries the full
   authoritative text so lossy delta drops never corrupt a client's view.
6. **Session IDs are UUIDv7** — globally unique and time-ordered; the fleet
   layer depends on this.

## Design discipline

- **Context transparency**: nothing enters the model's context that the embedder
  can't see and override; every model call is reconstructable from the journal.
  Prompt assembly stays small and auditable — tool/MCP schemas load on demand,
  not all up front.
- **Two-gate feature test**: a capability earns a place in core only if it
  passes *both* gates — would a second app need it unchanged (membership), and
  could a seam suffice instead of a built-in?
- **Declarative consumption**: the SDK owns the abstraction; embedders wire
  business logic and variables. Every capability is reachable through a
  `compose.Load()` manifest, not just the Go API.
- **Code style**: inline a helper used at a single call site rather than
  hoisting it; never hardcode a config value — add a default and thread it
  through; ask before removing code that looks intentional.

## Commands

```bash
go build ./... && go vet ./... && go test ./...   # the CI gate
golangci-lint run                                  # lint, zero tolerance
go test ./compose/... -update                      # regenerate golden files (review the diff!)
```

## Layout

- `event/` — Event/Op tagged unions + two-tier broker. The public contract.
- `provider/` — provider interface, normalized stream; `provider/faux` is the
  deterministic scripted provider used by tests and demos.
- `session/` — event-sourced JSONL session tree: identity, journal, resume, and
  usage/cost accounting; emits through the broker.
- `compose/` — YAML manifest → wired `*session.Session` (`compose.Load()`).
- `runner/` — batteries-included drivable session: assembles provider
  (`providers.Build`) + tools + broker + loop + journal into a `*Runner`
  (`New`/`Resume`/`Prompt`/`Events`/`Fold`/`Cost`/`SetModel`/`Close`),
  event-sourcing each settled turn. The composable alternative to hand-wiring
  loop+session.
- `testdata/` golden JSONL streams live next to the package they exercise.
