#!/bin/sh
set -eu

probe_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$probe_dir"

# This module intentionally lives below a workspace checkout but is not a
# workspace member. Keep every command explicit so a caller's GOWORK cannot
# accidentally change the dependency graph being tested.
export GOWORK=off

usage() {
	cat >&2 <<'EOF'
usage: ./probe.sh <metadata|test|typecheck|smoke|chrome|webmcp|go1.24.2>

  metadata   print toolchain, module graph, and checksum verification
  test       run the isolated probe tests
  typecheck  compile the isolated probe without running tests
  smoke      execute the generated-binding smoke report
  chrome     download, verify, launch, and query pinned Chrome for Testing
  webmcp     launch pinned Chrome with WebMCP flags and run the native matrix
  go1.24.2   run the exact baseline toolchain and require its version error
EOF
}

command_name=${1:-}
case "$command_name" in
metadata)
	go version
	go env GOWORK GOOS GOARCH GOVERSION GOTOOLCHAIN GOMOD
	go list -m all
	go mod verify
	;;
test)
	go test ./...
	;;
typecheck)
	go test -run '^$' ./...
	;;
smoke)
	go run .
	;;
chrome)
	./chrome-launch.sh
	;;
webmcp)
	./chrome-launch.sh webmcp
	;;
go1.24.2)
	GOTOOLCHAIN=go1.24.2 go version
	set +e
	output=$(GOTOOLCHAIN=go1.24.2 go test ./... 2>&1)
	status=$?
	set -e
	printf '%s\n' "$output"
	if [ "$status" -eq 0 ]; then
		echo "ERROR: Go 1.24.2 unexpectedly accepted modules requiring Go 1.26" >&2
		exit 1
	fi
	case "$output" in
		*"requires go >= 1.26"*)
			echo "EXPECTED: pinned bindings require Go >= 1.26" >&2
			;;
		*)
			echo "ERROR: Go 1.24.2 failed without the expected minimum-version diagnostic" >&2
			exit 1
			;;
	esac
	;;
*)
	usage
	exit 2
	;;
esac
