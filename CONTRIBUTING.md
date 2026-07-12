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
regenerate deliberately with `go test ./... -update` and review the diff like
code.

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
