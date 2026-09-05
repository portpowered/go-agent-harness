package audio

import (
	"context"
	"testing"
)

func TestSessionMediaRetiresCompletedResponseAccounting(t *testing.T) {
	media := NewSessionMediaAtRate(nil, 24000)
	defer media.Close()
	for i := 0; i < 10000; i++ {
		response := PlaybackResponse{ResponseID: string(rune(i + 1)), ItemID: string(rune(i + 1))}
		media.StartInboundResponse(response)
		if i%2 == 0 {
			if err := media.PushInbound([]int16{1, 2, 3}); err != nil {
				t.Fatal(err)
			}
		}
		if err := media.FlushInbound(); err != nil {
			t.Fatal(err)
		}
		if _, err := media.Endpoints().Inbound.ReadFrame(context.Background()); err != nil {
			t.Fatal(err)
		}
		if retained := len(media.inbound.responseSamples); retained > 2 {
			t.Fatalf("retained %d response totals after turn %d", retained, i)
		}
	}
}

func TestSessionMediaOutboundPreservesFrameIdentity(t *testing.T) {
	want := PCMFrame{Samples: []int16{1, 2}, Format: PCM16DeviceFormat(24000), StreamID: "capture", Epoch: 3, Sequence: 7, StartSample: 21}
	media := NewSessionMediaAtRate(func(_ context.Context, frame PCMFrame) error {
		if frame.StreamID != want.StreamID || frame.Epoch != want.Epoch || frame.Sequence != want.Sequence || frame.StartSample != want.StartSample || frame.Format != want.Format {
			t.Fatalf("lost frame identity: %+v", frame)
		}
		frame.Samples[0] = 99
		return nil
	}, 24000)
	defer media.Close()
	if err := media.Endpoints().Outbound.WriteFrame(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	if want.Samples[0] != 1 {
		t.Fatal("provider mutated caller samples")
	}
}
