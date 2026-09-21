package cli

import servicetest "github.com/portpowered/go-agent-harness/agent-cli/internal/services/servicetest"

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

func TestSessionCommandAudioOutputMatrix(t *testing.T) {
	wantSamples := cliAudioOutputFrame()
	for _, testCase := range []cliAudioOutputCase{
		{name: "combined", fileOutput: true, deviceOut: true},
		{name: "file only", fileOutput: true},
		{name: "device only", deviceOut: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runCLIAudioOutputCase(t, testCase, wantSamples)
		})
	}
}

type cliAudioOutputCase struct {
	name       string
	fileOutput bool
	deviceOut  bool
}

func runCLIAudioOutputCase(t *testing.T, testCase cliAudioOutputCase, wantSamples []int16) {
	t.Helper()
	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("new virtual registry: %v", err)
	}
	var observer *devicegw.DeviceSource
	if testCase.deviceOut {
		observer, err = devicegw.NewDeviceSource(registry, "virtual:input")
		if err != nil {
			t.Fatalf("open virtual output observer: %v", err)
		}
		defer observer.Close()
	}
	inferencer := newCLIAudioOutputInferencer(wantSamples, testCase.deviceOut, testCase.deviceOut)
	execute, audioOutPath := newCLIAudioOutputCommand(t, testCase, inferencer, registry)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- execute(ctx) }()
	if observer != nil {
		assertCLIAudioDeviceOutput(t, ctx, cancel, observer, inferencer, registry, runErr)
	}
	inferencer.releaseClose()
	awaitCLIAudioOutputCommand(t, ctx, inferencer, registry, runErr)
	assertCLIAudioOutputResult(t, testCase, audioOutPath, wantSamples, observer, inferencer, registry)
}

func newCLIAudioOutputCommand(t *testing.T, testCase cliAudioOutputCase, inferencer *cliAudioOutputInferencer, registry *devicegw.VirtualRegistry) (func(context.Context) error, string) {
	t.Helper()
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	configYAML := "model:\n  provider: openai\n  openai:\n    model: gpt-realtime\n    api_key: test-key\n"
	if err := os.WriteFile(filepath.Join(globalFlags.ConfigDirPath, config.ConfigFileName), []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write session config: %v", err)
	}
	command := newTestLiveSessionCommand(flags.NewAskFlags(), globalFlags, inferencer, registry).Generate()
	command.SetOut(io.Discard)
	audioOutPath := filepath.Join(t.TempDir(), "assistant.wav")
	args := []string{"--provider", config.ProviderOpenAI, "--model", "gpt-realtime", "--api-key", "test-key", "--prompt", "hello"}
	if testCase.fileOutput {
		args = append(args, "--audio-out", audioOutPath)
	}
	if testCase.deviceOut {
		args = append(args, "--audio-out-device", "virtual:output")
	}
	command.SetArgs(args)
	return command.ExecuteContext, audioOutPath
}

func assertCLIAudioDeviceOutput(t *testing.T, ctx context.Context, cancel context.CancelFunc, observer *devicegw.DeviceSource, inferencer *cliAudioOutputInferencer, registry *devicegw.VirtualRegistry, runErr <-chan error) {
	t.Helper()
	got := make([]int16, audio.FrameSize)
	readCtx, readCancel := context.WithTimeout(ctx, time.Second)
	readErr := observer.ReadFrame(readCtx, got)
	readCancel()
	if readErr != nil {
		inferencer.releaseOutputTerminal()
		inferencer.releaseClose()
		cancel()
		commandErr := <-runErr
		t.Fatalf("device output read failed (session connects=%d registry=%+v, command error=%v): %v", inferencer.connects.Load(), registry.Observations(), commandErr, readErr)
	}
	if !hasNonzeroPCMSample(got) {
		t.Fatal("device output frame is silent")
	}
	inferencer.releaseOutputTerminal()
}

func awaitCLIAudioOutputCommand(t *testing.T, ctx context.Context, inferencer *cliAudioOutputInferencer, registry *devicegw.VirtualRegistry, runErr <-chan error) {
	t.Helper()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("session command: %v (registry=%+v)", err, registry.Observations())
		}
	case <-ctx.Done():
		t.Fatalf("session command timed out (registry=%+v)", registry.Observations())
	}
	select {
	case <-inferencer.sessionClosed:
	case <-time.After(time.Second):
		t.Fatal("provider session did not finish closing after the command returned")
	}
}

func assertCLIAudioOutputResult(t *testing.T, testCase cliAudioOutputCase, audioOutPath string, wantSamples []int16, observer *devicegw.DeviceSource, inferencer *cliAudioOutputInferencer, registry *devicegw.VirtualRegistry) {
	t.Helper()
	if got := inferencer.connects.Load(); got != 1 {
		t.Fatalf("provider session connects = %d, want exactly one", got)
	}
	if got := inferencer.sessionCloseCount.Load(); got != 1 {
		t.Fatalf("provider session closes = %d, want exactly one", got)
	}
	assertCLIAudioOutputFile(t, testCase, audioOutPath, wantSamples)
	assertCLIAudioOutputDeviceLifecycle(t, testCase, observer, registry)
}

func assertCLIAudioOutputFile(t *testing.T, testCase cliAudioOutputCase, path string, wantSamples []int16) {
	t.Helper()
	if !testCase.fileOutput {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("device-only file sink stat error = %v, want file to remain absent", err)
		}
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read captured file output: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("captured file output is empty")
	}
	_, got, err := wavio.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("parse captured WAV: %v", err)
	}
	if testCase.deviceOut {
		if !hasNonzeroPCMSample(got) {
			t.Fatal("combined device-bound file capture is silent")
		}
		return
	}
	if !reflect.DeepEqual(got, wantSamples) {
		t.Fatal("file-only capture differs from assistant PCM")
	}
}

func assertCLIAudioOutputDeviceLifecycle(t *testing.T, testCase cliAudioOutputCase, observer *devicegw.DeviceSource, registry *devicegw.VirtualRegistry) {
	t.Helper()
	observations := registry.Observations()
	if !testCase.deviceOut {
		if observations.OpenCount != 0 || observations.ReleaseCount != 0 {
			t.Fatalf("file-only device observations = %+v, want no device acquisition", observations)
		}
		return
	}
	if observations.OpenCount != 2 || observations.ReleaseCount != 1 {
		t.Fatalf("device observations before observer close = %+v, want two opens and one release", observations)
	}
	if err := observer.Close(); err != nil {
		t.Fatalf("close virtual output observer: %v", err)
	}
	observations = registry.Observations()
	if observations.OpenCount != 2 || observations.ReleaseCount != 2 {
		t.Fatalf("device observations after cleanup = %+v, want balanced opens and releases", observations)
	}
}

func hasNonzeroPCMSample(samples []int16) bool {
	for _, sample := range samples {
		if sample != 0 {
			return true
		}
	}
	return false
}

func cliAudioOutputFrame() []int16 {
	frame := make([]int16, audio.FrameSize*2)
	for index := range frame {
		frame[index] = int16((index*37)%20000 - 10000)
	}
	return frame
}

type cliAudioOutputInferencer struct {
	audioPCM []byte

	connects          atomic.Int32
	sessionCloseCount atomic.Int32
	closeGate         chan struct{}
	outputTerminal    chan struct{}
	sessionClosed     chan struct{}
	closeGateOnce     sync.Once
	terminalOnce      sync.Once
}

func newCLIAudioOutputInferencer(samples []int16, holdClose, holdOutputTerminal bool) *cliAudioOutputInferencer {
	inferencer := &cliAudioOutputInferencer{
		audioPCM:       cliPCM16Bytes(samples),
		closeGate:      make(chan struct{}),
		outputTerminal: make(chan struct{}),
		sessionClosed:  make(chan struct{}),
	}
	if !holdClose {
		inferencer.releaseClose()
	}
	if !holdOutputTerminal {
		inferencer.releaseOutputTerminal()
	}
	return inferencer
}

func (i *cliAudioOutputInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	i.connects.Add(1)
	session := &cliAudioOutputSession{
		receive:        messages.NewTypedBuffer[messages.StreamMessage](16),
		done:           make(chan struct{}),
		audioPCM:       append([]byte(nil), i.audioPCM...),
		closeGate:      i.closeGate,
		outputTerminal: i.outputTerminal,
		closeDone:      i.sessionClosed,
		closeCount:     &i.sessionCloseCount,
	}
	if !session.receive.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("cli-audio-output-session", "test"),
	}) {
		return nil, ctx.Err()
	}
	return session, nil
}

func (i *cliAudioOutputInferencer) releaseClose() {
	i.closeGateOnce.Do(func() { close(i.closeGate) })
}

func (i *cliAudioOutputInferencer) releaseOutputTerminal() {
	i.terminalOnce.Do(func() { close(i.outputTerminal) })
}

type cliAudioOutputSession struct {
	receive        *messages.TypedBuffer[messages.StreamMessage]
	done           chan struct{}
	audioPCM       []byte
	closeGate      <-chan struct{}
	outputTerminal <-chan struct{}
	closeDone      chan<- struct{}
	closeCount     *atomic.Int32

	audioOnce sync.Once
	closeOnce sync.Once
}

func (s *cliAudioOutputSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	select {
	case <-s.done:
		return false
	case <-ctx.Done():
		return false
	default:
	}
	if msg.Type == messages.StreamTypeSessionClose {
		select {
		case <-s.closeGate:
		case <-ctx.Done():
			return false
		}
		if !s.receive.Write(context.Background(), messages.StreamMessage{
			Type:  messages.StreamTypeSessionClose,
			Value: messages.NewSessionCloseValue("cli-audio-output-session", "test complete"),
		}) {
			return false
		}
		return s.Close() == nil
	}
	s.audioOnce.Do(func() {
		s.receive.Write(context.Background(), messages.StreamMessage{
			Type:       messages.StreamTypeMessageStart,
			Role:       messages.RoleAssistant,
			ResponseID: "cli-audio-output-response",
			Value:      messages.NewMessageStartValue(),
		})
		s.receive.Write(context.Background(), messages.StreamMessage{
			Type:       messages.StreamTypeAudioDelta,
			Role:       messages.RoleAssistant,
			ResponseID: "cli-audio-output-response",
			Value:      messages.NewAudioDeltaValue(s.audioPCM),
		})
		go func() {
			select {
			case <-s.outputTerminal:
			case <-s.done:
				return
			}
			s.receive.Write(context.Background(), messages.StreamMessage{
				Type:       messages.StreamTypeMessageEnd,
				Role:       messages.RoleAssistant,
				ResponseID: "cli-audio-output-response",
				Value:      messages.NewMessageEndValue(messages.TokenUsage{}),
			})
		}()
	})
	return true
}

func (s *cliAudioOutputSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *cliAudioOutputSession) Done() <-chan struct{} { return s.done }

func (s *cliAudioOutputSession) Close() error {
	s.closeOnce.Do(func() {
		s.closeCount.Add(1)
		close(s.done)
		if s.closeDone != nil {
			close(s.closeDone)
		}
	})
	return nil
}

func (s *cliAudioOutputSession) RTCMedia() servicetest.RTCMediaEndpoints {
	if s == nil {
		return servicetest.RTCMediaEndpoints{}
	}
	return servicetest.RTCMediaEndpoints{Inbound: &cliSingleFrameInboundMedia{samples: cliPCM16Samples(s.audioPCM)}}
}

type cliSingleFrameInboundMedia struct {
	samples []int16
	mu      sync.Mutex
	offset  int
}

func (m *cliSingleFrameInboundMedia) ReadFrame(ctx context.Context) (audio.PCMFrame, error) {
	select {
	case <-ctx.Done():
		return audio.PCMFrame{}, ctx.Err()
	default:
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.offset >= len(m.samples) {
		return audio.PCMFrame{}, io.EOF
	}
	end := m.offset + audio.FrameSize
	if end > len(m.samples) {
		end = len(m.samples)
	}
	frame := append([]int16(nil), m.samples[m.offset:end]...)
	m.offset = end
	return audio.PCMFrame{Samples: frame}, nil
}

func (*cliSingleFrameInboundMedia) Close() error { return nil }

func cliPCM16Bytes(samples []int16) []byte {
	encoded := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(encoded[index*2:], uint16(sample))
	}
	return encoded
}

func cliPCM16Samples(encoded []byte) []int16 {
	samples := make([]int16, len(encoded)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(encoded[index*2:]))
	}
	return samples
}

var _ messages.SessionInferencer = (*cliAudioOutputInferencer)(nil)
var _ messages.Session = (*cliAudioOutputSession)(nil)
var _ servicetest.RTCMediaSession = (*cliAudioOutputSession)(nil)
var _ audio.InboundMedia = (*cliSingleFrameInboundMedia)(nil)
