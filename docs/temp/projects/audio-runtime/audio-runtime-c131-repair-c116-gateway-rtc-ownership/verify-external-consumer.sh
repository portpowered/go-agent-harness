#!/usr/bin/env bash
set -euo pipefail

# The C116 consumer is archived and intentionally remains outside this recovery
# directory. Run it as a separate module so workspace-only imports cannot pass.
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../../.." && pwd)"
consumer="${repo_root}/docs/temp/projects/audio-runtime/audio-runtime-c116-centralize-rtc-track-transport/external-consumer"
test -f "${consumer}/go.mod"
test -f "${consumer}/consumer_test.go"
cd "${consumer}"
exec rtk proxy bash -lc 'GOWORK=off go test -race ./... -count=1 -timeout=180s'

