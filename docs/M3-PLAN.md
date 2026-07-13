# M3 — guardrails (SDK half): tracking plan

Living checklist for the M3 milestone in this repo. The spec is
[`PRD.md`](PRD.md) (milestone table + tenets) and [`DESIGN.md`](DESIGN.md)
(normative seams); this doc tracks progress and sequencing. Cross-repo plan of
record lives in the umbrella orchestration repo
(`docs/projects/gofer-m3-plan-and-docs-refresh.md`).

## Scope

The SDK ships **seams**; the consuming application owns supervision, transport,
and approval UX. M3's permission story is deliberately **binary** — a tool call
is either sandboxable (run contained) or it raises an approval request. The
format-agnostic rule engine and the Claude Code `settings.json` import adapter
are **M4/M5, not M3**.

## Work packages

- [ ] **Bulk-output spill.** Tool executions stream raw output to an append-only
      per-call file under the session dir; `tool.call.finished` carries
      `{path, bytes, sha256, head/tail excerpt}` instead of an unbounded payload.
      Protects memory, makes every level of a session tree greppable on disk,
      surfaces errors from the source. (design: DESIGN.md)
- [ ] **Sandbox seam.** A containment interface the loop consults before running
      a tool. Binary policy: sandboxable → run contained; otherwise → emit
      `permission.requested`. Concrete backends (seatbelt / bwrap+seccomp) are an
      application/optional-package concern; the SDK owns the decision seam.
- [ ] **Approval protocol events.** Confirm `permission.requested` /
      `permission.resolved` events + the `permission.reply` op carry everything a
      real client's approval relay needs; add fields only if a live client proves
      a gap. No rule engine yet — the verdict is a human ask.
- [ ] **Headless exec adapter.** One-shot drivable session emitting JSONL events
      on stdout, with output-schema support (the app's `exec` verb consumes this).
- [ ] **LSP package.** Server registry + diagnostics seam; the loop surfaces
      diagnostics through the event stream.
- [ ] **OTel seams.** Assert context propagation through every call path; expose
      optional `*slog.Logger` injection points; keep the Event/Op stream as the
      span source. The SDK takes **no** otel dependency — the application owns
      exporters.

## Acceptance

A non-sandboxable tool call raises `permission.requested`; a client's
`permission.reply` gates execution. Spill files are present and referenced from
`tool.call.finished`. Full gate green (`go build ./... && go vet ./... && go
test ./... && golangci-lint run`).

## Explicitly deferred (M4/M5)

Format-agnostic `Rule` engine + CC `settings.json` loader · session tree /
subagent spawn seam · MCP client + tool-search index · provider breadth
(`openai-compat` + manifest `ModelInfo` overlay).
