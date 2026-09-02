package services

import (
	"context"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

func TestRTCDeviceBoundSessionAcceptedCancelDiscardsQueuedPlayback(t *testing.T) {
	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("new virtual registry: %v", err)
	}

	binding, err := PrepareRTCDeviceBindings(RTCDeviceBindingRequest{
		Registry:      registry,
		OutputDevice:  "virtual:output",
		OutputPresent: true,
	})
	if err != nil {
		t.Fatalf("prepare output binding: %v", err)
	}
	if binding == nil || binding.Sink == nil {
		t.Fatalf("binding = %#v, want output sink", binding)
	}
	defer func() { _ = binding.Close() }()

	observe, err := audio.NewDeviceSource(registry, "virtual:input")
	if err != nil {
		t.Fatalf("open virtual output observer: %v", err)
	}
	defer func() { _ = observe.Close() }()

	queued := cancelPlaybackFrame(17)
	if err := binding.Sink.Pump(context.Background(), &recordingRTCInboundMedia{
		frames: []rtc.PCMFrame{{Samples: queued}},
	}); err != nil {
		t.Fatalf("queue provider playback: %v", err)
	}
	if got := binding.Sink.PlaybackStats().QueuedSamples; got != len(queued) {
		t.Fatalf("queued playback samples = %d, want %d", got, len(queued))
	}

	provider := &cancelPlaybackSession{receive: messages.NewTypedBuffer[messages.StreamMessage](4), done: make(chan struct{})}
	bound := &rtcDeviceBoundSession{Session: provider, binding: binding}
	if !bound.Send(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeResponseCancel,
		Value: messages.NewResponseCancelValue(),
	}) {
		t.Fatal("accepted response cancel was rejected through the bool-only session path")
	}
	stats := binding.Sink.PlaybackStats()
	if stats.QueuedSamples != 0 || stats.DiscardedSamples != uint64(len(queued)) || stats.DiscardEvents != 1 {
		t.Fatalf("playback after accepted cancel = %+v, want empty queue and one exact discard", stats)
	}

	// A subsequent response may use the same local device. Only its samples
	// should be visible to the fake device consumer; the cancelled response's
	// queued frame must not be replayed first.
	responseOutcome := bound.SendWithOutcome(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeResponseCreate,
		Value: messages.NewResponseCreateValue(),
	})
	if !responseOutcome.OK() {
		t.Fatalf("subsequent response outcome = %+v", responseOutcome)
	}

	next := cancelPlaybackFrame(41)
	if err := binding.Sink.sink.WriteFrame(context.Background(), next); err != nil {
		t.Fatalf("queue subsequent response playback: %v", err)
	}
	got := make([]int16, audio.FrameSize)
	readContext, readCancel := context.WithTimeout(context.Background(), time.Second)
	defer readCancel()
	if err := observe.ReadFrame(readContext, got); err != nil {
		t.Fatalf("read subsequent response playback: %v", err)
	}
	if !reflect.DeepEqual(got, next) {
		t.Fatalf("device samples after cancel = first:%d, want subsequent response first:%d", got[0], next[0])
	}

	sent := provider.sentSnapshot()
	if len(sent) != 2 || sent[0].Type != messages.StreamTypeResponseCancel || sent[1].Type != messages.StreamTypeResponseCreate {
		t.Fatalf("provider messages = %#v, want accepted cancel then response create", sent)
	}
}

func TestRTCDeviceBoundSessionRejectedCancelDoesNotDiscardPlayback(t *testing.T) {
	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("new virtual registry: %v", err)
	}
	binding, err := PrepareRTCDeviceBindings(RTCDeviceBindingRequest{
		Registry:      registry,
		OutputDevice:  "virtual:output",
		OutputPresent: true,
	})
	if err != nil {
		t.Fatalf("prepare output binding: %v", err)
	}
	defer func() { _ = binding.Close() }()

	queued := cancelPlaybackFrame(23)
	if err := binding.Sink.sink.WriteFrame(context.Background(), queued); err != nil {
		t.Fatalf("queue provider playback: %v", err)
	}
	provider := &cancelPlaybackSession{
		receive:      messages.NewTypedBuffer[messages.StreamMessage](1),
		done:         make(chan struct{}),
		cancelStatus: messages.SessionSendBufferFull,
	}
	bound := &rtcDeviceBoundSession{Session: provider, binding: binding}
	outcome := bound.SendWithOutcome(context.Background(), messages.StreamMessage{Type: messages.StreamTypeResponseCancel})
	if outcome.OK() || outcome.Status != messages.SessionSendBufferFull {
		t.Fatalf("rejected response cancel outcome = %+v, want buffer_full", outcome)
	}
	stats := binding.Sink.PlaybackStats()
	if stats.QueuedSamples != len(queued) || stats.DiscardedSamples != 0 || stats.DiscardEvents != 0 {
		t.Fatalf("playback after rejected cancel = %+v, want queued samples unchanged", stats)
	}
}

func TestRTCDeviceSinkDiscardUnblocksPacedProviderBurst(t *testing.T) {
	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("new virtual registry: %v", err)
	}
	sink, err := NewRTCDeviceSink(registry, "virtual:output")
	if err != nil {
		t.Fatalf("open virtual RTC sink: %v", err)
	}
	defer func() { _ = sink.Close() }()

	_, high, err := audio.PlaybackQueueWatermarks(audio.DefaultDeviceFormat())
	if err != nil {
		t.Fatalf("resolve playback watermarks: %v", err)
	}
	frames := make([]rtc.PCMFrame, high/audio.FrameSize+2)
	for index := range frames {
		frames[index] = rtc.PCMFrame{Samples: cancelPlaybackFrame(int16(100 + index))}
	}
	pumpErr := make(chan error, 1)
	go func() {
		pumpErr <- sink.Pump(context.Background(), &recordingRTCInboundMedia{frames: frames})
	}()

	deadline := time.Now().Add(time.Second)
	for sink.PlaybackStats().QueuedSamples < high && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := sink.PlaybackStats().QueuedSamples; got != high {
		t.Fatalf("paced burst queued %d samples, want high watermark %d", got, high)
	}
	if got := sink.DiscardPlayback(); got != high {
		t.Fatalf("DiscardPlayback() = %d, want %d queued samples", got, high)
	}
	select {
	case err := <-pumpErr:
		if err != nil {
			t.Fatalf("pump after discard: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("discard did not wake the blocked playback-capacity waiter")
	}
	stats := sink.PlaybackStats()
	if stats.QueuedSamples != 0 || stats.DroppedSamples != 0 || stats.DiscardedSamples != uint64(high) {
		t.Fatalf("playback after paced discard = %+v", stats)
	}
}

func TestRTCDeviceSinkInterruptionUsesActuallyConsumedDeviceSamples(t *testing.T) {
	registry, err := audio.NewSimulatedDuplexRegistry(audio.DuplexScenario{
		Seed:    19,
		Render:  audio.ClockSpec{NominalRate: audio.SampleRate, Quanta: []int{audio.FrameSize}},
		Capture: audio.ClockSpec{NominalRate: audio.SampleRate, Quanta: []int{audio.FrameSize}},
	})
	if err != nil {
		t.Fatalf("new callback-clocked registry: %v", err)
	}
	sink, err := NewRTCDeviceSink(registry, "")
	if err != nil {
		t.Fatalf("open callback-clocked RTC sink: %v", err)
	}
	defer func() { _ = sink.Close() }()

	// Model playback can begin behind a queued hold-tone fade. That earlier
	// local cue must not inflate the provider-relative truncation cursor.
	generation, blocked := sink.playbackState()
	if err := sink.observedWritePlayback(context.Background(), cancelPlaybackFrame(101), generation, blocked, false); err != nil {
		t.Fatalf("queue pre-response hold-tone frame: %v", err)
	}

	response := rtc.PlaybackResponse{ResponseID: "resp-device-clock", ItemID: "item-device-clock"}
	sink.StartPlayback(response)
	frames := make([]rtc.PCMFrame, 10)
	for index := range frames {
		frames[index] = rtc.PCMFrame{
			Samples: cancelPlaybackFrame(int16(300 + index)), PlaybackResponse: response,
		}
	}
	pumpErr := make(chan error, 1)
	go func() {
		pumpErr <- sink.Pump(context.Background(), &recordingRTCInboundMedia{frames: frames})
	}()

	deadline := time.Now().Add(time.Second)
	for sink.PlaybackStats().QueuedSamples < 2*audio.FrameSize && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := registry.Advance(2); err != nil {
		t.Fatalf("advance hold-tone and model device callbacks: %v", err)
	}

	audioEndMS, ok := sink.InterruptPlayback(response)
	if !ok {
		t.Fatal("device interruption did not report a playback cursor")
	}
	if audioEndMS != 30 {
		t.Fatalf("device playback cursor = %d ms, want one 30 ms model frame after excluded hold tone", audioEndMS)
	}
	select {
	case err := <-pumpErr:
		if err != nil {
			t.Fatalf("pump after server-VAD interruption: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server-VAD interruption did not release the paced provider pump")
	}
	stats := sink.PlaybackStats()
	if stats.QueuedSamples != 0 || stats.DiscardEvents == 0 || stats.DroppedSamples != 0 {
		t.Fatalf("device queue after interruption = %+v", stats)
	}
}

func TestRTCDeviceBoundSessionDropsInFlightPlaybackAcrossCancelAndResume(t *testing.T) {
	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("new virtual registry: %v", err)
	}
	binding, err := PrepareRTCDeviceBindings(RTCDeviceBindingRequest{
		Registry:      registry,
		OutputDevice:  "virtual:output",
		OutputPresent: true,
	})
	if err != nil {
		t.Fatalf("prepare output binding: %v", err)
	}
	if binding == nil || binding.Sink == nil {
		t.Fatalf("binding = %#v, want output sink", binding)
	}
	defer func() { _ = binding.Close() }()

	provider := &cancelPlaybackSession{receive: messages.NewTypedBuffer[messages.StreamMessage](4), done: make(chan struct{})}
	bound := &rtcDeviceBoundSession{Session: provider, binding: binding}
	stale := cancelPlaybackFrame(73)
	inbound := &cancelBoundaryInboundMedia{
		frame: rtc.PCMFrame{Samples: stale},
		onFirstRead: func() {
			if outcome := bound.SendWithOutcome(context.Background(), messages.StreamMessage{
				Type:  messages.StreamTypeResponseCancel,
				Value: messages.NewResponseCancelValue(),
			}); !outcome.OK() {
				t.Fatalf("cancel outcome = %+v", outcome)
			}
			if outcome := bound.SendWithOutcome(context.Background(), messages.StreamMessage{
				Type:  messages.StreamTypeResponseCreate,
				Value: messages.NewResponseCreateValue(),
			}); !outcome.OK() {
				t.Fatalf("response create outcome = %+v", outcome)
			}
		},
	}
	if err := binding.Sink.Pump(context.Background(), inbound); err != nil {
		t.Fatalf("pump stale playback: %v", err)
	}
	if got := binding.Sink.PlaybackStats().QueuedSamples; got != 0 {
		t.Fatalf("stale playback queued %d samples after cancel/resume, want 0", got)
	}

	next := cancelPlaybackFrame(91)
	if err := binding.Sink.Pump(context.Background(), &recordingRTCInboundMedia{
		frames: []rtc.PCMFrame{{Samples: next}},
	}); err != nil {
		t.Fatalf("current playback write = %v", err)
	}
	if got := binding.Sink.PlaybackStats().QueuedSamples; got != len(next) {
		t.Fatalf("current response queued %d samples, want %d", got, len(next))
	}
}

type cancelBoundaryInboundMedia struct {
	mu          sync.Mutex
	frame       rtc.PCMFrame
	onFirstRead func()
	read        bool
}

func (m *cancelBoundaryInboundMedia) ReadFrame(context.Context) (rtc.PCMFrame, error) {
	m.mu.Lock()
	if m.read {
		m.mu.Unlock()
		return rtc.PCMFrame{}, io.EOF
	}
	m.read = true
	frame, onFirstRead := m.frame, m.onFirstRead
	m.mu.Unlock()
	if onFirstRead != nil {
		onFirstRead()
	}
	return frame, nil
}

func (*cancelBoundaryInboundMedia) Close() error { return nil }

func cancelPlaybackFrame(seed int16) []int16 {
	frame := make([]int16, audio.FrameSize)
	for index := range frame {
		frame[index] = seed + int16(index%31)
	}
	return frame
}

type cancelPlaybackSession struct {
	receive      *messages.TypedBuffer[messages.StreamMessage]
	done         chan struct{}
	cancelStatus messages.SessionSendStatus

	mu   sync.Mutex
	sent []messages.StreamMessage
}

func (s *cancelPlaybackSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

func (s *cancelPlaybackSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	if err := ctx.Err(); err != nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: err}
	}
	if msg.Type == messages.StreamTypeResponseCancel && s.cancelStatus != "" {
		return messages.SessionSendOutcome{Status: s.cancelStatus}
	}
	s.mu.Lock()
	s.sent = append(s.sent, msg)
	s.mu.Unlock()
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

func (s *cancelPlaybackSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *cancelPlaybackSession) Done() <-chan struct{} { return s.done }

func (s *cancelPlaybackSession) Close() error { return nil }

func (s *cancelPlaybackSession) sentSnapshot() []messages.StreamMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]messages.StreamMessage(nil), s.sent...)
}

var (
	_ messages.Session                  = (*cancelPlaybackSession)(nil)
	_ messages.SessionSendOutcomeSender = (*cancelPlaybackSession)(nil)
)
