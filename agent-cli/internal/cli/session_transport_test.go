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
	if !strings.Contains(help, "--transport string") || !strings.Contains(help, "ws") || !strings.Contains(help, "webrtc") {
		t.Fatalf("session help does not document --transport values:\n%s", help)
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
