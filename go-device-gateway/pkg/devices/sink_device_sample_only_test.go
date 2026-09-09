package devices

import audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestDeviceSinkSampleOnlyWritesFullFramesAndTailExactly(t *testing.T) {
	handle := newSampleOnlySinkTestHandle()
	sink := newSampleOnlySink(t, handle)

	fullFrame := sampleOnlyPattern(audio.FrameSize, -240)
	fullFromSamples := sampleOnlyPattern(audio.FrameSize, 240)
	tail := []int16{-32768, -17, 0, 19, 32767}
	want := [][]int16{
		append([]int16(nil), fullFrame...),
		append([]int16(nil), fullFromSamples...),
		append([]int16(nil), tail...),
	}
	if err := sink.WriteFrame(context.Background(), fullFrame); err != nil {
		t.Fatalf("WriteFrame(sample-only) = %v", err)
	}
	if err := sink.WriteSamples(context.Background(), fullFromSamples); err != nil {
		t.Fatalf("WriteSamples(full, sample-only) = %v", err)
	}
	if err := sink.WriteSamples(context.Background(), tail); err != nil {
		t.Fatalf("WriteSamples(tail, sample-only) = %v", err)
	}

	fullFrame[0] = 1
	fullFromSamples[0] = 2
	tail[0] = 3
	if got := handle.sampleWrites(); !reflect.DeepEqual(got, want) {
		t.Fatalf("sample-only writes = %v, want exact ordered PCM %v", summarizeSampleWrites(got), summarizeSampleWrites(want))
	}
}

func TestDeviceSinkSampleOnlyBackendMutationCannotReachCallers(t *testing.T) {
	handle := newSampleOnlySinkTestHandle()
	handle.mutateInput = true
	sink := newSampleOnlySink(t, handle)

	frame := sampleOnlyPattern(audio.FrameSize, -7)
	wantFrame := append([]int16(nil), frame...)
	if err := sink.WriteFrame(context.Background(), frame); err != nil {
		t.Fatalf("mutating WriteFrame(sample-only) = %v", err)
	}
	if !reflect.DeepEqual(frame, wantFrame) {
		t.Fatalf("backend mutation changed caller frame: first=%d want=%d", frame[0], wantFrame[0])
	}

	tail := []int16{-4, -3, -2, -1, 0, 1, 2, 3, 4}
	wantTail := append([]int16(nil), tail...)
	if err := sink.WriteSamples(context.Background(), tail); err != nil {
		t.Fatalf("mutating WriteSamples(sample-only) = %v", err)
	}
	if !reflect.DeepEqual(tail, wantTail) {
		t.Fatalf("backend mutation changed caller tail: got=%v want=%v", tail, wantTail)
	}
}

func TestDeviceSinkSampleOnlyErrorsAndCancellation(t *testing.T) {
	sentinel := errors.New("sample-only backend write failed")
	handle := newSampleOnlySinkTestHandle()
	handle.writeErr = sentinel
	sink := newSampleOnlySink(t, handle)
	if err := sink.WriteFrame(context.Background(), make([]int16, audio.FrameSize)); !errors.Is(err, sentinel) {
		t.Fatalf("sample-only WriteFrame error = %v, want errors.Is sentinel", err)
	}
	if err := sink.WriteSamples(context.Background(), []int16{1, 2, 3}); !errors.Is(err, sentinel) {
		t.Fatalf("sample-only WriteSamples error = %v, want errors.Is sentinel", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	before := handle.writeCalls()
	if err := sink.WriteFrame(cancelled, make([]int16, audio.FrameSize)); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled sample-only WriteFrame = %v, want context.Canceled", err)
	}
	if err := sink.WriteSamples(cancelled, []int16{4, 5, 6}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled sample-only WriteSamples = %v, want context.Canceled", err)
	}
	if got := handle.writeCalls(); got != before {
		t.Fatalf("pre-cancelled sample-only writes = %d, want unchanged at %d", got, before)
	}

	blocked := newSampleOnlySinkTestHandle()
	blocked.started = make(chan struct{})
	blocked.release = make(chan struct{})
	blocked.block = true
	blockedSink := newSampleOnlySink(t, blocked)
	writeDone := make(chan error, 1)
	ctx, stop := context.WithCancel(context.Background())
	go func() { writeDone <- blockedSink.WriteSamples(ctx, []int16{7, 8, 9}) }()
	waitSampleOnlySignal(t, blocked.started)
	stop()
	if err := waitSampleOnlyError(t, writeDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled blocked sample-only write = %v, want context.Canceled", err)
	}
}

func TestDeviceSinkSampleOnlyValidationAndExactlyOnceClose(t *testing.T) {
	handle := newSampleOnlySinkTestHandle()
	sink := newSampleOnlySink(t, handle)
	for name, frame := range map[string][]int16{
		"empty": make([]int16, 0),
		"short": make([]int16, audio.FrameSize-1),
		"long":  make([]int16, audio.FrameSize+1),
	} {
		if err := sink.WriteFrame(context.Background(), frame); !errors.Is(err, audio.ErrInvalidFrameSize) {
			t.Errorf("invalid %s WriteFrame = %v, want ErrInvalidFrameSize", name, err)
		}
	}
	if err := sink.WriteSamples(context.Background(), nil); err != nil {
		t.Fatalf("empty sample-only WriteSamples = %v, want nil", err)
	}
	var nilContext context.Context
	if err := sink.WriteSamples(nilContext, []int16{1, 2, 3}); err != nil {
		t.Fatalf("nil-context sample-only WriteSamples = %v", err)
	}
	if got := handle.writeCalls(); got != 1 {
		t.Fatalf("validation/backend calls = %d, want one valid tail write", got)
	}

	const closers = 16
	var wg sync.WaitGroup
	for range closers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := sink.Close(); err != nil {
				t.Errorf("concurrent Close = %v", err)
			}
		}()
	}
	wg.Wait()
	if got := handle.closeCalls(); got != 1 {
		t.Fatalf("sample-only close calls = %d, want exactly once", got)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("repeated Close = %v", err)
	}
	if err := sink.WriteFrame(context.Background(), make([]int16, audio.FrameSize)); !errors.Is(err, audio.ErrClosed) {
		t.Fatalf("WriteFrame after sample-only Close = %v, want ErrClosed", err)
	}
	if err := sink.WriteSamples(context.Background(), []int16{4}); !errors.Is(err, audio.ErrClosed) {
		t.Fatalf("WriteSamples after sample-only Close = %v, want ErrClosed", err)
	}

	blocked := newSampleOnlySinkTestHandle()
	blocked.started = make(chan struct{})
	blocked.release = make(chan struct{})
	blocked.block = true
	blockedSink := newSampleOnlySink(t, blocked)
	writeDone := make(chan error, 1)
	go func() { writeDone <- blockedSink.WriteFrame(context.Background(), make([]int16, audio.FrameSize)) }()
	waitSampleOnlySignal(t, blocked.started)
	closeDone := make(chan error, 1)
	go func() { closeDone <- blockedSink.Close() }()
	if err := waitSampleOnlyError(t, closeDone); err != nil {
		t.Fatalf("Close during blocked sample-only write = %v", err)
	}
	if err := waitSampleOnlyError(t, writeDone); !errors.Is(err, audio.ErrClosed) {
		t.Fatalf("blocked sample-only write after Close = %v, want ErrClosed", err)
	}
	if got := blocked.closeCalls(); got != 1 {
		t.Fatalf("blocked sample-only close calls = %d, want one", got)
	}
}

func TestDeviceSinkSampleOnlyPreservesNativeFrameAndBytePrecedence(t *testing.T) {
	multi := &sampleOnlyMultiCapabilityHandle{}
	multiSink := newSampleOnlySink(t, multi)
	full := sampleOnlyPattern(audio.FrameSize, 1)
	if err := multiSink.WriteFrame(context.Background(), full); err != nil {
		t.Fatalf("multi-capability WriteFrame = %v", err)
	}
	if err := multiSink.WriteSamples(context.Background(), full); err != nil {
		t.Fatalf("multi-capability full WriteSamples = %v", err)
	}
	if err := multiSink.WriteSamples(context.Background(), []int16{1, 2, 3}); err != nil {
		t.Fatalf("multi-capability partial WriteSamples = %v", err)
	}
	if frames, samples := multi.counts(); frames != 2 || samples != 1 {
		t.Fatalf("multi-capability dispatch counts = frame:%d sample:%d, want frame:2 sample:1", frames, samples)
	}

	byteHandle := &adapterSinkByteHandle{direction: DirectionOutput}
	byteSink, err := NewDeviceSink(&adapterTestRegistryStub{handle: byteHandle}, "output")
	if err != nil {
		t.Fatal(err)
	}
	byteFull := []int16{-32768, -1, 0, 1, 32767}
	if err := byteSink.WriteFrame(context.Background(), makeByteCompatibilityFrame(byteFull)); err != nil {
		t.Fatalf("byte-only full WriteFrame = %v", err)
	}
	wantFull := pcm16Bytes(makeByteCompatibilityFrame(byteFull))
	if !reflect.DeepEqual(byteHandle.data, wantFull) {
		t.Fatalf("byte-only full PCM = %v, want %v", byteHandle.data, wantFull)
	}
	if err := byteSink.WriteSamples(context.Background(), byteFull); err != nil {
		t.Fatalf("byte-only partial WriteSamples = %v", err)
	}
	if !reflect.DeepEqual(byteHandle.data, pcm16Bytes(byteFull)) {
		t.Fatalf("byte-only partial PCM = %v, want %v", byteHandle.data, pcm16Bytes(byteFull))
	}
}

type sampleOnlySinkTestHandle struct {
	mu          sync.Mutex
	writes      [][]int16
	writeErr    error
	mutateInput bool
	block       bool
	started     chan struct{}
	release     chan struct{}
	startOnce   sync.Once
	closeOnce   sync.Once
	closeCount  int
	writeCount  int
}

func newSampleOnlySinkTestHandle() *sampleOnlySinkTestHandle {
	return &sampleOnlySinkTestHandle{}
}

func (h *sampleOnlySinkTestHandle) DeviceDirection() Direction { return DirectionOutput }

func (h *sampleOnlySinkTestHandle) WriteSamples(ctx context.Context, samples []int16) error {
	h.mu.Lock()
	h.writeCount++
	h.writes = append(h.writes, samples)
	h.mu.Unlock()
	h.startOnce.Do(func() {
		if h.started != nil {
			close(h.started)
		}
	})
	if h.mutateInput && len(samples) > 0 {
		samples[0] = 1234
	}
	if h.block {
		select {
		case <-h.release:
			return audio.ErrClosed
		case <-ctx.Done():
			return audio.ContextError(ctx)
		}
	}
	return h.writeErr
}

func (h *sampleOnlySinkTestHandle) Close() error {
	h.closeOnce.Do(func() {
		h.mu.Lock()
		h.closeCount++
		h.mu.Unlock()
		if h.release != nil {
			close(h.release)
		}
	})
	return nil
}

func (h *sampleOnlySinkTestHandle) sampleWrites() [][]int16 {
	h.mu.Lock()
	defer h.mu.Unlock()
	got := make([][]int16, len(h.writes))
	for i := range h.writes {
		got[i] = append([]int16(nil), h.writes[i]...)
	}
	return got
}

func (h *sampleOnlySinkTestHandle) writeCalls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.writeCount
}

func (h *sampleOnlySinkTestHandle) closeCalls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closeCount
}

type sampleOnlyMultiCapabilityHandle struct {
	mu           sync.Mutex
	frameWrites  int
	sampleWrites int
}

func (h *sampleOnlyMultiCapabilityHandle) DeviceDirection() Direction { return DirectionOutput }

func (h *sampleOnlyMultiCapabilityHandle) WriteFrame(_ context.Context, _ []int16) error {
	h.mu.Lock()
	h.frameWrites++
	h.mu.Unlock()
	return nil
}

func (h *sampleOnlyMultiCapabilityHandle) WriteSamples(_ context.Context, _ []int16) error {
	h.mu.Lock()
	h.sampleWrites++
	h.mu.Unlock()
	return nil
}

func (h *sampleOnlyMultiCapabilityHandle) Close() error { return nil }

func (h *sampleOnlyMultiCapabilityHandle) counts() (int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.frameWrites, h.sampleWrites
}

func newSampleOnlySink(t *testing.T, handle OpenedDevice) *DeviceSink {
	t.Helper()
	sink, err := NewDeviceSink(&adapterTestRegistryStub{handle: handle}, "output")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sink.Close() })
	return sink
}

func sampleOnlyPattern(size, offset int) []int16 {
	pattern := make([]int16, size)
	for i := range pattern {
		pattern[i] = int16(i + offset)
	}
	pattern[0] = -32768
	pattern[1] = 32767
	pattern[len(pattern)-1] = int16(offset - 1)
	return pattern
}

func summarizeSampleWrites(writes [][]int16) [][]int16 {
	if len(writes) <= 3 {
		return writes
	}
	return [][]int16{writes[0], writes[1], writes[len(writes)-1]}
}

func waitSampleOnlySignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("sample-only backend did not start within one second")
	}
}

func waitSampleOnlyError(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatal("sample-only operation did not finish within one second")
		return nil
	}
}

func makeByteCompatibilityFrame(prefix []int16) []int16 {
	frame := make([]int16, audio.FrameSize)
	copy(frame, prefix)
	return frame
}

func pcm16Bytes(samples []int16) []byte {
	encoded := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(encoded[i*2:], uint16(sample))
	}
	return encoded
}
