package services_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

// TestRunSessionRTCDeviceBindingStartsRuntimePumps proves the production
// session boundary starts both device pumps from the provider-owned RTC media
// endpoints. The virtual registry provides exact device IDs and independent
// input/output loopbacks, while the fake session models a provider that owns
// the media endpoint lifecycle.
func TestRunSessionRTCDeviceBindingStartsRuntimePumps(t *testing.T) {
	registry := newRTCDeviceRoundtripRegistry(t)
	feed, err := audio.NewDeviceSink(registry, rtcRoundtripMicFeedID)
	if err != nil {
		t.Fatalf("open virtual microphone feeder: %v", err)
	}
	observe, err := audio.NewDeviceSource(registry, rtcRoundtripSpeakerID)
	if err != nil {
		_ = feed.Close()
		t.Fatalf("open virtual speaker observer: %v", err)
	}
	peer := newLoopbackRTCTrackPeer(rtcRoundtripFrameCount)
	sessionInferencer := newRuntimeRTCSessionInferencer(peer)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.Cleanup(func() {
		if session := sessionInferencer.sessionValue(); session != nil {
			_ = session.Close()
		}
		_ = peer.Close()
		_ = feed.Close()
		_ = observe.Close()
	})

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- services.RunSession(ctx, io.Discard, services.SessionRunOptions{
			ReplayPath:        "synthetic.json",
			SessionInferencer: sessionInferencer,
			RTCDeviceBinding: services.RTCDeviceBindingRequest{
				Registry:      registry,
				InputDevice:   rtcRoundtripInputID,
				OutputDevice:  rtcRoundtripOutputID,
				InputPresent:  true,
				OutputPresent: true,
			},
		})
	}()

	var session *runtimeRTCSession
	select {
	case session = <-sessionInferencer.connected:
	case <-ctx.Done():
		t.Fatalf("provider session did not connect: %v", ctx.Err())
	}

	wantFrames := make([][]int16, rtcRoundtripFrameCount)
	for frameIndex := range wantFrames {
		wantFrames[frameIndex] = rtcRoundtripPCMFrame(frameIndex)
		if err := feed.WriteFrame(ctx, wantFrames[frameIndex]); err != nil {
			t.Fatalf("feed virtual microphone frame %d: %v", frameIndex, err)
		}
	}

	readCtx, readCancel := context.WithTimeout(ctx, rtcRoundtripTimeout)
	defer readCancel()
	for frameIndex, want := range wantFrames {
		got := make([]int16, audio.FrameSize)
		if err := observe.ReadFrame(readCtx, got); err != nil {
			t.Fatalf("observe virtual speaker frame %d: %v", frameIndex, err)
		}
		if pcmAbsoluteEnergy(got) == 0 {
			t.Fatalf("observed virtual speaker frame %d has no emitted audio energy", frameIndex)
		}
		for sampleIndex := range want {
			if got[sampleIndex] != want[sampleIndex] {
				t.Fatalf("speaker frame %d sample %d = %d, want %d", frameIndex, sampleIndex, got[sampleIndex], want[sampleIndex])
			}
		}
	}

	if got := peer.Stats(); got.Writes != rtcRoundtripFrameCount || got.Reads != rtcRoundtripFrameCount {
		t.Fatalf("runtime RTC peer stats = %+v, want %d writes and reads", got, rtcRoundtripFrameCount)
	}
	session.finish()

	select {
	case err := <-runErrCh:
		if err != nil {
			t.Fatalf("RunSession with runtime RTC pumps: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("RunSession did not finish after provider close: %v", ctx.Err())
	}

	if err := feed.Close(); err != nil {
		t.Fatalf("close virtual microphone feeder: %v", err)
	}
	if err := observe.Close(); err != nil {
		t.Fatalf("close virtual speaker observer: %v", err)
	}
	if got := registry.Observations(); got.OpenCount != 4 || got.ReleaseCount != 4 {
		t.Fatalf("runtime registry observations = %+v, want four opens and releases", got)
	}
}

func TestRunSessionRTCDeviceBindingPropagatesPumpError(t *testing.T) {
	registry := newRTCDeviceRoundtripRegistry(t)
	feed, err := audio.NewDeviceSink(registry, rtcRoundtripMicFeedID)
	if err != nil {
		t.Fatalf("open virtual microphone feeder: %v", err)
	}
	wantErr := errors.New("outbound RTC track failed")
	sessionInferencer := newRuntimeRTCSessionInferencerWithMedia(services.RTCMediaEndpoints{
		Outbound: failingRTCOutboundMedia{err: wantErr},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	t.Cleanup(func() {
		if session := sessionInferencer.sessionValue(); session != nil {
			_ = session.Close()
		}
		_ = feed.Close()
	})

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- services.RunSession(ctx, io.Discard, services.SessionRunOptions{
			ReplayPath:        "synthetic.json",
			SessionInferencer: sessionInferencer,
			RTCDeviceBinding: services.RTCDeviceBindingRequest{
				Registry:     registry,
				InputDevice:  rtcRoundtripInputID,
				InputPresent: true,
			},
		})
	}()

	select {
	case <-sessionInferencer.connected:
	case <-ctx.Done():
		t.Fatalf("provider session did not connect: %v", ctx.Err())
	}
	if err := feed.WriteFrame(ctx, rtcRoundtripPCMFrame(0)); err != nil {
		t.Fatalf("feed virtual microphone frame: %v", err)
	}

	select {
	case err := <-runErrCh:
		if !errors.Is(err, wantErr) {
			t.Fatalf("RunSession error = %v, want outbound pump error", err)
		}
		var sourceErr *services.RTCDeviceSourceError
		if !errors.As(err, &sourceErr) {
			t.Fatalf("RunSession error = %v, want RTCDeviceSourceError", err)
		}
	case <-ctx.Done():
		t.Fatalf("RunSession did not surface the outbound pump error: %v", ctx.Err())
	}

	if err := feed.Close(); err != nil {
		t.Fatalf("close virtual microphone feeder: %v", err)
	}
	if got := registry.Observations(); got.OpenCount != 2 || got.ReleaseCount != 2 {
		t.Fatalf("pump-error registry observations = %+v, want two opens and releases", got)
	}
}

type runtimeRTCSessionInferencer struct {
	media     services.RTCMediaEndpoints
	connected chan *runtimeRTCSession

	mu      sync.Mutex
	session *runtimeRTCSession
}

func newRuntimeRTCSessionInferencer(peer *loopbackRTCTrackPeer) *runtimeRTCSessionInferencer {
	return newRuntimeRTCSessionInferencerWithMedia(services.RTCMediaEndpoints{
		Inbound:  peer,
		Outbound: peer,
	})
}

func newRuntimeRTCSessionInferencerWithMedia(media services.RTCMediaEndpoints) *runtimeRTCSessionInferencer {
	return &runtimeRTCSessionInferencer{
		media:     media,
		connected: make(chan *runtimeRTCSession, 1),
	}
}

func (i *runtimeRTCSessionInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session := &runtimeRTCSession{
		recv:  messages.NewTypedBuffer[messages.StreamMessage](8),
		done:  make(chan struct{}),
		media: i.media,
	}
	i.mu.Lock()
	i.session = session
	i.mu.Unlock()
	if !session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("runtime-rtc-session", "test"),
	}) {
		return nil, ctx.Err()
	}
	select {
	case i.connected <- session:
		return session, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (i *runtimeRTCSessionInferencer) sessionValue() *runtimeRTCSession {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.session
}

type runtimeRTCSession struct {
	recv  *messages.TypedBuffer[messages.StreamMessage]
	done  chan struct{}
	media services.RTCMediaEndpoints

	doneOnce sync.Once
}

type failingRTCOutboundMedia struct {
	err error
}

func (m failingRTCOutboundMedia) WriteFrame(context.Context, rtc.PCMFrame) error { return m.err }
func (m failingRTCOutboundMedia) Close() error                                   { return nil }

func (s *runtimeRTCSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	select {
	case <-ctx.Done():
		return false
	default:
	}
	return true
}

func (s *runtimeRTCSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.recv }

func (s *runtimeRTCSession) Done() <-chan struct{} { return s.done }

func (s *runtimeRTCSession) Close() error {
	s.doneOnce.Do(func() { close(s.done) })
	return nil
}

func (s *runtimeRTCSession) finish() { _ = s.Close() }

func (s *runtimeRTCSession) RTCMedia() services.RTCMediaEndpoints { return s.media }

var (
	_ messages.SessionInferencer = (*runtimeRTCSessionInferencer)(nil)
	_ messages.Session           = (*runtimeRTCSession)(nil)
	_ services.RTCMediaSession   = (*runtimeRTCSession)(nil)
)
