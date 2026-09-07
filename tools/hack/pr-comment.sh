#!/usr/bin/env bash
#
# Post (or update) one pull request comment from the files given as
# arguments, concatenated in order.
#
# The comment carries a hidden marker and is rewritten in place on every run,
# so a pull request that is pushed to ten times ends up with one current
# comment rather than ten stale ones.
#
# Usage: tools/hack/pr-comment.sh report-a.md report-b.md ...
#
# Environment (all supplied by GitHub Actions):
#   GITHUB_TOKEN       token with pull-requests: write
#   GITHUB_REPOSITORY  owner/name
#   PR_NUMBER          pull request to comment on
#   COMMENT_MARKER     identifies this comment (default: <!-- ci-report -->)

set -euo pipefail

: "${GITHUB_TOKEN:?GITHUB_TOKEN is required}"
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
: "${PR_NUMBER:?PR_NUMBER is required}"
MARKER=${COMMENT_MARKER:-'<!-- ci-report -->'}
API="${GITHUB_API_URL:-https://api.github.com}/repos/$GITHUB_REPOSITORY"

if [[ $# -eq 0 ]]; then
	echo "usage: $0 <markdown-file>..." >&2
	exit 1
fi

body_file=$(mktemp)
trap 'rm -f "$body_file"' EXIT

{
	echo "$MARKER"
	echo "## CI report"
	echo
	for f in "$@"; do
		# Whitespace, not size, decides whether a section is really there: a
		# job that failed before writing its report still hands this script a
		# file holding a single newline.
		if [[ -f "$f" ]] && grep -q '[^[:space:]]' "$f"; then
			cat "$f"
		else
			# Saying a section is missing beats a comment that quietly omits
			# it and reads as if everything passed.
			echo "### $(basename "$f" .md)"
			echo
			echo "_Not produced: the job that writes this section did not get that far._"
		fi
		echo
	done
} >"$body_file"

api() {
	local method=$1 url=$2
	shift 2
	curl -sS --fail-with-body -X "$method" \
		-H "Authorization: Bearer $GITHUB_TOKEN" \
		-H "Accept: application/vnd.github+json" \
		-H "X-GitHub-Api-Version: 2022-11-28" \
		"$url" "$@"
}

existing=$(api GET "$API/issues/$PR_NUMBER/comments?per_page=100" |
	MARKER="$MARKER" python3 -c '
import json, os, sys
marker = os.environ["MARKER"]
for c in json.load(sys.stdin):
    if marker in (c.get("body") or ""):
        print(c["id"])
        break
')

payload=$(python3 -c 'import json,sys; print(json.dumps({"body": sys.stdin.read()}))' <"$body_file")

if [[ -n "$existing" ]]; then
	echo "updating comment $existing"
	api PATCH "$API/issues/comments/$existing" -d "$payload" >/dev/null
else
	echo "creating a new comment on #$PR_NUMBER"
	api POST "$API/issues/$PR_NUMBER/comments" -d "$payload" >/dev/null
fi
