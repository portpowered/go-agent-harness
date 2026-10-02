package audio

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/selfhearing"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/contract"
)

func TestPCM16FeedbackGateRejectsNilContext(t *testing.T) {
	gate, err := NewPCM16FeedbackGate(selfhearing.DefaultSelfHearingConfig(), nil, SampleRate, SampleRate)
	if err != nil {
		t.Fatalf("new local feedback gate: %v", err)
	}
	defer func() {
		if err := gate.Close(); err != nil {
			t.Errorf("feedback gate Close(): %v", err)
		}
	}()
	var ctx context.Context
	wrote := false
	if err := gate.WritePlayback(ctx, make([]int16, FrameSize), func() error { wrote = true; return nil }); !errors.Is(err, contract.ErrNilContext) {
		t.Fatalf("WritePlayback(nil ctx) = %v, want ErrNilContext", err)
	}
	if wrote {
		t.Fatal("WritePlayback wrote to the sink with a nil context")
	}
	if frames, err := gate.FilterCapture(ctx, make([]int16, FrameSize)); !errors.Is(err, contract.ErrNilContext) || frames != nil {
		t.Fatalf("FilterCapture(nil ctx) = (%d frames, %v), want ErrNilContext", len(frames), err)
	}
}
