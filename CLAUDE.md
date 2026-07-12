# CLAUDE.md

@CONTRIBUTING.md

Guidance for Claude Code when working in this repository.

## What this is

`agent-sdk-go` — an importable, provider-agnostic agent framework
(loop, sessions, permissions, tools, skills, MCP, ACP, plugins, declarative
manifests). It is the library half of the gofer platform; the application half
(daemon + TUI) lives in [`jedwards1230/gofer`](https://github.com/jedwards1230/gofer)
and consumes this SDK through the same typed Event/Op contract every other
client uses.

Full product requirements + design: [`docs/PRD.md`](docs/PRD.md). Read it
before structural changes — the contract, tenets, and milestone sequencing are
all there.

## Architecture invariants (violations are bugs)

1. **Membership test**: code belongs in the SDK only if a second application
   would need it unchanged. Supervision, rosters, and TUI live in gofer.
2. **The SDK never imports gofer.** CI builds the SDK standalone.
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
- `session/` — session identity + turn execution, emits through the broker.
- `compose/` — YAML manifest → wired `*session.Session` (`compose.Load()`).
- `testdata/` golden JSONL streams live next to the package they exercise.
