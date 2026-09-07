#!/usr/bin/env bash
# Reproduce the cumulative session failures from CI runs 34121252743 through 34137090509.
set -euo pipefail
mode=${1:-normal}
case "$mode" in normal|coverage|race|all) ;; *) echo "Usage: $0 [normal|coverage|race|all]" >&2; exit 2 ;; esac
count=${COUNT:-3}
if [[ ! "$count" =~ ^[1-9][0-9]*$ ]]; then
  echo "COUNT must be a positive integer" >&2
  exit 2
fi
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root/agent-cli"

# Top-level names deliberately include every subtest (including all stress trials).
scenarios=(
  TestSessionCommand_OpenAIRealtimeReplayAudioTurnDivergentResupplyFailsWithMismatch
  TestSessionToolResultConversationMissingContinuationIsBounded
  TestShippedSessionProcessFamilyBCorrection
  TestSessionCLI_DuplexPCMMultiTurnRejectsLaterTurnAudioControl
  TestSessionCommand_ActiveScheduledAudioPreservesToolResultLifecycle
  TestSessionConfigToolFilterThroughRealCLI
  TestAgentBinaryTest46HighRateToolAudioRegression
  TestRunCustomerSimulationSuiteFamilyBUsesRecordedCorrectionBoundaries
  TestReadImageSpokenFailedContinuationIsActionable
  TestSessionToolResultConversationCorruptAudioDeltaIsRejected
  TestShippedSessionProcessDuplexConversation
  TestSessionCLI_DuplexPCMMultiTurnRejectsLaterTurnTranscriptControl
)
pattern="^($(IFS='|'; echo "${scenarios[*]}"))$"
modes=("$mode")
if [[ "$mode" == all ]]; then modes=(normal coverage race); fi
failed=0
for current in "${modes[@]}"; do
  cgo=0
  flags=(-count="$count")
  case "$current" in
    coverage) flags+=(-covermode=atomic) ;;
    race) cgo=1; flags+=(-race) ;;
  esac
  for package in ./internal/transport/cli ./test/integration; do
    selected=$pattern
    if [[ "$package" == ./internal/transport/cli ]]; then
      selected='^TestSessionCommandAudioInterruptOrdering$'
    fi
    echo "==> session CI regressions: mode=$current package=$package count=$count"
    # Preserve the existing eight-minute command/test bound and shorter child deadlines.
    # Stress is hermetic: loopback devices and a fake provider, no live API credentials.
    CGO_ENABLED=$cgo YUI_AUDIO_STRESS=1 go run ./cmd/testtimeout --timeout 480s --       go test "$package" -tags=nomicrophone -timeout 480s       "${flags[@]}" -run "$selected" -v || failed=1
  done
done
exit "$failed"
