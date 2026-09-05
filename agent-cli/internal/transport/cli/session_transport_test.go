package cli

import sessionclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"

import sessionservicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

func TestSessionCommandTransportHelpDocumentsDeferredWebRTCCapability(t *testing.T) {
	command := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), newTestSessionService(sessionservicewire.SessionDependencies{Clock: sessionclock.Real{}}), nil).Generate()
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetArgs([]string{"--help"})

	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("session --help: %v", err)
	}
	help := out.String()
	for _, want := range []string{
		"--transport string",
		"ws (default, supported)",
		"webrtc (deferred/unavailable customer path)",
		"--signaling string",
		"Deferred/unavailable WebRTC signaling endpoint",
		"requires --transport webrtc",
		"--transport webrtc requires this flag",
		"--media-source string",
		"Deferred/unavailable WebRTC receive-only external media source",
		"cannot be combined with --audio-in",
		"customer-reachable network signaling",
		"spoken-audio input wiring",
		"file, stdin, or microphone speech input",
		"supported --transport ws",
		"--audio-in-device",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("session help does not document %q:\n%s", want, help)
		}
	}
}

func TestSessionCommandTransportRejectsUnknownValueBeforeSessionSetup(t *testing.T) {
	command := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), newTestSessionService(sessionservicewire.SessionDependencies{Clock: sessionclock.Real{}}), nil).Generate()
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
	command := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), newTestSessionService(sessionservicewire.SessionDependencies{Clock: sessionclock.Real{}}), nil).Generate()
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
	command := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), newTestSessionService(sessionservicewire.SessionDependencies{Clock: sessionclock.Real{}}), nil).Generate()
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

func TestSessionCommandRejectsValidWebRTCBeforeSessionSetup(t *testing.T) {
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = filepath.Join(t.TempDir(), "missing-config")
	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("new virtual audio registry: %v", err)
	}
	inferencer := &cliSideEffectSessionInferencer{}
	toolCapabilityCalls := 0
	owner := NewSessionCommand(flags.NewAskFlags(), globalFlags, newTestSessionService(sessionservicewire.SessionDependencies{Clock: sessionclock.Real{}}), nil)
	recordPath := filepath.Join(t.TempDir(), "must-not-be-created.session.json")
	command := owner.Generate()
	command.SetArgs([]string{
		"--provider", "grok",
		"--record", recordPath,
		"--transport", "webrtc",
		"--signaling", "loopback://customer-boundary",
		"--media-source", "fixture://customer-boundary",
		"--audio-in-device", "virtual:input",
		"--audio-out-device", "virtual:output",
		"must fail before setup",
	})

	err = command.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("valid WebRTC command unexpectedly succeeded")
	}
	var unavailableErr *SessionWebRTCUnavailableError
	if !errors.As(err, &unavailableErr) {
		t.Fatalf("error type = %T, want *SessionWebRTCUnavailableError: %v", err, err)
	}
	if !errors.Is(err, ErrSessionWebRTCUnavailable) {
		t.Fatalf("error does not preserve WebRTC capability classification: %v", err)
	}
	for _, want := range []string{
		"not yet customer-usable",
		"customer-reachable network signaling",
		"spoken-audio input",
		"--transport ws",
		"--audio-in",
		"--audio-in-device",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
	if inferencer.connects != 0 {
		t.Fatalf("provider session connects = %d, want zero", inferencer.connects)
	}
	if toolCapabilityCalls != 0 {
		t.Fatalf("session tool capability loads = %d, want zero", toolCapabilityCalls)
	}
	if got := registry.Observations(); got.OpenCount != 0 || got.ReleaseCount != 0 {
		t.Fatalf("audio device observations = %+v, want no acquisition", got)
	}
	if _, statErr := os.Stat(recordPath); !os.IsNotExist(statErr) {
		t.Fatalf("recording path stat error = %v, want path to remain absent", statErr)
	}
}

func TestSessionCommandMediaSourceWithoutWebRTCIsRejectedBeforeSessionSetup(t *testing.T) {
	command := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), newTestSessionService(sessionservicewire.SessionDependencies{Clock: sessionclock.Real{}}), nil).Generate()
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
	command := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), newTestSessionService(sessionservicewire.SessionDependencies{Clock: sessionclock.Real{}}), nil).Generate()
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

type cliSideEffectSessionInferencer struct {
	connects int
}

func (i *cliSideEffectSessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	i.connects++
	return nil, errors.New("provider session should not be connected")
}
