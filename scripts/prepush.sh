#!/usr/bin/env bash
# Local pre-push gate: timed, fail-fast stages of Make targets.
#
# Stages run in order and a failed stage stops the gate. Phases inside a stage
# are independent and run concurrently, at most PREPUSH_JOBS at a time
# (default 4; 1 runs them serially in the listed order with live output).
# Every test runs once: the hermetic coverage pass (CGO disabled, microphone
# stub) both proves the tests pass and feeds the coverage gate, and
# test-cgo-delta runs natively only the packages whose files differ in the
# native cgo build. PREPUSH_SCOPE=changed (default) limits that test stage to
# the packages affected by changes since COVERAGE_BASE; PREPUSH_SCOPE=full
# runs every package like CI.
#
# Each phase is "NAME" or "NAME VAR=value..." where NAME is an existing Make
# target, so the target keeps its normal configuration and diagnostics.

set -uo pipefail

scope="${PREPUSH_SCOPE:-changed}"
case "$scope" in
changed | full) ;;
*) echo "prepush: PREPUSH_SCOPE must be changed or full, got '$scope'" >&2; exit 2 ;;
esac
jobs="${PREPUSH_JOBS:-4}"
case "$jobs" in
'' | *[!0-9]* | 0) echo "prepush: PREPUSH_JOBS must be a positive integer, got '$jobs'" >&2; exit 2 ;;
esac

# Keep these lists synchronized with the local gate contract
# (factory/scripts/tests/test_prepush_target.py). Longest phases first.
readonly stage_format=("fmt")
stage_static=(
	"lint"
	"verify-architecture"
	"staticcheck"
	"vet"
	"build"
	"coverage-registration"
	"check-ci-test-partition"
)

# changed_since_base PATTERN: whether a committed, staged, unstaged or
# untracked path matching the extended regex PATTERN differs from the merge
# base with COVERAGE_BASE (default origin/main). Unknown bases count as changed.
changed_since_base() {
	local base merge_base
	base="${COVERAGE_BASE:-origin/main}"
	merge_base="$(git merge-base "$base" HEAD 2>/dev/null)" || return 0
	{
		git diff --name-only "$merge_base" HEAD
		git diff --name-only HEAD
		git ls-files --others --exclude-standard
	} 2>/dev/null | grep -Eq "$1"
}

# The factory script tests (a CI unit check) only exercise factory/ and the
# Make targets they drive; the changed scope skips them when neither changed.
# PREPUSH_FACTORY_SCRIPTS=always|never overrides that choice.
case "${PREPUSH_FACTORY_SCRIPTS:-auto}" in
always) stage_static+=("test-factory-scripts") ;;
never) ;;
*)
	if [ "$scope" = full ] || changed_since_base '^(factory/|Makefile$)'; then
		stage_static+=("test-factory-scripts")
	fi
	;;
esac
readonly stage_static
# test-tools runs a real golangci-lint (its working-tree snapshot test), which
# holds golangci-lint's machine-wide lock, so it must not overlap `lint`.
readonly stage_tests=(
	"coverage COVERAGE_SCOPE=$scope"
	"test-cgo-delta"
	"test-tools TEST_TOOLS_ARCHITECTURE_GATE=0"
)

make_command="${PREPUSH_MAKE:-make}"
run_started=$SECONDS
failed_phase=""
failed_status=0
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

phase_name() { printf '%s' "${1%% *}"; }

# Phase result cache: a phase that passed for exactly this content (the
# working tree including untracked files, the COVERAGE_BASE commit, the Go
# toolchain and platform) is not run again, so re-running the gate after a
# flaky or unrelated failure repeats only the phases that have not passed.
# PREPUSH_CACHE=0 disables it; PREPUSH_CACHE_DIR moves it.
cache_dir=""
content_key=""
if [ "${PREPUSH_CACHE:-1}" = 1 ]; then
	cache_dir="${PREPUSH_CACHE_DIR:-.cache/prepush}"
	content_key="$(
		set -e
		index="$work_dir/content-index"
		cp "$(git rev-parse --git-path index)" "$index"
		GIT_INDEX_FILE="$index" git add -A . >/dev/null 2>&1
		{
			GIT_INDEX_FILE="$index" git write-tree
			git rev-parse "${COVERAGE_BASE:-origin/main}" 2>/dev/null || echo "no-base"
			go version 2>/dev/null || true
			uname -sm
		} | shasum -a 256 | cut -d' ' -f1
	)" || content_key=""
	if [ -z "$content_key" ] || ! mkdir -p "$cache_dir"; then
		cache_dir=""
	fi
fi

cache_marker() {
	printf '%s/%s' "$cache_dir" "$(printf '%s\n%s\n%s\n' "$content_key" "$scope" "$1" | shasum -a 256 | cut -d' ' -f1)"
}

# uncached_phases PHASE...: print (NUL-separated) the phases without a pass
# recorded for this content, reporting the others as cached.
uncached_phases() {
	local spec
	for spec in "$@"; do
		if [ -n "$cache_dir" ] && [ -f "$(cache_marker "$spec")" ]; then
			echo "==> prepush phase $(phase_name "$spec") cached: passed for this content" >&2
		else
			printf '%s\0' "$spec"
		fi
	done
}

# run_phase SPEC LOG_OR_EMPTY: run one phase, timing it; with a log file the
# output is captured for printing as one block.
run_phase() {
	local spec="$1" log="$2" name started status=0
	name="$(phase_name "$spec")"
	started=$SECONDS
	local arguments=()
	read -r -a arguments <<<"$spec"
	# Make variables go before the target so the target stays the last argument.
	local target="${arguments[0]}"
	arguments=("${arguments[@]:1}")
	if [ -n "$log" ]; then
		"$make_command" --no-print-directory ${arguments[@]+"${arguments[@]}"} "$target" >"$log" 2>&1 || status=$?
	else
		"$make_command" --no-print-directory ${arguments[@]+"${arguments[@]}"} "$target" || status=$?
	fi
	if [ "$status" = 0 ] && [ -n "$cache_dir" ]; then
		: >"$(cache_marker "$spec")" || true
	fi
	echo "$status $((SECONDS - started))" >"$work_dir/$name.status"
	return "$status"
}

record_result() {
	local name="$1" result status elapsed
	result="$(cat "$work_dir/$name.status")"
	status="${result%% *}"
	elapsed="${result#* }"
	echo "==> prepush phase $name completed in ${elapsed}s"
	if [ "$status" != 0 ] && [ -z "$failed_phase" ]; then
		failed_phase="$name"
		failed_status="$status"
	fi
}

# run_stage PHASE...: run the phases at most $jobs at a time; stop launching
# new phases after a failure and wait for the running ones.
run_stage() {
	local specs=("$@") next=0 running=() pids=() remaining=() index name
	if [ "$jobs" -eq 1 ] || [ "${#specs[@]}" -eq 1 ]; then
		for index in "${!specs[@]}"; do
			name="$(phase_name "${specs[$index]}")"
			echo "==> prepush phase: $name"
			run_phase "${specs[$index]}" ""
			record_result "$name"
			[ -z "$failed_phase" ] || return
		done
		return
	fi
	while [ "$next" -lt "${#specs[@]}" ] || [ "${#running[@]}" -gt 0 ]; do
		while [ -z "$failed_phase" ] && [ "$next" -lt "${#specs[@]}" ] && [ "${#running[@]}" -lt "$jobs" ]; do
			name="$(phase_name "${specs[$next]}")"
			echo "==> prepush phase: $name (started)"
			run_phase "${specs[$next]}" "$work_dir/$name.log" &
			pids[next]=$!
			running+=("$next")
			next=$((next + 1))
		done
		if [ -n "$failed_phase" ]; then
			next="${#specs[@]}"
		fi
		[ "${#running[@]}" -gt 0 ] || break
		sleep 0.2
		remaining=()
		for index in "${running[@]}"; do
			name="$(phase_name "${specs[$index]}")"
			if [ -f "$work_dir/$name.status" ]; then
				wait "${pids[$index]}" 2>/dev/null || true
				cat "$work_dir/$name.log"
				record_result "$name"
			else
				remaining+=("$index")
			fi
		done
		running=(${remaining[@]+"${remaining[@]}"})
	done
}

echo "==> prepush starting (scope $scope, at most $jobs phase(s) at once)"
for stage in stage_format stage_static stage_tests; do
	eval "phases=(\"\${${stage}[@]}\")"
	pending=()
	while IFS= read -r -d '' spec; do
		pending+=("$spec")
	done < <(uncached_phases "${phases[@]}")
	if [ "${#pending[@]}" -gt 0 ]; then
		run_stage "${pending[@]}"
	fi
	if [ -n "$failed_phase" ]; then
		break
	fi
done

run_elapsed=$((SECONDS - run_started))
if [[ -n "$failed_phase" ]]; then
	echo "==> prepush failed at phase $failed_phase (exit $failed_status)" >&2
	echo "==> prepush total completed in ${run_elapsed}s" >&2
	exit "$failed_status"
fi

echo "==> prepush passed"
echo "==> prepush total completed in ${run_elapsed}s"
