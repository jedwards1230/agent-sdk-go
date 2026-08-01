#!/usr/bin/env bash
#
# check-independence.sh — the SDK never imports application code.
#
#   scripts/check-independence.sh            check `go list -deps ./...`
#   scripts/check-independence.sh --stdin    check a dependency list on stdin
#
# CLAUDE.md architecture invariant #2 says the SDK builds standalone; this makes
# it a gate instead of a habit. The consuming application is
# github.com/jedwards1230/gofer, so a dependency on that module — or anything
# under it — means the invariant is already broken.
#
# Scope: TEST dependencies count too
# ----------------------------------
# `go list -deps ./...` covers 231 packages; `go list -deps -test ./...` covers
# 307. Those ~76 test-only dependencies are code in this repo just the same, and
# "the SDK never imports application code" is false the moment an SDK _test.go
# imports the daemon — that is in fact the easiest place for it to happen, since
# a test reaching for the app's helpers feels harmless while writing it. The
# check therefore runs with -test, which is a strict superset.
#
# Matching: the module PATH, not the substring "gofer"
# ---------------------------------------------------
# A bare substring flags `github.com/unrelated/gofergo` and any third-party
# module that happens to contain those five letters. Breaking CI on an
# unrelated dependency is the same cry-wolf failure the numeric half of this
# harness works hard to avoid, and a gate people learn to ignore protects
# nothing. The pattern below anchors on the full module path and still fails
# closed on everything UNDER it (`/…` subpackages, `.test` synthetic packages,
# and the `[pkg.test]` bracket forms that -test introduces).
#
# The trade: renaming the consuming application means updating FORBIDDEN_MODULE
# here. That is a deliberate choice of precision over rename-survival — a rename
# is a conscious, greppable event, whereas a false positive arrives unannounced
# on someone else'"'"'s PR.
#
# Three traps this script exists to avoid
# ---------------------------------------
# 1. `rg -rn gofer --include='*.go'` is NOT this check. In ripgrep -r means
#    --replace and --include is not a flag at all, so that command fails
#    FALSE-CLEAN — it reports success without having searched anything. Never
#    substitute it.
#
# 2. `go list -deps ./... | grep gofer` exits 1 when the tree is CLEAN. Under
#    `set -e` with pipefail a naive step inverts the gate: clean looks like
#    failure, dirty (grep exits 0) looks like success. Every grep exit status is
#    therefore handled explicitly below, and status >= 2 (a real grep error) is
#    an error, never a pass.
#
# 3. An empty dependency list passes every substring test. A typo in the go
#    command, a build failure swallowed into an empty capture, or a --stdin
#    invocation with nothing piped would all sail through. The list is
#    therefore sanity-checked before it is searched: non-empty, and containing
#    this module's own path.
set -euo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
readonly REPO_ROOT
readonly MODULE="github.com/jedwards1230/agent-sdk-go"
readonly FORBIDDEN_MODULE="github.com/jedwards1230/gofer"
# Anchored at a path boundary on both sides: a line may be a bare import path,
# a `pkg [pkg.test]` pair, or a `.test` synthetic package, so the module path
# can be preceded by start-of-line, whitespace or `[`, and must be followed by
# `/`, `.`, `]`, whitespace or end-of-line. `gofergo` is followed by `g` and so
# does not match; `gofer/internal/daemon`, `gofer.test` and `gofer` all do.
readonly FORBIDDEN_RE='(^|[[:space:]]|\[)github\.com/jedwards1230/gofer([/.]|\]|[[:space:]]|$)' 

FROM_STDIN=0

usage() {
	cat <<'EOF'
usage: scripts/check-independence.sh [--stdin]

  (no flag)   run `go list -deps ./...` and check its output
  --stdin     read the dependency list from stdin instead (one path per line)
              (the default form runs `go list -deps -test ./...`)

  The stdin form exists so the check itself is testable: pipe a synthetic list
  containing a gofer path and it must exit non-zero, without touching go.mod.
  A piped list must still contain this module's own path — a list that does not
  is not a dependency list, and passing it would prove nothing.
EOF
}

die() {
	printf 'check-independence.sh: %s\n' "$*" >&2
	exit 2
}

while [ $# -gt 0 ]; do
	case "$1" in
	--stdin) FROM_STDIN=1 ;;
	-h | --help)
		usage
		exit 0
		;;
	*) die "unknown argument: $1 (try --help)" ;;
	esac
	shift
done

if [ "${FROM_STDIN}" -eq 1 ]; then
	deps="$(cat)"
	source_desc="stdin"
else
	# -test includes test-only dependencies, a strict superset of the plain
	# form. An SDK test file importing the application is exactly as much of a
	# violation as a non-test file doing it.
	deps="$(cd "${REPO_ROOT}" && go list -deps -test ./...)"
	source_desc="go list -deps -test ./..."
fi

# Trap 3: refuse to search an implausible list.
if [ -z "${deps}" ]; then
	die "dependency list from ${source_desc} is empty; refusing to pass vacuously"
fi
count="$(printf '%s\n' "${deps}" | grep -c . || true)"
case "${deps}" in
*"${MODULE}"*) ;;
*) die "dependency list from ${source_desc} (${count} lines) does not mention ${MODULE}; it is not a dependency list for this module" ;;
esac

# Trap 2: every grep exit status handled explicitly. 0 = matched (bad),
# 1 = no match (good), >= 2 = grep itself failed (an error, never a pass).
set +e
matched="$(printf '%s\n' "${deps}" | grep -E -- "${FORBIDDEN_RE}")"
rc=$?
set -e

case "${rc}" in
0)
	printf 'FAIL: the SDK must build standalone, but %s reports dependencies on %s:\n\n' \
		"${source_desc}" "${FORBIDDEN_MODULE}" >&2
	printf '%s\n' "${matched}" | sed 's/^/  /' >&2
	printf '\nCLAUDE.md architecture invariant #2: the SDK never imports application code.\n' >&2
	exit 1
	;;
1)
	printf 'OK: %d dependencies from %s, none under %s.\n' \
		"${count}" "${source_desc}" "${FORBIDDEN_MODULE}"
	;;
*)
	die "grep exited ${rc} searching ${source_desc}; the check did not run"
	;;
esac
