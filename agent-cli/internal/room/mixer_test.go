package room

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"sync"
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

func TestPCM16MixerUsesDeterministicCadenceAndEmitsSilence(t *testing.T) {
	format := PCM16Format{SampleRate: 100, Channels: 1, FrameDuration: 20 * time.Millisecond}
	cadence := newDeterministicPCM16Cadence()
	factoryCalls := make(chan time.Duration, 1)
	mixer, err := NewPCM16MixerWithConfig(context.Background(), PCM16MixerConfig{
		Format:            format,
		InputQueueFrames:  4,
		OutputQueueFrames: 4,
		CadenceFactory: func(interval time.Duration) PCM16Cadence {
			factoryCalls <- interval
			return cadence
		},
	})
	if err != nil {
		t.Fatalf("new mixer: %v", err)
	}
	t.Cleanup(func() { _ = mixer.Close() })

	for _, id := range []string{"alpha", "beta"} {
		if err := mixer.AddInput(id); err != nil {
			t.Fatalf("add %s: %v", id, err)
		}
	}
	if err := mixer.Write("alpha", pcm16(100, 200, 300)); err != nil {
		t.Fatalf("write alpha: %v", err)
	}
	if err := mixer.Write("beta", pcm16(10, 20, 30)); err != nil {
		t.Fatalf("write beta: %v", err)
	}

	cadence.Advance()
	got := readMixerFrameWithContext(t, mixer)
	want := pcm16(110, 220)
	if !bytes.Equal(got, want) {
		t.Fatalf("first deterministic frame = %v, want %v", decodePCM16(got), decodePCM16(want))
	}
	select {
	case interval := <-factoryCalls:
		if interval != format.FrameDuration {
			t.Fatalf("cadence interval = %s, want %s", interval, format.FrameDuration)
		}
	case <-time.After(time.Second):
		t.Fatal("mixer did not create its cadence source")
	}

	cadence.Advance()
	got = readMixerFrameWithContext(t, mixer)
	want = pcm16(330, 0)
	if !bytes.Equal(got, want) {
		t.Fatalf("second deterministic frame = %v, want %v", decodePCM16(got), decodePCM16(want))
	}

	cadence.Advance()
	got = readMixerFrameWithContext(t, mixer)
	want = pcm16(0, 0)
	if !bytes.Equal(got, want) {
		t.Fatalf("silence deterministic frame = %v, want %v", decodePCM16(got), decodePCM16(want))
	}
	if got := len(mixer.Frames()); got != 0 {
		t.Fatalf("queued frames after one output per cadence = %d, want 0", got)
	}
}

func TestPCM16MixerManualAdvanceUsesProductionMixPath(t *testing.T) {
	format := PCM16Format{SampleRate: 100, Channels: 1, FrameDuration: 20 * time.Millisecond}
	mixer, err := NewPCM16MixerWithConfig(context.Background(), PCM16MixerConfig{
		Format:            format,
		InputQueueFrames:  4,
		OutputQueueFrames: 2,
		Manual:            true,
	})
	if err != nil {
		t.Fatalf("new manual mixer: %v", err)
	}
	t.Cleanup(func() { _ = mixer.Close() })
	for _, id := range []string{"alpha", "beta"} {
		if err := mixer.AddInput(id); err != nil {
			t.Fatalf("add %s: %v", id, err)
		}
	}
	if err := mixer.Write("alpha", pcm16(100, 200)); err != nil {
		t.Fatalf("write alpha: %v", err)
	}
	if err := mixer.Write("beta", pcm16(10, 20)); err != nil {
		t.Fatalf("write beta: %v", err)
	}
	if err := mixer.Advance(context.Background()); err != nil {
		t.Fatalf("advance first frame: %v", err)
	}
	got := readMixerFrameWithContext(t, mixer)
	want := pcm16(110, 220)
	if !bytes.Equal(got, want) {
		t.Fatalf("manual mixed frame = %v, want %v", decodePCM16(got), decodePCM16(want))
	}
	if err := mixer.Advance(context.Background()); err != nil {
		t.Fatalf("advance silence frame: %v", err)
	}
	got = readMixerFrameWithContext(t, mixer)
	want = pcm16(0, 0)
	if !bytes.Equal(got, want) {
		t.Fatalf("manual silence frame = %v, want %v", decodePCM16(got), decodePCM16(want))
	}

	regular, err := NewPCM16MixerWithConfig(context.Background(), PCM16MixerConfig{Format: format})
	if err != nil {
		t.Fatalf("new regular mixer: %v", err)
	}
	defer regular.Close()
	if err := regular.Advance(context.Background()); !errors.Is(err, ErrMixerManualAdvance) {
		t.Fatalf("regular mixer advance error = %v, want %v", err, ErrMixerManualAdvance)
	}
}

func TestPCM16MixerCancellationStopsDeterministicCadence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cadence := newDeterministicPCM16Cadence()
	factoryReady := make(chan struct{}, 1)
	mixer, err := NewPCM16MixerWithConfig(ctx, PCM16MixerConfig{
		Format:            PCM16Format{SampleRate: 100, Channels: 1, FrameDuration: time.Second},
		InputQueueFrames:  1,
		OutputQueueFrames: 1,
		CadenceFactory: func(time.Duration) PCM16Cadence {
			factoryReady <- struct{}{}
			return cadence
		},
	})
	if err != nil {
		cancel()
		t.Fatalf("new mixer: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = mixer.Close()
	})

	select {
	case <-factoryReady:
	case <-time.After(time.Second):
		t.Fatal("mixer did not start its deterministic cadence")
	}
	cancel()

	select {
	case <-cadence.stopped:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not stop the deterministic cadence")
	}
	select {
	case _, ok := <-mixer.Frames():
		if ok {
			t.Fatal("mixer emitted a frame after cadence cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("mixer output remained open after cadence cancellation")
	}
}

func TestPCM16MixerWriteContextCancellationPreservesQueuedPCM(t *testing.T) {
	mixer, frameBytes := newFullInputMixer(t)
	defer mixer.Close()
	if err := mixer.Write("alpha", make([]byte, frameBytes)); err != nil {
		t.Fatalf("fill input: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- mixer.WriteContext(ctx, "alpha", pcm16(7))
	}()
	select {
	case err := <-writeDone:
		t.Fatalf("blocked write returned before cancellation: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-writeDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled write = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled write remained blocked")
	}
	if got := mixer.Stats().Inputs["alpha"].Bytes; got != frameBytes {
		t.Fatalf("queued bytes after cancelled write = %d, want %d", got, frameBytes)
	}
}

func TestPCM16MixerBlockedWriteUnblocksOnClose(t *testing.T) {
	mixer, frameBytes := newFullInputMixer(t)
	if err := mixer.Write("alpha", make([]byte, frameBytes)); err != nil {
		t.Fatalf("fill input: %v", err)
	}
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- mixer.Write("alpha", pcm16(8))
	}()
	select {
	case err := <-writeDone:
		t.Fatalf("blocked write returned before close: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := mixer.Close(); err != nil {
		t.Fatalf("close mixer: %v", err)
	}
	select {
	case err := <-writeDone:
		if !errors.Is(err, ErrMixerClosed) {
			t.Fatalf("write after close = %v, want ErrMixerClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not unblock write")
	}
}

func TestPCM16MixerBlockedWriteUnblocksOnInputRemoval(t *testing.T) {
	mixer, frameBytes := newFullInputMixer(t)
	defer mixer.Close()
	if err := mixer.Write("alpha", make([]byte, frameBytes)); err != nil {
		t.Fatalf("fill input: %v", err)
	}
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- mixer.Write("alpha", pcm16(9))
	}()
	select {
	case err := <-writeDone:
		t.Fatalf("blocked write returned before input removal: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := mixer.RemoveInput("alpha"); err != nil {
		t.Fatalf("remove input: %v", err)
	}
	select {
	case err := <-writeDone:
		if !errors.Is(err, ErrMixerInputMissing) {
			t.Fatalf("write after input removal = %v, want ErrMixerInputMissing", err)
		}
	case <-time.After(time.Second):
		t.Fatal("input removal did not unblock write")
	}
}

func TestPCM16MixerBlockedWriteUnblocksOnInternalFailure(t *testing.T) {
	mixer, frameBytes := newFullInputMixer(t)
	defer mixer.Close()
	if err := mixer.Write("alpha", make([]byte, frameBytes)); err != nil {
		t.Fatalf("fill input: %v", err)
	}
	failure := errors.New("test mixer failure")
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- mixer.Write("alpha", pcm16(10))
	}()
	select {
	case err := <-writeDone:
		t.Fatalf("blocked write returned before internal failure: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	mixer.setError(failure)
	select {
	case err := <-writeDone:
		if !errors.Is(err, failure) {
			t.Fatalf("write after internal failure = %v, want %v", err, failure)
		}
	case <-time.After(time.Second):
		t.Fatal("internal failure did not unblock write")
	}
}

func TestPCM16MixerRejectsChunkLargerThanBoundedQueue(t *testing.T) {
	mixer, frameBytes := newFullInputMixer(t)
	defer mixer.Close()
	oversized := make([]byte, frameBytes+2)
	if err := mixer.WriteContext(context.Background(), "alpha", oversized); !errors.Is(err, ErrMixerInputBufferFull) {
		t.Fatalf("oversized write = %v, want ErrMixerInputBufferFull", err)
	}
	if got := mixer.Stats().Inputs["alpha"].Bytes; got != 0 {
		t.Fatalf("queued bytes after oversized write = %d, want 0", got)
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
		releaseOnce := sync.Once{}
		type readResult struct {
			pcm []byte
			err error
		}
		readResultCh := make(chan readResult, 1)
		allFrames := providerDeltas * providerFrames
		go func() {
			got := make([]byte, 0, allFrames*frameBytes)
			for frameIndex := 0; frameIndex < allFrames; frameIndex++ {
				frame, frameErr := mixer.ReadFrame(ctx)
				if frameErr != nil {
					readResultCh <- readResult{err: frameErr}
					return
				}
				if len(frame) != frameBytes {
					readResultCh <- readResult{err: errors.New("mixer emitted an invalid frame size")}
					return
				}
				got = append(got, frame...)
				if frameIndex == 0 {
					close(firstFrame)
					// This represents a room pump blocked in session ingestion after
					// it consumed one frame from the mixer's output queue.
					<-releaseDownstream
				}
			}
			readResultCh <- readResult{pcm: got}
		}()
		released := false
		defer func() {
			if !released {
				releaseOnce.Do(func() { close(releaseDownstream) })
			}
		}()

		if err := mixer.Write("alpha", providerPCM16Delta(0, providerDeltaBytes)); err != nil {
			t.Fatalf("first provider delta: %v", err)
		}
		select {
		case <-firstFrame:
		case result := <-readResultCh:
			t.Fatalf("receive first output frame: %v", result.err)
		case <-ctx.Done():
			t.Fatalf("receive first output frame: %v", ctx.Err())
		}

		waitForMixerStats(t, mixer, func(stats PCM16MixerStats) bool {
			return stats.Output.Frames == stats.Output.CapacityFrames && stats.Inputs["alpha"].Frames > 0
		})

		// Keep the provider-shaped 400 ms arrival cadence while the downstream
		// consumer is stalled. The first additional delta still fits in the
		// bounded input queue; the next one must wait rather than be rejected.
		time.Sleep(providerCadence)
		if err := mixer.Write("alpha", providerPCM16Delta(1, providerDeltaBytes)); err != nil {
			t.Fatalf("provider delta 1: %v", err)
		}
		time.Sleep(providerCadence)
		writeDone := make(chan error, 1)
		go func() {
			writeDone <- mixer.WriteContext(ctx, "alpha", providerPCM16Delta(2, providerDeltaBytes))
		}()
		select {
		case err := <-writeDone:
			t.Fatalf("stalled provider delta returned before downstream release: %v", err)
		case <-time.After(100 * time.Millisecond):
		}

		stats := mixer.Stats()
		inputStats := stats.Inputs["alpha"]
		t.Logf("stalled input=%s/%s output=%s/%s; provider delta 2 is waiting", inputStats.Duration, inputStats.CapacityDuration, stats.Output.Duration, stats.Output.CapacityDuration)
		if stats.Output.Frames != stats.Output.CapacityFrames {
			t.Fatalf("output queue recovered unexpectedly: %+v", stats.Output)
		}
		if inputStats.Duration < 600*time.Millisecond {
			t.Fatalf("input occupancy = %s, want accumulated burst backlog", inputStats.Duration)
		}

		releaseOnce.Do(func() { close(releaseDownstream) })
		released = true
		select {
		case err := <-writeDone:
			if err != nil {
				t.Fatalf("provider delta 2 after downstream recovery: %v", err)
			}
		case <-ctx.Done():
			t.Fatalf("provider delta 2 remained blocked after downstream recovery: %v", ctx.Err())
		}
		var result readResult
		select {
		case result = <-readResultCh:
		case <-ctx.Done():
			t.Fatalf("drain provider-shaped output after downstream recovery: %v", ctx.Err())
		}
		if result.err != nil {
			t.Fatalf("drain provider-shaped output after downstream recovery: %v", result.err)
		}
		want := make([]byte, 0, allFrames*frameBytes)
		for delta := 0; delta < providerDeltas; delta++ {
			want = append(want, providerPCM16Delta(delta, providerDeltaBytes)...)
		}
		if !bytes.Equal(result.pcm, want) {
			t.Fatalf("recovered downstream changed PCM ordering: got %d bytes, want %d", len(result.pcm), len(want))
		}
		stats = mixer.Stats()
		inputStats = stats.Inputs["alpha"]
		if inputStats.Duration > inputStats.CapacityDuration || stats.Output.Duration > stats.Output.CapacityDuration {
			t.Fatalf("queue occupancy exceeded capacity after recovery: %+v", stats)
		}
		if errors.Is(mixer.Err(), ErrMixerInputBufferFull) {
			t.Fatalf("mixer recorded input overflow after bounded backpressure: %v", mixer.Err())
		}
	})
}

func newFullInputMixer(t *testing.T) (*PCM16Mixer, int) {
	t.Helper()
	mixer, err := NewPCM16MixerWithConfig(context.Background(), PCM16MixerConfig{
		Format:            PCM16Format{SampleRate: 100, Channels: 1, FrameDuration: time.Second},
		InputQueueFrames:  1,
		OutputQueueFrames: 1,
	})
	if err != nil {
		t.Fatalf("new mixer: %v", err)
	}
	if err := mixer.AddInput("alpha"); err != nil {
		mixer.Close()
		t.Fatalf("add input: %v", err)
	}
	return mixer, mixer.FrameBytes()
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

func readMixerFrameWithContext(t *testing.T, mixer *PCM16Mixer) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	frame, err := mixer.ReadFrame(ctx)
	if err != nil {
		t.Fatalf("read deterministic mixer frame: %v", err)
	}
	if got, want := len(frame), mixer.FrameBytes(); got != want {
		t.Fatalf("deterministic mixer frame bytes = %d, want %d", got, want)
	}
	return frame
}

type deterministicPCM16Cadence struct {
	ticks    chan time.Time
	stopped  chan struct{}
	stopOnce sync.Once
}

func newDeterministicPCM16Cadence() *deterministicPCM16Cadence {
	return &deterministicPCM16Cadence{
		ticks:   make(chan time.Time, 8),
		stopped: make(chan struct{}),
	}
}

func (c *deterministicPCM16Cadence) C() <-chan time.Time {
	return c.ticks
}

func (c *deterministicPCM16Cadence) Stop() {
	c.stopOnce.Do(func() { close(c.stopped) })
}

func (c *deterministicPCM16Cadence) Advance() {
	c.ticks <- time.Time{}
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
