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
2. **Prefer a permanent guard to a one-off mutation, and hook it where the
   TARGET owns the call.** A mutation proves the benchmark reached its target
   *today*; a guard keeps checking. But a counter is only worth what its
   position buys: counting a method the benchmark body itself can call proves
   the fixture is wired, not that the target ran. Hook a construction-time
   option closure the target invokes — `internal/benchguard` counts
   `toolindex.Options.Resident`, which only `Index.Wrap` ever calls — and
   assert both a per-iteration floor and a digest of the arguments the target
   handed it.

   **A guard is not a sandbox.** Nothing in Go makes a counter unreachable from
   the file that owns it, and this document previously claimed otherwise. An
   adversarial review disproved it: with the counter on the fixture's own
   `Specs` method, deleting `toolindex` from the loop entirely still counted,
   allocations fell 29%, and the guard passed. What a target-owned hook buys is
   that faking it requires reconstructing the target's whole argument stream,
   which is visible in review as deliberate rather than slipping in during a
   refactor. The numeric gate is the independent second line — see the
   symmetric tolerance below.
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

**The comparison is symmetric.** A move beyond tolerance in *either* direction
fails. The downward half is not pedantry: a benchmark that stops doing its work
allocates **less**, so an upward-only gate reports the exact failure this
harness exists to catch as good news. A verified adversarial fixture drove
`BenchmarkIndexWrap` from 8709 allocs/op to 0 while satisfying its guard
completely; the symmetric gate caught it at −100%. A genuine optimization
therefore has to land a `--update` commit — which is correct, because it makes
the win explicit and reviewed instead of silently absorbed into the baseline.

A **zero baseline** is handled explicitly rather than as a percentage: `x/0` is
undefined, so any nonzero observation against a `0` row fails and is reported
in absolute terms. Benchmarks legitimately sitting at `0 allocs/op` exist
(`session/journal_bench_internal_test.go` has some), and reporting an unbounded
regression as `+0.00%` would be the reporting equivalent of not gating it.

The baseline is also validated before it is trusted: non-numeric metrics, rows
with the wrong field count, a file with no usable rows, and a run that compares
**zero** rows are each a hard exit-2 failure. Every one of those was a way to
get a green gate having measured nothing.

The nastiest of those was a metric that merely *looks* numeric. `500x` is
coerced to `500` by awk without complaint, so the row became a plausible
baseline nobody wrote and the resulting −99.6% collapse was reported as
`improved` — **exit 0, green gate, nothing measured**. A bare `abc` does not
crash either (`judge()` catches the coerced zero baseline before `pct()`
divides), but it at least reports something; this shape reports nothing at all,
so there is no symptom to notice. Fixture:
`scripts/testdata/baselines/numeric-looking.txt`.

### The small row is load-bearing — do not delete it

Neither layer catches *partial* drift on its own. Dropping only the `.Specs()`
projection tail from `BenchmarkIndexWrap` leaves `Wrap` intact, so the guard
counts a full n-per-iteration and passes cleanly; numerically it fires **only**
on the smallest row, `n=8`, at −1.17% B/op against the ±1% band — a **0.17
percentage-point margin**. `n=64` (−0.11%) and `n=512` (−0.003%) stay quiet,
because a fixed projection cost disappears into a larger index. So `n=8` is not
redundant coverage of the bigger rows; it is the only thing standing between
partial drift and a green gate. Deleting it, or loosening the tolerance much
past 1%, silently reopens that gap.

Both allocation metrics are gated because they move independently. The classic
"copy the whole slice on every call" regression holds the allocation **count**
flat and grows the **bytes** linearly; an allocs-only gate walks straight past
it. `scripts/testdata/regression-bytes-only.txt` is exactly that shape, kept as
a fixture so CI re-proves the bytes half of the gate can still fire.

### Thresholds, and the measurements they came from

| Metric | Tolerance | Worst observed deviation from baseline (either direction) | Headroom |
|---|---|---|---|
| `allocs/op` | ±1% | 0.0115% (`IndexWrap/n=512`: 8708 ↔ 8709) | ~87× |
| `B/op` | ±1% | 0.0068% (`IndexWrap/n=512`: −89 on 1310974) | ~147× |

Measured over 12 consecutive `bench.sh --check` runs on darwin/arm64 plus 3 on
linux/amd64 (`-benchtime=100x -count=5`, median per metric), all 15 green.
`IndexProject` (2 allocs/op, 304 B/op) and `IndexWrap/n=8` (137, 19128) were
**bit-identical in all 15 runs on both platforms**; only the two larger
`IndexWrap` rows moved at all, and never by more than the numbers above. The
deviations are two-sided, which is what the symmetric tolerance has to absorb:
a ±1% band is ~87× the worst drift actually observed in either direction.

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
B=scripts/testdata/baselines
# exit 1 — a violation detected and reported
scripts/bench.sh --check --input scripts/testdata/regression-bytes-only.txt
scripts/bench.sh --check --input scripts/testdata/missing-benchmark.txt
scripts/bench.sh --check --input scripts/testdata/improvement-collapse.txt
scripts/bench.sh --check --baseline $B/zero-metrics.txt --input scripts/testdata/regression-bytes-only.txt
# exit 2 — the baseline is unusable; refuse rather than pass vacuously
scripts/bench.sh --check --baseline $B/malformed.txt      --input scripts/testdata/regression-bytes-only.txt
scripts/bench.sh --check --baseline $B/no-rows.txt        --input scripts/testdata/regression-bytes-only.txt
scripts/bench.sh --check --baseline $B/unmatched-rows.txt --input scripts/testdata/regression-bytes-only.txt

# exit 1 — the consuming application's module path, including under it
printf 'github.com/jedwards1230/agent-sdk-go/loop\ngithub.com/jedwards1230/gofer/daemon\n' \
  | scripts/check-independence.sh --stdin
# exit 0 — an unrelated module that merely contains "gofer" must NOT fire
printf 'github.com/jedwards1230/agent-sdk-go/loop\ngithub.com/unrelated/gofergo\n' \
  | scripts/check-independence.sh --stdin
```

The `bench` CI job runs this whole matrix, asserting each exit code **exactly**
— a `!` negation would accept "the script is broken" (exit 2) as if it were
"the script found a regression" (exit 1).

The independence check runs `go list -deps **-test** ./...` (307 packages, vs
231 without `-test`). Those ~76 test-only dependencies are the *easiest* place
for the invariant to break — a test reaching for the application's helpers
feels harmless while you write it — so leaving them outside the gate would have
made "the SDK never imports application code" false as written.

It matches the consuming application's full module path
(`github.com/jedwards1230/gofer`, plus anything under it: `/…` subpackages,
`.test` synthetic packages, `[pkg.test]` bracket forms), not the bare substring
`gofer`, which flagged unrelated modules like `github.com/unrelated/gofergo`.
The trade is that renaming the application means updating the pattern — a
conscious, greppable event, where a false positive arrives unannounced on
someone else's PR.

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
  `scripts/bench.sh --check` fails on an `allocs/op` or `B/op` move beyond
  ±1% against `scripts/bench-baseline.txt` — in **either** direction — and
  `check-independence.sh` fails on any dependency under the consuming
  application's module path, test dependencies included. The same job replays a
  matrix of committed synthetic inputs to prove both scripts can still fail,
  and that the independence check does *not* fire on a lookalike module.
- **Embedder gate**: the SDK builds and tests green with zero application
  imports.
- Permission-corpus gate: fails if an imported CC `settings.json` rule ever
  changes verdict.
- Golden-drift check (`compose/` stream shapes); adopt a coverage ratchet and
  `govulncheck` as the suite grows.
