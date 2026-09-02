package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	serverVADInterruptedOutputFixture = "testdata/openai-server-vad-interrupted-output.base64"
	serverVADInterruptedOutputBytes   = 1440
	serverVADInterruptedOutputSHA256  = "bf9f2ae334b63a8f5e4f6868cebfbe3d64a07a34bfefcbef1c0cfac4806c78d6"
)

// TestSessionCommandReplaysOpenAIServerVADBargeInThrough16kDevice starts at
// the shipped command and replays the server-VAD ordering where OpenAI audio is
// already queued locally when server VAD reports speech. No virtual device
// callback is advanced, so the strict outbound truncate at zero milliseconds
// proves the boundary uses rendered samples rather than received samples.
func TestSessionCommandReplaysOpenAIServerVADBargeInThrough16kDevice(t *testing.T) {
	delta := loadServerVADInterruptedAudio(t)
	capturePath := filepath.Join(t.TempDir(), "server-vad-openai-barge-in.session.json")
	writeServerVADBargeInCapture(t, capturePath, delta)

	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("new virtual registry: %v", err)
	}
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	command := NewSessionCommandWithDeviceRegistry(flags.NewAskFlags(), globalFlags, nil, nil, registry).Generate()
	command.SetOut(io.Discard)
	var stderr bytes.Buffer
	command.SetErr(&stderr)
	command.SetArgs([]string{
		"--replay", capturePath,
		"--prompt", "replay server-vad barge in",
		"--audio-out-device", "virtual:output",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := command.ExecuteContext(ctx); err != nil {
		t.Fatalf("execute server-VAD server-VAD edge replay: %v; stderr=%q", err, stderr.String())
	}
}

func loadServerVADInterruptedAudio(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(serverVADInterruptedOutputFixture)
	if err != nil {
		t.Fatalf("read server-VAD interrupted audio fixture: %v", err)
	}
	encoded := strings.TrimSpace(string(data))
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode server-VAD interrupted audio fixture: %v", err)
	}
	if len(decoded) != serverVADInterruptedOutputBytes {
		t.Fatalf("server-VAD interrupted audio bytes = %d, want %d", len(decoded), serverVADInterruptedOutputBytes)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(decoded)); got != serverVADInterruptedOutputSHA256 {
		t.Fatalf("server-VAD interrupted audio SHA-256 = %s, want %s", got, serverVADInterruptedOutputSHA256)
	}
	return encoded
}

func writeServerVADBargeInCapture(t *testing.T, path, delta string) {
	t.Helper()
	sequence := 0
	records := make([]gwtesting.CapturedSessionEvent, 0, 10)
	add := func(direction gwtesting.SessionEventDirection, payload any) {
		sequence++
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal server-VAD replay event %d: %v", sequence, err)
		}
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatalf("read server-VAD replay event type %d: %v", sequence, err)
		}
		records = append(records, gwtesting.CapturedSessionEvent{
			Sequence: sequence, Direction: direction, TimestampMs: int64(sequence), Type: envelope.Type,
			PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage, Payload: data,
		})
	}

	add(gwtesting.DirectionClientToServer, map[string]any{
		"type": "session.update", "session": map[string]any{
			"model": "gpt-realtime-2.1-mini",
			"audio": map[string]any{"output": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}}},
		},
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "session.created", "session": map[string]any{
			"id": "sess-server-vad-edge", "type": "realtime", "model": "gpt-realtime-2.1-mini",
			"audio": map[string]any{"output": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}}},
		},
	})
	add(gwtesting.DirectionClientToServer, map[string]any{
		"type": "conversation.item.create", "item": map[string]any{
			"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "replay server-vad barge in"}},
		},
	})
	add(gwtesting.DirectionClientToServer, map[string]any{"type": "response.create"})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.created", "response": map[string]any{"id": "resp-server-vad-edge", "status": "in_progress"},
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.output_audio.delta", "response_id": "resp-server-vad-edge", "item_id": "item-server-vad-edge",
		"output_index": 0, "content_index": 0, "delta": delta,
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "input_audio_buffer.speech_started", "audio_start_ms": 7156, "item_id": "item-server-vad-user",
	})
	add(gwtesting.DirectionClientToServer, map[string]any{
		"type": "conversation.item.truncate", "item_id": "item-server-vad-edge", "content_index": 0, "audio_end_ms": 0,
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.output_audio.done", "response_id": "resp-server-vad-edge", "item_id": "item-server-vad-edge",
		"output_index": 0, "content_index": 0,
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.done", "response": map[string]any{
			"id": "resp-server-vad-edge", "status": "cancelled", "status_details": map[string]any{"type": "cancelled"},
		},
	})

	capture := gwtesting.SessionCapture{
		Version:  gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{Name: "openai", Model: "gpt-realtime-2.1-mini"},
		Session:  gwtesting.SessionMetadata{ID: "sess-server-vad-edge", StartedAtUTC: "2026-09-01T23:11:01.000000Z"},
		Records:  records,
	}
	data, err := json.Marshal(capture)
	if err != nil {
		t.Fatalf("marshal protected server-VAD replay capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write protected server-VAD replay capture: %v", err)
	}
}
