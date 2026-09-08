package agentruntime

import (
	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

// roomLatencyRuntimeObserver bridges the legacy participant runtime
// observation callback to the room-owned latency ledger while the old room
// orchestration remains in place. It translates only the causal boundaries
// understood by that ledger; unrelated session diagnostics keep their owner.
type roomLatencyRuntimeObserver struct {
	recorder      runtimeRooms.LatencyRecorder
	participantID string
}

func (o roomLatencyRuntimeObserver) ObserveSessionRuntime(observation SessionRuntimeObservation) {
	if o.recorder == nil {
		return
	}
	var kind runtimeRooms.LatencyObservationKind
	switch observation.Kind {
	case SessionRuntimeObservationInputCommit:
		kind = runtimeRooms.LatencyObservationInputCommit
	case SessionRuntimeObservationResponseCreate:
		kind = runtimeRooms.LatencyObservationResponseCreate
	case SessionRuntimeObservationAudioOutput,
		SessionRuntimeObservationAudioInput,
		SessionRuntimeObservationAudioPlaybackReceipt,
		SessionRuntimeObservationAudioRenderTapUnavailable,
		SessionRuntimeObservationTurnCompleted,
		SessionRuntimeObservationTerminal:
		return
	default:
		return
	}
	o.recorder.ObserveRuntime(o.participantID, runtimeRooms.LatencyObservation{
		Kind: kind, ResponseID: observation.ResponseID,
		Timestamp: observation.Timestamp, Tick: observation.Tick,
	})
}
