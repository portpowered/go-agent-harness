package wire

import (
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestLatencyServiceProjectsOnlyPublicRuntimeBoundaries(t *testing.T) {
	t.Parallel()
	base := time.Unix(100, 0).UTC()
	latency := NewLatencyService()
	recorder := latency.NewRecorder(
		clock.NewDeterministic(base, time.Millisecond),
		rooms.AudioFormat{SampleRate: 24000, Channels: 1, FrameDuration: 20 * time.Millisecond},
	)
	recorder.ObserveSpeakerBytes("human", []string{"agent"}, 4)
	recorder.ObserveSpeechStopped("agent")
	observer := latency.NewRuntimeObserver(recorder, "agent")
	inputAt := base.Add(2 * time.Millisecond)
	responseAt := base.Add(3 * time.Millisecond)
	observer.ObserveSessionRuntime(sessiontrace.SessionRuntimeObservation{
		Kind: sessiontrace.SessionRuntimeObservationInputCommit, Tick: 20, Timestamp: inputAt,
		Payload: []byte("private audio payload"),
	})
	observer.ObserveSessionRuntime(sessiontrace.SessionRuntimeObservation{
		Kind: sessiontrace.SessionRuntimeObservationResponseCreate, Tick: 21, Timestamp: responseAt,
		ResponseID: "response-1",
	})
	observer.ObserveSessionRuntime(sessiontrace.SessionRuntimeObservation{
		Kind: sessiontrace.SessionRuntimeObservationAudioOutput, Payload: []byte("ignored"),
	})

	events := recorder.Bundle().Events
	if len(events) != 4 {
		t.Fatalf("latency event count = %d, want initial speaker/speech plus two runtime boundaries: %+v", len(events), events)
	}
	input, response := events[2], events[3]
	if input.Kind != rooms.RoomLatencyEventInputCommit || input.ParticipantID != "agent" || input.Tick != 20 || !input.Timestamp.Equal(inputAt) {
		t.Fatalf("input-commit event = %+v", input)
	}
	if response.Kind != rooms.RoomLatencyEventResponseCreate || response.ParticipantID != "agent" || response.ResponseID != "response-1" || response.Tick != 21 || !response.Timestamp.Equal(responseAt) {
		t.Fatalf("response-create event = %+v", response)
	}
}
