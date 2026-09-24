package latency

import (
	"path/filepath"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// Service is the private implementation of the public roomevidence latency
// capability. It keeps file I/O and observation storage behind the service
// boundary while allowing Wire to return only the service-owned contract.
type Service struct{}

func NewService() roomevidence.LatencyService { return Service{} }

func (Service) NewRecorder(source platformclock.Source, format rooms.AudioFormat) rooms.LatencyRecorder {
	return New(source, format)
}

func (Service) NewRuntimeObserver(recorder rooms.LatencyRecorder, participantID string) sessiontrace.RuntimeObserver {
	return runtimeObserver{recorder: recorder, participantID: participantID}
}

func (Service) ReadBundle(path string) (rooms.RoomLatencyBundle, error) {
	return ReadBundle(path)
}

func (Service) AnalyzeBundle(bundle rooms.RoomLatencyBundle) (rooms.RoomLatencyReport, error) {
	return Analyze(bundle)
}

func (Service) Report(destination string) (rooms.RoomLatencyReport, error) {
	return AnalyzeFile(filepath.Join(destination, roomevidence.LatencyPath))
}

var _ roomevidence.LatencyService = Service{}

type runtimeObserver struct {
	recorder      rooms.LatencyRecorder
	participantID string
}

func (o runtimeObserver) ObserveSessionRuntime(observation sessiontrace.SessionRuntimeObservation) {
	if o.recorder == nil {
		return
	}
	var kind rooms.LatencyObservationKind
	switch observation.Kind {
	case sessiontrace.SessionRuntimeObservationInputCommit:
		kind = rooms.LatencyObservationInputCommit
	case sessiontrace.SessionRuntimeObservationResponseCreate:
		kind = rooms.LatencyObservationResponseCreate
	case sessiontrace.SessionRuntimeObservationAudioOutput,
		sessiontrace.SessionRuntimeObservationAudioInput,
		sessiontrace.SessionRuntimeObservationAudioPlaybackReceipt,
		sessiontrace.SessionRuntimeObservationAudioRenderTapUnavailable,
		sessiontrace.SessionRuntimeObservationTurnCompleted,
		sessiontrace.SessionRuntimeObservationTerminal:
		return
	default:
		return
	}
	o.recorder.ObserveRuntime(o.participantID, rooms.LatencyObservation{
		Kind: kind, ResponseID: observation.ResponseID,
		Timestamp: observation.Timestamp, Tick: observation.Tick,
	})
}

var _ sessiontrace.RuntimeObserver = runtimeObserver{}
