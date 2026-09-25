#!/usr/bin/env bash
# Run one Go test package as N disjoint shards of its top-level tests.
#
# The package test binary is compiled once, its top-level tests are listed
# and assigned round-robin (in sorted order) to shards 1..N, and each
# selected shard runs as its own process of that binary. Every listed test is
# owned by exactly one shard, so running all shards executes the same corpus
# as `go test <package>`.
#
# Usage:
#   scripts/go-test-shards.sh --dir MODULE_DIR --package PKG --shards N \
#     [--shard K] [--cover-prefix PATH] [--go GO] \
#     [--build-flag FLAG]... [-- TEST_BINARY_FLAGS...]
#
# Without --shard every shard runs concurrently; with --shard only shard K
# runs (used by CI matrix jobs). With --cover-prefix, shard K writes
# PATH-K.out (the binary must be built with coverage via --build-flag).
set -euo pipefail

usage() {
	sed -n '2,19p' "$0" | sed 's/^# \{0,1\}//' >&2
	exit 2
}

module_dir=""
package=""
shards=""
only_shard=""
cover_prefix=""
go_binary="${GO:-go}"
build_flags=()
run_flags=()

while [ "$#" -gt 0 ]; do
	case "$1" in
	--dir) module_dir="$2"; shift 2 ;;
	--package) package="$2"; shift 2 ;;
	--shards) shards="$2"; shift 2 ;;
	--shard) only_shard="$2"; shift 2 ;;
	--cover-prefix) cover_prefix="$2"; shift 2 ;;
	--go) go_binary="$2"; shift 2 ;;
	--build-flag) build_flags+=("$2"); shift 2 ;;
	--) shift; run_flags=("$@"); break ;;
	*) echo "go-test-shards: unknown argument $1" >&2; usage ;;
	esac
done

if [ -z "$module_dir" ] || [ -z "$package" ] || [ -z "$shards" ]; then
	usage
fi
case "$shards" in
'' | *[!0-9]* | 0) echo "go-test-shards: --shards must be a positive integer, got '$shards'" >&2; exit 2 ;;
esac
if [ -n "$only_shard" ]; then
	case "$only_shard" in
	'' | *[!0-9]* | 0) echo "go-test-shards: --shard must be a positive integer, got '$only_shard'" >&2; exit 2 ;;
	esac
	if [ "$only_shard" -gt "$shards" ]; then
		echo "go-test-shards: --shard $only_shard exceeds --shards $shards" >&2
		exit 2
	fi
fi
if [ -n "$cover_prefix" ]; then
	case "$cover_prefix" in
	/*) ;;
	*) cover_prefix="$(pwd)/$cover_prefix" ;;
	esac
fi

work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT
test_binary="$work_dir/package.test"

package_dir="$(cd "$module_dir" && "$go_binary" list -f '{{.Dir}}' "$package")"
echo "==> go-test-shards compiling $module_dir/$package once for $shards shard(s)"
(cd "$module_dir" && "$go_binary" test -c -o "$test_binary" ${build_flags[@]+"${build_flags[@]}"} "$package")

# -test.list runs TestMain, so list from the package directory like a run.
tests=()
while IFS= read -r name; do
	tests+=("$name")
done < <(cd "$package_dir" && GOCOVERDIR="$work_dir" "$test_binary" -test.list '^Test' | grep -E '^Test[A-Za-z0-9_]*$' | LC_ALL=C sort)
if [ "${#tests[@]}" -eq 0 ]; then
	echo "go-test-shards: $package lists no top-level tests" >&2
	exit 1
fi

shard_pattern() {
	local shard="$1" index names=""
	for index in "${!tests[@]}"; do
		if [ $((index % shards + 1)) -eq "$shard" ]; then
			names="${names:+$names|}${tests[$index]}"
		fi
	done
	# An empty shard selects nothing rather than everything.
	printf '^(%s)$' "${names:-NoTestsInThisShard}"
}

run_shard() {
	local shard="$1"
	local flags=(-test.run "$(shard_pattern "$shard")")
	if [ -n "$cover_prefix" ]; then
		flags+=(-test.coverprofile "$cover_prefix-$shard.out")
	fi
	(cd "$package_dir" && "$test_binary" "${flags[@]}" ${run_flags[@]+"${run_flags[@]}"})
}

if [ -n "$only_shard" ]; then
	selected=("$only_shard")
else
	selected=()
	for ((shard = 1; shard <= shards; shard++)); do
		selected+=("$shard")
	done
fi

pids=()
for shard in "${selected[@]}"; do
	if [ "${#selected[@]}" -eq 1 ]; then
		echo "==> go-test-shards $package shard $shard/$shards"
		run_shard "$shard"
		exit $?
	fi
	run_shard "$shard" >"$work_dir/shard-$shard.log" 2>&1 &
	pids+=("$!")
done

status=0
for index in "${!pids[@]}"; do
	shard="${selected[$index]}"
	if wait "${pids[$index]}"; then
		shard_status=0
	else
		shard_status=$?
		status=1
	fi
	echo "==> go-test-shards $package shard $shard/$shards (exit $shard_status)"
	cat "$work_dir/shard-$shard.log"
done
exit "$status"
