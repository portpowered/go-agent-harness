#!/usr/bin/env bash
# Run `go test` over PACKAGES of MODULE, reusing Go's test result cache except
# for the module's packages listed in scripts/go-test-fresh-packages.txt,
# which always run (-count=1).
#
# Go's test cache replays a package's result when its test binary, cacheable
# flags, and the environment variables and in-module files the test read are
# unchanged. It cannot see files a test reads from another module of the
# workspace or the repository root, nor what a subprocess reads (e.g. `go
# list`), so a package whose tests depend on those must never be replayed.
# Such packages run in a second, concurrent `go test -count=1`; the other
# packages keep whatever count flag the caller passed (none, or
# GO_TEST_COUNT's -count=1 for a fully fresh run). With --coverprofile, the
# fresh run's profile is appended to FILE, so FILE holds every package's
# blocks as one invocation would have written them (the coverage gate merges
# repeated blocks).
#
# Usage: go-test-fresh-split.sh [--go GO] --module MODULE [--coverprofile FILE] PACKAGE... -- GO_TEST_FLAG...
# Run from MODULE's directory (MODULE is its path from the repository root);
# PACKAGE is a `go list` pattern relative to it. Listed packages outside
# PACKAGES are ignored.
set -euo pipefail

go_cmd=go
module=""
profile=""
while [ "$#" -gt 0 ]; do
	case "$1" in
	--go) go_cmd="$2"; shift 2 ;;
	--module) module="$2"; shift 2 ;;
	--coverprofile) profile="$2"; shift 2 ;;
	*) break ;;
	esac
done
packages=()
while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do
	packages+=("$1")
	shift
done
[ "$#" -eq 0 ] || shift
flags=("$@")
if [ -z "$module" ] || [ "${#packages[@]}" -eq 0 ]; then
	echo "go-test-fresh-split: --module and at least one package are required" >&2
	exit 2
fi
fresh="$(awk -v module="$module" '$1 == module { print $2 }' "$(dirname "$0")/go-test-fresh-packages.txt")"

# run_go_test PROFILE PACKAGE... -- EXTRA_FLAG...: go test with the caller's
# flags, writing PROFILE when coverage is on.
run_go_test() {
	local out="$1" args=()
	shift
	while [ "$1" != "--" ]; do
		args+=("$1")
		shift
	done
	shift
	args+=(${flags[@]+"${flags[@]}"} "$@")
	[ -z "$profile" ] || args+=("-coverprofile=$out")
	"$go_cmd" test "${args[@]}"
}

if [ -z "${fresh// /}" ]; then
	run_go_test "$profile" "${packages[@]}" --
	exit
fi

# `go list` must see the same build tags as `go test`.
list_flags=()
for flag in ${flags[@]+"${flags[@]}"}; do
	case "$flag" in -tags=*) list_flags+=("$flag") ;; esac
done
# shellcheck disable=SC2086 # fresh is a whitespace-separated package list.
fresh_paths="$("$go_cmd" list ${list_flags[@]+"${list_flags[@]}"} $fresh)"
cached=()
selected_fresh=()
while IFS= read -r path; do
	[ -n "$path" ] || continue
	if printf '%s\n' "$fresh_paths" | grep -qxF -- "$path"; then
		selected_fresh+=("$path")
	else
		cached+=("$path")
	fi
done < <("$go_cmd" list ${list_flags[@]+"${list_flags[@]}"} "${packages[@]}")

if [ "${#selected_fresh[@]}" -eq 0 ]; then
	run_go_test "$profile" "${cached[@]}" --
	exit
fi
if [ "${#cached[@]}" -eq 0 ]; then
	run_go_test "$profile" "${selected_fresh[@]}" -- -count=1
	exit
fi

fresh_profile="${profile:+$profile.fresh}"
run_go_test "$fresh_profile" "${selected_fresh[@]}" -- -count=1 &
fresh_pid=$!
status=0
run_go_test "$profile" "${cached[@]}" -- || status=$?
wait "$fresh_pid" || status=$?
if [ -n "$profile" ] && [ -f "$fresh_profile" ]; then
	if [ -f "$profile" ]; then
		tail -n +2 "$fresh_profile" >>"$profile"
		rm -f "$fresh_profile"
	else
		mv "$fresh_profile" "$profile"
	fi
fi
exit "$status"
