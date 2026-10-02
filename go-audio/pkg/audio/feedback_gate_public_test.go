package audio

import (
	"io"
	"testing"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/selfhearing"
)

func TestFeedbackGateCapturePositionTracksFilteredCaptureAndDiscardHeldIsSafe(t *testing.T) {
	var unset *PCM16FeedbackGate
	if unset.CapturePosition() != 0 {
		t.Fatal("nil gate reports a capture position")
	}
	unset.DiscardHeld()

	gate, err := NewPCM16FeedbackGate(selfhearing.DefaultSelfHearingConfig(), io.Discard, SampleRate, SampleRate)
	if err != nil {
		t.Fatalf("NewPCM16FeedbackGate: %v", err)
	}
	t.Cleanup(func() {
		if err := gate.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	if gate.CapturePosition() != 0 {
		t.Fatalf("fresh gate capture position = %s, want 0", gate.CapturePosition())
	}
	if _, err := gate.FilterCapture(t.Context(), make([]int16, SampleRate/50)); err != nil {
		t.Fatalf("FilterCapture: %v", err)
	}
	if gate.CapturePosition() <= 0 {
		t.Fatalf("capture position = %s after a 20 ms frame, want it to advance", gate.CapturePosition())
	}
	gate.DiscardHeld()
	if _, err := gate.FilterCapture(t.Context(), make([]int16, SampleRate/50)); err != nil {
		t.Fatalf("FilterCapture after DiscardHeld: %v", err)
	}
}
