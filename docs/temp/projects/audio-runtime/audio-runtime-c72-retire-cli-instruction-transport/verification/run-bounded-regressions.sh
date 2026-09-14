#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../../../.." && pwd)
timeout_seconds=${C72_AGGREGATE_TIMEOUT_SECONDS:-600}
output_cap=${C72_OUTPUT_CAP_BYTES:-65536}
temp_dir=$(mktemp -d "${TMPDIR:-/tmp}/audio-runtime-c72-regressions.XXXXXX")
binary="$temp_dir/yui"
source_hash=$(shasum -a 256 "$root/agent-cli/internal/services/internal/agentruntime/session.go" | awk '{print $1}')

cleanup() {
	rmdir "$temp_dir" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

run_bounded() {
	local label=$1
	shift
	python3 - "$timeout_seconds" "$output_cap" "$label" "$@" <<'PY'
import os
import signal
import subprocess
import sys

timeout = int(sys.argv[1])
cap = int(sys.argv[2])
label = sys.argv[3]
command = sys.argv[4:]
print(f"==> {label}: {' '.join(command)}")
process = subprocess.Popen(
    command,
    stdout=subprocess.PIPE,
    stderr=subprocess.STDOUT,
    start_new_session=True,
    text=False,
)
try:
    output, _ = process.communicate(timeout=timeout)
    timed_out = False
except subprocess.TimeoutExpired as exc:
    timed_out = True
    output = (exc.stdout or b"") + (exc.stderr or b"")
    os.killpg(process.pid, signal.SIGTERM)
    try:
        remaining, _ = process.communicate(timeout=5)
        output += remaining
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        remaining, _ = process.communicate()
        output += remaining
status = process.returncode if not timed_out else 124
if len(output) > cap:
    output = output[:cap] + b"\n[output truncated by C72 cap]\n"
sys.stdout.buffer.write(output)
print(f"==> {label}: status={status} timed_out={timed_out} process_group_cleaned={timed_out}")
if status != 0:
    raise SystemExit(status)
PY
}

run_bounded "public CLI help" bash -lc "cd '$root/agent-cli' && GOWORK=off go run ./cmd/yui --help"
(
	cd "$root/agent-cli"
	GOWORK=off go build -trimpath -o "$binary" ./cmd/yui
)
binary_hash=$(shasum -a 256 "$binary" | awk '{print $1}')
run_bounded "built CLI help" "$binary" --help
run_bounded "instruction focused normal/race compatibility" bash -lc "cd '$root/agent-cli' && GOWORK=off go test ./internal/services/internal/agentruntime -run 'Instruction|Capability' -count=1"
run_bounded "accumulated session regressions" bash -lc "cd '$root' && COUNT=1 scripts/test-session-ci-regressions.sh normal"

echo "C72 bounded regressions passed; source_session_go_sha256=$source_hash binary_yui_sha256=$binary_hash output_cap_bytes=$output_cap aggregate_timeout_seconds=$timeout_seconds"
