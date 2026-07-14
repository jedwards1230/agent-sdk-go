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

- [x] **Bulk-output spill.** Tool executions stream raw output to an append-only
      per-call file under the session dir (`<id>/calls/<call-id>.log`);
      `tool.call.finished` carries `spill_path` (store-root-relative) + `spill_bytes`
      + `spill_sha256` plus a bounded head+tail excerpt in `result` instead of an
      unbounded payload. bash streams straight to the file (no full-buffer path);
      other tools' bounded strings are written through the same sink. **Escape
      hatch (shipped #39):** excerpt-by-default, but the `read` tool returns FULL
      uncapped content, and every elision marker names the **absolute** spill path
      so the model can `read` it to load the whole output from any cwd (Root≠Cwd
      safe). Protects memory, makes every level of a session tree greppable on
      disk, surfaces errors from the source. (`spill/`, design: DESIGN.md)
- [x] **Sandbox seam (SDK side shipped).** `Decision{RunContained,Ask,Deny}` +
      `Guard`/`Granter`/`Container` interfaces (`loop/guard.go`) and `RuleGuard`,
      the M3 binary+deny policy composing a `permission.Engine` with an optional
      `Container`. Binary policy: sandboxable → run contained; otherwise (not
      sandboxable, no backend, or a `Container` error) → emit
      `permission.requested` and let a human decide — never silently block or run
      uncontained (decided 2026-07-13). Concrete backends (seatbelt / bwrap+seccomp)
      remain an application/optional-package concern; the SDK owns only the
      decision seam. `Config.Guard`/`Config.Approver` default nil ⇒ every call
      runs uncontained, unchanged from pre-M3 behavior. (design: DESIGN.md
      "Guard / decision seam")
- [x] **Approval protocol events.** Confirmed `permission.requested` /
      `permission.resolved` + the `permission.reply` op are sufficient for the
      emit → await → reply flow — no contract change needed. Wired end to end:
      `runner.gate`/`awaitApproval` (`loop/loop.go`) emit the events; `Gate`
      (`loop/gate.go`) is the reference `Approver` bridging an emitted
      `permission.requested` to an inbound `permission.reply` op, blocking the
      loop's own goroutine (no spawned goroutine, so a cancelled turn leaks
      nothing) on a per-id buffered channel selected against `ctx.Done()`. Static
      deny resolves without a preceding request (no human asked); every
      fail-closed path (nil approver, await error, container error) is explicit
      in DESIGN.md.
- [x] **Headless exec adapter.** One-shot drivable session emitting JSONL events
      on stdout, with output-schema support (the app's `exec` verb consumes this).
- [x] **LSP package.** `lsp/` ships a curated `Registry` (language → launch
      command, resolved on PATH) and a stdlib-only JSON-RPC-over-stdio
      `Client` speaking the LSP base protocol, wired to a neutral `Publisher`
      seam: the client hands every `textDocument/publishDiagnostics`
      notification to `Publisher.Publish` as a normalized `Batch`. `lsp`
      never imports `event/` — `Batch.Strings()` renders diagnostics as
      `[]string` for the consumer to assign onto
      `event.ToolCallFinished.Diagnostics` / `loop.ToolResult.Diagnostics`
      (both already exist) unchanged. Faked-transport tested (an in-memory
      `io.Pipe` Transport driving a scripted fake server) — no real language
      server runs in CI. The embedded ~370-server dataset, a lazy
      per-file-event manager, and `lsp_*` tools remain a later consuming
      layer, not shipped here. (`lsp/`, design: DESIGN.md "LSP")
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

CC `settings.json` loader + native manifest loader · TTL / anti-escalation /
dangerous-downgrade grant policy · session tree / subagent spawn seam · MCP
client + tool-search index · provider breadth (`openai-compat` + manifest
`ModelInfo` overlay).

**Permission-format home (decided 2026-07-13):** the rule engine and all format
loaders live under `permission/` — the engine consumes one typed `Rule`; each
format (CC `settings.json`, the native manifest block) is a thin `bytes → []Rule`
loader sharing the same matcher/glob helpers. This is a *permission-format*
concern, **not** provider support — it is unrelated to `provider/` and must not
couple to it. Ship only the CC loader + native format; other agents' formats
would be future siblings, never core.

**Thin engine landed (M3):** `permission.Rule` + `permission.Engine`
(`New`/`Evaluate`/`Grant`) shipped as the format-agnostic core described above —
deny > ask > allow precedence, unmatched ⇒ ask, `Grant` for a runtime
session-scoped allow. It imports only `event` + stdlib and has no vendor-format
loader yet; those (and TTL/anti-escalation) stay M4/M5 in the same package.
