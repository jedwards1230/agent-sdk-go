# M7 Round A+B — daily-driver build (SDK half)

> **Living checklist.** Status is updated as pieces land. Integration branch
> `milestone/m7-round-ab`; every piece is its own PR **based on that branch**, not
> `main`. The integration→`main` PR is user-gated.
> Cross-repo source of record: `jedwards1230/home-orchestration`
> `docs/projects/gofer-m7-round-ab-plan.md`. Application half:
> `jedwards1230/gofer` `docs/milestones/M7-round-ab.md`.

## Why

The consuming application cannot carry a full working day: nothing compacts a
long session, and the tool surface in daily use (MCP servers, web search) has no
home. This round adds the framework seams that close those gaps. The SDK provides
seams and accounting; **policy stays in the application** — the membership test
is unchanged.

## Status

| # | Piece | State | PR |
|---|---|---|---|
| 1 | `Runner.Compact` seam (`#89`) | in review | [#111](https://github.com/jedwards1230/agent-sdk-go/pull/111) |
| 3 | Optional `mcp/` package: client + tool projection | pending | — |
| 4 | Search `Provider` interface + Brave / SearXNG | in flight | — |
| 5 | Skills: `SKILL.md` loading, progressive disclosure | in review | [#115](https://github.com/jedwards1230/agent-sdk-go/pull/115) |
| — | Index-first tool-registry contract | pending | — |

`lsp/` shipped in M3 and needs no SDK change this round — the application-side
wiring and live verification are the deliverable there.

## Scope guards

- **The SDK never imports application code.** `rg -n 'jedwards1230/gofer' -g '*.go' .`
  and `go list -deps ./... | grep gofer` must both stay at zero hits — the build
  graph, not the word: naming the consuming application in doc prose is fine and
  often clarifying (see `docs/DESIGN.md`, `docs/PRD.md`). CI builds the SDK
  standalone.
- **MCP and search live as *optional* packages**, per the settled M3
  extension-tier decision (core → optional SDK package → subprocess plugin). Go
  compiles only what you import, so an optional networked package does not
  violate the networking-free core. Optional is fine; coupling is not.
- **Membership test**: a seam here only if a second application would need it
  unchanged. Compaction *policy* (when to trigger, how to render it) belongs to
  the embedder; the seam and its accounting belong here.
- **Context transparency**: nothing enters the model's context the embedder
  cannot see and override. Tool and MCP schemas load **index-first**, full schemas
  on demand — federating many servers must never dump every schema into context.
  The projection point is a single line: `req.Tools = r.cfg.Tools.Specs()` in
  `(*runner).callModel` (`loop/loop.go:276`), re-evaluated **once per model call**.
  `loop.ToolRegistry` is a consumer-side interface of just `Get` + `Specs`, so
  index-first can land as a *decorator* satisfying it — leaving `tool.Tool` and
  `tool.Registry` untouched. MCP-federated tools go through that same decorator:
  N servers must not mean N resident schemas.
- Adding or changing an event kind is an API change. Document it.

## Decisions

Recorded as they settle.

- **Release discipline at the boundary**: cut a real release tag before the
  consuming app re-pins. A squash-merge of the integration PR deletes the branch
  and orphans any pseudo-version pointing at it (M2 lesson, repeated at M3).
- **The independence invariant is the build graph, not a substring.** Verify with
  `go list -deps ./... | grep gofer` (authoritative) and
  `rg -n 'jedwards1230/gofer' -g '*.go' .` (import-level cross-check). Both are at
  **0**. Do **not** use `rg -rn gofer --include='*.go'` — in ripgrep `-r` is
  `--replace`, so `-rn` rewrites matches to "n", and `--include` is a grep flag rg
  does not have; that command fails *false-clean*.

## Open: consumer-neutrality drift (not this round)

M3 ran a deliberate independence sweep (`#45`) that took gofer mentions to zero
**including docs**. That has since drifted to **41** prose mentions: 22 in
`docs/proposals/checkpoint-task-handle-seam.md`, 10 in `docs/PRD.md`, 7 in
`docs/DESIGN.md`, 2 in `NOTICE`. The drift is concentrated in one newer proposal
rather than spread.

Deliberately **not** acted on this round. A library naming its consumer in design
prose is defensible, `NOTICE` arguably requires it, and scrubbing to hit a
substring count would make the seam docs worse. But whether the SDK should *read*
as consumer-neutral is a positioning question for the owner to decide, not one to
settle silently — it is flagged for the milestone boundary.

## Definition of done

Per piece: implementation + tests, CI green, review threads resolved, docs
updated in the same PR (`docs/PRD.md` for scope, `docs/DESIGN.md` for normative
interfaces). Round-level: the compaction seam is drivable and observable, an MCP
server's tools project into a session, a search provider answers from two
independent backends, and a skill's body loads only on invocation.
