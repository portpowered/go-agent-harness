package services

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestSessionReplayRejectsCorruptCaptureBeforeAudioSinkOrProviderSetup(t *testing.T) {
	capture := gwtesting.SessionCapture{
		Version:  gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{Name: "grok", Model: "grok-replay"},
		Session:  gwtesting.SessionMetadata{FixtureProvenance: gwtesting.SessionFixtureProvenanceSynthetic},
		Records: []gwtesting.CapturedSessionEvent{
			{
				Sequence:    1,
				Direction:   gwtesting.DirectionServerToClient,
				Type:        string(messages.StreamTypeTextDelta),
				PayloadType: gwtesting.SessionPayloadTypeStreamMessage,
				Payload:     json.RawMessage(`{"type":"TEXT.DELTA","value":{"type":"delta_text","content":"verified"}}`),
			},
		},
	}
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal capture: %v", err)
	}
	corrupt := []byte(strings.Replace(string(data), "verified", "corrupted", 1))
	capturePath := filepath.Join(t.TempDir(), "corrupt.session.json")
	if err := os.WriteFile(capturePath, corrupt, 0o600); err != nil {
		t.Fatalf("write capture: %v", err)
	}
	audioPath := filepath.Join(t.TempDir(), "derived", "assistant.wav")

	err = RunSessionWithAudioOut(context.Background(), io.Discard, SessionRunOptions{ReplayPath: capturePath}, audioPath)
	if err == nil {
		t.Fatal("corrupt replay unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), capturePath) || !strings.Contains(err.Error(), gwtesting.SessionCaptureErrorClassIntegrityChecksum) {
		t.Fatalf("error = %v, want capture path and integrity checksum classification", err)
	}
	if _, statErr := os.Stat(audioPath); !os.IsNotExist(statErr) {
		t.Fatalf("derived audio sink exists after preflight failure: stat error = %v", statErr)
	}
}
