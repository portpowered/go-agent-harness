package wire

import (
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
)

func TestWireConstructsSecondIndependentRoomEvidenceService(t *testing.T) {
	first := NewService()
	second := NewService()
	if first == nil || second == nil {
		t.Fatal("wire returned a nil room evidence service")
	}
	for index, service := range []roomevidence.Service{first, second} {
		destination := filepath.Join(t.TempDir(), "bundle")
		recorder, err := service.Open(roomevidence.Options{Destination: destination})
		if err != nil {
			t.Fatalf("open independent service %d: %v", index, err)
		}
		if err := recorder.Finalize(roomevidence.RoomResult{TerminationReason: roomevidence.RoomTerminationStopped}, nil, recorder.StartedAt()); err != nil {
			t.Fatalf("finalize independent service %d: %v", index, err)
		}
	}
}
