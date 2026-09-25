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
# A JOB_NAME ending in `*` is a prefix: it stands for every job whose name
# starts with the rest (e.g. the lanes of a matrix, "CI (static lint *"), each
# of which must succeed, and waits while no such job is listed yet. GitHub
# lists every job of a run (queued or not) once the run starts, so a matrix's
# lanes can be added, removed or merged without editing the waiter.
#
# A JOB_NAME (or prefix) that matches no job for --missing-timeout seconds
# (default 120) fails the wait: a renamed or removed sibling is a workflow
# error, not something to wait out for the whole --timeout.
#
# Usage: scripts/ci-await-jobs.sh [--timeout SECONDS] [--missing-timeout SECONDS] [--interval SECONDS] JOB_NAME...
# Env:   GH_TOKEN (actions: read), GITHUB_REPOSITORY, GITHUB_RUN_ID; GH (gh
#        binary, default gh).
set -euo pipefail

timeout=1500
missing_timeout=120
interval=5
while [ "$#" -gt 0 ]; do
	case "$1" in
	--timeout) timeout="$2"; shift 2 ;;
	--missing-timeout) missing_timeout="$2"; shift 2 ;;
	--interval) interval="$2"; shift 2 ;;
	--) shift; break ;;
	-*) echo "ci-await-jobs: unknown option $1" >&2; exit 2 ;;
	*) break ;;
	esac
done
if [ "$#" -eq 0 ]; then
	echo "usage: ci-await-jobs.sh [--timeout SECONDS] [--missing-timeout SECONDS] [--interval SECONDS] JOB_NAME..." >&2
	exit 2
fi
repo="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
run_id="${GITHUB_RUN_ID:?GITHUB_RUN_ID is required}"
gh="${GH:-gh}"

deadline=$((SECONDS + timeout))
missing_deadline=$((SECONDS + missing_timeout))
while :; do
	# One "name<TAB>status<TAB>conclusion<TAB>attempt" line per job.
	if ! listing="$("$gh" api --paginate "repos/$repo/actions/runs/$run_id/jobs?filter=latest&per_page=100" \
		--jq '.jobs[] | [.name, .status, (.conclusion // ""), (.run_attempt // 0 | tostring)] | @tsv')"; then
		echo "::warning::could not list the jobs of run $run_id; retrying"
		listing=""
	fi
	pending=()
	failed=()
	missing=()
	for pattern in "$@"; do
		# The newest attempt of each job with exactly this name, or of each
		# job whose name starts with the prefix of a pattern ending in `*`.
		rows="$(awk -F '\t' -v pattern="$pattern" '
			function matches(name) {
				if (substr(pattern, length(pattern)) == "*") return index(name, substr(pattern, 1, length(pattern) - 1)) == 1
				return name == pattern
			}
			matches($1) && (!($1 in best) || $4 + 0 >= best[$1]) { best[$1] = $4 + 0; row[$1] = $0 }
			END { for (name in row) print row[name] }' <<<"$listing")"
		if [ -z "$rows" ]; then
			pending+=("$pattern (not started)")
			missing+=("$pattern")
			continue
		fi
		while IFS= read -r row; do
			name="$(cut -f1 <<<"$row")"
			status="$(cut -f2 <<<"$row")"
			conclusion="$(cut -f3 <<<"$row")"
			if [ "$status" != completed ]; then
				pending+=("$name ($status)")
			elif [ "$conclusion" != success ]; then
				failed+=("$name ($conclusion)")
			fi
		done <<<"$rows"
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
	# A failed listing proves nothing about which jobs exist.
	if [ -n "$listing" ] && [ "${#missing[@]}" -gt 0 ] && [ "$SECONDS" -ge "$missing_deadline" ]; then
		echo "::error::no job matched after ${missing_timeout}s: ${missing[*]}"
		exit 1
	fi
	if [ "$SECONDS" -ge "$deadline" ]; then
		echo "::error::timed out after ${timeout}s waiting for: ${pending[*]}"
		exit 1
	fi
	echo "ci-await-jobs: waiting for ${pending[*]}"
	sleep "$interval"
done
