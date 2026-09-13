package wire

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/pion/rtp"
	rtctransport "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtctransport"
)

func TestInboundTransportOwnsResampledFrameStorage(t *testing.T) {
	shared := make([]int16, 480)
	resampleCalls, timerCalls := 0, 0
	resample := func(_ []int16, _, _ int) ([]int16, error) {
		resampleCalls++
		for index := range shared {
			shared[index] = int16(resampleCalls)
		}
		return shared, nil
	}
	track, err := NewService().NewInboundTrack(&testPacketSource{packets: []*rtp.Packet{
		testPacket(1, 1000, 1, 111, 1), testPacket(2, 1960, 1, 111, 2),
	}}, &testDecoder{}, rtctransport.InboundTrackConfig{
		SampleRate: 24000, JitterDepth: 20 * time.Millisecond,
		NewTimer: func(time.Duration) <-chan time.Time {
			timerCalls++
			if timerCalls == 1 {
				return time.After(0)
			}
			return make(chan time.Time)
		},
		Resample: resample,
	})
	if err != nil {
		t.Fatalf("NewInboundTrack() error = %v", err)
	}
	defer func() {
		if closeErr := track.Close(); closeErr != nil {
			t.Errorf("Close() error = %v", closeErr)
		}
	}()

	first, err := track.ReadFrame(context.Background())
	if err != nil {
		t.Fatalf("first ReadFrame() error = %v", err)
	}
	firstSamples := slices.Clone(first.Samples)
	second, err := track.ReadFrame(context.Background())
	if err != nil {
		t.Fatalf("second ReadFrame() error = %v", err)
	}
	if resampleCalls != 2 || len(firstSamples) != 480 || len(second.Samples) != 480 {
		t.Fatalf("resampled frames = calls %d, lengths %d/%d; want two independent 480-sample frames", resampleCalls, len(firstSamples), len(second.Samples))
	}
	if firstSamples[0] != 1 || second.Samples[0] != 2 {
		t.Fatalf("resampled frame values = %d/%d, want 1/2", firstSamples[0], second.Samples[0])
	}
	if &first.Samples[0] == &second.Samples[0] {
		t.Fatal("resampled frames unexpectedly share storage")
	}
}
