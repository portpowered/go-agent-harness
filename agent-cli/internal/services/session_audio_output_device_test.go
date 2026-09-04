package services

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

// TestRunSessionWithAudioOutAndRTCDeviceOutputRoutesOneSession proves that
// file capture and RTC playback are independent consumers of one provider
// session. When both are selected, the file observes the exact PCM accepted
// by the device path rather than a separate upstream AUDIO.DELTA stream.
func TestRunSessionWithAudioOutAndRTCDeviceOutputRoutesOneSession(t *testing.T) {
	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("new virtual registry: %v", err)
	}
	deviceObserver, err := audio.NewDeviceSource(registry, "virtual:input")
	if err != nil {
		t.Fatalf("open virtual device observer: %v", err)
	}
	defer deviceObserver.Close()

	fileSamples := sessionAudioFrame(1200)
	deviceSamples := sessionAudioFrame(-2400)
	media := &singleFrameInboundMedia{
		frame:  rtc.PCMFrame{Samples: deviceSamples},
		closed: make(chan struct{}),
	}
	inferencer := &combinedAudioOutputInferencer{
		media:             RTCMediaEndpoints{Inbound: media},
		audioPCM:          pcm16Bytes(fileSamples),
		allowSessionClose: make(chan struct{}),
	}
	path := filepath.Join(t.TempDir(), "combined-response.wav")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() {
		runErr <- RunSessionWithAudioOut(ctx, io.Discard, SessionRunOptions{
			ReplayPath:        "synthetic.json",
			Prompt:            "hello",
			PromptProvided:    true,
			SessionInferencer: inferencer,
			RTCDeviceBinding: RTCDeviceBindingRequest{
				Registry:      registry,
				OutputDevice:  "virtual:output",
				OutputPresent: true,
			},
		}, path)
	}()

	readCtx, readCancel := context.WithTimeout(ctx, time.Second)
	defer readCancel()
	gotDeviceSamples := make([]int16, audio.FrameSize)
	if err := deviceObserver.ReadFrame(readCtx, gotDeviceSamples); err != nil {
		t.Fatalf("read virtual device output: %v", err)
	}
	if !equalInt16(gotDeviceSamples, deviceSamples) {
		t.Fatalf("device output samples differ from session inbound RTC PCM")
	}
	close(inferencer.allowSessionClose)
	if err := <-runErr; err != nil {
		t.Fatalf("combined file/device session: %v", err)
	}
	if got := inferencer.connects.Load(); got != 1 {
		t.Fatalf("provider session connects = %d, want exactly one", got)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read captured assistant audio: %v", err)
	}
	rate, gotFileSamples, err := wavio.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("read captured WAV: %v", err)
	}
	if rate != audio.SampleRate {
		t.Fatalf("captured device WAV rate = %d, want %d", rate, audio.SampleRate)
	}
	if !equalInt16(gotFileSamples, deviceSamples) {
		t.Fatalf("captured device samples = %d, want exact %d samples accepted by playback device", len(gotFileSamples), len(deviceSamples))
	}
	if got := registry.Observations(); got.OpenCount != 2 || got.ReleaseCount != 1 {
		t.Fatalf("device observations before observer close = %+v, want binding+observer opens and binding release", got)
	}
	select {
	case <-media.closed:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for provider media cleanup")
	}
	if got := media.closeCount.Load(); got != 1 {
		t.Fatalf("provider media closes = %d, want exactly one", got)
	}

	if err := deviceObserver.Close(); err != nil {
		t.Fatalf("close virtual device observer: %v", err)
	}
	if got := registry.Observations(); got.OpenCount != 2 || got.ReleaseCount != 2 {
		t.Fatalf("device observations after cleanup = %+v, want exactly two opens and releases", got)
	}
}

type combinedAudioOutputInferencer struct {
	media             RTCMediaEndpoints
	audioPCM          []byte
	allowSessionClose chan struct{}
	connects          atomic.Int32
}

func (i *combinedAudioOutputInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	i.connects.Add(1)
	session := &combinedAudioOutputSession{
		receive:           messages.NewTypedBuffer[messages.StreamMessage](16),
		done:              make(chan struct{}),
		media:             i.media,
		audioPCM:          append([]byte(nil), i.audioPCM...),
		allowSessionClose: i.allowSessionClose,
	}
	if !session.receive.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("combined-audio-session", "test"),
	}) {
		return nil, ctx.Err()
	}
	return session, nil
}

type combinedAudioOutputSession struct {
	receive           *messages.TypedBuffer[messages.StreamMessage]
	done              chan struct{}
	media             RTCMediaEndpoints
	audioPCM          []byte
	allowSessionClose chan struct{}

	audioOnce sync.Once
	closeOnce sync.Once
}

func (s *combinedAudioOutputSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	select {
	case <-s.done:
		return false
	case <-ctx.Done():
		return false
	default:
	}
	if msg.Type == messages.StreamTypeSessionClose {
		if !s.receive.Write(context.Background(), messages.StreamMessage{
			Type:  messages.StreamTypeSessionClose,
			Value: messages.NewSessionCloseValue("combined-audio-session", "test complete"),
		}) {
			return false
		}
		select {
		case <-s.allowSessionClose:
		case <-ctx.Done():
			return false
		}
		_ = s.Close()
		return true
	}
	s.audioOnce.Do(func() {
		s.receive.Write(context.Background(), messages.StreamMessage{
			Type:  messages.StreamTypeAudioDelta,
			Role:  messages.RoleAssistant,
			Value: messages.NewAudioDeltaValue(s.audioPCM),
		})
		s.receive.Write(context.Background(), messages.StreamMessage{
			Type:  messages.StreamTypeMessageEnd,
			Role:  messages.RoleAssistant,
			Value: messages.NewMessageEndValue(messages.TokenUsage{}),
		})
	})
	return true
}

func (s *combinedAudioOutputSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *combinedAudioOutputSession) Done() <-chan struct{} { return s.done }

func (s *combinedAudioOutputSession) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		if s.media.Inbound != nil {
			_ = s.media.Inbound.Close()
		}
		if s.media.Outbound != nil {
			_ = s.media.Outbound.Close()
		}
	})
	return nil
}

func (s *combinedAudioOutputSession) RTCMedia() RTCMediaEndpoints { return s.media }

type singleFrameInboundMedia struct {
	frame      rtc.PCMFrame
	read       atomic.Bool
	closeCount atomic.Int32
	closed     chan struct{}
	closeOnce  sync.Once
}

func (m *singleFrameInboundMedia) ReadFrame(ctx context.Context) (rtc.PCMFrame, error) {
	select {
	case <-ctx.Done():
		return rtc.PCMFrame{}, ctx.Err()
	default:
	}
	if m.read.Swap(true) {
		return rtc.PCMFrame{}, io.EOF
	}
	return rtc.PCMFrame{Samples: append([]int16(nil), m.frame.Samples...)}, nil
}

func (m *singleFrameInboundMedia) Close() error {
	m.closeOnce.Do(func() {
		m.closeCount.Add(1)
		close(m.closed)
	})
	return nil
}

var _ messages.SessionInferencer = (*combinedAudioOutputInferencer)(nil)
var _ messages.Session = (*combinedAudioOutputSession)(nil)
var _ RTCMediaSession = (*combinedAudioOutputSession)(nil)
var _ rtc.InboundMedia = (*singleFrameInboundMedia)(nil)
