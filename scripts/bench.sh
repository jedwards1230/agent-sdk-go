#!/usr/bin/env bash
#
# bench.sh — run the baselined benchmarks and gate their allocation profile.
#
#   scripts/bench.sh            run them and print the aggregated numbers
#   scripts/bench.sh --check    run them and fail on an allocation regression
#   scripts/bench.sh --update   rewrite the committed baseline (deliberate!)
#
# What it gates, and what it deliberately does not
# ------------------------------------------------
# Only allocs/op and B/op. ns/op is never parsed into the comparison: wall time
# on a shared CI runner varies by tens of percent run to run, and a gate that
# fails PRs it has no opinion about gets ignored, then disabled. The two
# allocation metrics are, by measurement, reproducible to the byte on a serial
# benchmark at a fixed iteration count.
#
# BOTH allocation metrics are gated, because they move independently. The
# classic "copy the whole slice on every call" regression keeps the allocation
# COUNT flat and grows the BYTES linearly — an allocs-only gate walks straight
# past it. The reverse (pooling bytes into many small allocations) keeps bytes
# flat and grows the count.
#
# The baseline file is the allowlist
# ----------------------------------
# It names the packages to run and the benchmarks to gate. A benchmark that
# shows up in the output but is not in the baseline is reported and IGNORED —
# so a peer adding a benchmark in a package this gate happens to run cannot
# turn it red. A baseline entry MISSING from the output FAILS: deleting or
# renaming a gated benchmark has to be a deliberate re-baseline, not a silent
# loss of coverage.
#
# Determinism knobs
# -----------------
# -benchtime=<N>x pins a FIXED iteration count rather than a duration, so
# allocs/op does not depend on how fast the machine felt today. -count=<N>
# repeats the whole run and the MEDIAN per metric is taken: the median cannot
# be moved by a single GC-perturbed outlier in either direction, where a min
# would bias the baseline low (tightening the gate over time) and a mean would
# let one bad sample drag it.
set -euo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
readonly REPO_ROOT

# Tunables. Defaults are measured, not guessed — see docs/TESTING.md for the
# raw per-run spread they were chosen from.
BASELINE="${BENCH_BASELINE:-${REPO_ROOT}/scripts/bench-baseline.txt}"
BENCHTIME="${BENCH_BENCHTIME:-100x}"
COUNT="${BENCH_COUNT:-5}"
ALLOCS_TOLERANCE_PCT="${BENCH_ALLOCS_TOLERANCE_PCT:-1}"
BYTES_TOLERANCE_PCT="${BENCH_BYTES_TOLERANCE_PCT:-1}"

MODE="run"
INPUT=""

usage() {
	cat <<'EOF'
usage: scripts/bench.sh [--check | --update] [--input FILE] [--baseline FILE]

  (no mode)         run the baselined benchmarks and print aggregated results
  --check           compare against the baseline; exit 1 on a regression
  --update          rewrite the baseline from a fresh run (review the diff!)

  --input FILE      read pre-captured `go test -bench` output from FILE instead
                    of running Go. Makes the comparison logic directly testable:
                    feed it a synthetic regressed capture and watch --check fail.
  --baseline FILE   use FILE as the baseline (default scripts/bench-baseline.txt)

environment overrides:
  BENCH_BENCHTIME              (default 100x)   fixed iteration count
  BENCH_COUNT                  (default 5)      repeats; median is taken
  BENCH_ALLOCS_TOLERANCE_PCT   (default 1)      allocs/op regression tolerance
  BENCH_BYTES_TOLERANCE_PCT    (default 1)      B/op regression tolerance
EOF
}

die() {
	printf 'bench.sh: %s\n' "$*" >&2
	exit 2
}

while [ $# -gt 0 ]; do
	case "$1" in
	--check) MODE="check" ;;
	--update) MODE="update" ;;
	--input)
		[ $# -ge 2 ] || die "--input needs a file"
		INPUT="$2"
		shift
		;;
	--baseline)
		[ $# -ge 2 ] || die "--baseline needs a file"
		BASELINE="$2"
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*) die "unknown argument: $1 (try --help)" ;;
	esac
	shift
done

[ -f "${BASELINE}" ] || die "baseline not found: ${BASELINE}"
if [ -n "${INPUT}" ]; then
	[ -f "${INPUT}" ] || die "input capture not found: ${INPUT}"
	[ "${MODE}" != "update" ] || die "--update needs a real run; it does not accept --input"
fi

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

raw="${WORK}/raw.txt"
observed="${WORK}/observed.tsv"

# The packages to run are exactly the ones the baseline names. Comment lines
# and blanks are skipped; the rest is <pkg>\t<bench>\t<allocs>\t<bytes>.
packages() {
	awk -F'\t' '/^[^#]/ && NF >= 4 { print $1 }' "${BASELINE}" | sort -u
}

# capture fills ${raw} with `go test -bench` output, or copies a pre-captured
# file when --input was given.
capture() {
	if [ -n "${INPUT}" ]; then
		cat -- "${INPUT}" >"${raw}"
		return
	fi

	local pkgs
	pkgs="$(packages)"
	[ -n "${pkgs}" ] || die "baseline names no packages: ${BASELINE}"

	printf 'running: go test -run=^$ -bench=. -benchmem -benchtime=%s -count=%s\n' \
		"${BENCHTIME}" "${COUNT}" >&2
	printf '%s\n' "${pkgs}" | sed 's/^/  /' >&2

	local status=0
	# shellcheck disable=SC2086 # pkgs is a deliberately word-split package list
	(cd "${REPO_ROOT}" && go test -run='^$' -bench=. -benchmem \
		-benchtime="${BENCHTIME}" -count="${COUNT}" ${pkgs}) >"${raw}" 2>&1 || status=$?
	if [ "${status}" -ne 0 ]; then
		cat "${raw}" >&2
		die "go test exited ${status}; benchmarks must pass before they can be gated"
	fi
}

# aggregate parses ${raw} into one median record per benchmark. Only allocs/op
# and B/op are read; ns/op is not even extracted, so it cannot leak into a
# comparison by accident later.
aggregate() {
	awk '
		/^pkg:[ \t]/ { pkg = $2; next }
		/^Benchmark/ {
			if (pkg == "") next
			name = $1
			sub(/-[0-9]+$/, "", name)   # strip the GOMAXPROCS suffix
			allocs = ""; bytes = ""
			for (i = 2; i <= NF; i++) {
				if ($i == "allocs/op") allocs = $(i-1)
				else if ($i == "B/op") bytes = $(i-1)
			}
			if (allocs != "" && bytes != "") print pkg "\t" name "\t" allocs "\t" bytes
		}
	' "${raw}" | awk -F'\t' '
		function median(arr, key, cnt,   i, j, t, v) {
			for (i = 1; i <= cnt; i++) v[i] = arr[key, i] + 0
			for (i = 1; i < cnt; i++)
				for (j = 1; j <= cnt - i; j++)
					if (v[j] > v[j+1]) { t = v[j]; v[j] = v[j+1]; v[j+1] = t }
			return v[int((cnt + 1) / 2)]
		}
		{
			key = $1 SUBSEP $2
			seen[key]++
			pkgOf[key] = $1; nameOf[key] = $2
			a[key, seen[key]] = $3
			b[key, seen[key]] = $4
		}
		END {
			for (key in seen)
				printf "%s\t%s\t%d\t%d\n", pkgOf[key], nameOf[key],
					median(a, key, seen[key]), median(b, key, seen[key])
		}
	' | sort
}

write_baseline() {
	local out="$1"
	{
		cat <<EOF
# agent-sdk-go allocation baseline — see docs/TESTING.md.
#
# Regenerate DELIBERATELY with scripts/bench.sh --update and review the diff
# like code. Never regenerate as a reflex because CI is red: the gate going red
# means either a real allocation regression or a benchmark that changed shape,
# and both need a human sentence in the PR description.
#
# This file is also the ALLOWLIST: bench.sh runs exactly the packages named
# here and gates exactly the benchmark names here. It contains no session/
# entries on purpose — those benchmarks belong to another workstream and must
# not be able to turn this gate red.
#
# Captured with: -run='^$' -bench=. -benchmem -benchtime=${BENCHTIME} -count=${COUNT}
# (median per metric). ns/op is intentionally absent: it is not gated.
#
# fields: package<TAB>benchmark<TAB>allocs/op<TAB>B/op
EOF
		cat "${observed}"
	} >"${out}"
}

compare() {
	awk -F'\t' \
		-v allocs_tol="${ALLOCS_TOLERANCE_PCT}" \
		-v bytes_tol="${BYTES_TOLERANCE_PCT}" '
		function limit(base, tol) { return base + (base * tol / 100.0) }
		function pct(base, obs) { return base == 0 ? 0 : (obs - base) * 100.0 / base }

		function judge(pkg, name, metric, base, obs, tol,   lim) {
			lim = limit(base, tol)
			if (obs > lim) {
				printf "REGRESSION  %s\n            %s\n", pkg, name
				printf "            %-10s baseline %d, observed %d  (delta %+d, %+.2f%%; tolerance %s%% allows up to %.2f)\n",
					metric ":", base, obs, obs - base, pct(base, obs), tol, lim
				return 1
			}
			# Only announce an improvement that is bigger than the tolerance.
			# Reporting a 13-byte drop on a 1.3 MB benchmark as "improved,
			# re-baseline to lock it in" trains the reader to skip this output,
			# and skipped output is how a real regression gets waved through.
			if (obs < base - (base * tol / 100.0)) {
				printf "improved    %s %s: %s baseline %d, observed %d (%+.2f%%) — re-baseline to lock it in\n",
					pkg, name, metric, base, obs, pct(base, obs)
			}
			return 0
		}

		FNR == NR {
			if ($0 ~ /^#/ || NF < 4) next
			key = $1 SUBSEP $2
			bpkg[key] = $1; bname[key] = $2; ballocs[key] = $3; bbytes[key] = $4
			next
		}
		{
			key = $1 SUBSEP $2
			if (!(key in ballocs)) {
				printf "ignored     %s %s: present in output, absent from the baseline (%d allocs/op, %d B/op)\n",
					$1, $2, $3, $4
				next
			}
			saw[key] = 1
			failures += judge($1, $2, "allocs/op", ballocs[key], $3, allocs_tol)
			failures += judge($1, $2, "B/op", bbytes[key], $4, bytes_tol)
			gated++
		}
		END {
			for (key in ballocs) {
				if (key in saw) continue
				printf "MISSING     %s\n            %s\n", bpkg[key], bname[key]
				printf "            baselined but absent from the benchmark output; deleting or renaming a gated benchmark must be a deliberate --update\n"
				failures++
			}
			if (failures > 0) {
				printf "\nFAIL: %d allocation gate violation(s) across %d gated benchmark(s).\n", failures, gated
				printf "If the change genuinely costs more, say why in the PR and run scripts/bench.sh --update.\n"
				exit 1
			}
			printf "\nOK: %d gated benchmark(s) within tolerance (allocs/op %s%%, B/op %s%%).\n",
				gated, allocs_tol, bytes_tol
		}
	' "${BASELINE}" "${observed}"
}

capture
aggregate >"${observed}"

if [ ! -s "${observed}" ]; then
	cat "${raw}" >&2
	die "parsed zero benchmark records; refusing to pass vacuously"
fi

printf '\n%-64s %10s %10s\n' "BENCHMARK" "allocs/op" "B/op"
awk -F'\t' '{ printf "%-64s %10s %10s\n", $2, $3, $4 }' "${observed}"

case "${MODE}" in
run)
	printf '\n(no gate applied; use --check to compare against %s)\n' "${BASELINE}"
	;;
check)
	printf '\n'
	compare
	;;
update)
	write_baseline "${WORK}/new-baseline.txt"
	mv "${WORK}/new-baseline.txt" "${BASELINE}"
	printf '\nbaseline rewritten: %s\n' "${BASELINE}"
	printf 'Review the diff like code — a widened baseline is a deliberate decision, not a formality.\n'
	;;
esac
