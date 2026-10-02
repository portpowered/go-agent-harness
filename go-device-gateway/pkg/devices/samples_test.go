//go:build windows || (cgo && !nomicrophone && (linux || darwin))

package devices

import (
	"context"
	"reflect"
	"testing"
	"time"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

type pacedPlaybackBackendForTest interface {
	WaitForPlaybackCapacity(context.Context, int) error
	WriteFrame(context.Context, []int16) error
	PlaybackStats() audio.PlaybackQueueStats
	DeviceFormat() audio.DeviceFormat
}

// awaitQueuedFrame waits, polling on a ticker, until the producer has queued
// at least one frame for the next render callback.
func awaitQueuedFrame(t *testing.T, backend pacedPlaybackBackendForTest, frameIndex int) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for backend.PlaybackStats().QueuedSamples < audio.FrameSize {
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("frame %d did not reach the native playback queue", frameIndex)
		}
	}
}

// testPacedPlaybackBackend drives a provider-shaped burst through one native
// queue contract while callbacks consume it. Platform tests supply their real
// callback seam; the shared assertions require exact FIFO PCM and zero loss.
func testPacedPlaybackBackend(t *testing.T, backend pacedPlaybackBackendForTest, render func([]byte)) {
	t.Helper()
	const frameCount = 40
	// Prime to the high watermark of the backend's own format: a fixed rate
	// would expect more (or fewer) frames than the queue admits before
	// capacity waits block the producer.
	_, high, err := audio.PlaybackQueueWatermarks(backend.DeviceFormat())
	if err != nil {
		t.Fatal(err)
	}
	primeFrames := high / audio.FrameSize
	primed := make(chan struct{})
	producerDone := make(chan error, 1)
	go func() {
		for frameIndex := range frameCount {
			frame := int16Samples(frameIndex*audio.FrameSize, audio.FrameSize)
			if err := backend.WaitForPlaybackCapacity(context.Background(), len(frame)); err != nil {
				producerDone <- err
				return
			}
			if err := backend.WriteFrame(context.Background(), frame); err != nil {
				producerDone <- err
				return
			}
			if frameIndex+1 == primeFrames {
				close(primed)
			}
		}
		producerDone <- nil
	}()

	select {
	case <-primed:
	case err := <-producerDone:
		t.Fatalf("producer stopped before priming the high watermark: %v", err)
	case <-time.After(time.Second):
		t.Fatal("producer did not prime the playback high watermark")
	}

	for frameIndex := range frameCount {
		awaitQueuedFrame(t, backend, frameIndex)
		raw := make([]byte, audio.FrameSize*2)
		render(raw)
		got := make([]int16, audio.FrameSize)
		if err := codec.DecodePCM16Into(got, raw); err != nil {
			t.Fatal(err)
		}
		want := int16Samples(frameIndex*audio.FrameSize, audio.FrameSize)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("rendered frame %d changed or reordered: first=%d last=%d", frameIndex, got[0], got[len(got)-1])
		}
	}
	select {
	case err := <-producerDone:
		if err != nil {
			t.Fatalf("paced producer: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("paced producer did not finish")
	}
	stats := backend.PlaybackStats()
	if stats.QueuedSamples != 0 || stats.DroppedSamples != 0 || stats.OverflowEvents != 0 {
		t.Fatalf("paced native playback lost samples: %+v", stats)
	}
}
