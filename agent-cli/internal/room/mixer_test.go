package room

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"reflect"
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
	defer closeMixerForTeardown(t, mixer)
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
	defer closeMixerForTeardown(t, mixer)
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
	defer closeMixerForTeardown(t, mixer)
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
	defer closeMixerForTeardown(t, mixer)
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
	defer closeMixerForTeardown(t, regular)
	if err := regular.Advance(context.Background()); !errors.Is(err, ErrMixerManualAdvance) {
		t.Fatalf("regular mixer advance error = %v, want %v", err, ErrMixerManualAdvance)
	}
}

func TestPCM16MixerReadFrameWithSourcesTracksContributors(t *testing.T) {
	format := PCM16Format{SampleRate: 1000, Channels: 1, FrameDuration: 4 * time.Millisecond}
	mixer, err := NewPCM16MixerWithConfig(context.Background(), PCM16MixerConfig{
		Format:            format,
		InputQueueFrames:  4,
		OutputQueueFrames: 2,
		Manual:            true,
	})
	if err != nil {
		t.Fatalf("new mixer: %v", err)
	}
	t.Cleanup(func() { _ = mixer.Close() })
	for _, id := range []string{"beta", "alpha"} {
		if err := mixer.AddInput(id); err != nil {
			t.Fatalf("add input %s: %v", id, err)
		}
	}
	if err := mixer.Write("alpha", pcm16(100, 200)); err != nil {
		t.Fatalf("write alpha: %v", err)
	}
	if err := mixer.Write("beta", pcm16(0, 0, 0)); err != nil {
		t.Fatalf("write beta: %v", err)
	}
	mixer.inputs["beta"].data = append(mixer.inputs["beta"].data, 0x7f)
	alphaBefore := append([]byte(nil), mixer.inputs["alpha"].data...)
	betaBefore := append([]byte(nil), mixer.inputs["beta"].data...)
	if err := mixer.Advance(context.Background()); !errors.Is(err, ErrMixerInvalidFormat) {
		t.Fatalf("malformed advance = %v, want ErrMixerInvalidFormat", err)
	}
	if !bytes.Equal(mixer.inputs["alpha"].data, alphaBefore) || !bytes.Equal(mixer.inputs["beta"].data, betaBefore) {
		t.Fatalf("failed mix changed queued inputs: alpha=%v beta=%v", mixer.inputs["alpha"].data, mixer.inputs["beta"].data)
	}
	mixer.inputs["beta"].data = pcm16(0, 0)
	if err := mixer.Advance(context.Background()); err != nil {
		t.Fatalf("corrected advance: %v", err)
	}
	frame, err := mixer.ReadFrameWithSources(context.Background())
	if err != nil {
		t.Fatalf("read frame with sources: %v", err)
	}
	if want := pcm16(100, 200, 0, 0); !bytes.Equal(frame.PCM, want) {
		t.Fatalf("mixed frame = %v, want %v", decodePCM16(frame.PCM), decodePCM16(want))
	}
	if want := []string{"alpha", "beta"}; !reflect.DeepEqual(frame.Sources, want) {
		t.Fatalf("mixed frame sources = %v, want %v", frame.Sources, want)
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
	defer closeMixerForTeardown(t, mixer)
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

func TestPCM16MixerWriteContextWithDispositionReportsBoundedBackpressure(t *testing.T) {
	format := PCM16Format{SampleRate: 100, Channels: 1, FrameDuration: time.Second}
	cadence := newDeterministicPCM16Cadence()
	mixer, err := NewPCM16MixerWithConfig(context.Background(), PCM16MixerConfig{
		Format:            format,
		InputQueueFrames:  1,
		OutputQueueFrames: 1,
		CadenceFactory: func(time.Duration) PCM16Cadence {
			return cadence
		},
	})
	if err != nil {
		t.Fatalf("new mixer: %v", err)
	}
	defer closeMixerForTeardown(t, mixer)
	if err := mixer.AddInput("alpha"); err != nil {
		t.Fatalf("add input: %v", err)
	}
	frame := make([]byte, mixer.FrameBytes())
	frame[0] = 1
	if _, err := mixer.WriteContextWithDisposition(context.Background(), "alpha", frame); err != nil {
		t.Fatalf("fill input: %v", err)
	}

	type writeResult struct {
		disposition PCM16WriteDisposition
		err         error
	}
	resultCh := make(chan writeResult, 1)
	go func() {
		disposition, writeErr := mixer.WriteContextWithDisposition(context.Background(), "alpha", frame)
		resultCh <- writeResult{disposition: disposition, err: writeErr}
	}()
	select {
	case result := <-resultCh:
		t.Fatalf("full-queue write returned before cadence drain: %+v", result)
	case <-time.After(50 * time.Millisecond):
	}
	cadence.Advance()
	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Fatalf("backpressured write: %v", result.err)
		}
		if result.disposition != PCM16WriteBackpressured {
			t.Fatalf("write disposition=%q, want %q", result.disposition, PCM16WriteBackpressured)
		}
	case <-time.After(time.Second):
		t.Fatal("backpressured write did not complete after cadence drain")
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
	defer closeMixerForTeardown(t, mixer)
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
	defer closeMixerForTeardown(t, mixer)
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
	defer closeMixerForTeardown(t, mixer)
	oversized := make([]byte, frameBytes+2)
	if err := mixer.WriteContext(context.Background(), "alpha", oversized); !errors.Is(err, ErrMixerInputBufferFull) {
		t.Fatalf("oversized write = %v, want ErrMixerInputBufferFull", err)
	}
	if got := mixer.Stats().Inputs["alpha"].Bytes; got != 0 {
		t.Fatalf("queued bytes after oversized write = %d, want 0", got)
	}
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
