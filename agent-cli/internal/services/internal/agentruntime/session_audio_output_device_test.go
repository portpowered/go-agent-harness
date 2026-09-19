package agentruntime

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

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

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

// TestRunSessionWithAudioOutAndRTCDeviceOutputRoutesOneSession proves that
// file capture and RTC playback are independent consumers of one provider
// session. When both are selected, the file observes the exact PCM accepted
// by the device path rather than a separate upstream AUDIO.DELTA stream.
func TestRunSessionWithAudioOutAndRTCDeviceOutputRoutesOneSession(t *testing.T) {
	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("new virtual registry: %v", err)
	}
	deviceObserver, err := devicegw.NewDeviceSource(registry, "virtual:input")
	if err != nil {
		t.Fatalf("open virtual device observer: %v", err)
	}
	defer deviceObserver.Close()

	fileSamples := sessionAudioFrame(1200)
	deviceSamples := sessionAudioFrame(-2400)
	media := &singleFrameInboundMedia{
		frame:  audio.PCMFrame{Samples: deviceSamples},
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
		runErr <- RunSessionWithAudioOut(ctx, io.Discard, SessionRunOptions{ModelCatalog: testModelCatalog(),
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
	frame      audio.PCMFrame
	read       atomic.Bool
	closeCount atomic.Int32
	closed     chan struct{}
	closeOnce  sync.Once
}

func (m *singleFrameInboundMedia) ReadFrame(ctx context.Context) (audio.PCMFrame, error) {
	select {
	case <-ctx.Done():
		return audio.PCMFrame{}, ctx.Err()
	default:
	}
	if m.read.Swap(true) {
		return audio.PCMFrame{}, io.EOF
	}
	return audio.PCMFrame{Samples: append([]int16(nil), m.frame.Samples...)}, nil
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
var _ audio.InboundMedia = (*singleFrameInboundMedia)(nil)

func TestRunSessionWithAudioOut_PreflightsOutputPathBeforeSessionConnect(t *testing.T) {
	directoryTarget := filepath.Join(t.TempDir(), "response.wav")
	if err := os.Mkdir(directoryTarget, 0o700); err != nil {
		t.Fatal(err)
	}

	for name, path := range map[string]string{
		"missing parent":   filepath.Join(t.TempDir(), "missing", "response.wav"),
		"directory target": directoryTarget,
	} {
		t.Run(name, func(t *testing.T) {
			inferencer := &scriptedSessionInferencer{}
			err := RunSessionWithAudioOut(context.Background(), io.Discard, SessionRunOptions{
				ModelCatalog:      testModelCatalog(),
				ReplayPath:        "synthetic.json",
				SessionInferencer: inferencer,
			}, path)
			if err == nil || !strings.Contains(err.Error(), "--audio-out") {
				t.Fatalf("preflight error = %v, want --audio-out context", err)
			}
			if name == "directory target" {
				var streamErr *audio.StreamError
				if !errors.As(err, &streamErr) || streamErr.Path != path || streamErr.Operation != "open" {
					t.Fatalf("directory target error = %v, want typed open StreamError for %q", err, path)
				}
			}
			if inferencer.connected {
				t.Fatal("invalid audio output path connected to the session")
			}
		})
	}
}

func TestRunSessionWithAudioOut_DoesNotTruncateWhenSessionOptionsAreInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preserved.wav")
	want := []byte("preserve existing output")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	inferencer := &scriptedSessionInferencer{}
	err := RunSessionWithAudioOut(context.Background(), io.Discard, SessionRunOptions{
		ModelCatalog:      testModelCatalog(),
		RecordPath:        "record.json",
		ReplayPath:        "replay.json",
		SessionInferencer: inferencer,
	}, path)
	if err == nil || !strings.Contains(err.Error(), "--record") || !strings.Contains(err.Error(), "--replay") {
		t.Fatalf("invalid session options error = %v, want both capture flags", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("invalid session options changed output target to %q", got)
	}
	if inferencer.connected {
		t.Fatal("invalid session options connected to the session")
	}
}

func TestRunSessionWithAudioOut_FinalizesOnCleanInterrupt(t *testing.T) {
	first, second := sessionAudioFrame(800), sessionAudioFrame(-900)
	providerRelease := make(chan struct{})
	firstWritten := make(chan struct{})
	writer := &sessionAudioOutRepairWriter{firstWritten: firstWritten}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inferencer := &sessionAudioOutRepairInferencer{
		first:   pcm16Bytes(first),
		second:  pcm16Bytes(second),
		release: providerRelease,
		cancel:  cancel,
	}
	defer inferencer.releaseProvider()

	errCh := make(chan error, 1)
	go func() {
		errCh <- RunSessionWithAudioOut(ctx, writer, SessionRunOptions{
			ModelCatalog:      testModelCatalog(),
			ReplayPath:        "synthetic.json",
			SessionInferencer: inferencer,
			RuntimeObserver:   inferencer,
		}, "-")
	}()
	select {
	case <-firstWritten:
	case <-time.After(2 * time.Second):
		t.Fatal("first accepted audio delta did not reach the output writer")
	}
	cancel()
	inferencer.releaseProvider()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("clean interrupt error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("clean interrupt did not finalize the session")
	}
	want := append(pcm16Bytes(first), pcm16Bytes(second)...)
	if got := writer.snapshot(); !bytes.Equal(got, want) {
		t.Fatalf("interrupted PCM = %d bytes, want both accepted deltas (%d bytes)", len(got), len(want))
	}
}

func TestRunSessionWithAudioOut_PreservesSinkWriteAndProviderCloseErrors(t *testing.T) {
	wantErr := errors.New("stdout write failed")
	closeErr := errors.New("provider close failed after sink write")
	inferencer := &durationTestInferencer{
		events: []messages.StreamMessage{{
			Type:  messages.StreamTypeAudioDelta,
			Role:  messages.RoleAssistant,
			Value: messages.NewAudioDeltaValue(pcm16Bytes(sessionAudioFrame(700))),
		}},
		sessionCloseErr: closeErr,
	}
	err := RunSessionWithAudioOut(context.Background(), sessionAudioOutRepairErrorWriter{err: wantErr}, SessionRunOptions{
		ModelCatalog:      testModelCatalog(),
		ReplayPath:        "synthetic.json",
		SessionInferencer: inferencer,
	}, "-")
	if !errors.Is(err, wantErr) {
		t.Fatalf("write error = %v, want underlying sink error", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("write error = %v, want provider close error", err)
	}
}

type sessionAudioOutRepairWriter struct {
	mu           sync.Mutex
	data         bytes.Buffer
	firstWritten chan struct{}
	firstOnce    sync.Once
}

func (w *sessionAudioOutRepairWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	n, err := w.data.Write(data)
	firstWritten := w.firstWritten
	w.mu.Unlock()
	if n > 0 && firstWritten != nil {
		w.firstOnce.Do(func() { close(firstWritten) })
	}
	return n, err
}

func (w *sessionAudioOutRepairWriter) snapshot() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.data.Bytes()...)
}

type sessionAudioOutRepairErrorWriter struct{ err error }

func (w sessionAudioOutRepairErrorWriter) Write([]byte) (int, error) { return 0, w.err }

type sessionAudioOutRepairInferencer struct {
	first       []byte
	second      []byte
	release     chan struct{}
	releaseOnce sync.Once
	cancel      context.CancelFunc
}

func (i *sessionAudioOutRepairInferencer) ObserveSessionRuntime(SessionRuntimeObservation) {
	if i.cancel != nil {
		i.cancel()
	}
}

func (i *sessionAudioOutRepairInferencer) releaseProvider() {
	i.releaseOnce.Do(func() { close(i.release) })
}

func (i *sessionAudioOutRepairInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session := newScriptedSession()
	go func() {
		session.recv.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeSessionOpen,
			Value: messages.NewSessionOpenValue("audio-contract-session", "session"),
		})
		session.recv.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeAudioDelta,
			Role:  messages.RoleAssistant,
			Value: messages.NewAudioDeltaValue(i.first),
		})
		<-i.release
		session.recv.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeAudioDelta,
			Role:  messages.RoleAssistant,
			Value: messages.NewAudioDeltaValue(i.second),
		})
		session.recv.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeMessageEnd,
			Role:  messages.RoleAssistant,
			Value: messages.NewMessageEndValue(messages.TokenUsage{}),
		})
	}()
	return session, nil
}
