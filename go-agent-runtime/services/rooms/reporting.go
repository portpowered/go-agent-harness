package rooms

import (
	"encoding/json"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// ReportingService derives a versioned latency report from a finalized room
// evidence bundle. Hosts may render the returned JSON through their own CLI
// or embedding surface.
type ReportingService interface {
	LatencyReport(string) (json.RawMessage, error)
}

// LatencyObservationKind identifies a runtime boundary recorded by a room
// latency ledger. These values are part of the room evidence contract; the
// implementation and storage remain private to the service.
type LatencyObservationKind string

const (
	LatencyObservationInputCommit    LatencyObservationKind = "input_commit"
	LatencyObservationResponseCreate LatencyObservationKind = "response_create"
)

// LatencyObservation is the bounded runtime projection used by a room host.
// Provider payloads and credentials never cross this seam.
type LatencyObservation struct {
	Kind       LatencyObservationKind
	ResponseID string
	Timestamp  time.Time
	Tick       uint64
}

// LatencyRecorder accepts invocation-scoped timing observations. It is an
// interface so room orchestration can receive the whole service capability
// without importing the evidence implementation.
type LatencyRecorder interface {
	ObserveSpeakerAudio(string, []string, audio.PCMFrame)
	ObserveSpeakerBytes(string, []string, int)
	ObserveSpeechStopped(string)
	ObserveRuntime(string, LatencyObservation)
	ObserveProviderAudio(string, string)
	ObservePeerAudio(string, string, audio.PCMFrame)
	ObservePeerBytes(string, string, int)
	Bundle() RoomLatencyBundle
	Write(string) error
}

// LatencyService owns construction and derived report operations for room
// latency evidence. Implementations are composed through the room Wire
// package; callers depend only on this narrow public service contract.
type LatencyService interface {
	NewRecorder(platformclock.Source, AudioFormat) LatencyRecorder
	ReadBundle(string) (RoomLatencyBundle, error)
	AnalyzeBundle(RoomLatencyBundle) (RoomLatencyReport, error)
	Report(string) (RoomLatencyReport, error)
}
