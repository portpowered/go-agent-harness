#!/usr/bin/env bash
# Run independent shell commands concurrently, at most J at a time.
#
# Usage:
#   scripts/run-bounded.sh [--jobs J] -- 'LABEL::COMMAND' ['LABEL::COMMAND']...
#
# Each COMMAND runs under `bash -c` with its output captured; when it finishes
# its output is printed as one block headed by its label, exit status and wall
# time, so concurrent jobs never interleave lines. Jobs start in argument
# order. The script waits for every job and exits non-zero if any failed.
set -uo pipefail

jobs=""
while [ "$#" -gt 0 ]; do
	case "$1" in
	--jobs) jobs="$2"; shift 2 ;;
	--) shift; break ;;
	*) echo "run-bounded: unknown argument $1" >&2; exit 2 ;;
	esac
done
if [ "$#" -eq 0 ]; then
	sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//' >&2
	exit 2
fi
jobs="${jobs:-$#}"
case "$jobs" in
'' | *[!0-9]* | 0) echo "run-bounded: --jobs must be a positive integer, got '$jobs'" >&2; exit 2 ;;
esac

work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

labels=()
commands=()
for spec in "$@"; do
	case "$spec" in
	*::*) ;;
	*) echo "run-bounded: job '$spec' is not LABEL::COMMAND" >&2; exit 2 ;;
	esac
	labels+=("${spec%%::*}")
	commands+=("${spec#*::}")
done

run_job() {
	local index="$1" started=$SECONDS status=0
	bash -c "${commands[$index]}" >"$work_dir/$index.log" 2>&1 || status=$?
	echo "$status $((SECONDS - started))" >"$work_dir/$index.status"
	return "$status"
}

status=0
running=()
report() {
	local index="$1" result
	result="$(cat "$work_dir/$index.status")"
	echo "==> [${labels[$index]}] exit ${result%% *} in ${result#* }s"
	cat "$work_dir/$index.log"
	if [ "${result%% *}" != 0 ]; then
		status=1
	fi
}
# wait -n is unavailable in macOS bash 3.2, so poll for finished jobs.
reap() {
	local remaining=() index
	for index in ${running[@]+"${running[@]}"}; do
		if [ ! -f "$work_dir/$index.status" ] && ! kill -0 "${pids[$index]}" 2>/dev/null; then
			# The job's subshell exited without recording a status (for
			# example it was killed); report it as failed instead of waiting
			# for a status file that will never appear.
			local exited=0
			wait "${pids[$index]}" 2>/dev/null || exited=$?
			if [ ! -f "$work_dir/$index.status" ]; then
				if [ "$exited" = 0 ]; then
					exited=1
				fi
				echo "$exited ?" >"$work_dir/$index.status"
			fi
		fi
		if [ -f "$work_dir/$index.status" ]; then
			wait "${pids[$index]}" 2>/dev/null || true
			report "$index"
		else
			remaining+=("$index")
		fi
	done
	running=(${remaining[@]+"${remaining[@]}"})
}
pids=()
for index in "${!commands[@]}"; do
	while [ "${#running[@]}" -ge "$jobs" ]; do
		reap
		if [ "${#running[@]}" -ge "$jobs" ]; then
			sleep 0.2
		fi
	done
	run_job "$index" &
	pids[$index]=$!
	running+=("$index")
done
while [ "${#running[@]}" -gt 0 ]; do
	reap
	if [ "${#running[@]}" -gt 0 ]; then
		sleep 0.2
	fi
done
exit "$status"
