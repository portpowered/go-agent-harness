package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
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
