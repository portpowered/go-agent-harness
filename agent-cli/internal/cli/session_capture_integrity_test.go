package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestSessionCLIRejectsCorruptCaptureBeforeProviderOrDerivedArtifacts(t *testing.T) {
	capture := gatewaytesting.SessionCapture{
		Version:  gatewaytesting.SessionCaptureVersion,
		Provider: gatewaytesting.SessionProviderMetadata{Name: "grok", Model: "grok-replay"},
		Session:  gatewaytesting.SessionMetadata{FixtureProvenance: gatewaytesting.SessionFixtureProvenanceSynthetic},
		Records: []gatewaytesting.CapturedSessionEvent{{
			Sequence:    1,
			Direction:   gatewaytesting.DirectionServerToClient,
			TimestampMs: 0,
			Type:        "response.output_text.delta",
			PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage,
			Payload:     json.RawMessage(`{"type":"response.output_text.delta","delta":"verified"}`),
		}},
	}
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal capture: %v", err)
	}
	corrupted := bytes.Replace(data, []byte("verified"), []byte("corrupted"), 1)
	if bytes.Equal(corrupted, data) {
		t.Fatal("test mutation did not change capture bytes")
	}

	root := t.TempDir()
	capturePath := filepath.Join(root, "corrupt.session.json")
	if err := os.WriteFile(capturePath, corrupted, 0o600); err != nil {
		t.Fatalf("write corrupt capture: %v", err)
	}
	audioPath := filepath.Join(root, "derived", "assistant.wav")
	recordDir := filepath.Join(root, "recording")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for provider connection sentinel: %v", err)
	}
	defer listener.Close()
	connected := make(chan struct{}, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = conn.Close()
			connected <- struct{}{}
		}
	}()

	command := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{
		"--replay", capturePath,
		"--provider", "grok",
		"--model", "grok-replay",
		"--api-key", "test-key",
		"--base-url", "ws://" + listener.Addr().String(),
		"--audio-out", audioPath,
		"--record-dir", recordDir,
	})
	if err := command.ExecuteContext(context.Background()); err == nil {
		t.Fatal("corrupt replay unexpectedly succeeded")
	} else {
		message := err.Error() + "\n" + stderr.String() + "\n" + stdout.String()
		for _, expected := range []string{capturePath, gatewaytesting.SessionCaptureErrorClassIntegrityChecksum, "algorithm=sha256", "expected", "actual"} {
			if !strings.Contains(message, expected) {
				t.Fatalf("error = %q, want %q", message, expected)
			}
		}
	}

	select {
	case <-connected:
		t.Fatal("provider endpoint accepted a connection before capture integrity validation")
	case <-time.After(100 * time.Millisecond):
	}
	if _, statErr := os.Stat(audioPath); !os.IsNotExist(statErr) {
		t.Fatalf("audio sink exists after integrity preflight failure: stat error = %v", statErr)
	}
	if _, statErr := os.Stat(recordDir); !os.IsNotExist(statErr) {
		t.Fatalf("recording directory exists after integrity preflight failure: stat error = %v", statErr)
	}
}
