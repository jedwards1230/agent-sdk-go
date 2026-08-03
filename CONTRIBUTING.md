# Contributing to agent-sdk-go

An importable, provider-agnostic agent framework for Go. All changes go through
the workflow below.

## Prerequisites

Go ≥ 1.25 and `golangci-lint`.

## Build, test & lint

```bash
go build ./...
go vet ./...
go test ./...
golangci-lint run
```

Golden-file tests compare event streams against `testdata/*.golden.jsonl`;
regenerate deliberately with `go test ./compose/... -update` and review the
diff like code.

The `bench` CI job runs two more gates, both runnable locally:

```bash
./scripts/check-independence.sh   # no dependency may mention the consuming app
./scripts/bench.sh --check        # allocs/op and B/op vs scripts/bench-baseline.txt
./scripts/bench.sh                # just run the gated benchmarks and print them
./scripts/bench.sh --update       # re-baseline — deliberate, reviewed like code
```

`--check` gates `allocs/op` and `B/op` at ±1% each (never `ns/op`) against
serial benchmarks at a fixed iteration count. The tolerance is **symmetric**: a
benchmark that stops doing its work allocates *less*, so an outsized drop fails
too and a genuine optimization has to land a `--update` commit. If it goes red,
decide whether the move is a real regression, a real win, or a benchmark that
went blind *before* reaching for `--update`, and say which in the PR.

Thresholds, the measured run-to-run spread they were chosen from, the four
anti-blindness rules for writing benchmarks (including where to hook a guard so
it proves something), and why a `RunParallel` benchmark must never be baselined
are all in [`docs/TESTING.md`](docs/TESTING.md).

## Hard rules

- **The SDK never imports application code.** It must build and test green
  standalone — embedders are the first-class consumer.
- **Every event the loop can emit is typed** and part of the public contract
  (`event/`). Adding an event kind is an API change; document it.
- **Stream, don't accumulate.** New code paths emit incremental events; a
  settled `*.finished` event carries the authoritative payload.

## Before you open a PR

- Make sure all CI checks pass locally first (the commands above, exactly as CI
  runs them).

## Branching & commits

- Branch off `main`; never commit directly to `main`.
- Use [Conventional Commits](https://www.conventionalcommits.org/) prefixes
  (`feat:`, `fix:`, `docs:`, `chore:`, `refactor:`, `test:`, …).
- Sign your commits where possible (`git commit -S`).
- Keep each PR focused; delete dead code rather than commenting it out.

## Pull requests

- Open the PR against `main`.
- Every PR runs CI. Resolve **all** review threads before the PR is merged.
- An automated code review runs on each PR; address and resolve its threads
  like any other review.

## Documentation

Keep documentation current as part of the change, not as a follow-up — update
the README and `docs/` in the same PR.

### Naming the consuming application in docs

This repo is consumed by an application (currently `gofer`) that does not live
here. Naming it in doc prose is not automatically a problem — most mentions are
fine — but `docs/DESIGN.md` and `docs/PRD.md` are the *normative* docs: they
specify the SDK's contract, and a second embedder has to be able to read that
contract without needing to know anything about the first one. Classify a
mention into one of four kinds before touching it:

- **Attribution** (`NOTICE`) — arguably required by the license. Exempt,
  permanently.
- **Historical record** (`docs/milestones/*`, `docs/proposals/*`) — dated
  artifacts of what happened. These *should* name the consumer; genericizing a
  milestone doc into "a consuming application did X" makes it worse, not more
  neutral.
- **Motivating example** — "an embedder needed X, e.g. …" — fine, provided the
  seam or contract is already specified in neutral terms first.
- **Drift** — normative prose in `docs/DESIGN.md` / `docs/PRD.md` that
  specifies the contract *in terms of* the one consumer, so a second embedder
  cannot tell what is required of them from what this one happens to do.

Only the fourth is a problem, and the test is mechanical: **delete the
consumer's name from the sentence. If it still fully specifies the contract,
the mention was decoration and can stay. If the sentence goes vague or
ungrammatical because the load-bearing noun was the consumer's name, rewrite it
with a neutral one** (`the embedder`, `an application`, `application-native`,
…) — that is drift.

**No doc linter, no mention budget, no repo-wide count.** Tracking a number
creates exactly the scrubbing pressure this rule exists to remove — do not
reduce a count for its own sake, and do not touch a legitimate historical or
motivating mention to make a total look smaller. See
[agent-sdk-go#128](https://github.com/jedwards1230/agent-sdk-go/issues/128) for
the fuller writeup and worked examples.

**When a change makes a doc claim false, the claim is part of the diff.** Every
late defect found in the benchmark/compaction round was of one shape: a
statement that was true when written and that a later change in the same round
falsified — a script header naming the command it no longer runs, a doc
pointing at `Subscription.Forced` after a second cut-off path existed, a
"nothing was journaled" claim after the `Sync`-failure path was understood.
None was a code defect; the shipped behavior was right every time.

They surface late because per-PR review structurally cannot catch them: a
reviewer reading added lines cannot see a sentence three files away that just
became wrong. So when you change behavior, grep for prose that describes it —
the sentence you have to fix is usually not in the file you edited.

## A skipped check is not a passed check

`gh pr checks` prints `skipping` in the same column as `pass`, and a PR whose
review never ran reads exactly as green as one that passed. **Before merging,
confirm `review / review` is at a terminal `pass` — not merely non-failing.**

The review workflow skips drafts. Two different situations produce that, and
they call for opposite responses:

- **The PR is draft because nobody undrafted it.** No review has run and none
  will. This is the trap: it accumulates commits looking green. Undraft it.
- **The PR is draft because `claude[bot]` converted it.** That is
  `draft_on_blocking` — the review *did* run, found blocking findings, and
  drafted the PR as the signal. Check the timeline before assuming an
  oversight:

  ```bash
  gh api --paginate repos/<owner>/<repo>/issues/<pr>/timeline \
    --jq '.[] | select(.event=="convert_to_draft") | "\(.created_at) by \(.actor.login)"'
  ```

  **`--paginate` is required, not optional.** The timeline API pages at 30 and
  says nothing when it truncates. On this repo's own integration PR the
  timeline held 44 events, so the un-paginated form saw 30 and printed
  nothing — which reads as "nobody drafted it", the exact wrong branch of the
  two this section distinguishes. A `convert_to_draft` follows a review, so on
  a long-lived PR it is precisely the event that lands past page 1.

  Fix the findings, resolve the threads, then undraft to re-trigger.

Note also that `ready_for_review` can race a push: if you undraft and push
together, the only run may be the `synchronize` one that fired while still
draft, which skips. Push first, let it settle, then undraft — and if a run is
still missing, close/reopen (`reopened` is in the trigger types).

## Known false positives

Findings the automated review has raised that were refuted with evidence.
Refute with a command someone else can re-run, not an assertion — and only add
an entry once you have.

### "`SessionCompactionFailed.Err`'s doc omits the panic / `runtime.Goexit` paths"

Raised on
[#141](https://github.com/jedwards1230/agent-sdk-go/pull/141) against
`event/event.go`, asking that the doc mention the two exits that return no
error. It already did, at both doc sites, on the exact commit the review read
(`f62d971`):

```bash
git show f62d971:event/event.go | sed -n '377,382p'   # type-level doc
git show f62d971:event/event.go | sed -n '408,413p'   # field-level doc
```

The type-level doc reads "…the string form of the same error
`runner.Runner.Compact` returns (or, for the two exits that return nothing, a
message naming the panic or the Goexit)", and the field doc names
`runtime.Goexit` explicitly. The finding quotes only the leading clause and
stops before the parenthetical that answers it.

Worth knowing because the *first* pass of this same finding, one commit
earlier, was real — the field doc genuinely was stale then and was fixed in
[#147](https://github.com/jedwards1230/agent-sdk-go/pull/147). A re-raise
against a fixed tree reads identically to the original. Check the doc on the
reviewed SHA before treating a repeat as outstanding work.

### "Returning `&param` stores a dangling pointer to the stack"

Raised twice on one line in
[#138](https://github.com/jedwards1230/agent-sdk-go/pull/138), against
`announce.NewCredential` returning `Credential{value: &s}`.

Go has no dangling pointers. Escape analysis moves a parameter whose address
outlives the call to the heap, and the garbage collector keeps it alive as long
as something references it. The suggested "fix" (`t := s; return &t`) is
identical after escape analysis. Verify:

```bash
go build -gcflags='-m' ./announce/ 2>&1 | grep 'moved to heap: s'
# announce/announce.go:289:20: moved to heap: s
```

A runtime check also holds: 1000 credentials stored in a slice, then deep stack
churn and two `runtime.GC()` calls, reveal their original values intact under
`-race`.

This is a C/C++ lifetime rule applied to Go. Expect it whenever a constructor
returns a struct holding the address of a parameter.
