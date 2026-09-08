package composite

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func TestFactoryFansOutOneProviderStreamToPhysicalAndFilePlayback(t *testing.T) {
	controller := &testController{}
	physicalPlayback := newTestPlayback(controller)
	filePlayback := newTestPlayback(nil)
	physical := &testService{handle: &testHandle{ports: devices.MediaPorts{Playback: physicalPlayback}}}
	finite := &testService{handle: &testHandle{ports: devices.MediaPorts{Playback: filePlayback}}}
	factory := NewFactory(physical, finite)

	handle, err := factory.Open(context.Background(), devices.Request{
		PlaybackEnabled: true,
		FileOutput:      &devices.FileOutput{},
		SampleRate:      24_000,
		Channels:        audio.Channels,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	playback := handle.Media().Playback
	if _, ok := playback.(*fanoutPlayback); !ok {
		t.Fatalf("playback type = %T, want bounded fan-out", playback)
	}
	provider, ok := playback.(devices.PlaybackControllerProvider)
	if !ok || provider.PlaybackController() != controller {
		t.Fatal("composite playback did not preserve the physical playback controller")
	}

	frames := []audio.PCMFrame{
		{Samples: []int16{1, -2, 3}, Format: audio.PCM16DeviceFormat(24_000), StreamID: "response", Epoch: 7, Sequence: 2, StartSample: 480},
		{Samples: []int16{4}, Format: audio.PCM16DeviceFormat(24_000), StreamID: "response", Epoch: 7, Sequence: 3, StartSample: 483, EndOfResponse: true},
	}
	if err := playback.Pump(context.Background(), &sliceInbound{frames: frames}); err != nil {
		t.Fatalf("Pump: %v", err)
	}
	if got := physicalPlayback.snapshot(); !reflect.DeepEqual(got, frames) {
		t.Fatalf("physical frames = %#v, want %#v", got, frames)
	}
	if got := filePlayback.snapshot(); !reflect.DeepEqual(got, frames) {
		t.Fatalf("file frames = %#v, want %#v", got, frames)
	}
	physicalFrames := physicalPlayback.snapshot()
	physicalFrames[0].Samples[0] = 99
	if filePlayback.snapshot()[0].Samples[0] != 1 {
		t.Fatal("fan-out children share mutable sample storage")
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	physicalHandle, ok := physical.handle.(*testHandle)
	if !ok {
		t.Fatalf("physical handle type = %T, want *testHandle", physical.handle)
	}
	finiteHandle, ok := finite.handle.(*testHandle)
	if !ok {
		t.Fatalf("finite handle type = %T, want *testHandle", finite.handle)
	}
	if physicalHandle.CloseCount() != 1 || finiteHandle.CloseCount() != 1 {
		t.Fatalf("role close counts = physical %d, finite %d; want one each", physicalHandle.CloseCount(), finiteHandle.CloseCount())
	}
	if len(physical.requests) != 1 || physical.requests[0].FileOutput != nil || !physical.requests[0].PlaybackEnabled {
		t.Fatalf("physical request = %#v, want physical playback without file port", physical.requests)
	}
	if len(finite.requests) != 1 || finite.requests[0].FileOutput == nil || !finite.requests[0].PlaybackEnabled {
		t.Fatalf("finite request = %#v, want file playback", finite.requests)
	}
}

func TestFactoryUsesFiniteRoleForFileOnlyPlayback(t *testing.T) {
	filePlayback := newTestPlayback(nil)
	finite := &testService{handle: &testHandle{ports: devices.MediaPorts{Playback: filePlayback}}}
	handle, err := NewFactory(nil, finite).Open(context.Background(), devices.Request{
		FileOutput: &devices.FileOutput{},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if handle.Media().Playback != filePlayback {
		t.Fatal("file-only request did not expose finite playback")
	}
	if err := handle.Media().Playback.Pump(context.Background(), &sliceInbound{}); err != nil {
		t.Fatalf("Pump: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestFactoryRecordsPostConversionPhysicalPlaybackThroughBoundedTap(t *testing.T) {
	sink := &testSampleSink{}
	physicalPlayback := &tapPlayback{}
	physical := &testService{handle: &testHandle{ports: devices.MediaPorts{Playback: physicalPlayback}}}
	handle, err := NewFactory(physical, nil).Open(context.Background(), devices.Request{
		PlaybackEnabled: true,
		FileOutput:      &devices.FileOutput{Sink: sink, SampleRate: 24_000},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if handle.Media().Playback != physicalPlayback {
		t.Fatalf("playback type = %T, want physical playback with tap", handle.Media().Playback)
	}
	observer := physicalPlayback.observerFunc()
	if observer == nil {
		t.Fatal("physical playback did not receive the output tap")
	}
	want := []int16{8, -9, 10}
	if err := observer(context.Background(), 16_000, want); err != nil {
		t.Fatalf("observe device samples: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := sink.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("recorded samples = %v, want post-conversion device samples %v", got, want)
	}
	if sink.CloseCount() != 1 {
		t.Fatalf("sink close count = %d, want one", sink.CloseCount())
	}
}

func TestOutputTapDoesNotWaitForSlowSinkWhileQueueHasCapacity(t *testing.T) {
	sink := &blockingSampleSink{started: make(chan struct{}), release: make(chan struct{})}
	tap, err := newOutputTap(context.Background(), sink)
	if err != nil {
		t.Fatalf("newOutputTap: %v", err)
	}
	defer func() {
		close(sink.release)
		if err := tap.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	result := make(chan error, 1)
	go func() { result <- tap.Observe(context.Background(), 16_000, []int16{1, 2, 3}) }()
	select {
	case <-sink.started:
	case <-time.After(time.Second):
		t.Fatal("slow output sink was not reached")
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("output tap waited for slow sink despite queue capacity")
	}
}

func TestOutputTapReportsBoundedOverflowWithoutWaiting(t *testing.T) {
	sink := &blockingSampleSink{started: make(chan struct{}), release: make(chan struct{})}
	tap, err := newOutputTap(context.Background(), sink)
	if err != nil {
		t.Fatalf("newOutputTap: %v", err)
	}
	if err := tap.Observe(context.Background(), 16_000, []int16{1}); err != nil {
		t.Fatalf("initial Observe: %v", err)
	}
	select {
	case <-sink.started:
	case <-time.After(time.Second):
		t.Fatal("slow output sink was not reached")
	}
	for index := 0; index < outputTapQueueCapacity; index++ {
		if err := tap.Observe(context.Background(), 16_000, []int16{int16(index + 2)}); err != nil {
			t.Fatalf("queued Observe %d: %v", index, err)
		}
	}
	result := make(chan error, 1)
	go func() { result <- tap.Observe(context.Background(), 16_000, []int16{99}) }()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("overflow Observe = %v, want optional tap failure to stay off physical path", err)
		}
	case <-time.After(time.Second):
		t.Fatal("overflow Observe waited for the slow optional sink")
	}
	if err := tap.Observe(context.Background(), 16_000, []int16{100}); err != nil {
		t.Fatalf("Observe after overflow = %v, want physical playback to continue", err)
	}
	close(sink.release)
	if err := tap.Close(); err == nil {
		t.Fatal("Close succeeded after dropped recording frames")
	} else {
		var overflow outputTapOverflowError
		if !errors.As(err, &overflow) {
			t.Fatalf("Close error = %v, want outputTapOverflowError", err)
		}
	}
}

func TestOutputTapCloseJoinsConcurrentObserver(t *testing.T) {
	sink := &testSampleSink{}
	tap, err := newOutputTap(context.Background(), sink)
	if err != nil {
		t.Fatalf("newOutputTap: %v", err)
	}
	if err := tap.beginSend(); err != nil {
		t.Fatalf("beginSend: %v", err)
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- tap.Close() }()
	if !waitForTapClosed(tap, time.Second) {
		t.Fatal("Close did not mark the tap closed")
	}
	tap.endSend()
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after the observer joined")
	}
}

func TestOutputTapDrainsAdmittedSamplesAfterCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sink := &testSampleSink{}
	tap, err := newOutputTap(ctx, sink)
	if err != nil {
		t.Fatalf("newOutputTap: %v", err)
	}
	want := []int16{4, -5, 6}
	if err := tap.Observe(ctx, 16_000, want); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	cancel()
	if err := tap.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := sink.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("drained samples = %v, want %v", got, want)
	}
}

func waitForTapClosed(tap *outputTap, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		tap.mu.Lock()
		closed := tap.closed
		tap.mu.Unlock()
		if closed {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

func TestFactoryClosesPhysicalRoleWhenFiniteAdmissionFails(t *testing.T) {
	closeErr := errors.New("physical close")
	physicalHandle := &testHandle{ports: devices.MediaPorts{Playback: newTestPlayback(nil)}, closeErr: closeErr}
	finiteErr := errors.New("finite admission")
	factory := NewFactory(
		&testService{handle: physicalHandle},
		&testService{openErr: finiteErr},
	)
	_, err := factory.Open(context.Background(), devices.Request{PlaybackEnabled: true, FileOutput: &devices.FileOutput{}})
	if !errors.Is(err, finiteErr) || !errors.Is(err, closeErr) {
		t.Fatalf("Open error = %v, want admission and cleanup errors", err)
	}
	if physicalHandle.CloseCount() != 1 {
		t.Fatalf("physical close count = %d, want one", physicalHandle.CloseCount())
	}
}

func TestFanoutReturnsChildFailureWithoutBlockingInbound(t *testing.T) {
	failure := errors.New("physical playback failed")
	physical := &failingPlayback{err: failure}
	file := newTestPlayback(nil)
	fanout, err := newFanoutPlayback([]devices.Playback{physical, file})
	if err != nil {
		t.Fatal(err)
	}
	if err := fanout.Pump(context.Background(), &blockingInbound{}); !errors.Is(err, failure) {
		t.Fatalf("Pump error = %v, want child failure", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := fanout.WaitForPump(waitCtx); !errors.Is(err, failure) {
		t.Fatalf("WaitForPump error = %v, want child failure", err)
	}
}

type testService struct {
	mu       sync.Mutex
	requests []devices.Request
	handle   devices.Handle
	openErr  error
}

func (s *testService) Open(_ context.Context, request devices.Request) (devices.Handle, error) {
	s.mu.Lock()
	s.requests = append(s.requests, request)
	s.mu.Unlock()
	return s.handle, s.openErr
}

type testHandle struct {
	ports    devices.MediaPorts
	closeErr error
	mu       sync.Mutex
	closes   int
}

func (h *testHandle) Media() devices.MediaPorts { return h.ports }

func (h *testHandle) Close() error {
	h.mu.Lock()
	h.closes++
	h.mu.Unlock()
	return h.closeErr
}

func (h *testHandle) CloseCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closes
}

type testPlayback struct {
	mu         sync.Mutex
	frames     []audio.PCMFrame
	controller audio.PlaybackController
	closed     int
}

func newTestPlayback(controller audio.PlaybackController) *testPlayback {
	return &testPlayback{controller: controller}
}

func (p *testPlayback) Pump(ctx context.Context, inbound audio.InboundMedia) error {
	for {
		frame, err := inbound.ReadFrame(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, audio.ErrSessionMediaClosed) {
				return nil
			}
			return err
		}
		frame = cloneFrame(frame)
		p.mu.Lock()
		p.frames = append(p.frames, frame)
		p.mu.Unlock()
	}
}

func (p *testPlayback) Close() error {
	p.mu.Lock()
	p.closed++
	p.mu.Unlock()
	return nil
}

func (p *testPlayback) PlaybackController() audio.PlaybackController { return p.controller }

func (p *testPlayback) snapshot() []audio.PCMFrame {
	p.mu.Lock()
	defer p.mu.Unlock()
	return cloneFrames(p.frames)
}

func (p *testPlayback) CloseCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

type failingPlayback struct{ err error }

func (p *failingPlayback) Pump(context.Context, audio.InboundMedia) error { return p.err }
func (p *failingPlayback) Close() error                                   { return nil }

type tapPlayback struct {
	mu       sync.Mutex
	observer func(context.Context, int, []int16) error
}

func (p *tapPlayback) Pump(context.Context, audio.InboundMedia) error { return nil }
func (p *tapPlayback) Close() error                                   { return nil }
func (p *tapPlayback) SetPlaybackSamplesObserver(observer func(context.Context, int, []int16) error) {
	p.mu.Lock()
	p.observer = observer
	p.mu.Unlock()
}
func (p *tapPlayback) observerFunc() func(context.Context, int, []int16) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.observer
}

type testSampleSink struct {
	mu      sync.Mutex
	samples []int16
	closed  int
}

type blockingSampleSink struct {
	started chan struct{}
	release chan struct{}
}

func (*blockingSampleSink) WriteFrame(context.Context, []int16) error { return nil }
func (s *blockingSampleSink) WriteSamples(context.Context, []int16) error {
	select {
	case <-s.started:
	default:
		close(s.started)
	}
	<-s.release
	return nil
}
func (*blockingSampleSink) Close() error { return nil }

func (s *testSampleSink) WriteFrame(context.Context, []int16) error { return nil }
func (s *testSampleSink) WriteSamples(_ context.Context, samples []int16) error {
	s.mu.Lock()
	s.samples = append(s.samples, samples...)
	s.mu.Unlock()
	return nil
}
func (s *testSampleSink) Close() error {
	s.mu.Lock()
	s.closed++
	s.mu.Unlock()
	return nil
}
func (s *testSampleSink) snapshot() []int16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int16(nil), s.samples...)
}
func (s *testSampleSink) CloseCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

type testController struct{}

func (*testController) StartPlayback(audio.PlaybackResponse) {}
func (*testController) InterruptPlayback(audio.PlaybackResponse) (int, bool) {
	return 0, false
}

type sliceInbound struct {
	frames []audio.PCMFrame
	mu     sync.Mutex
}

func (s *sliceInbound) ReadFrame(context.Context) (audio.PCMFrame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.frames) == 0 {
		return audio.PCMFrame{}, io.EOF
	}
	frame := s.frames[0]
	s.frames = s.frames[1:]
	return cloneFrame(frame), nil
}

func (*sliceInbound) Close() error { return nil }

type blockingInbound struct{}

func (*blockingInbound) ReadFrame(ctx context.Context) (audio.PCMFrame, error) {
	<-ctx.Done()
	return audio.PCMFrame{}, ctx.Err()
}

func (*blockingInbound) Close() error { return nil }

func cloneFrames(frames []audio.PCMFrame) []audio.PCMFrame {
	copyOf := make([]audio.PCMFrame, len(frames))
	for index, frame := range frames {
		copyOf[index] = cloneFrame(frame)
	}
	return copyOf
}

var _ devices.Service = (*testService)(nil)
var _ devices.Handle = (*testHandle)(nil)
var _ devices.Playback = (*testPlayback)(nil)
var _ devices.Playback = (*failingPlayback)(nil)
var _ devices.PlaybackSamplesObserverProvider = (*tapPlayback)(nil)
var _ audio.InboundMedia = (*sliceInbound)(nil)
var _ audio.InboundMedia = (*blockingInbound)(nil)
