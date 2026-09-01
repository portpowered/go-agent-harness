package services

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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
// session. The file observes assistant AUDIO.DELTA PCM before the device path;
// the virtual output sink observes the session-owned inbound RTC PCM.
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
	media := &singleFrameInboundMedia{frame: rtc.PCMFrame{Samples: deviceSamples}}
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
	_, gotFileSamples, err := wavio.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("read captured WAV: %v", err)
	}
	if !equalInt16(gotFileSamples, fileSamples) {
		t.Fatalf("captured assistant samples = %d, want exact %d samples from AUDIO.DELTA", len(gotFileSamples), len(fileSamples))
	}
	if got := registry.Observations(); got.OpenCount != 2 || got.ReleaseCount != 1 {
		t.Fatalf("device observations before observer close = %+v, want binding+observer opens and binding release", got)
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

// TestRunSessionWithAudioOutAndRTCDeviceOutputPreservesCombinedErrors proves
// that independent output failures retain their owning flag/device context and
// that the combined session still releases its provider and device resources
// exactly once. Both writes are held until the two failure paths are active so
// the assertion does not depend on scheduler ordering.
func TestRunSessionWithAudioOutAndRTCDeviceOutputPreservesCombinedErrors(t *testing.T) {
	fileErr := errors.New("assistant file write failed")
	deviceErr := errors.New("speaker device write failed")
	fileStarted := make(chan struct{})
	deviceStarted := make(chan struct{})
	releaseWrites := make(chan struct{})

	registry := newCombinedOutputErrorRegistry(t, deviceErr, deviceStarted, releaseWrites)
	media := &singleFrameInboundMedia{frame: rtc.PCMFrame{Samples: sessionAudioFrame(-3600)}}
	inferencer := &combinedAudioOutputInferencer{
		media:             RTCMediaEndpoints{Inbound: media},
		audioPCM:          pcm16Bytes(sessionAudioFrame(1800)),
		allowSessionClose: make(chan struct{}),
	}
	close(inferencer.allowSessionClose)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	fileWriter := &coordinatedAudioErrorWriter{err: fileErr, started: fileStarted, release: releaseWrites}
	runErr := make(chan error, 1)
	go func() {
		runErr <- RunSessionWithAudioOut(ctx, fileWriter, SessionRunOptions{
			ReplayPath:        "synthetic.json",
			Prompt:            "hello",
			PromptProvided:    true,
			SessionInferencer: inferencer,
			RTCDeviceBinding: RTCDeviceBindingRequest{
				Registry:      registry,
				OutputDevice:  registry.device.ID,
				OutputPresent: true,
			},
		}, "-")
	}()

	for name, started := range map[string]<-chan struct{}{
		"file output":   fileStarted,
		"device output": deviceStarted,
	} {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatalf("%s failure path did not start: %v", name, ctx.Err())
		}
	}
	close(releaseWrites)

	var gotErr error
	select {
	case gotErr = <-runErr:
	case <-ctx.Done():
		t.Fatalf("combined error session timed out: %v", ctx.Err())
	}
	if !errors.Is(gotErr, fileErr) || !errors.Is(gotErr, deviceErr) {
		t.Fatalf("combined error = %v, want both output failures", gotErr)
	}
	if !strings.Contains(gotErr.Error(), `--audio-out "-"`) {
		t.Fatalf("combined error = %v, want --audio-out context", gotErr)
	}
	if !strings.Contains(gotErr.Error(), string(registry.device.ID)) {
		t.Fatalf("combined error = %v, want --audio-out-device/device context %q", gotErr, registry.device.ID)
	}
	var sinkErr *RTCDeviceSinkError
	if !errors.As(gotErr, &sinkErr) || sinkErr.DeviceID != registry.device.ID || sinkErr.Operation != "write" {
		t.Fatalf("combined error = %v, want typed device write error for %q", gotErr, registry.device.ID)
	}
	if got := inferencer.connects.Load(); got != 1 {
		t.Fatalf("provider session connects = %d, want exactly one", got)
	}
	if got := media.closeCount.Load(); got != 1 {
		t.Fatalf("provider media closes = %d, want exactly one", got)
	}
	if got := registry.openCount.Load(); got != 1 {
		t.Fatalf("device opens = %d, want exactly one", got)
	}
	if got := registry.closeCount.Load(); got != 1 {
		t.Fatalf("device closes = %d, want exactly one", got)
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
}

type coordinatedAudioErrorWriter struct {
	err     error
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (w *coordinatedAudioErrorWriter) Write([]byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.release
	return 0, w.err
}

type combinedOutputErrorRegistry struct {
	device       audio.Device
	writeErr     error
	writeStarted chan struct{}
	release      <-chan struct{}
	openCount    atomic.Int32
	closeCount   atomic.Int32
}

func newCombinedOutputErrorRegistry(t *testing.T, writeErr error, writeStarted chan struct{}, release <-chan struct{}) *combinedOutputErrorRegistry {
	t.Helper()
	device, err := audio.NewDevice("test", "speaker", "Test Speaker", audio.DirectionOutput)
	if err != nil {
		t.Fatalf("new test output device: %v", err)
	}
	return &combinedOutputErrorRegistry{
		device:       device,
		writeErr:     writeErr,
		writeStarted: writeStarted,
		release:      release,
	}
}

func (r *combinedOutputErrorRegistry) List() ([]audio.Device, error) {
	return []audio.Device{r.device}, nil
}

func (r *combinedOutputErrorRegistry) Default(audio.Direction) (audio.Device, error) {
	return r.device, nil
}

func (r *combinedOutputErrorRegistry) Open(id audio.DeviceID) (audio.OpenedDevice, error) {
	if id != r.device.ID {
		return nil, audio.NewDeviceNotFoundError(id)
	}
	r.openCount.Add(1)
	return &combinedOutputErrorDevice{
		registry: r,
		writeErr: r.writeErr,
		started:  r.writeStarted,
		release:  r.release,
	}, nil
}

type combinedOutputErrorDevice struct {
	registry *combinedOutputErrorRegistry
	writeErr error
	started  chan struct{}
	release  <-chan struct{}
	once     sync.Once
}

func (d *combinedOutputErrorDevice) DeviceDirection() audio.Direction { return audio.DirectionOutput }

func (d *combinedOutputErrorDevice) WriteFrame(context.Context, []int16) error {
	d.once.Do(func() { close(d.started) })
	<-d.release
	return d.writeErr
}

func (d *combinedOutputErrorDevice) Close() error {
	d.registry.closeCount.Add(1)
	return nil
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
	m.closeCount.Add(1)
	return nil
}

var _ messages.SessionInferencer = (*combinedAudioOutputInferencer)(nil)
var _ messages.Session = (*combinedAudioOutputSession)(nil)
var _ RTCMediaSession = (*combinedAudioOutputSession)(nil)
var _ rtc.InboundMedia = (*singleFrameInboundMedia)(nil)
var _ audio.DeviceRegistry = (*combinedOutputErrorRegistry)(nil)
var _ audio.OpenedDevice = (*combinedOutputErrorDevice)(nil)
