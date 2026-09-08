package agentruntime_test

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

import (
	"context"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	services "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	providerswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/wire"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

const feedbackBypassFrameCount = 6

// TestRunSessionHeadphoneShapedPlaybackPreservesIndependentSpeech exercises
// the production session boundary with a local topology whose microphone and
// speaker are separate virtual pairs. Assistant playback and user speech
// overlap at the device pumps, but the independent user frames must remain
// provider-bound and must not produce a local feedback warning or cancel.
func TestRunSessionHeadphoneShapedPlaybackPreservesIndependentSpeech(t *testing.T) {
	registry := newRTCDeviceRoundtripRegistry(t)
	userFeed, err := devicegw.NewDeviceSink(registry, rtcRoundtripMicFeedID)
	if err != nil {
		t.Fatalf("open virtual microphone feeder: %v", err)
	}
	speakerObserve, err := devicegw.NewDeviceSource(registry, rtcRoundtripSpeakerID)
	if err != nil {
		_ = userFeed.Close()
		t.Fatalf("open virtual speaker observer: %v", err)
	}

	warning := make(chan string, 1)
	providerFrames := make(chan audio.PCMFrame, feedbackBypassFrameCount+1)
	media := audio.NewSessionMedia(func(ctx context.Context, frame audio.PCMFrame) error {
		owned := audio.PCMFrame{Samples: append([]int16(nil), frame.Samples...)}
		select {
		case providerFrames <- owned:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	inferencer := newFeedbackBypassSessionInferencer(media)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.Cleanup(func() {
		if session := inferencer.sessionValue(); session != nil {
			_ = session.Close()
		}
		_ = userFeed.Close()
		_ = speakerObserve.Close()
	})

	runErr := make(chan error, 1)
	go func() {
		runErr <- services.RunSession(ctx, io.Discard, services.SessionRunOptions{
			Provider:          "openai",
			Model:             services.DefaultOpenAIRealtimeModel,
			APIKey:            "test-key",
			ConfigDir:         t.TempDir(),
			ModelCatalog:      providerswire.NewModelCatalog(),
			BareLive:          true,
			SessionInferencer: inferencer,
			RTCDeviceBinding: services.RTCDeviceBindingRequest{
				Registry:              registry,
				InputPresent:          true,
				OutputPresent:         true,
				FeedbackWarningWriter: feedbackBypassWarningWriter(warning),
			},
		})
	}()

	var session *feedbackBypassSession
	select {
	case session = <-inferencer.connected:
	case <-ctx.Done():
		t.Fatalf("headphone-shaped session did not connect: %v", ctx.Err())
	}

	wantFrames := make([][]int16, feedbackBypassFrameCount)
	readCtx, readCancel := context.WithTimeout(ctx, 3*time.Second)
	defer readCancel()
	for frameIndex := range wantFrames {
		playback := feedbackBypassPCMFrame(frameIndex, 31)
		if err := media.PushInbound(playback); err != nil {
			t.Fatalf("push assistant playback frame %d: %v", frameIndex, err)
		}
		observedPlayback := make([]int16, audio.FrameSize)
		if err := speakerObserve.ReadFrame(readCtx, observedPlayback); err != nil {
			t.Fatalf("observe assistant playback frame %d: %v", frameIndex, err)
		}
		if !reflect.DeepEqual(observedPlayback, playback) {
			t.Fatalf("speaker playback frame %d changed at the device boundary", frameIndex)
		}

		wantFrames[frameIndex] = feedbackBypassPCMFrame(frameIndex, 907)
		if err := userFeed.WriteFrame(ctx, wantFrames[frameIndex]); err != nil {
			t.Fatalf("feed independent user frame %d: %v", frameIndex, err)
		}
	}

	// The paired-live policy may retain the first bounded analysis window even
	// when it ultimately classifies the capture as non-feedback. Collect only
	// after the complete deterministic window has crossed the gate so the test
	// checks the user-visible order and loss contract rather than an immediate
	// delivery latency that the policy intentionally does not promise.
	for frameIndex := range wantFrames {
		select {
		case got := <-providerFrames:
			if !reflect.DeepEqual(got.Samples, wantFrames[frameIndex]) {
				t.Fatalf("provider frame %d = %d samples with unexpected PCM", frameIndex, len(got.Samples))
			}
		case <-readCtx.Done():
			t.Fatalf("independent user frame %d did not reach provider media: %v", frameIndex, readCtx.Err())
		}
	}

	session.finish()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("headphone-shaped session: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("headphone-shaped session did not finish: %v", ctx.Err())
	}

	assertFeedbackBypassNotInterrupted(t, warning, session)
	if err := userFeed.Close(); err != nil {
		t.Fatalf("close virtual microphone feeder: %v", err)
	}
	if err := speakerObserve.Close(); err != nil {
		t.Fatalf("close virtual speaker observer: %v", err)
	}
	if got := registry.Observations(); got.OpenCount != 4 || got.ReleaseCount != 4 {
		t.Fatalf("headphone-shaped registry observations = %+v, want four opens and releases", got)
	}
}

// TestRunSessionReplayBypassesPairedDeviceFeedbackController uses a connected
// virtual speaker/microphone pair so the test would lose correlated frames if
// replay accidentally enabled the live acoustic gate. Replay must keep the
// device pumps available for round-trip callers while bypassing that policy.
func TestRunSessionReplayBypassesPairedDeviceFeedbackController(t *testing.T) {
	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("new paired virtual registry: %v", err)
	}
	warning := make(chan string, 1)
	providerFrames := make(chan audio.PCMFrame, feedbackBypassFrameCount+1)
	media := audio.NewSessionMedia(func(ctx context.Context, frame audio.PCMFrame) error {
		select {
		case providerFrames <- audio.PCMFrame{Samples: append([]int16(nil), frame.Samples...)}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	inferencer := newFeedbackBypassSessionInferencer(media)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.Cleanup(func() {
		if session := inferencer.sessionValue(); session != nil {
			_ = session.Close()
		}
	})

	runErr := make(chan error, 1)
	go func() {
		runErr <- services.RunSession(ctx, io.Discard, services.SessionRunOptions{
			ReplayPath:        "synthetic.json",
			SessionInferencer: inferencer,
			RTCDeviceBinding: services.RTCDeviceBindingRequest{
				Registry:              registry,
				InputPresent:          true,
				OutputPresent:         true,
				FeedbackWarningWriter: feedbackBypassWarningWriter(warning),
			},
		})
	}()

	var session *feedbackBypassSession
	select {
	case session = <-inferencer.connected:
	case <-ctx.Done():
		t.Fatalf("replay session did not connect: %v", ctx.Err())
	}

	readCtx, readCancel := context.WithTimeout(ctx, 3*time.Second)
	defer readCancel()
	for frameIndex := 0; frameIndex < feedbackBypassFrameCount; frameIndex++ {
		want := feedbackBypassPCMFrame(frameIndex, 417)
		if err := media.PushInbound(want); err != nil {
			t.Fatalf("push replay playback frame %d: %v", frameIndex, err)
		}
		select {
		case got := <-providerFrames:
			if !reflect.DeepEqual(got.Samples, want) {
				t.Fatalf("replay provider frame %d differs from playback", frameIndex)
			}
		case <-readCtx.Done():
			t.Fatalf("replay playback frame %d was not preserved: %v", frameIndex, readCtx.Err())
		}
	}

	session.finish()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("replay session: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("replay session did not finish: %v", ctx.Err())
	}
	select {
	case got := <-warning:
		t.Fatalf("replay emitted local feedback warning %q", got)
	default:
	}
}

type feedbackBypassWarningWriter chan string

func (w feedbackBypassWarningWriter) Write(data []byte) (int, error) {
	select {
	case w <- string(data):
	default:
	}
	return len(data), nil
}

func feedbackBypassPCMFrame(frameIndex, seed int) []int16 {
	samples := make([]int16, audio.FrameSize)
	state := uint32(seed*7919 + frameIndex*104729 + 1)
	for index := range samples {
		state = state*1664525 + 1013904223
		samples[index] = int16(int32(state>>16)%24000 - 12000) //nolint:gosec // bounded deterministic PCM fixture
	}
	return samples
}

type feedbackBypassSessionInferencer struct {
	media     *audio.SessionMedia
	connected chan *feedbackBypassSession

	mu      sync.Mutex
	session *feedbackBypassSession
}

func newFeedbackBypassSessionInferencer(media *audio.SessionMedia) *feedbackBypassSessionInferencer {
	return &feedbackBypassSessionInferencer{
		media:     media,
		connected: make(chan *feedbackBypassSession, 1),
	}
}

// Request declares this seam's native PCM rate explicitly so the shared
// session audio contract resolver does not substitute the realtime provider
// default. The virtual device topology in these tests is fixed at the
// package's compatibility rate; declaring it here keeps device and provider
// rates identical, matching the resampling contract at the RTC device sink
// boundary (no conversion needed when both sides already agree).
func (i *feedbackBypassSessionInferencer) Request() inference.SessionRequest {
	return inference.SessionRequest{Config: models.SessionConfig{
		InputAudioSampleRate:  models.SampleRate(audio.SampleRate),
		OutputAudioSampleRate: models.SampleRate(audio.SampleRate),
	}}
}

func (i *feedbackBypassSessionInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session := &feedbackBypassSession{
		receive: messages.NewTypedBuffer[messages.StreamMessage](16),
		done:    make(chan struct{}),
		media:   i.media,
	}
	i.mu.Lock()
	i.session = session
	i.mu.Unlock()
	if !session.receive.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("feedback-bypass-session", "test"),
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

func (i *feedbackBypassSessionInferencer) sessionValue() *feedbackBypassSession {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.session
}

type feedbackBypassSession struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	media   *audio.SessionMedia

	mu   sync.Mutex
	sent []messages.StreamMessage
	once sync.Once
}

func (s *feedbackBypassSession) Send(ctx context.Context, message messages.StreamMessage) bool {
	select {
	case <-s.done:
		return false
	case <-ctx.Done():
		return false
	default:
	}
	s.mu.Lock()
	s.sent = append(s.sent, message)
	s.mu.Unlock()
	return true
}

func (s *feedbackBypassSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *feedbackBypassSession) Done() <-chan struct{} { return s.done }

func (s *feedbackBypassSession) RTCMedia() services.RTCMediaEndpoints {
	return s.media.Endpoints()
}

func (s *feedbackBypassSession) Close() error {
	s.once.Do(func() {
		close(s.done)
		_ = s.media.Close()
	})
	return nil
}

func (s *feedbackBypassSession) finish() { _ = s.Close() }

func (s *feedbackBypassSession) sentSnapshot() []messages.StreamMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]messages.StreamMessage(nil), s.sent...)
}

var (
	_ messages.SessionInferencer = (*feedbackBypassSessionInferencer)(nil)
	_ messages.Session           = (*feedbackBypassSession)(nil)
	_ services.RTCMediaSession   = (*feedbackBypassSession)(nil)
)

func assertFeedbackBypassNotInterrupted(t *testing.T, warning <-chan string, session *feedbackBypassSession) {
	t.Helper()
	select {
	case got := <-warning:
		t.Fatalf("headphone-shaped playback emitted feedback warning %q", got)
	default:
	}
	for _, message := range session.sentSnapshot() {
		if message.Type == messages.StreamTypeResponseCancel {
			t.Fatal("headphone-shaped independent speech caused response cancellation")
		}
	}
}
