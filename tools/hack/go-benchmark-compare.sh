#!/usr/bin/env bash
#
# Compare benchmark results between HEAD and a base ref.
#
# Two numbers come out of a Go benchmark and they deserve different
# treatment. Allocation counts are deterministic: the same code allocates
# the same number of objects every run, so any increase is a real change and
# is treated as a failure. Wall time on a shared CI runner is not: neighbours
# on the same machine move it by double digits, so it is reported and never
# gated. Look at the ns/op column, do not let it block a merge.
#
# Usage: tools/hack/go-benchmark-compare.sh
#
# Environment:
#   BASE_REF     git ref to compare against (default: origin/main)
#   BENCH_COUNT  runs per benchmark (default: 6)
#
# Exit codes: 0 no allocation regression, 1 execution error,
#             2 allocation regression detected.

set -euo pipefail

BASE_REF=${BASE_REF:-origin/main}
BENCH_COUNT=${BENCH_COUNT:-6}
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

cd "$REPO_ROOT"

log() { echo "[bench] $*"; }

run_benchmarks() {
	local out=$1 what=$2
	log "running benchmarks on $what"
	if ! go test -run='^$' -bench=. -benchmem -count="$BENCH_COUNT" ./... >"$out" 2>&1; then
		log "ERROR: benchmarks failed on $what"
		cat "$out"
		return 1
	fi
}

if ! git rev-parse --verify --quiet "${BASE_REF}^{commit}" >/dev/null; then
	log "ERROR: base ref $BASE_REF not found; the checkout needs history (fetch-depth: 0)"
	exit 1
fi

if ! git diff --quiet HEAD -- 2>/dev/null; then
	log "ERROR: working tree has uncommitted changes to tracked files"
	log "       the comparison checks out $BASE_REF and would carry them along"
	exit 1
fi

head_commit=$(git rev-parse HEAD)
# Restore the branch, not the commit: checking the SHA back out would leave
# the caller on a detached HEAD, where the next commit belongs to no branch.
# In CI there is no branch to go back to, so fall back to the commit.
head_ref=$(git symbolic-ref --quiet --short HEAD || echo "$head_commit")
work_dir=$(mktemp -d)
restore() {
	local code=$?
	git checkout --quiet "$head_ref" 2>/dev/null || true
	rm -rf "$work_dir"
	exit $code
}
trap restore EXIT INT TERM

run_benchmarks "$work_dir/head.txt" "HEAD (${head_commit:0:8})"

log "switching to $BASE_REF"
git checkout --quiet "$BASE_REF"
run_benchmarks "$work_dir/base.txt" "$BASE_REF"
git checkout --quiet "$head_ref"

# Reduce each file to "name median_ns median_allocs". The median is the
# defensible summary of a handful of noisy runs: one slow run cannot drag it
# the way it drags a mean.
summarize() {
	awk '
	/^Benchmark/ {
		name = $1
		sub(/-[0-9]+$/, "", name)
		for (i = 1; i <= NF; i++) {
			if ($(i+1) == "ns/op")     { nc[name]++; ns[name SUBSEP nc[name]] = $i }
			if ($(i+1) == "allocs/op") { ac[name]++; al[name SUBSEP ac[name]] = $i }
		}
	}
	function median(arr, key, n,   i, j, t, v) {
		for (i = 1; i <= n; i++) v[i] = arr[key SUBSEP i]
		for (i = 1; i <= n; i++)
			for (j = i + 1; j <= n; j++)
				if (v[j] < v[i]) { t = v[i]; v[i] = v[j]; v[j] = t }
		return (n % 2) ? v[(n + 1) / 2] : (v[n / 2] + v[n / 2 + 1]) / 2
	}
	END {
		for (name in nc)
			printf "%s %.0f %.0f\n", name, median(ns, name, nc[name]), median(al, name, ac[name])
	}
	' "$1" | sort
}

summarize "$work_dir/base.txt" >"$work_dir/base.sum"
summarize "$work_dir/head.txt" >"$work_dir/head.sum"

if [[ ! -s "$work_dir/head.sum" ]]; then
	log "ERROR: no benchmarks were parsed from the HEAD run"
	exit 1
fi

echo
join -j 1 "$work_dir/base.sum" "$work_dir/head.sum" | awk '
BEGIN {
	printf "%-44s %12s %12s %8s   %7s %7s %6s\n", "benchmark", "base ns/op", "head ns/op", "delta", "base", "head", "allocs"
}
{
	name = $1; bns = $2; bal = $3; hns = $4; hal = $5
	dns = (bns > 0) ? (hns - bns) / bns * 100 : 0
	flag = ""
	if (hal > bal) { flag = "  REGRESSION"; bad++ }
	else if (hal < bal) flag = "  improved"
	printf "%-44s %12d %12d %+7.1f%%   %7d %7d %+6d%s\n", name, bns, hns, dns, bal, hal, hal - bal, flag
}
END { exit (bad > 0) }
' && status=0 || status=1
echo

# A benchmark that only exists on one side is not a regression, but a
# disappearing benchmark is worth saying out loud.
comm -23 <(cut -d' ' -f1 "$work_dir/base.sum") <(cut -d' ' -f1 "$work_dir/head.sum") |
	while read -r gone; do log "NOTE: $gone exists on $BASE_REF but not on HEAD"; done

if [[ $status -ne 0 ]]; then
	log "RESULT: allocations increased; see the rows marked REGRESSION above"
	exit 2
fi

log "RESULT: no allocation regressions against $BASE_REF"
