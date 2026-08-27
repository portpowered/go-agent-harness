package cli

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestSessionCommandTransportHelpDocumentsSupportedValues(t *testing.T) {
	command := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetArgs([]string{"--help"})

	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("session --help: %v", err)
	}
	help := out.String()
	for _, want := range []string{
		"--transport string",
		"ws",
		"webrtc",
		"--signaling string",
		"requires --transport webrtc",
		"--transport webrtc requires this flag",
		"--media-source string",
		"cannot be combined with --audio-in",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("session help does not document %q:\n%s", want, help)
		}
	}
}

func TestSessionCommandTransportRejectsUnknownValueBeforeSessionSetup(t *testing.T) {
	command := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
	command.SetArgs([]string{"--transport", "quic"})

	err := command.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("unknown --transport value returned nil")
	}
	var transportErr *SessionTransportError
	if !errors.As(err, &transportErr) {
		t.Fatalf("error type = %T, want *SessionTransportError: %v", err, err)
	}
	if !errors.Is(err, ErrInvalidSessionTransport) {
		t.Fatalf("error does not preserve ErrInvalidSessionTransport: %v", err)
	}
	for _, want := range []string{"--transport", "ws", "webrtc", "quic"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
}

func TestValidateSessionTransportNormalizesSupportedValues(t *testing.T) {
	for _, testCase := range []struct {
		raw  string
		want string
	}{
		{raw: "ws", want: SessionTransportWebSocket},
		{raw: " webrtc ", want: SessionTransportWebRTC},
		{raw: "WEBRTC", want: SessionTransportWebRTC},
	} {
		t.Run(testCase.raw, func(t *testing.T) {
			got, err := validateSessionTransport(testCase.raw)
			if err != nil {
				t.Fatalf("validateSessionTransport(%q): %v", testCase.raw, err)
			}
			if got != testCase.want {
				t.Fatalf("normalized transport = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestSessionCommandSignalingWithoutWebRTCIsRejectedBeforeSessionSetup(t *testing.T) {
	command := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
	command.SetArgs([]string{"--signaling", "loopback"})

	err := command.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("--signaling without --transport webrtc returned nil")
	}
	var signalingErr *SessionSignalingError
	if !errors.As(err, &signalingErr) {
		t.Fatalf("error type = %T, want *SessionSignalingError: %v", err, err)
	}
	if !errors.Is(err, ErrSessionSignalingRequiresWebRTC) {
		t.Fatalf("error does not preserve signaling classification: %v", err)
	}
	for _, want := range []string{"--signaling", "--transport", "webrtc", "ws"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
}

func TestSessionCommandWebRTCWithoutSignalingIsRejectedBeforeSessionSetup(t *testing.T) {
	command := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
	command.SetArgs([]string{"--transport", "webrtc"})

	err := command.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("--transport webrtc without --signaling returned nil")
	}
	var signalingErr *SessionSignalingError
	if !errors.As(err, &signalingErr) {
		t.Fatalf("error type = %T, want *SessionSignalingError: %v", err, err)
	}
	if !errors.Is(err, ErrSessionWebRTCRequiresSignaling) {
		t.Fatalf("error does not preserve missing-signaling classification: %v", err)
	}
	for _, want := range []string{"--transport", "webrtc", "--signaling"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
}

func TestSessionCommandAcceptsWebRTCWithSignaling(t *testing.T) {
	command := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetArgs([]string{"--transport", "webrtc", "--signaling", " loopback ", "--media-source", "rtsp://fixture/camera"})

	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("--transport webrtc --signaling loopback --media-source: %v", err)
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Fatalf("accepted transport selection did not reach the normal session help path: %q", out.String())
	}
}

func TestSessionCommandMediaSourceWithoutWebRTCIsRejectedBeforeSessionSetup(t *testing.T) {
	command := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
	command.SetArgs([]string{"--media-source", "rtsp://fixture/camera"})

	err := command.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("--media-source without --transport webrtc returned nil")
	}
	var mediaErr *SessionMediaSourceError
	if !errors.As(err, &mediaErr) {
		t.Fatalf("error type = %T, want *SessionMediaSourceError: %v", err, err)
	}
	if !errors.Is(err, ErrSessionMediaSourceRequiresWebRTC) {
		t.Fatalf("error does not preserve media-source classification: %v", err)
	}
	for _, want := range []string{"--media-source", "--transport", "webrtc", "ws"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
}

func TestSessionCommandMediaSourceWithAudioInIsRejectedBeforeSessionSetup(t *testing.T) {
	command := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
	command.SetArgs([]string{
		"--transport", "webrtc",
		"--signaling", "loopback",
		"--media-source", "rtsp://fixture/camera",
		"--audio-in", "fixture.wav",
	})

	err := command.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("--media-source with --audio-in returned nil")
	}
	var mediaErr *SessionMediaSourceError
	if !errors.As(err, &mediaErr) {
		t.Fatalf("error type = %T, want *SessionMediaSourceError: %v", err, err)
	}
	if !errors.Is(err, ErrSessionMediaSourceConflictsWithAudioIn) {
		t.Fatalf("error does not preserve media/audio conflict classification: %v", err)
	}
	for _, want := range []string{"--media-source", "--audio-in", "incompatible"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
}

func TestValidateSessionMediaSourceRejectsEmptyExplicitValue(t *testing.T) {
	err := validateSessionMediaSource(SessionTransportWebRTC, " ", true, false)
	if err == nil {
		t.Fatal("empty --media-source value returned nil")
	}
	var mediaErr *SessionMediaSourceError
	if !errors.As(err, &mediaErr) {
		t.Fatalf("error type = %T, want *SessionMediaSourceError: %v", err, err)
	}
	if !errors.Is(err, ErrSessionMediaSourceEmpty) {
		t.Fatalf("error does not preserve empty media-source classification: %v", err)
	}
	if !strings.Contains(err.Error(), "--media-source") {
		t.Fatalf("error %q does not name --media-source", err)
	}
}

func TestSessionCommandRunsSelectedWebRTCTransport(t *testing.T) {
	const (
		signaling = "loopback://cli-signaling"
		media     = "fixture://cli-media"
	)

	inferencer := newCLITestSessionInferencer()
	runtime := &cliTestRTCRuntime{}
	owner := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, inferencer)
	var got services.SessionRuntimeSelection
	owner.SetSessionRTCRuntimeFactory(func(selection services.SessionRuntimeSelection) (services.SessionRTCRuntime, error) {
		got = selection
		return runtime, nil
	})

	command := owner.Generate()
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetArgs([]string{
		"--provider", "grok",
		"--record", filepath.Join(t.TempDir(), "cli-webrtc.session.json"),
		"--transport", "webrtc",
		"--signaling", signaling,
		"--media-source", media,
		"complete the CLI turn",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := command.ExecuteContext(ctx); err != nil {
		t.Fatalf("WebRTC session command: %v", err)
	}

	if got != (services.SessionRuntimeSelection{
		Transport:         services.SessionTransportWebRTC,
		SignalingEndpoint: signaling,
		MediaSource:       media,
	}) {
		t.Fatalf("runtime selection = %#v, want transport/signaling/media values from CLI", got)
	}
	if gotStarts := runtime.startCount(); gotStarts != 1 {
		t.Fatalf("RTC runtime starts = %d, want one selected WebRTC runtime", gotStarts)
	}
	if !strings.Contains(out.String(), "rtc CLI completed turn") {
		t.Fatalf("CLI output does not contain completed turn:\n%s", out.String())
	}
}

type cliTestRTCRuntime struct {
	mu     sync.Mutex
	starts int
}

func (r *cliTestRTCRuntime) Start(context.Context) (services.SessionRTCDataPlane, error) {
	r.mu.Lock()
	r.starts++
	r.mu.Unlock()
	return nil, nil
}

func (r *cliTestRTCRuntime) startCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts
}

func (r *cliTestRTCRuntime) Close() error { return nil }

type cliTestSessionInferencer struct {
	session *cliTestSession
}

func newCLITestSessionInferencer() *cliTestSessionInferencer {
	session := &cliTestSession{
		receive: messages.NewTypedBuffer[messages.StreamMessage](32),
		done:    make(chan struct{}),
	}
	session.receive.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("cli-webrtc", "fixture"),
	})
	return &cliTestSessionInferencer{session: session}
}

func (i *cliTestSessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}

type cliTestSession struct {
	receive      *messages.TypedBuffer[messages.StreamMessage]
	done         chan struct{}
	responseOnce sync.Once
	closeOnce    sync.Once
}

func (s *cliTestSession) Send(ctx context.Context, _ messages.StreamMessage) bool {
	s.responseOnce.Do(func() {
		for _, msg := range []messages.StreamMessage{
			{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("rtc CLI completed turn")},
			{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
			{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("cli-webrtc", "completed")},
		} {
			s.receive.Write(ctx, msg)
		}
	})
	return true
}

func (s *cliTestSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.receive }

func (s *cliTestSession) Done() <-chan struct{} { return s.done }

func (s *cliTestSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}
