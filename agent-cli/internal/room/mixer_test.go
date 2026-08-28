package room

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

func TestPCM16MixerMixesEveryActiveInputAndClips(t *testing.T) {
	format := PCM16Format{SampleRate: 1000, Channels: 1, FrameDuration: 10 * time.Millisecond}
	mixer, err := NewPCM16MixerWithConfig(context.Background(), PCM16MixerConfig{
		Format:            format,
		InputQueueFrames:  4,
		OutputQueueFrames: 4,
	})
	if err != nil {
		t.Fatalf("new mixer: %v", err)
	}
	defer mixer.Close()

	for _, id := range []string{"alpha", "beta", "gamma"} {
		if err := mixer.AddInput(id); err != nil {
			t.Fatalf("add %s: %v", id, err)
		}
	}
	if err := mixer.Write("alpha", pcm16(1000, -1000)); err != nil {
		t.Fatalf("write alpha: %v", err)
	}
	if err := mixer.Write("beta", pcm16(2000, -2000)); err != nil {
		t.Fatalf("write beta: %v", err)
	}
	if err := mixer.Write("gamma", pcm16(30000, -30000)); err != nil {
		t.Fatalf("write gamma: %v", err)
	}

	want := pcm16(32767, -32768, 0, 0, 0, 0, 0, 0, 0, 0)
	got := readMixerFrame(t, mixer, want)
	if !bytes.Equal(got, want) {
		t.Fatalf("mixed frame = %v, want %v", decodePCM16(got), decodePCM16(want))
	}
}

func TestPCM16MixerPreservesPartialInputAcrossCadenceFrames(t *testing.T) {
	format := PCM16Format{SampleRate: 100, Channels: 1, FrameDuration: 20 * time.Millisecond}
	mixer, err := NewPCM16MixerWithConfig(context.Background(), PCM16MixerConfig{
		Format:            format,
		InputQueueFrames:  4,
		OutputQueueFrames: 4,
	})
	if err != nil {
		t.Fatalf("new mixer: %v", err)
	}
	defer mixer.Close()
	if err := mixer.AddInput("speaker"); err != nil {
		t.Fatalf("add input: %v", err)
	}
	if err := mixer.Write("speaker", pcm16(1)); err != nil {
		t.Fatalf("write first partial chunk: %v", err)
	}
	if err := mixer.Write("speaker", pcm16(2, 3, 4)); err != nil {
		t.Fatalf("write second partial chunk: %v", err)
	}

	readMixerFrame(t, mixer, pcm16(1, 2))
	readMixerFrame(t, mixer, pcm16(3, 4))
}

func TestPCM16MixerRemovalDiscardsOnlyRemovedInput(t *testing.T) {
	format := PCM16Format{SampleRate: 1000, Channels: 1, FrameDuration: 10 * time.Millisecond}
	mixer, err := NewPCM16MixerWithConfig(context.Background(), PCM16MixerConfig{
		Format:            format,
		InputQueueFrames:  4,
		OutputQueueFrames: 4,
	})
	if err != nil {
		t.Fatalf("new mixer: %v", err)
	}
	defer mixer.Close()
	for _, id := range []string{"keep", "remove"} {
		if err := mixer.AddInput(id); err != nil {
			t.Fatalf("add %s: %v", id, err)
		}
	}
	if err := mixer.Write("keep", pcm16(5)); err != nil {
		t.Fatalf("write keep: %v", err)
	}
	if err := mixer.Write("remove", pcm16(7)); err != nil {
		t.Fatalf("write remove: %v", err)
	}
	if err := mixer.RemoveInput("remove"); err != nil {
		t.Fatalf("remove input: %v", err)
	}
	readMixerFrame(t, mixer, pcm16(5, 0, 0, 0, 0, 0, 0, 0, 0, 0))
	if got := mixer.Inputs(); len(got) != 1 || got[0] != "keep" {
		t.Fatalf("active inputs = %v, want [keep]", got)
	}
}

func TestPCM16MixerCancellationUnblocksReadFrame(t *testing.T) {
	mixer, err := NewPCM16MixerWithConfig(context.Background(), PCM16MixerConfig{
		Format:            PCM16Format{SampleRate: 100, Channels: 1, FrameDuration: time.Second},
		InputQueueFrames:  1,
		OutputQueueFrames: 1,
	})
	if err != nil {
		t.Fatalf("new mixer: %v", err)
	}
	defer mixer.Close()

	readCtx, readCancel := context.WithTimeout(context.Background(), time.Second)
	defer readCancel()
	readCancel()
	_, err = mixer.ReadFrame(readCtx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("read after cancellation error = %v, want cancellation", err)
	}
}

func TestPCM16MixerProviderShapedPressureTrace(t *testing.T) {
	format := DefaultPCM16Format()
	frameBytes, err := format.FrameBytes()
	if err != nil {
		t.Fatalf("frame bytes: %v", err)
	}
	const (
		providerDeltaBytes = 19_200
		providerDeltas     = 3
		providerCadence    = 400 * time.Millisecond
		inputQueueFrames   = 40
		outputQueueFrames  = 4
	)
	if providerDeltaBytes%frameBytes != 0 {
		t.Fatalf("provider delta bytes = %d, want a whole number of %d-byte frames", providerDeltaBytes, frameBytes)
	}
	providerFrames := providerDeltaBytes / frameBytes

	t.Run("cadence-drained", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		mixer, err := NewPCM16MixerWithConfig(ctx, PCM16MixerConfig{
			Format:            format,
			InputQueueFrames:  inputQueueFrames,
			OutputQueueFrames: outputQueueFrames,
		})
		if err != nil {
			t.Fatalf("new mixer: %v", err)
		}
		defer mixer.Close()
		if err := mixer.AddInput("alpha"); err != nil {
			t.Fatalf("add input: %v", err)
		}

		want := make([]byte, 0, providerDeltas*providerDeltaBytes)
		for delta := 0; delta < providerDeltas; delta++ {
			want = append(want, providerPCM16Delta(delta, providerDeltaBytes)...)
		}
		got := make([]byte, 0, len(want))
		readErr := make(chan error, 1)
		go func() {
			for frame := 0; frame < providerDeltas*providerFrames; frame++ {
				pcm, frameErr := mixer.ReadFrame(ctx)
				if frameErr != nil {
					readErr <- frameErr
					return
				}
				got = append(got, pcm...)
				// Model a healthy session-ingestion hop that is slower than the
				// test goroutine but still well inside the 20 ms cadence.
				time.Sleep(2 * time.Millisecond)
			}
			readErr <- nil
		}()

		for delta := 0; delta < providerDeltas; delta++ {
			if delta > 0 {
				time.Sleep(providerCadence)
			}
			if err := mixer.Write("alpha", providerPCM16Delta(delta, providerDeltaBytes)); err != nil {
				t.Fatalf("provider delta %d: %v", delta, err)
			}
			stats := mixer.Stats()
			inputStats := stats.Inputs["alpha"]
			t.Logf("delta=%d input=%s/%s output=%s/%s", delta, inputStats.Duration, inputStats.CapacityDuration, stats.Output.Duration, stats.Output.CapacityDuration)
			if inputStats.Duration > inputStats.CapacityDuration || stats.Output.Duration > stats.Output.CapacityDuration {
				t.Fatalf("queue occupancy exceeded capacity: %+v", stats)
			}
		}

		select {
		case err := <-readErr:
			if err != nil {
				t.Fatalf("drain provider-shaped output: %v", err)
			}
		case <-ctx.Done():
			t.Fatalf("drain provider-shaped output: %v", ctx.Err())
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("delivered PCM changed: got %d bytes, want %d", len(got), len(want))
		}
	})

	t.Run("downstream-stall", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		mixer, err := NewPCM16MixerWithConfig(ctx, PCM16MixerConfig{
			Format:            format,
			InputQueueFrames:  inputQueueFrames,
			OutputQueueFrames: outputQueueFrames,
		})
		if err != nil {
			t.Fatalf("new mixer: %v", err)
		}
		defer mixer.Close()
		if err := mixer.AddInput("alpha"); err != nil {
			t.Fatalf("add input: %v", err)
		}

		firstFrame := make(chan struct{})
		releaseDownstream := make(chan struct{})
		readErr := make(chan error, 1)
		go func() {
			frame, frameErr := mixer.ReadFrame(ctx)
			if frameErr != nil {
				readErr <- frameErr
				return
			}
			if len(frame) != frameBytes {
				readErr <- errors.New("mixer emitted an invalid frame size")
				return
			}
			close(firstFrame)
			// This represents a room pump blocked in session ingestion after
			// it consumed one frame from the mixer's output queue.
			<-releaseDownstream
			readErr <- nil
		}()
		defer close(releaseDownstream)

		if err := mixer.Write("alpha", providerPCM16Delta(0, providerDeltaBytes)); err != nil {
			t.Fatalf("first provider delta: %v", err)
		}
		select {
		case <-firstFrame:
		case err := <-readErr:
			t.Fatalf("receive first output frame: %v", err)
		case <-ctx.Done():
			t.Fatalf("receive first output frame: %v", ctx.Err())
		}

		waitForMixerStats(t, mixer, func(stats PCM16MixerStats) bool {
			return stats.Output.Frames == stats.Output.CapacityFrames && stats.Inputs["alpha"].Frames > 0
		})

		var overflowErr error
		for delta := 1; delta < providerDeltas+3; delta++ {
			time.Sleep(providerCadence)
			overflowErr = mixer.Write("alpha", providerPCM16Delta(delta, providerDeltaBytes))
			stats := mixer.Stats()
			inputStats := stats.Inputs["alpha"]
			t.Logf("stalled delta=%d input=%s/%s output=%s/%s write_error=%v", delta, inputStats.Duration, inputStats.CapacityDuration, stats.Output.Duration, stats.Output.CapacityDuration, overflowErr)
			if errors.Is(overflowErr, ErrMixerInputBufferFull) {
				break
			}
			if overflowErr != nil {
				t.Fatalf("provider delta %d: %v", delta, overflowErr)
			}
		}
		if !errors.Is(overflowErr, ErrMixerInputBufferFull) {
			t.Fatalf("stalled downstream never exposed input pressure: %v", overflowErr)
		}
		stats := mixer.Stats()
		inputStats := stats.Inputs["alpha"]
		if stats.Output.Frames != stats.Output.CapacityFrames {
			t.Fatalf("output queue recovered unexpectedly: %+v", stats.Output)
		}
		if inputStats.Duration < 600*time.Millisecond {
			t.Fatalf("input occupancy = %s, want accumulated burst backlog", inputStats.Duration)
		}
	})
}

func waitForMixerStats(t *testing.T, mixer *PCM16Mixer, predicate func(PCM16MixerStats) bool) PCM16MixerStats {
	t.Helper()
	deadline := time.NewTimer(500 * time.Millisecond)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		stats := mixer.Stats()
		if predicate(stats) {
			return stats
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("mixer stats did not reach expected state: %+v", stats)
			return stats
		}
	}
}

func providerPCM16Delta(delta, byteCount int) []byte {
	pcm := make([]byte, byteCount)
	for sample := 0; sample < byteCount/2; sample++ {
		value := int16(1000 + delta*300 + sample%200)
		binary.LittleEndian.PutUint16(pcm[sample*2:sample*2+2], uint16(value))
	}
	return pcm
}

func readMixerFrame(t *testing.T, mixer *PCM16Mixer, want []byte) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		frame, err := mixer.ReadFrame(ctx)
		if err != nil {
			t.Fatalf("read mixer frame: %v", err)
		}
		if bytes.Equal(frame, want) {
			return frame
		}
	}
}

func pcm16(samples ...int16) []byte {
	pcm := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(pcm[index*2:index*2+2], uint16(sample))
	}
	return pcm
}

func decodePCM16(pcm []byte) []int16 {
	samples := make([]int16, len(pcm)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(pcm[index*2 : index*2+2]))
	}
	return samples
}
