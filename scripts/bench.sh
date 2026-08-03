#!/usr/bin/env bash
#
# bench.sh — run the module's benchmarks and gate the allocation profile of
# the ones named in the baseline.
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
# The comparison is SYMMETRIC: a move beyond tolerance in either direction
# fails. A gate that only fires upward cannot see the failure this harness
# exists to catch — a benchmark that stops doing its work allocates LESS, and
# an upward-only gate reports that collapse as an improvement. A genuine
# optimization therefore has to land a --update commit, which is the right
# outcome: the win becomes explicit and reviewed instead of silently absorbed
# into the noise floor.
#
# The RUN set and the GATE set are different things
# ---------------------------------------------------
# The run set is every package under BENCH_PKGS (default ./..., i.e. the whole
# module) — see the tunables below. The baseline file is the gate set: the
# allowlist of benchmark names whose allocation profile is actually compared.
# A benchmark that runs but is not in the baseline is reported and IGNORED —
# so adding a benchmark anywhere in the module cannot turn this gate red by
# accident. A baseline entry MISSING from the output FAILS: deleting or
# renaming a gated benchmark has to be a deliberate re-baseline, not a silent
# loss of coverage.
#
# Running the whole module and gating only a named subset is a deliberate,
# narrower coverage claim than the run set implies: an unlisted benchmark
# CANNOT fail this gate no matter how badly it regresses. See "OK: N gated, M
# run but NOT gated" in the summary this script prints, and docs/TESTING.md.
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
BENCH_PKGS_DEFAULT="./..."
BENCHTIME="${BENCH_BENCHTIME:-100x}"
COUNT="${BENCH_COUNT:-5}"
ALLOCS_TOLERANCE_PCT="${BENCH_ALLOCS_TOLERANCE_PCT:-1}"
BYTES_TOLERANCE_PCT="${BENCH_BYTES_TOLERANCE_PCT:-1}"

MODE="run"
INPUT=""

usage() {
	cat <<'EOF'
usage: scripts/bench.sh [--check | --update] [--input FILE] [--baseline FILE]

  (no mode)         run the module's benchmarks and print aggregated results
  --check           compare the baselined subset against the baseline; exit 1
                    on a regression
  --update          rewrite the baseline from a fresh run (review the diff!)

  --input FILE      read pre-captured `go test -bench` output from FILE instead
                    of running Go. Makes the comparison logic directly testable:
                    feed it a synthetic regressed capture and watch --check fail.
  --baseline FILE   use FILE as the baseline (default scripts/bench-baseline.txt)

environment overrides:
  BENCH_PKGS                   (default ./...)  packages to run, space-separated
  BENCH_BENCHTIME               (default 100x)  fixed iteration count
  BENCH_COUNT                   (default 5)     repeats; median is taken
  BENCH_ALLOCS_TOLERANCE_PCT    (default 1)     allocs/op drift tolerance, +/-
  BENCH_BYTES_TOLERANCE_PCT     (default 1)     B/op drift tolerance, +/-

The run set (BENCH_PKGS) and the gate set (the baseline) are independent: every
benchmark in the run set is executed and reported, but only the ones named in
the baseline can fail --check. See "The RUN set and the GATE set are different
things" above.
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

# capture fills ${raw} with `go test -bench` output, or copies a pre-captured
# file when --input was given.
#
# The run set is BENCH_PKGS (default ./..., the whole module), NOT the
# baseline: a benchmark in a package the baseline has never heard of still
# runs and is reported, just not gated. Read into an array rather than relying
# on unquoted word-splitting, so a multi-package override
# (BENCH_PKGS="./session/... ./event/...") is unambiguous.
capture() {
	if [ -n "${INPUT}" ]; then
		cat -- "${INPUT}" >"${raw}"
		return
	fi

	local -a pkgs
	read -ra pkgs <<<"${BENCH_PKGS:-${BENCH_PKGS_DEFAULT}}"
	[ "${#pkgs[@]}" -gt 0 ] || die "BENCH_PKGS resolved to no packages to run"

	printf 'running: go test -run=^$ -bench=. -benchmem -benchtime=%s -count=%s\n' \
		"${BENCHTIME}" "${COUNT}" >&2
	printf '%s\n' "${pkgs[@]}" | sed 's/^/  /' >&2

	local status=0
	(cd "${REPO_ROOT}" && go test -run='^$' -bench=. -benchmem \
		-benchtime="${BENCHTIME}" -count="${COUNT}" "${pkgs[@]}") >"${raw}" 2>&1 || status=$?
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
# This file is the GATE ALLOWLIST, not the run set: bench.sh runs the whole
# module (BENCH_PKGS, default ./...) and gates exactly the benchmark names
# listed here — everything else is run and reported as "ignored". It contains
# no session/ entries on purpose: those benchmarks run and are reported on
# every CI pass, but their numbers are not yet characterized on the runner, so
# they must not be able to turn this gate red until a workstream measures and
# baselines them deliberately.
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
		function isnum(v) { return v ~ /^[0-9]+$/ }
		function pct(base, obs) { return (obs - base) * 100.0 / base }

		# judge is SYMMETRIC: a move beyond tolerance in EITHER direction fails.
		#
		# The upward half is the obvious one. The downward half exists because
		# the failure this whole harness is built to catch — a benchmark that
		# stops doing the work it claims — shows up as allocations going DOWN.
		# Reporting that as "improved, nice work" is the gate cheering for its
		# own blindness. A genuine optimization is therefore required to land a
		# re-baseline commit, which is the correct outcome: it makes the win
		# explicit and reviewed instead of silently absorbed.
		function judge(pkg, name, metric, base, obs, tol,   hi, lo) {
			# A zero baseline has no percentage: x/0 is undefined, and the old
			# code printed a flat "+0.00%" for what could be an unbounded
			# regression. Treat any nonzero observation against a zero baseline
			# as a hard failure stated in absolute terms.
			if (base == 0) {
				if (obs == 0) return 0
				printf "REGRESSION  %s\n            %s\n", pkg, name
				printf "            %-10s baseline 0, observed %d (delta %+d; percentage undefined against a zero baseline — any allocation here is a regression from none)\n",
					metric ":", obs, obs
				return 1
			}

			hi = base + (base * tol / 100.0)
			lo = base - (base * tol / 100.0)

			if (obs > hi) {
				printf "REGRESSION  %s\n            %s\n", pkg, name
				printf "            %-10s baseline %d, observed %d  (delta %+d, %+.2f%%; tolerance %s%% allows up to %.2f)\n",
					metric ":", base, obs, obs - base, pct(base, obs), tol, hi
				return 1
			}
			if (obs < lo) {
				printf "UNEXPLAINED IMPROVEMENT  %s\n            %s\n", pkg, name
				printf "            %-10s baseline %d, observed %d  (delta %+d, %+.2f%%; tolerance %s%% allows down to %.2f)\n",
					metric ":", base, obs, obs - base, pct(base, obs), tol, lo
				printf "            Either the benchmark stopped doing its work (the blindness this gate exists to catch),\n"
				printf "            or this is a real optimization. If it is real, lock it in with scripts/bench.sh --update.\n"
				return 1
			}
			return 0
		}

		FNR == NR {
			if ($0 ~ /^#/ || $0 ~ /^[ \t]*$/) next
			if (NF < 4) {
				printf "MALFORMED   baseline line %d has %d tab-separated fields, want 4: %s\n", FNR, NF, $0
				malformed++
				next
			}
			# Validate before use. awk coerces a non-numeric field with +0,
			# and NEITHER coercion crashes — that is the whole problem. Both
			# shapes produce a confident, wrong answer:
			#
			#   "500x" -> 500. The row gates against a number nobody wrote,
			#   and a -99.6% collapse is reported as an improvement at exit 0.
			#   This is the one that actually got through; it is what
			#   scripts/testdata/baselines/numeric-looking.txt locks.
			#
			#   "abc"  -> 0. judge() catches base == 0 before pct() is ever
			#   called, so there is no division by zero. Instead it reports a
			#   REGRESSION blaming a legitimate zero baseline ("any allocation
			#   here is a regression from none") — the right verdict attributed
			#   to the wrong cause, which sends the reader after a phantom
			#   allocation instead of the malformed row. And when the observed
			#   value is also 0, judge() returns 0 and the row passes silently.
			#
			# Fail loudly and name the offending line instead.
			if (!isnum($3) || !isnum($4)) {
				printf "MALFORMED   baseline line %d: allocs/op \"%s\" and B/op \"%s\" must both be non-negative integers\n", FNR, $3, $4
				malformed++
				next
			}
			key = $1 SUBSEP $2
			bpkg[key] = $1; bname[key] = $2; ballocs[key] = $3 + 0; bbytes[key] = $4 + 0
			baselined++
			next
		}
		{
			key = $1 SUBSEP $2
			if (!(key in ballocs)) {
				printf "ignored     %s %s: present in output, absent from the baseline (%d allocs/op, %d B/op)\n",
					$1, $2, $3, $4
				ignored++
				next
			}
			saw[key] = 1
			failures += judge($1, $2, "allocs/op", ballocs[key], $3 + 0, allocs_tol)
			failures += judge($1, $2, "B/op", bbytes[key], $4 + 0, bytes_tol)
			gated++
		}
		END {
			if (malformed > 0) {
				printf "\nFAIL: %d malformed baseline row(s); refusing to gate against a baseline it cannot parse.\n", malformed
				exit 2
			}
			for (key in ballocs) {
				if (key in saw) continue
				printf "MISSING     %s\n            %s\n", bpkg[key], bname[key]
				printf "            baselined but absent from the benchmark output; deleting or renaming a gated benchmark must be a deliberate --update\n"
				failures++
			}
			# The vacuity backstop. Every path above can only report on rows it
			# actually compared, so a baseline that yields none would otherwise
			# print a cheerful OK having gated nothing. This asserts the
			# positive — work was done — rather than trusting the absence of
			# complaints, and it stays correct even if the two places that
			# parse this file ever drift apart.
			if (baselined == 0) {
				printf "\nFAIL: the baseline contains no usable rows; refusing to pass vacuously.\n"
				exit 2
			}
			if (gated == 0) {
				printf "\nFAIL: %d baselined benchmark(s) but ZERO were compared; refusing to pass vacuously.\n", baselined
				exit 2
			}
			if (failures > 0) {
				printf "\nFAIL: %d allocation gate violation(s) across %d gated benchmark(s).\n", failures, gated
				printf "If the change is genuine — in either direction — say why in the PR and run scripts/bench.sh --update.\n"
				exit 1
			}
			printf "\nOK: %d gated benchmark(s) within tolerance (allocs/op +/-%s%%, B/op +/-%s%%); %d run but NOT gated (unlisted benchmarks cannot fail this gate).\n",
				gated, allocs_tol, bytes_tol, ignored + 0
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
