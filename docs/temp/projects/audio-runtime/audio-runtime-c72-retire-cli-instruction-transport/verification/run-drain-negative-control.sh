#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../../../.." && pwd)
source_file="$root/go-agent-runtime/services/sessionupdate/internal/service/session.go"
test_pattern='^TestDrainAfterDoneForwardsAlreadyBufferedDelta$'
temp_dir=$(mktemp -d "${TMPDIR:-/tmp}/audio-runtime-c72-drain.XXXXXX")
backup="$temp_dir/session.go"
mutation_log="$temp_dir/mutation.log"
cp "$source_file" "$backup"

restore() {
	cp "$backup" "$source_file"
}
trap restore EXIT INT TERM

before=$(shasum -a 256 "$source_file" | awk '{print $1}')
python3 - "$source_file" <<'PY'
from pathlib import Path
import sys

path = Path(sys.argv[1])
source = path.read_text()
old = '''func (s *decoratedSession) drainAfterDone(innerReceive *messages.TypedBuffer[messages.StreamMessage]) {
\tfor {
\t\tmsg, ok := innerReceive.Read()
\t\tif !ok || !s.forward(msg) {
\t\t\treturn
\t\t}
\t}
}'''
new = '''func (s *decoratedSession) drainAfterDone(innerReceive *messages.TypedBuffer[messages.StreamMessage]) {
\treturn
}'''
if old not in source:
    raise SystemExit("drain helper shape changed; refusing an unpinned mutation")
path.write_text(source.replace(old, new, 1))
PY

set +e
(
	cd "$root/go-agent-runtime"
	GOWORK=off go test ./services/sessionupdate/internal/service -run "$test_pattern" -count=1
) >"$mutation_log" 2>&1
status=$?
set -e
if [[ "$status" -eq 0 ]]; then
	echo "drain negative control unexpectedly passed" >&2
	cat "$mutation_log" >&2
	exit 1
fi

cp "$backup" "$source_file"
trap - EXIT INT TERM
after=$(shasum -a 256 "$source_file" | awk '{print $1}')
if [[ "$after" != "$before" ]]; then
	echo "source restoration hash mismatch: before=$before after=$after" >&2
	exit 1
fi
(
	cd "$root/go-agent-runtime"
	GOWORK=off go test ./services/sessionupdate/internal/service -run "$test_pattern" -count=1
)
rm -f "$backup" "$mutation_log"
rmdir "$temp_dir"
echo "drain negative control failed as expected (status=$status); source restored sha256=$after"
