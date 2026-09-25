#!/usr/bin/env bash
# Run one Go test package as N disjoint shards of its top-level tests.
#
# The package test binary is compiled once, its top-level tests are listed
# and assigned to shards 1..N, and each selected shard runs as its own
# process of that binary. Every listed test is owned by exactly one shard, so
# running all shards executes the same corpus as `go test <package>`.
# Assignment is greedy longest-first over --weights (lines "TestName seconds";
# unlisted tests get the mean weight), so shards finish close together;
# without weights it is round-robin in sorted order.
#
# Usage:
#   scripts/go-test-shards.sh --dir MODULE_DIR --package PKG --shards N \
#     [--shard K] [--jobs J] [--weights FILE] [--cover-prefix PATH] [--go GO] \
#     [--shared-dir-env NAME] [--build-flag FLAG]... [-- TEST_BINARY_FLAGS...]
#
# Without --shard every shard runs, at most J at a time (default: all); with
# --shard only shard K runs (used by CI matrix jobs). With --cover-prefix,
# shard K writes PATH-K.out (the binary must be built with coverage via
# --build-flag). With --shared-dir-env, every run of the test binary sees
# NAME set to one scratch directory, and the binary first runs once with no
# tests selected so its TestMain can prepare shared state (e.g. build helper
# binaries) there before the shards start. A passing shard prints its tests
# slower than SLOW_TEST_SECONDS (default 5) and its summed test time.
set -euo pipefail

usage() {
	sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//' >&2
	exit 2
}

module_dir=""
package=""
shards=""
only_shard=""
jobs=""
weights=""
cover_prefix=""
shared_dir_env=""
go_binary="${GO:-go}"
build_flags=()
run_flags=()

while [ "$#" -gt 0 ]; do
	case "$1" in
	--dir) module_dir="$2"; shift 2 ;;
	--package) package="$2"; shift 2 ;;
	--shards) shards="$2"; shift 2 ;;
	--shard) only_shard="$2"; shift 2 ;;
	--jobs) jobs="$2"; shift 2 ;;
	--weights) weights="$2"; shift 2 ;;
	--cover-prefix) cover_prefix="$2"; shift 2 ;;
	--shared-dir-env) shared_dir_env="$2"; shift 2 ;;
	--go) go_binary="$2"; shift 2 ;;
	--build-flag) build_flags+=("$2"); shift 2 ;;
	--) shift; run_flags=("$@"); break ;;
	*) echo "go-test-shards: unknown argument $1" >&2; usage ;;
	esac
done

require_positive() {
	case "$2" in
	'' | *[!0-9]* | 0) echo "go-test-shards: $1 must be a positive integer, got '$2'" >&2; exit 2 ;;
	esac
}

if [ -z "$module_dir" ] || [ -z "$package" ] || [ -z "$shards" ]; then
	usage
fi
require_positive --shards "$shards"
jobs="${jobs:-$shards}"
require_positive --jobs "$jobs"
if [ -n "$only_shard" ]; then
	require_positive --shard "$only_shard"
	if [ "$only_shard" -gt "$shards" ]; then
		echo "go-test-shards: --shard $only_shard exceeds --shards $shards" >&2
		exit 2
	fi
fi
absolute() {
	case "$1" in
	/*) printf '%s' "$1" ;;
	*) printf '%s/%s' "$(pwd)" "$1" ;;
	esac
}
if [ -n "$weights" ]; then
	if [ ! -f "$weights" ]; then
		echo "go-test-shards: --weights file $weights does not exist" >&2
		exit 2
	fi
	weights="$(absolute "$weights")"
fi
if [ -n "$cover_prefix" ]; then
	cover_prefix="$(absolute "$cover_prefix")"
fi

work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT
test_binary="$work_dir/package.test"

package_dir="$(cd "$module_dir" && "$go_binary" list -f '{{.Dir}}' "$package")"
echo "==> go-test-shards compiling $module_dir/$package once for $shards shard(s)"
phase_started=$SECONDS
(cd "$module_dir" && "$go_binary" test -c -o "$test_binary" ${build_flags[@]+"${build_flags[@]}"} "$package")
echo "==> go-test-shards compiled in $((SECONDS - phase_started))s"

# -test.list runs TestMain, so list from the package directory like a run.
tests_file="$work_dir/tests"
(cd "$package_dir" && GOCOVERDIR="$work_dir" "$test_binary" -test.list '^Test') | grep -E '^Test[A-Za-z0-9_]*$' | LC_ALL=C sort >"$tests_file" || true
if [ ! -s "$tests_file" ]; then
	echo "go-test-shards: $package lists no top-level tests" >&2
	exit 1
fi

if [ -n "$shared_dir_env" ]; then
	mkdir "$work_dir/shared"
	export "$shared_dir_env=$work_dir/shared"
	echo "==> go-test-shards preparing shared state in \$$shared_dir_env"
	phase_started=$SECONDS
	(cd "$package_dir" && GOCOVERDIR="$work_dir" "$test_binary" -test.run '^$' ${run_flags[@]+"${run_flags[@]}"}) >"$work_dir/prepare.log" 2>&1 || {
		cat "$work_dir/prepare.log"
		exit 1
	}
	echo "==> go-test-shards prepared shared state in $((SECONDS - phase_started))s"
fi

# Assign every test to exactly one shard: heaviest first onto the
# least-loaded shard; ties go to the earlier name and the lower shard index.
assignment="$work_dir/assignment"
awk -v shards="$shards" -v weights="$weights" '
	BEGIN {
		if (weights != "") {
			while ((getline line < weights) > 0) {
				split(line, field, /[ \t]+/)
				if (field[1] ~ /^Test/) { known[field[1]] = field[2] + 0; sum += field[2] + 0; count++ }
			}
		}
		fallback = count > 0 ? sum / count : 1
	}
	{ name[NR] = $0; weight[NR] = ($0 in known) ? known[$0] : fallback; order[NR] = NR }
	END {
		for (i = 2; i <= NR; i++) {
			current = order[i]
			for (j = i - 1; j >= 1; j--) {
				previous = order[j]
				if (weight[previous] > weight[current] || (weight[previous] == weight[current] && name[previous] < name[current])) break
				order[j + 1] = previous
			}
			order[j + 1] = current
		}
		for (s = 1; s <= shards; s++) load[s] = 0
		for (k = 1; k <= NR; k++) {
			best = 1
			for (s = 2; s <= shards; s++) if (load[s] < load[best]) best = s
			load[best] += weight[order[k]]
			print name[order[k]] "\t" best
		}
	}
' "$tests_file" >"$assignment"

shard_pattern() {
	local names
	names="$(awk -v shard="$1" -F '\t' '$2 == shard { print $1 }' "$assignment" | LC_ALL=C sort | paste -sd '|' -)"
	# An empty shard selects nothing rather than everything.
	printf '^(%s)$' "${names:-NoTestsInThisShard}"
}

run_shard() {
	local shard="$1"
	local flags=(-test.run "$(shard_pattern "$shard")")
	if [ -n "$cover_prefix" ]; then
		flags+=(-test.coverprofile "$cover_prefix-$shard.out")
	fi
	# Run verbosely into a log: a failing shard prints the whole log, a passing
	# shard prints only its result lines, its slow tests, and a time summary.
	local log="$work_dir/shard-$shard.verbose.log" status=0 started=$SECONDS
	(cd "$package_dir" && "$test_binary" -test.v "${flags[@]}" ${run_flags[@]+"${run_flags[@]}"}) >"$log" 2>&1 || status=$?
	if [ "$status" -ne 0 ]; then
		cat "$log"
		return "$status"
	fi
	# Parallel subtests report after their parent, so a parent's own duration
	# can read 0.00s; the wall time below is the shard's real cost.
	awk -v slow="${SLOW_TEST_SECONDS:-5}" -v wall=$((SECONDS - started)) '
		/^--- (PASS|SKIP): / {
			duration = $NF
			gsub(/[()s]/, "", duration)
			tests++
			total += duration
			if (duration + 0 >= slow) print "slow test: " $3 " " $NF
			next
		}
		/^(PASS|FAIL|ok|coverage:)/ { print }
		END { printf "shard summary: %d top-level tests, %.1fs summed top-level test time, %ds wall\n", tests, total, wall }
	' "$log"
}

if [ -n "$only_shard" ]; then
	echo "==> go-test-shards $package shard $only_shard/$shards"
	run_shard "$only_shard"
	exit $?
fi

# Run every shard, at most $jobs at a time, and report them in shard order.
pids=()
status=0
reported=0
report_next() {
	local shard=$((reported + 1)) shard_status=0
	wait "${pids[$reported]}" || shard_status=$?
	if [ "$shard_status" -ne 0 ]; then
		status=1
	fi
	echo "==> go-test-shards $package shard $shard/$shards (exit $shard_status)"
	cat "$work_dir/shard-$shard.log"
	reported=$((reported + 1))
}
for ((shard = 1; shard <= shards; shard++)); do
	if [ $((${#pids[@]} - reported)) -ge "$jobs" ]; then
		report_next
	fi
	run_shard "$shard" >"$work_dir/shard-$shard.log" 2>&1 &
	pids+=("$!")
done
while [ "$reported" -lt "${#pids[@]}" ]; do
	report_next
done
exit "$status"
