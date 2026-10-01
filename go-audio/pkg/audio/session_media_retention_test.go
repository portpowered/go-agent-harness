package audio

import (
	"context"
	"errors"
	"testing"
)

func TestSessionMediaRetiresCompletedResponseAccounting(t *testing.T) {
	media := NewSessionMediaAtRate(nil, 24000)
	t.Cleanup(func() {
		if err := media.Close(); err != nil {
			t.Errorf("SessionMedia.Close() = %v", err)
		}
	})
	for i := range 10000 {
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
	t.Cleanup(func() {
		if err := media.Close(); err != nil {
			t.Errorf("SessionMedia.Close() = %v", err)
		}
	})
	if err := media.Endpoints().Outbound.WriteFrame(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	if want.Samples[0] != 1 {
		t.Fatal("provider mutated caller samples")
	}
}

func TestSessionMediaFailClassifiesPendingTailWhenFrameQueueIsFull(t *testing.T) {
	media := NewSessionMedia(func(context.Context, PCMFrame) error { return nil })
	defer closeForTest(t, media)

	frame := make([]int16, DefaultSessionMediaFrameSamples)
	for index := range sessionMediaMaxQueuedFrames {
		if err := media.PushInbound(frame); err != nil {
			t.Fatalf("push queued frame %d: %v", index, err)
		}
	}
	// A partial tail can be admitted while no complete frame is available for
	// the bounded queue. Failing now must classify that tail rather than drop
	// it behind an otherwise successful provider error.
	if err := media.PushInbound([]int16{7}); err != nil {
		t.Fatalf("push pending response tail: %v", err)
	}
	providerErr := errors.New("provider stream failed")
	media.FailInbound(providerErr)

	inbound := media.Endpoints().Inbound
	for index := range sessionMediaMaxQueuedFrames {
		if _, err := inbound.ReadFrame(context.Background()); err != nil {
			t.Fatalf("read retained frame %d: %v", index, err)
		}
	}
	_, err := inbound.ReadFrame(context.Background())
	if !errors.Is(err, providerErr) {
		t.Fatalf("terminal error = %v, want provider error %v", err, providerErr)
	}
	if !errors.Is(err, ErrSessionMediaInboundBacklog) {
		t.Fatalf("terminal error = %v, want explicit backlog classification", err)
	}
}
