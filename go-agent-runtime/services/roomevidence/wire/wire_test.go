package wire

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestWireConstructsSecondIndependentRoomEvidenceService(t *testing.T) {
	first := NewService()
	second := NewService()
	if first == nil || second == nil {
		t.Fatal("wire returned a nil room evidence service")
	}
	for index, service := range []roomevidence.Service{first, second} {
		destination := filepath.Join(t.TempDir(), "bundle")
		recorder, err := service.Open(roomevidence.RecordingRequest{Destination: destination})
		if err != nil {
			t.Fatalf("open independent service %d: %v", index, err)
		}
		if _, err := finalizeForTest(recorder, roomevidence.RoomResult{TerminationReason: roomevidence.RoomTerminationStopped}, nil, recorder.StartedAt()); err != nil {
			t.Fatalf("finalize independent service %d: %v", index, err)
		}
	}
	latency := NewLatencyService()
	if latency == nil {
		t.Fatal("wire returned a nil room latency service")
	}
	latencyRecorder := latency.NewRecorder(
		clock.NewDeterministic(time.Unix(0, 0).UTC(), time.Millisecond),
		rooms.AudioFormat{SampleRate: 24000, Channels: 1, FrameDuration: 20 * time.Millisecond},
	)
	if latencyRecorder == nil {
		t.Fatal("wire returned a nil room latency recorder")
	}
	latencyRecorder.ObserveSpeakerBytes("speaker", []string{"listener"}, 2)
	if got := len(latencyRecorder.Bundle().Events); got != 1 {
		t.Fatalf("latency events = %d, want one public observation", got)
	}
}
