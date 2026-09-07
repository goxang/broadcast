#!/usr/bin/env bash
#
# Compare benchmark results between HEAD and a base ref.
#
# Two numbers come out of a Go benchmark and they need different treatment.
#
# Allocation counts are deterministic: the same code allocates the same
# number of objects every run, so any increase is a real change and fails
# immediately.
#
# Wall time is not deterministic on a shared runner, and three things are
# done about that. The two sides are run alternately, round by round, so a
# runner that slows down halfway through slows both sides equally instead of
# only the one that ran second. Both are built with -trimpath, without which
# each side carries its own build directory and the resulting shift in code
# and data made identical source differ by up to 10% on the smallest
# benchmarks — a difference this would otherwise have blamed on the change
# under review. And a benchmark that still looks slower is re-measured on its
# own with a much longer sampling time before it is allowed to fail the
# build.
#
# The last part is what makes a percentage gate honest. A gate that fires on
# noise gets ignored, which is worse than not having one.
#
# Usage: tools/hack/go-benchmark-compare.sh
#
# Environment:
#   BASE_REF             git ref to compare against (default: origin/main)
#   BENCH_PKGS           packages to benchmark (default: ./...)
#   BENCH_PATTERN        benchmarks to run (default: .)
#   BENCH_COUNT          rounds per side, quick pass (default: 6)
#   BENCH_TIME           -benchtime per benchmark, quick pass (default: 200ms)
#   BENCH_CONFIRM_COUNT  rounds per side when re-measuring (default: 10)
#   BENCH_CONFIRM_TIME   -benchtime when re-measuring (default: 1s)
#   BENCH_CONFIRM_MAX    most benchmarks to re-measure, worst first (default: 8)
#   BENCH_MAX_SLOWDOWN   percent of wall-time regression tolerated (default: 5)
#   BENCH_MIN_DELTA_NS   ns a regression must also exceed (default: 1)
#   BENCH_REPORT         if set, a markdown summary is written to this path
#
# Exit codes: 0 no regression, 1 execution error, 2 regression detected.

set -euo pipefail

BASE_REF=${BASE_REF:-origin/main}
BENCH_PKGS=${BENCH_PKGS:-./...}
BENCH_PATTERN=${BENCH_PATTERN:-.}
BENCH_COUNT=${BENCH_COUNT:-6}
BENCH_TIME=${BENCH_TIME:-200ms}
BENCH_CONFIRM_COUNT=${BENCH_CONFIRM_COUNT:-10}
BENCH_CONFIRM_TIME=${BENCH_CONFIRM_TIME:-1s}
BENCH_CONFIRM_MAX=${BENCH_CONFIRM_MAX:-8}
BENCH_MAX_SLOWDOWN=${BENCH_MAX_SLOWDOWN:-5}
BENCH_MIN_DELTA_NS=${BENCH_MIN_DELTA_NS:-1}
BENCH_REPORT=${BENCH_REPORT:-}
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

cd "$REPO_ROOT"

log() { echo "[bench] $*"; }

if ! git rev-parse --verify --quiet "${BASE_REF}^{commit}" >/dev/null; then
	log "ERROR: base ref $BASE_REF not found; the checkout needs history (fetch-depth: 0)"
	exit 1
fi

work_dir=$(mktemp -d)
base_dir="$work_dir/base"
cleanup() {
	local code=$?
	git worktree remove --force "$base_dir" 2>/dev/null || true
	rm -rf "$work_dir"
	exit $code
}
trap cleanup EXIT INT TERM

# The base ref is checked out into its own worktree rather than over the top
# of the current one. HEAD stays where it is, so an interrupted run cannot
# strand the caller on the wrong commit, and uncommitted work is left alone.
log "checking out $BASE_REF ($(git rev-parse --short "$BASE_REF")) into a worktree"
git worktree add --detach --quiet "$base_dir" "$BASE_REF"

# Warm both build caches before any measurement, so the first round is not
# paying for a compile the later rounds get for free.
log "building both sides"
go test -trimpath -run='^$' -bench='^$' -count=1 "$BENCH_PKGS" >/dev/null
(cd "$base_dir" && go test -trimpath -run='^$' -bench='^$' -count=1 "$BENCH_PKGS" >/dev/null)

# Reduce a raw benchmark log to "name mean_ns allocs".
#
# The mean, not the fastest round and not the median. Measured against two
# byte-identical binaries, where the right answer is zero, the mean was wrong
# by at most 4.9%, the median by 13.2% and the fastest round by 11.6%: both
# order statistics pick an extreme out of ten samples, and with a spread this
# wide an extreme moves around far more than the average of the same ten does.
#
# Allocation counts are deterministic, so every round reports the same one.
summarize() {
	awk '
	/^Benchmark/ {
		name = $1
		sub(/-[0-9]+$/, "", name)
		for (i = 1; i <= NF; i++) {
			if ($(i+1) == "ns/op")     { total[name] += $i; rounds[name]++ }
			if ($(i+1) == "allocs/op") al[name] = $i + 0
		}
	}
	END {
		for (name in total) printf "%s %.4f %.0f\n", name, total[name] / rounds[name], al[name]
	}
	' "$1" | sort
}

# measure <pattern> <rounds> <benchtime> <out-prefix>
# Runs both sides alternately and writes <out-prefix>.{base,head}.sum.
measure() {
	local pattern=$1 rounds=$2 benchtime=$3 prefix=$4
	local side round dir

	: >"$prefix.base.log"
	: >"$prefix.head.log"
	for ((round = 1; round <= rounds; round++)); do
		log "round $round/$rounds"
		for side in head base; do
			if [[ $side == head ]]; then dir=$REPO_ROOT; else dir=$base_dir; fi
			if ! (cd "$dir" && go test -trimpath -run='^$' -bench="$pattern" \
				-benchmem -benchtime="$benchtime" -count=1 "$BENCH_PKGS") \
				>>"$prefix.$side.log" 2>&1; then
				log "ERROR: benchmarks failed on $side"
				tail -30 "$prefix.$side.log"
				exit 1
			fi
		done
	done

	summarize "$prefix.base.log" >"$prefix.base.sum"
	summarize "$prefix.head.log" >"$prefix.head.sum"
}

log "quick pass: $BENCH_COUNT rounds per side at -benchtime=$BENCH_TIME"
measure "$BENCH_PATTERN" "$BENCH_COUNT" "$BENCH_TIME" "$work_dir/quick"

if [[ ! -s "$work_dir/quick.head.sum" ]]; then
	log "ERROR: no benchmarks matched $BENCH_PATTERN on HEAD"
	exit 1
fi

cp "$work_dir/quick.base.sum" "$work_dir/base.sum"
cp "$work_dir/quick.head.sum" "$work_dir/head.sum"

# Anything that looks slower gets re-measured, worst first and no more than
# BENCH_CONFIRM_MAX of them, which keeps the job's worst case bounded: a
# genuinely slow runner can otherwise nominate every benchmark. If more than
# that many regressed, the build is failing regardless and the worst handful
# make the point.
#
# Allocation regressions are not re-measured: the count does not move between
# runs, so a second opinion would say the same thing more slowly.
join -j 1 "$work_dir/base.sum" "$work_dir/head.sum" |
	awk -v maxslow="$BENCH_MAX_SLOWDOWN" -v minns="$BENCH_MIN_DELTA_NS" '
		$2 > 0 {
			delta = ($4 - $2) / $2 * 100
			if (delta > maxslow && $4 - $2 > minns) printf "%.4f %s\n", delta, $1
		}
	' | sort -rn | head -n "$BENCH_CONFIRM_MAX" | cut -d' ' -f2 | sort >"$work_dir/suspects"

confirmed=""
if [[ -s "$work_dir/suspects" ]]; then
	confirmed=$(tr '\n' ' ' <"$work_dir/suspects")
	log "re-measuring at -benchtime=$BENCH_CONFIRM_TIME: $confirmed"
	pattern="^($(paste -sd'|' "$work_dir/suspects"))\$"
	measure "$pattern" "$BENCH_CONFIRM_COUNT" "$BENCH_CONFIRM_TIME" "$work_dir/confirm"

	# The longer measurement wins wherever it exists; everything else keeps
	# its quick-pass number.
	for side in base head; do
		awk '
			FNR == NR { best[$1] = $0; next }
			!($1 in best) { best[$1] = $0 }
			END { for (k in best) print best[k] }
		' "$work_dir/confirm.$side.sum" "$work_dir/quick.$side.sum" |
			sort >"$work_dir/$side.sum"
	done
fi

report_body="$work_dir/report.txt"

# One awk pass produces both outputs: the table that lands in the CI log, and
# the diff-fenced block the pull request comment renders. A diff fence is the
# only thing that colours a line on GitHub, so rows are prefixed by whether
# they are good news (+, green) or bad news (-, red) rather than by the sign
# of the number, which for wall time points the other way.
set +e
join -j 1 "$work_dir/base.sum" "$work_dir/head.sum" | awk \
	-v maxslow="$BENCH_MAX_SLOWDOWN" \
	-v minns="$BENCH_MIN_DELTA_NS" \
	-v report="$report_body" '
BEGIN {
	printf "%-38s %11s %11s %8s %8s %8s\n", \
		"benchmark", "base ns/op", "head ns/op", "delta", "base", "head"
	printf "  %-38s %10s %10s %8s %7s %7s\n", \
		"benchmark", "base ns", "head ns", "delta", "base", "head" >report
	printf "  %-38s %10s %10s %8s %7s %7s\n", \
		"", "", "", "", "allocs", "allocs" >report
}
{
	name = $1; bns = $2; bal = $3; hns = $4; hal = $5
	sub(/^Benchmark/, "", name)
	delta = (bns > 0) ? (hns - bns) / bns * 100 : 0

	# A wall-time verdict needs both a percentage and an absolute move. On an
	# 11ns benchmark a single nanosecond of jitter is 9%, so the percentage
	# alone would keep failing the smallest benchmarks for changes too small
	# to be worth anyone reading about.
	slower = (delta > maxslow && hns - bns > minns)
	faster = (delta < -maxslow && bns - hns > minns)

	# The two columns are judged separately and then combined: an allocation
	# increase is a regression at any wall time, and a wall-time blowout is a
	# regression at any allocation count.
	bad = (hal > bal) || slower
	good = (hal < bal) || faster
	mark = bad ? "-" : (good ? "+" : " ")
	if (bad) {
		failures++
		flag = "  REGRESSION (" ((hal > bal) ? "allocations" : "wall time") ")"
	} else {
		flag = good ? "  improved" : ""
	}

	printf "%-38s %11.1f %11.1f %+7.1f%% %8d %8d%s\n", name, bns, hns, delta, bal, hal, flag
	printf "%s %-38s %10.1f %10.1f %+7.1f%% %7d %7d\n", mark, name, bns, hns, delta, bal, hal >report
	rows++
}
END {
	if (rows == 0) print "  no benchmarks in common with the base ref" >report
	exit (failures > 0)
}
'
status=$?
set -e

if [[ $status -gt 1 ]]; then
	log "ERROR: comparison failed"
	exit 1
fi

# A benchmark that only exists on one side is not a regression, but one that
# disappeared is worth saying out loud.
comm -23 <(cut -d' ' -f1 "$work_dir/base.sum") <(cut -d' ' -f1 "$work_dir/head.sum") |
	while read -r gone; do log "NOTE: $gone exists on $BASE_REF but not on HEAD"; done

if [[ -n "$BENCH_REPORT" ]]; then
	{
		echo "### Benchmarks"
		echo
		echo "\`$BENCH_PKGS\` against \`$BASE_REF\`, mean of $BENCH_COUNT" \
			"interleaved rounds at \`-benchtime=$BENCH_TIME\`."
		echo "Green is better, red is a regression: allocations must not increase" \
			"and wall time must stay within ${BENCH_MAX_SLOWDOWN}%."
		echo
		echo '```diff'
		cat "$report_body"
		echo '```'
		if [[ -n "$confirmed" ]]; then
			echo
			echo "Re-measured at \`-benchtime=$BENCH_CONFIRM_TIME\` over" \
				"$BENCH_CONFIRM_COUNT rounds because the quick pass showed them slower:" \
				"\`${confirmed% }\`."
		fi
	} >"$BENCH_REPORT"
fi

if [[ $status -ne 0 ]]; then
	log "RESULT: regression against $BASE_REF; see the rows marked REGRESSION above"
	exit 2
fi

log "RESULT: no regressions against $BASE_REF (allocations, and wall time within ${BENCH_MAX_SLOWDOWN}%)"
