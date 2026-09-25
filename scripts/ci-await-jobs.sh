#!/usr/bin/env bash
# Wait until the named sibling jobs of this workflow run have finished, and
# fail unless every one of them succeeded.
#
# A job that must run a step after its siblings (the coverage gate needs every
# coverage profile) would otherwise need a separate `needs:` job, which pays a
# runner start, a checkout and a Go setup after the slowest sibling. Waiting
# at the end of a job that runs concurrently with them costs nothing when it
# is the slowest one itself. Jobs are read with filter=latest, so a re-run of
# failed jobs sees the newest attempt of each sibling.
#
# Usage: scripts/ci-await-jobs.sh [--timeout SECONDS] [--interval SECONDS] JOB_NAME...
# Env:   GH_TOKEN (actions: read), GITHUB_REPOSITORY, GITHUB_RUN_ID; GH (gh
#        binary, default gh).
set -euo pipefail

timeout=1500
interval=5
while [ "$#" -gt 0 ]; do
	case "$1" in
	--timeout) timeout="$2"; shift 2 ;;
	--interval) interval="$2"; shift 2 ;;
	--) shift; break ;;
	-*) echo "ci-await-jobs: unknown option $1" >&2; exit 2 ;;
	*) break ;;
	esac
done
if [ "$#" -eq 0 ]; then
	echo "usage: ci-await-jobs.sh [--timeout SECONDS] [--interval SECONDS] JOB_NAME..." >&2
	exit 2
fi
repo="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
run_id="${GITHUB_RUN_ID:?GITHUB_RUN_ID is required}"
gh="${GH:-gh}"

deadline=$((SECONDS + timeout))
while :; do
	# One "name<TAB>status<TAB>conclusion<TAB>attempt" line per job.
	if ! listing="$("$gh" api --paginate "repos/$repo/actions/runs/$run_id/jobs?filter=latest&per_page=100" \
		--jq '.jobs[] | [.name, .status, (.conclusion // ""), (.run_attempt // 0 | tostring)] | @tsv')"; then
		echo "::warning::could not list the jobs of run $run_id; retrying"
		listing=""
	fi
	pending=()
	failed=()
	for name in "$@"; do
		# The newest attempt of the job with exactly this name.
		row="$(awk -F '\t' -v name="$name" '$1 == name && $4 + 0 >= best { best = $4 + 0; row = $0 } END { print row }' <<<"$listing")"
		if [ -z "$row" ]; then
			pending+=("$name (not started)")
			continue
		fi
		status="$(cut -f2 <<<"$row")"
		conclusion="$(cut -f3 <<<"$row")"
		if [ "$status" != completed ]; then
			pending+=("$name ($status)")
		elif [ "$conclusion" != success ]; then
			failed+=("$name ($conclusion)")
		fi
	done
	if [ "${#failed[@]}" -gt 0 ]; then
		for job in "${failed[@]}"; do
			echo "::error::sibling job $job did not succeed"
		done
		exit 1
	fi
	if [ "${#pending[@]}" -eq 0 ]; then
		echo "ci-await-jobs: all $# sibling jobs succeeded"
		exit 0
	fi
	if [ "$SECONDS" -ge "$deadline" ]; then
		echo "::error::timed out after ${timeout}s waiting for: ${pending[*]}"
		exit 1
	fi
	echo "ci-await-jobs: waiting for ${pending[*]}"
	sleep "$interval"
done
