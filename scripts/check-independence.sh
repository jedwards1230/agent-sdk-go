#!/usr/bin/env bash
#
# check-independence.sh — the SDK never imports application code.
#
#   scripts/check-independence.sh            check `go list -deps ./...`
#   scripts/check-independence.sh --stdin    check a dependency list on stdin
#
# CLAUDE.md architecture invariant #2 says the SDK builds standalone; this makes
# it a gate instead of a habit. The consuming application is `gofer`, so any
# dependency path containing "gofer" means the invariant is already broken.
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
readonly FORBIDDEN="gofer"

FROM_STDIN=0

usage() {
	cat <<'EOF'
usage: scripts/check-independence.sh [--stdin]

  (no flag)   run `go list -deps ./...` and check its output
  --stdin     read the dependency list from stdin instead (one path per line)

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
	deps="$(cd "${REPO_ROOT}" && go list -deps ./...)"
	source_desc="go list -deps ./..."
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
matched="$(printf '%s\n' "${deps}" | grep -F -- "${FORBIDDEN}")"
rc=$?
set -e

case "${rc}" in
0)
	printf 'FAIL: the SDK must build standalone, but %s dependencies mention %q:\n\n' \
		"${source_desc}" "${FORBIDDEN}" >&2
	printf '%s\n' "${matched}" | sed 's/^/  /' >&2
	printf '\nCLAUDE.md architecture invariant #2: the SDK never imports application code.\n' >&2
	exit 1
	;;
1)
	printf 'OK: %d dependencies from %s, none mentioning %q.\n' \
		"${count}" "${source_desc}" "${FORBIDDEN}"
	;;
*)
	die "grep exited ${rc} searching ${source_desc}; the check did not run"
	;;
esac
