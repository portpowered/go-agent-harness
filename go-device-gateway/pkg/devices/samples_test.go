package devices

import audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

func int16Samples(start, count int) []int16 {
	samples := make([]int16, count)
	for index := range samples {
		samples[index] = int16(start + index)
	}
	return samples
}

type pacedPlaybackBackendForTest interface {
	WaitForPlaybackCapacity(context.Context, int) error
	WriteFrame(context.Context, []int16) error
	PlaybackStats() audio.PlaybackQueueStats
}

// testPacedPlaybackBackend drives a provider-shaped burst through one native
// queue contract while callbacks consume it. Platform tests supply their real
// callback seam; the shared assertions require exact FIFO PCM and zero loss.
func testPacedPlaybackBackend(t *testing.T, backend pacedPlaybackBackendForTest, render func([]byte)) {
	t.Helper()
	const frameCount = 40
	_, high, err := audio.PlaybackQueueWatermarks(audio.PCM16DeviceFormat(24000))
	if err != nil {
		t.Fatal(err)
	}
	primeFrames := high / audio.FrameSize
	primed := make(chan struct{})
	producerDone := make(chan error, 1)
	go func() {
		for frameIndex := 0; frameIndex < frameCount; frameIndex++ {
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

	for frameIndex := 0; frameIndex < frameCount; frameIndex++ {
		deadline := time.Now().Add(time.Second)
		for backend.PlaybackStats().QueuedSamples < audio.FrameSize {
			if time.Now().After(deadline) {
				t.Fatalf("frame %d did not reach the native playback queue", frameIndex)
			}
			time.Sleep(time.Millisecond)
		}
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
