#!/usr/bin/env bash

set -uo pipefail

# Keep this list synchronized with the local gate contract. Each name is an
# existing Make target so the target retains its normal configuration and
# diagnostics when invoked from the gate.
readonly phases=(
	"fmt"
	"vet"
	"lint"
	"staticcheck"
	"build"
	"test"
	"coverage-registration"
	"coverage-changed"
)

make_command="${PREPUSH_MAKE:-make}"
run_started=$SECONDS
failed_phase=""
failed_status=0

echo "==> prepush starting"
for phase in "${phases[@]}"; do
	phase_started=$SECONDS
	echo "==> prepush phase: $phase"

	phase_status=0
	if "$make_command" --no-print-directory "$phase"; then
		phase_status=0
	else
		phase_status=$?
	fi

	phase_elapsed=$((SECONDS - phase_started))
	echo "==> prepush phase $phase completed in ${phase_elapsed}s"

	if ((phase_status != 0)); then
		failed_phase="$phase"
		failed_status=$phase_status
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
