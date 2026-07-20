# agent-sdk-go — testing strategy

Principles: inject a small model interface for loop unit tests; replay real
recorded conversations for the flagship test; use real filesystems and stores
over `t.TempDir()` everywhere else. Volume is not coverage — the provider
wire code is the highest-risk path and gets tested first, not skipped for
being "thin".

## Layers

| Layer | Type | CI | Approach |
|---|---|---|---|
| provider client | unit + integ | every push | VCR cassette replay (JSON-aware body matcher) for "a real conversation works" + `httptest.Server` scripted SSE for edge shapes. Deterministic → runs in CI with no key |
| ↳ cassette record | manual | not CI | regenerating cassettes hits the real API — explicit opt-in task gated on a real key, committed as testdata |
| agent loop | unit | every push | inject the model interface; per-test ~30-line `iter.Seq` fakes for cancellation/steering/timing. A shared MockProvider is secondary, never primary |
| session / persistence | integ | every push | real store against `t.TempDir()`; JSONL append→replay round-trip. Never mock the store |
| permission engine | unit | every push | table-driven over the `Tool(spec)` grammar + an imported CC-settings corpus |
| tools | unit | every push | real FS via `t.TempDir()`; git-aware tools run real `git init` in a tempdir |
| sandbox exec | integ | OS-gated | real subprocess under the real sandbox, build-tagged per OS (Seatbelt on macOS legs, bwrap on Linux) |
| ACP | integ | every push | small dedicated fake protocol server for request/response fixtures, separate from the loop fakes. MCP joins this lane on the same approach when `mcp/` lands (M5) |

## Reasoning-replay matrix

Reasoning/thinking blocks have twice put a malformed request on the wire (two
separate live 400s in M1/M2). The matrix — not live testing — must catch the
third: cover **signed × unsigned** thinking blocks × **empty × non-empty**
thinking text × **single- × multi-turn** replay, asserting the reconstructed
provider request is well-formed (thinking signatures preserved, empty blocks
handled correctly) before it could ever reach the wire.

## Fixtures

- Script turns **in code** (typed builder funcs for SSE events), not files.
- JSONL fixtures only for session/compaction replay — captured histories are
  a different concern from turn scripting.
- Cassettes are committed testdata; regenerate only via the opt-in target.

## Explicitly avoid

- A shared "mock model" package as the primary loop-test path (it leaves the
  wire code untested).
- Two parallel loop implementations with duplicated harnesses.
- Skipping HTTP-layer tests because a provider wrapper looks thin.
- Full-PTY testing as a first move; tests named "golden" that diff inline
  literals instead of `testdata/` files.

## CI gates

- `go test -race` on push to main and release tags (fast non-race suite on
  PRs) — see `.github/workflows/ci.yml`.
- **Embedder gate**: the SDK builds and tests green with zero application
  imports.
- Permission-corpus gate: fails if an imported CC `settings.json` rule ever
  changes verdict.
- Golden-drift check (`compose/` stream shapes); adopt a coverage ratchet and
  `govulncheck` as the suite grows.
