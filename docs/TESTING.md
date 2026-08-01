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

## Benchmarks and the allocation gate

Benchmarks are not a performance hobby here — they are the only test that can
see a regression which keeps every assertion green. `scripts/bench.sh --check`
runs the baselined benchmarks and fails on an allocation regression;
`scripts/check-independence.sh` fails if any dependency path mentions the
consuming application. Both run in the unconditional `bench` CI job.

### The four anti-blindness rules

A benchmark that stops reaching its target does not fail. It gets faster, it
allocates less, and an allocation gate reads that as an improvement. Every one
of these rules exists because the failure mode is *silence*.

1. **Mutation-prove that a benchmark reaches the code it claims to measure.**
   Put a `panic` in the target branch: the new benchmark must hit it, and a
   pre-existing one must not. If neither hits it, the benchmark is measuring
   something else.
2. **Prefer a permanent guard to a one-off mutation.** A mutation proves the
   benchmark reached its target *today*. Route the fixture's real work through
   a closure that counts invocations, assert a documented per-iteration floor
   after the loop, and it stays proven — see `internal/benchguard`.
3. **Use the real object, not the package's usual fake.** A fake returning a
   canned value measures nothing. The seed benchmarks wrap a genuine
   `tool.Registry` through `loop.FromRegistry`, JSON-marshaled schemas and all;
   the counting decorator sits *between* the target and the real thing rather
   than replacing it.
4. **A mutation is evidence only if the mutated tree still BUILDS and the
   NAMED test or benchmark fails.** A compile error proves nothing. A pattern
   that matches nothing passes vacuously. Report mutations as: what changed,
   that it built, which named test went red.

### What the gate measures

Only `allocs/op` and `B/op`, both of them, on serial benchmarks at a fixed
iteration count. `ns/op` is never parsed into the comparison — wall time on a
shared runner varies by tens of percent, and a gate that fails PRs it has no
opinion about gets ignored and then disabled.

Both allocation metrics are gated because they move independently. The classic
"copy the whole slice on every call" regression holds the allocation **count**
flat and grows the **bytes** linearly; an allocs-only gate walks straight past
it. `scripts/testdata/regression-bytes-only.txt` is exactly that shape, kept as
a fixture so CI re-proves the bytes half of the gate can still fire.

### Thresholds, and the measurements they came from

| Metric | Tolerance | Worst observed run-to-run spread | Headroom |
|---|---|---|---|
| `allocs/op` | 1% | 0.011% (`IndexWrap/n=512`: 8708 ↔ 8709) | ~90× |
| `B/op` | 1% | 0.018% (`IndexWrap/n=64`: 162363 ↔ 162394) | ~55× |

Measured over 12 consecutive `bench.sh --check` runs on darwin/arm64 plus 3 on
linux/amd64 (`-benchtime=100x -count=5`, median per metric). `IndexProject`
(2 allocs/op, 304 B/op) and `IndexWrap/n=8` (137, 19136) were **bit-identical
in all 15 runs on both platforms**; only the two larger `IndexWrap` rows moved
at all, and never by more than the numbers above.

1% is therefore ~55–90× the observed noise while still being tight enough to
catch a real regression: a single extra allocation on the 2-alloc
`IndexProject` row is +50%, and copying the 512-entry index once per call would
be +1.9% on `IndexWrap/n=512` — which a laxer 5% tolerance would have missed.
That is the whole argument for measuring first: 5% looked "safe" and was in
fact blind to a real regression shape.

Two determinism choices make that spread possible:

- **`-benchtime=100x`, a fixed iteration count, never a duration.** Otherwise a
  slower machine runs fewer iterations and amortizes fixed costs differently.
- **`-count=5` with the MEDIAN per metric.** The median cannot be moved by one
  GC-perturbed outlier in either direction; a `min` would bias the baseline low
  and quietly tighten the gate over time, and a mean lets one bad sample drag
  it.

The fixtures also warm process-global caches (`encoding/json` builds a per-type
encoder on first use) before the measured loop, so no sample is a cold sample.
Skipping that warmup was worth a 1.5% skew on the first `-count` sample.

### Never baseline a `RunParallel` benchmark

Serial benchmarks reproduce allocation numbers to the byte. `b.RunParallel`
ones do not: scheduling shifts how much of a fixture's fixed work budget lands
inside `b.N`, and the same parallel journal benchmark measured `954 B/op,
3 allocs/op` on one run and `833 B/op, 2 allocs/op` on the next — a 33% swing
with no code change. Baselining that is how you build a gate that fails
unrelated PRs until someone deletes it. Write parallel benchmarks for
contention analysis; keep them out of `scripts/bench-baseline.txt`.

### Re-baselining (deliberate, never a reflex)

```bash
scripts/bench.sh              # run the gated benchmarks, print the numbers
scripts/bench.sh --check      # compare against the baseline (what CI runs)
scripts/bench.sh --update     # rewrite scripts/bench-baseline.txt
```

`--update` is reviewed like code. The gate going red means either a real
allocation regression or a benchmark that changed shape; both need a sentence
in the PR saying which, and why the new number is acceptable. Re-baselining
because CI is red, without that sentence, converts the gate into decoration.

The baseline is also the **allowlist**: `bench.sh` runs exactly the packages it
names and gates exactly the benchmark names in it. A benchmark present in the
output but absent from the baseline is reported and ignored, so a peer adding
one cannot turn this gate red. A baseline entry missing from the output *fails*
— deleting or renaming a gated benchmark must be a deliberate `--update`. To
put a new package under the gate, add one placeholder row naming it
(`<import path><TAB>PLACEHOLDER<TAB>0<TAB>0`) and run `--update`, which rewrites
the file from the observed run.

Re-run the gate's own proofs at any time, without touching Go code:

```bash
scripts/bench.sh --check --input scripts/testdata/regression-bytes-only.txt  # must exit 1
scripts/bench.sh --check --input scripts/testdata/missing-benchmark.txt      # must exit 1
printf 'github.com/jedwards1230/agent-sdk-go/loop\ngithub.com/jedwards1230/gofer/daemon\n' \
  | scripts/check-independence.sh --stdin                                    # must exit 1
```

> `rg -rn gofer --include='*.go'` is **not** an independence check. In ripgrep
> `-r` means `--replace` and `--include` is not a flag, so it fails
> false-clean. And `go list -deps ./... | grep gofer` exits **1 when clean** —
> under `set -e` a naive step inverts the gate. `check-independence.sh` handles
> every grep exit status explicitly and refuses to search an empty or
> implausible dependency list.

## CI gates

- `go test -race` on push to main and release tags (fast non-race suite on
  PRs) — see `.github/workflows/ci.yml`.
- **Allocation gate** (`bench` job, unconditional on every PR and push):
  `scripts/bench.sh --check` fails on an `allocs/op` or `B/op` regression
  beyond 1% against `scripts/bench-baseline.txt`, and `check-independence.sh`
  fails on any dependency mentioning the consuming application. The same job
  replays two committed synthetic captures to prove the comparison itself can
  still fail.
- **Embedder gate**: the SDK builds and tests green with zero application
  imports.
- Permission-corpus gate: fails if an imported CC `settings.json` rule ever
  changes verdict.
- Golden-drift check (`compose/` stream shapes); adopt a coverage ratchet and
  `govulncheck` as the suite grows.
