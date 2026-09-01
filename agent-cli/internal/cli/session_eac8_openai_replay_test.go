package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
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
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

const (
	eac8OpenAIOutputFixture = "testdata/eac8-openai-long-output.base64"
	eac8OpenAIOutputChunks  = 20
	eac8OpenAIChunkBytes    = 19200
	eac8OpenAIOutputSHA256  = "4d598d2f1b85df9cd74a33909621a5b2223a41b8e49b7070a3bfe7b5bdffa6d2"
)

// TestSessionCommandReplaysEAC8OpenAIAudioTo16kLoopback starts at the shipped
// session command and consumes actual response.output_audio.delta payloads
// extracted from eac8.json. The capture crosses the former 256-frame media
// limit, so exact loopback equality proves the OpenAI decoder, SessionMedia,
// RTC sink, 24-to-16 kHz conversion, and device pacing preserve the stream.
func TestSessionCommandReplaysEAC8OpenAIAudioTo16kLoopback(t *testing.T) {
	deltas, providerPCM := loadEAC8OpenAIAudioDeltas(t)
	providerSamples := decodeEAC8PCM16(t, providerPCM)
	want, err := wavio.Resample(providerSamples, wavio.Rate24kHz, audio.SampleRate)
	if err != nil {
		t.Fatalf("independently resample eac8 PCM: %v", err)
	}

	capturePath := filepath.Join(t.TempDir(), "eac8-openai-edge-replay.session.json")
	writeEAC8OpenAICapture(t, capturePath, deltas)

	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("new virtual registry: %v", err)
	}
	openedObserver, err := registry.OpenWithFormat("virtual:input", audio.PCM16DeviceFormat(audio.SampleRate))
	if err != nil {
		t.Fatalf("open 16 kHz loopback observer: %v", err)
	}
	observer, ok := openedObserver.(*audio.VirtualStream)
	if !ok {
		t.Fatalf("loopback observer = %T, want *audio.VirtualStream", openedObserver)
	}
	defer func() { _ = observer.Close() }()

	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	command := NewSessionCommandWithDeviceRegistry(flags.NewAskFlags(), globalFlags, nil, nil, registry).Generate()
	command.SetOut(io.Discard)
	var stderr bytes.Buffer
	command.SetErr(&stderr)
	command.SetArgs([]string{
		"--replay", capturePath,
		"--audio-out-device", "virtual:output",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- command.ExecuteContext(ctx) }()

	got := make([]int16, 0, len(want))
	for len(got) < len(want) {
		batch := make([]int16, min(audio.FrameSize, len(want)-len(got)))
		if err := observer.ReadSamples(ctx, batch); err != nil {
			select {
			case commandErr := <-runErr:
				t.Fatalf("read loopback at sample %d after command error %v; stderr=%q: %v", len(got), commandErr, stderr.String(), err)
			default:
				t.Fatalf("read loopback at sample %d; stderr=%q: %v", len(got), stderr.String(), err)
			}
		}
		got = append(got, batch...)
	}
	if err := <-runErr; err != nil {
		t.Fatalf("execute eac8 OpenAI edge replay: %v; stderr=%q", err, stderr.String())
	}
	if !equalPCM16(got, want) {
		t.Fatalf("eac8 loopback PCM differs: got %d samples, want %d exact samples", len(got), len(want))
	}
}

func loadEAC8OpenAIAudioDeltas(t *testing.T) ([]string, []byte) {
	t.Helper()
	file, err := os.Open(eac8OpenAIOutputFixture)
	if err != nil {
		t.Fatalf("open eac8 OpenAI audio fixture: %v", err)
	}
	defer func() { _ = file.Close() }()

	var deltas []string
	var pcm []byte
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 64*1024)
	for scanner.Scan() {
		encoded := strings.TrimSpace(scanner.Text())
		if encoded == "" || strings.HasPrefix(encoded, "#") {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatalf("decode eac8 OpenAI delta %d: %v", len(deltas), err)
		}
		if len(decoded) != eac8OpenAIChunkBytes {
			t.Fatalf("eac8 OpenAI delta %d bytes = %d, want %d", len(deltas), len(decoded), eac8OpenAIChunkBytes)
		}
		deltas = append(deltas, encoded)
		pcm = append(pcm, decoded...)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan eac8 OpenAI audio fixture: %v", err)
	}
	if len(deltas) != eac8OpenAIOutputChunks {
		t.Fatalf("eac8 OpenAI deltas = %d, want %d", len(deltas), eac8OpenAIOutputChunks)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(pcm)); got != eac8OpenAIOutputSHA256 {
		t.Fatalf("eac8 OpenAI PCM SHA-256 = %s, want %s", got, eac8OpenAIOutputSHA256)
	}
	return deltas, pcm
}

func decodeEAC8PCM16(t *testing.T, pcm []byte) []int16 {
	t.Helper()
	if len(pcm)%2 != 0 {
		t.Fatalf("eac8 OpenAI PCM has odd byte count %d", len(pcm))
	}
	samples := make([]int16, len(pcm)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(pcm[index*2:]))
	}
	return samples
}

func writeEAC8OpenAICapture(t *testing.T, path string, deltas []string) {
	t.Helper()
	sequence := 0
	records := make([]gwtesting.CapturedSessionEvent, 0, len(deltas)+7)
	add := func(direction gwtesting.SessionEventDirection, payload any) {
		sequence++
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal eac8 replay event %d: %v", sequence, err)
		}
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatalf("read eac8 replay event type %d: %v", sequence, err)
		}
		records = append(records, gwtesting.CapturedSessionEvent{
			Sequence: sequence, Direction: direction, TimestampMs: int64(sequence),
			Type: envelope.Type, PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage, Payload: data,
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
			"id": "sess-eac8-edge", "type": "realtime", "model": "gpt-realtime-2.1-mini",
			"audio": map[string]any{"output": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}}},
		},
	})
	add(gwtesting.DirectionClientToServer, map[string]any{
		"type": "conversation.item.create", "item": map[string]any{
			"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "replay eac8 audio"}},
		},
	})
	add(gwtesting.DirectionClientToServer, map[string]any{"type": "response.create"})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.created", "response": map[string]any{"id": "resp-eac8-edge", "status": "in_progress"},
	})
	for _, delta := range deltas {
		add(gwtesting.DirectionServerToClient, map[string]any{
			"type": "response.output_audio.delta", "response_id": "resp-eac8-edge", "item_id": "item-eac8-edge",
			"output_index": 0, "content_index": 0, "delta": delta,
		})
	}
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.output_audio.done", "response_id": "resp-eac8-edge", "item_id": "item-eac8-edge",
		"output_index": 0, "content_index": 0,
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.done", "response": map[string]any{"id": "resp-eac8-edge", "status": "completed"},
	})

	capture := gwtesting.SessionCapture{
		Version:  gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{Name: "openai", Model: "gpt-realtime-2.1-mini"},
		Session:  gwtesting.SessionMetadata{ID: "sess-eac8-edge", StartedAtUTC: "2026-09-01T21:16:03.816284Z"},
		Records:  records,
	}
	data, err := json.Marshal(capture)
	if err != nil {
		t.Fatalf("marshal protected eac8 replay capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write protected eac8 replay capture: %v", err)
	}
}

func equalPCM16(left, right []int16) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
