package audio

import (
	"context"
	"errors"
	"testing"
)

func TestSessionMediaFailClassifiesPendingTailWhenFrameQueueIsFull(t *testing.T) {
	media := NewSessionMedia(func(context.Context, PCMFrame) error { return nil })
	defer func() { _ = media.Close() }()

	frame := make([]int16, DefaultSessionMediaFrameSamples)
	for index := 0; index < sessionMediaMaxQueuedFrames; index++ {
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
	for index := 0; index < sessionMediaMaxQueuedFrames; index++ {
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
