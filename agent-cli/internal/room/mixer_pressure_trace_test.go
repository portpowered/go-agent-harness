package room

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

const (
	pressureTraceDeltaBytes   = 19_200
	pressureTraceDeltas       = 3
	pressureTraceCadence      = 400 * time.Millisecond
	pressureTraceInputFrames  = 40
	pressureTraceOutputFrames = 4
)

// pressureTraceReadResult is the downstream reader's outcome in the
// provider-shaped pressure trace.
type pressureTraceReadResult struct {
	pcm []byte
	err error
}

func TestPCM16MixerProviderShapedPressureTrace(t *testing.T) {
	format := DefaultPCM16Format()
	frameBytes, err := format.FrameBytes()
	if err != nil {
		t.Fatalf("frame bytes: %v", err)
	}
	if pressureTraceDeltaBytes%frameBytes != 0 {
		t.Fatalf("provider delta bytes = %d, want a whole number of %d-byte frames", pressureTraceDeltaBytes, frameBytes)
	}
	providerFrames := pressureTraceDeltaBytes / frameBytes

	t.Run("cadence-drained", func(t *testing.T) {
		runPressureTraceCadenceDrained(t, format, providerFrames)
	})
	t.Run("downstream-stall", func(t *testing.T) {
		runPressureTraceDownstreamStall(t, format, frameBytes, providerFrames)
	})
	// Keep the provider-shaped pressure regression alongside the byte retry.
}

func newPressureTraceMixer(ctx context.Context, t *testing.T, format PCM16Format) *PCM16Mixer {
	t.Helper()
	mixer, err := NewPCM16MixerWithConfig(ctx, PCM16MixerConfig{
		Format:            format,
		InputQueueFrames:  pressureTraceInputFrames,
		OutputQueueFrames: pressureTraceOutputFrames,
	})
	if err != nil {
		t.Fatalf("new mixer: %v", err)
	}
	return mixer
}

func pressureTraceWantPCM(capacity int) []byte {
	want := make([]byte, 0, capacity)
	for delta := 0; delta < pressureTraceDeltas; delta++ {
		want = append(want, providerPCM16Delta(delta, pressureTraceDeltaBytes)...)
	}
	return want
}

func runPressureTraceCadenceDrained(t *testing.T, format PCM16Format, providerFrames int) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	mixer := newPressureTraceMixer(ctx, t, format)
	defer closeMixerForTeardown(t, mixer)
	if err := mixer.AddInput("alpha"); err != nil {
		t.Fatalf("add input: %v", err)
	}

	want := pressureTraceWantPCM(pressureTraceDeltas * pressureTraceDeltaBytes)
	got := make([]byte, 0, len(want))
	readErr := make(chan error, 1)
	go func() {
		for frame := 0; frame < pressureTraceDeltas*providerFrames; frame++ {
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

	for delta := 0; delta < pressureTraceDeltas; delta++ {
		if delta > 0 {
			time.Sleep(pressureTraceCadence)
		}
		if err := mixer.Write("alpha", providerPCM16Delta(delta, pressureTraceDeltaBytes)); err != nil {
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
}

// startPressureTraceStalledReader drains allFrames frames, blocking on
// releaseDownstream after the first frame to model a room pump stalled in
// session ingestion.
func startPressureTraceStalledReader(ctx context.Context, mixer *PCM16Mixer, frameBytes, allFrames int, firstFrame chan<- struct{}, releaseDownstream <-chan struct{}) <-chan pressureTraceReadResult {
	readResultCh := make(chan pressureTraceReadResult, 1)
	go func() {
		got := make([]byte, 0, allFrames*frameBytes)
		for frameIndex := 0; frameIndex < allFrames; frameIndex++ {
			frame, frameErr := mixer.ReadFrame(ctx)
			if frameErr != nil {
				readResultCh <- pressureTraceReadResult{err: frameErr}
				return
			}
			if len(frame) != frameBytes {
				readResultCh <- pressureTraceReadResult{err: errors.New("mixer emitted an invalid frame size")}
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
		readResultCh <- pressureTraceReadResult{pcm: got}
	}()
	return readResultCh
}

func runPressureTraceDownstreamStall(t *testing.T, format PCM16Format, frameBytes, providerFrames int) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	mixer := newPressureTraceMixer(ctx, t, format)
	defer closeMixerForTeardown(t, mixer)
	if err := mixer.AddInput("alpha"); err != nil {
		t.Fatalf("add input: %v", err)
	}

	firstFrame := make(chan struct{})
	releaseDownstream := make(chan struct{})
	releaseOnce := sync.Once{}
	allFrames := pressureTraceDeltas * providerFrames
	readResultCh := startPressureTraceStalledReader(ctx, mixer, frameBytes, allFrames, firstFrame, releaseDownstream)
	released := false
	defer func() {
		if !released {
			releaseOnce.Do(func() { close(releaseDownstream) })
		}
	}()

	if err := mixer.Write("alpha", providerPCM16Delta(0, pressureTraceDeltaBytes)); err != nil {
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
	time.Sleep(pressureTraceCadence)
	if err := mixer.Write("alpha", providerPCM16Delta(1, pressureTraceDeltaBytes)); err != nil {
		t.Fatalf("provider delta 1: %v", err)
	}
	time.Sleep(pressureTraceCadence)
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- mixer.WriteContext(ctx, "alpha", providerPCM16Delta(2, pressureTraceDeltaBytes))
	}()
	assertPressureTraceStalled(t, mixer, writeDone)

	releaseOnce.Do(func() { close(releaseDownstream) })
	released = true
	assertPressureTraceRecovery(ctx, t, mixer, writeDone, readResultCh, allFrames*frameBytes)
}

// assertPressureTraceStalled requires the pending provider write to stay
// blocked while the downstream consumer is stalled and the queues to hold the
// accumulated burst backlog.
func assertPressureTraceStalled(t *testing.T, mixer *PCM16Mixer, writeDone <-chan error) {
	t.Helper()
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
}

func assertPressureTraceRecovery(ctx context.Context, t *testing.T, mixer *PCM16Mixer, writeDone <-chan error, readResultCh <-chan pressureTraceReadResult, wantCapacity int) {
	t.Helper()
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("provider delta 2 after downstream recovery: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("provider delta 2 remained blocked after downstream recovery: %v", ctx.Err())
	}
	var result pressureTraceReadResult
	select {
	case result = <-readResultCh:
	case <-ctx.Done():
		t.Fatalf("drain provider-shaped output after downstream recovery: %v", ctx.Err())
	}
	if result.err != nil {
		t.Fatalf("drain provider-shaped output after downstream recovery: %v", result.err)
	}
	want := pressureTraceWantPCM(wantCapacity)
	if !bytes.Equal(result.pcm, want) {
		t.Fatalf("recovered downstream changed PCM ordering: got %d bytes, want %d", len(result.pcm), len(want))
	}
	stats := mixer.Stats()
	inputStats := stats.Inputs["alpha"]
	if inputStats.Duration > inputStats.CapacityDuration || stats.Output.Duration > stats.Output.CapacityDuration {
		t.Fatalf("queue occupancy exceeded capacity after recovery: %+v", stats)
	}
	if errors.Is(mixer.Err(), ErrMixerInputBufferFull) {
		t.Fatalf("mixer recorded input overflow after bounded backpressure: %v", mixer.Err())
	}
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
		closeMixerForTeardown(t, mixer)
		t.Fatalf("add input: %v", err)
	}
	return mixer, mixer.FrameBytes()
}

// closeMixerForTeardown closes mixer when a test finishes. Close reports the
// mixer's sticky pipeline error, which tests assert explicitly where it is
// part of the contract, so teardown records it without failing the test.
func closeMixerForTeardown(t *testing.T, mixer *PCM16Mixer) {
	t.Helper()
	if err := mixer.Close(); err != nil {
		t.Logf("close mixer: %v", err)
	}
}
