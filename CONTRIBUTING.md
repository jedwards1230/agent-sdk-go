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

`--check` gates `allocs/op` and `B/op` at 1% each (never `ns/op`) against
serial benchmarks at a fixed iteration count. If it goes red, decide whether
the extra allocation is a real regression or an intended cost *before*
reaching for `--update`, and say which in the PR. Thresholds, the measured
run-to-run spread they were chosen from, the four anti-blindness rules for
writing benchmarks, and why a `RunParallel` benchmark must never be baselined
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

## Known false positives

Findings the automated review has raised that were refuted with evidence.
Refute with a command someone else can re-run, not an assertion — and only add
an entry once you have.

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
