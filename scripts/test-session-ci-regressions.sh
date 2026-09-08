#!/usr/bin/env bash
# Reproduce historical session failures, including CI run 34174519177.
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
  TestSessionCommand_ExperimentalToolSetActive_DisabledSleepRejectsSuccess
  TestAgentBinaryTest46HighRateToolAudioRegression
  TestAgentBinaryDefaultHoldToneIsSeparateFromProviderPCM
  TestRunCustomerSimulationSuiteFamilyBUsesRecordedCorrectionBoundaries
  TestReadImageSpokenFailedContinuationIsActionable
  TestSessionCommand_LiveScheduledAudioDoesNotCrossDelayedSessionUpdated
  TestSessionCommand_LiveRecordDirAudioInTurnUsesLiveLifecycle
  TestSessionCommand_RecordThenReplayScheduledAudioUsesShippedCLI
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
  echo "==> session CI regressions: mode=$current package=go-device-gateway/pkg/devices count=$count"
  (cd "$root/go-device-gateway" && CGO_ENABLED=$cgo go test ./pkg/devices \
    -timeout 480s "${flags[@]}" -run '^TestSimulated' -v) || failed=1
  echo "==> session CI regressions: mode=$current package=go-llm-gateway/pkg/providers/openai count=$count"
  (cd "$root/go-llm-gateway" && CGO_ENABLED=$cgo go test ./pkg/providers/openai \
    -timeout 300s "${flags[@]}" -run '^TestComposed' -v) || failed=1
done
exit "$failed"
