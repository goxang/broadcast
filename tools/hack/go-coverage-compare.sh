#!/usr/bin/env bash
#
# Compare test coverage between HEAD and a base ref.
#
# The absolute floor (make go.test.coverage) says whether the package is
# tested well enough. This says whether a change made it worse, which is the
# question a reviewer actually has in front of a diff. Both are useful: a
# change can sit comfortably above the floor and still delete half the tests
# for the code it touches.
#
# HEAD's profile is reused if it already exists, so this can follow the
# coverage run rather than repeat it.
#
# Usage: tools/hack/go-coverage-compare.sh
#
# Environment:
#   BASE_REF          git ref to compare against (default: origin/main)
#   COVERAGE_PKGS     packages to cover (default: ./...)
#   COVERAGE_PROFILE  HEAD's profile (default: coverage.out)
#   COVERAGE_MAX_DROP percentage points of coverage loss tolerated (default: 5)
#   COVERAGE_REPORT   if set, a markdown summary is written to this path
#
# Exit codes: 0 within tolerance, 1 execution error, 2 coverage dropped too far.

set -euo pipefail

BASE_REF=${BASE_REF:-origin/main}
COVERAGE_PKGS=${COVERAGE_PKGS:-./...}
COVERAGE_PROFILE=${COVERAGE_PROFILE:-coverage.out}
COVERAGE_MAX_DROP=${COVERAGE_MAX_DROP:-5}
COVERAGE_REPORT=${COVERAGE_REPORT:-}
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

cd "$REPO_ROOT"

log() { echo "[coverage] $*"; }

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

if [[ ! -s "$COVERAGE_PROFILE" ]]; then
	log "no $COVERAGE_PROFILE yet; running the suite on HEAD"
	go test -coverprofile="$COVERAGE_PROFILE" -covermode=atomic "$COVERAGE_PKGS" >/dev/null
fi

log "checking out $BASE_REF ($(git rev-parse --short "$BASE_REF")) into a worktree"
git worktree add --detach --quiet "$base_dir" "$BASE_REF"
(cd "$base_dir" && go test -coverprofile="$work_dir/base.out" -covermode=atomic "$COVERAGE_PKGS" >/dev/null)

# Reduce `go tool cover -func` to "file:func percent".
#
# The line number it prints is deliberately dropped: a function that moved
# down the file is not a coverage change, and keying on the line would report
# every one as if it were. The module path in front of the file name goes too,
# since it is the same on every row and long enough to push the numbers out of
# their columns in the pull request comment. The package directory stays: this
# module has several packages, and a bare file name would collide across them.
summarize() {
	go tool cover -func="$1" | awk '
		/^total:/ { printf "total: %s\n", $NF; next }
		NF >= 3 {
			split($1, loc, ":")
			n = split(loc[1], part, "/")
			file = (n > 1) ? part[n-1] "/" part[n] : part[n]
			printf "%s:%s %s\n", file, $2, $NF
		}
	' | sort
}

summarize "$work_dir/base.out" >"$work_dir/base.sum"
summarize "$COVERAGE_PROFILE" >"$work_dir/head.sum"

base_total=$(awk '/^total:/ { sub(/%/, "", $2); print $2 + 0 }' "$work_dir/base.sum")
head_total=$(awk '/^total:/ { sub(/%/, "", $2); print $2 + 0 }' "$work_dir/head.sum")
delta=$(awk -v b="$base_total" -v h="$head_total" 'BEGIN { printf "%.1f", h - b }')

printf '[coverage] base %s%%  head %s%%  delta %+.1f points\n' "$base_total" "$head_total" "$delta"

# Functions whose coverage moved. A function present on only one side is new
# or deleted, not a change in how well it is covered, so it is left out.
join -j 1 "$work_dir/base.sum" "$work_dir/head.sum" |
	awk '$1 != "total:" { sub(/%/, "", $2); sub(/%/, "", $3); if ($2 != $3) printf "%s %s %s\n", $1, $2, $3 }' \
		>"$work_dir/changed"

if [[ -n "$COVERAGE_REPORT" ]]; then
	{
		echo "### Coverage"
		echo
		echo "Statement coverage against \`$BASE_REF\`. Green is better;" \
			"a drop of more than ${COVERAGE_MAX_DROP} points is a failure."
		echo
		echo '```diff'
		printf '  %-46s %10s %10s %8s\n' "" "base" "head" "delta"
		awk -v b="$base_total" -v h="$head_total" -v d="$delta" -v drop="$COVERAGE_MAX_DROP" \
			'BEGIN {
				mark = (d > 0) ? "+" : ((d < -drop) ? "-" : ((d < 0) ? "-" : " "))
				printf "%s %-46s %9.1f%% %9.1f%% %+7.1f\n", mark, "total", b, h, d
			}'
		if [[ -s "$work_dir/changed" ]]; then
			echo
			awk '{
				d = $3 - $2
				mark = (d > 0) ? "+" : "-"
				printf "%s %-46s %9.1f%% %9.1f%% %+7.1f\n", mark, $1, $2, $3, d
			}' "$work_dir/changed" | head -20
		fi
		echo '```'
	} >"$COVERAGE_REPORT"
fi

if awk -v b="$base_total" -v h="$head_total" -v drop="$COVERAGE_MAX_DROP" \
	'BEGIN { exit !(b - h > drop) }'; then
	log "RESULT: coverage fell from ${base_total}% to ${head_total}%, more than the ${COVERAGE_MAX_DROP} point tolerance"
	exit 2
fi

log "RESULT: coverage within tolerance (${base_total}% -> ${head_total}%)"
