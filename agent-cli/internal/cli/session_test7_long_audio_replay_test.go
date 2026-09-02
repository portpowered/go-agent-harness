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
	test7OpenAILongOutputFixture = "testdata/test7-openai-long-output.base64"
	test7SeedChunks              = 4
	test7OutputChunks            = 127
	test7FullChunkBytes          = 19200
	test7FinalChunkBytes         = 12000
	test7ProviderBytes           = 2431200
	test7SeedSHA256              = "2660b60334df1c3ea7c6d2c8419e5c861eeb9d8a54187d12878e715ec1d1bc11"
)

// TestSessionCommandReplaysTest7LongOpenAIAudioTo16kLoopback exercises the
// longest response shape captured in test7.json at the session/device edges.
// The real response contained 126 full 400 ms deltas plus a 250 ms tail: 50.65
// seconds of PCM16/24 kHz audio delivered much faster than playback. Cycling
// four consecutive captured deltas keeps the fixture compact while preserving
// real provider PCM, provider chunk sizing, total duration, and burst pressure.
// Exact 16 kHz loopback equality proves that conversion and device pacing lose,
// duplicate, and reorder zero samples across the long response.
func TestSessionCommandReplaysTest7LongOpenAIAudioTo16kLoopback(t *testing.T) {
	deltas, providerPCM := loadTest7LongOpenAIAudio(t)
	providerSamples := decodeTest7PCM16(t, providerPCM)
	want, err := wavio.Resample(providerSamples, wavio.Rate24kHz, audio.SampleRate)
	if err != nil {
		t.Fatalf("independently resample test7 PCM: %v", err)
	}

	capturePath := filepath.Join(t.TempDir(), "test7-long-openai-edge-replay.session.json")
	writeTest7LongOpenAICapture(t, capturePath, deltas)

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
	command.SetArgs([]string{"--replay", capturePath, "--audio-out-device", "virtual:output"})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- command.ExecuteContext(ctx) }()

	got := make([]int16, 0, len(want))
	for len(got) < len(want) {
		batch := make([]int16, min(audio.FrameSize, len(want)-len(got)))
		if err := observer.ReadSamples(ctx, batch); err != nil {
			t.Fatalf("read test7 loopback at sample %d; stderr=%q: %v", len(got), stderr.String(), err)
		}
		got = append(got, batch...)
	}
	if err := <-runErr; err != nil {
		t.Fatalf("execute test7 OpenAI edge replay: %v; stderr=%q", err, stderr.String())
	}
	if !equalPCM16(got, want) {
		t.Fatalf("test7 loopback PCM differs: got %d samples, want %d exact samples", len(got), len(want))
	}
	stats := observer.PlaybackStats()
	if stats.DroppedSamples != 0 || stats.OverflowEvents != 0 || stats.QueuedSamples != 0 {
		t.Fatalf("test7 device lost or retained samples: %+v", stats)
	}
	t.Logf("test7 long replay preserved %d provider samples as %d exact 16 kHz device samples; playback=%+v", len(providerSamples), len(got), stats)
}

func decodeTest7PCM16(t *testing.T, pcm []byte) []int16 {
	t.Helper()
	if len(pcm)%2 != 0 {
		t.Fatalf("test7 OpenAI PCM has odd byte count %d", len(pcm))
	}
	samples := make([]int16, len(pcm)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(pcm[index*2:]))
	}
	return samples
}

func loadTest7LongOpenAIAudio(t *testing.T) ([]string, []byte) {
	t.Helper()
	file, err := os.Open(test7OpenAILongOutputFixture)
	if err != nil {
		t.Fatalf("open test7 OpenAI audio fixture: %v", err)
	}
	defer func() { _ = file.Close() }()

	var seeds [][]byte
	var seedPCM []byte
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 64*1024)
	for scanner.Scan() {
		encoded := strings.TrimSpace(scanner.Text())
		if encoded == "" || strings.HasPrefix(encoded, "#") {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatalf("decode test7 OpenAI seed delta %d: %v", len(seeds), err)
		}
		if len(decoded) != test7FullChunkBytes {
			t.Fatalf("test7 OpenAI seed delta %d bytes = %d, want %d", len(seeds), len(decoded), test7FullChunkBytes)
		}
		seeds = append(seeds, decoded)
		seedPCM = append(seedPCM, decoded...)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan test7 OpenAI audio fixture: %v", err)
	}
	if len(seeds) != test7SeedChunks {
		t.Fatalf("test7 OpenAI seed deltas = %d, want %d", len(seeds), test7SeedChunks)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(seedPCM)); got != test7SeedSHA256 {
		t.Fatalf("test7 OpenAI seed PCM SHA-256 = %s, want %s", got, test7SeedSHA256)
	}

	deltas := make([]string, 0, test7OutputChunks)
	providerPCM := make([]byte, 0, test7ProviderBytes)
	for index := 0; index < test7OutputChunks; index++ {
		chunk := seeds[index%len(seeds)]
		if index == test7OutputChunks-1 {
			chunk = chunk[:test7FinalChunkBytes]
		}
		deltas = append(deltas, base64.StdEncoding.EncodeToString(chunk))
		providerPCM = append(providerPCM, chunk...)
	}
	if len(providerPCM) != test7ProviderBytes {
		t.Fatalf("test7 synthesized provider PCM bytes = %d, want %d", len(providerPCM), test7ProviderBytes)
	}
	return deltas, providerPCM
}

func writeTest7LongOpenAICapture(t *testing.T, path string, deltas []string) {
	t.Helper()
	sequence := 0
	records := make([]gwtesting.CapturedSessionEvent, 0, len(deltas)+7)
	add := func(direction gwtesting.SessionEventDirection, payload any) {
		sequence++
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal test7 replay event %d: %v", sequence, err)
		}
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatalf("read test7 replay event type %d: %v", sequence, err)
		}
		records = append(records, gwtesting.CapturedSessionEvent{
			Sequence: sequence, Direction: direction, TimestampMs: int64(sequence), Type: envelope.Type,
			PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage, Payload: data,
		})
	}

	add(gwtesting.DirectionClientToServer, map[string]any{
		"type": "session.update", "session": map[string]any{
			"model": "gpt-realtime-2.1",
			"audio": map[string]any{"output": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}}},
		},
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "session.created", "session": map[string]any{
			"id": "sess-test7-long", "type": "realtime", "model": "gpt-realtime-2.1",
			"audio": map[string]any{"output": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}}},
		},
	})
	add(gwtesting.DirectionClientToServer, map[string]any{
		"type": "conversation.item.create", "item": map[string]any{
			"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "replay test7 long audio"}},
		},
	})
	add(gwtesting.DirectionClientToServer, map[string]any{"type": "response.create"})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.created", "response": map[string]any{"id": "resp-test7-long", "status": "in_progress"},
	})
	for _, delta := range deltas {
		add(gwtesting.DirectionServerToClient, map[string]any{
			"type": "response.output_audio.delta", "response_id": "resp-test7-long", "item_id": "item-test7-long",
			"output_index": 0, "content_index": 0, "delta": delta,
		})
	}
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.output_audio.done", "response_id": "resp-test7-long", "item_id": "item-test7-long",
		"output_index": 0, "content_index": 0,
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.done", "response": map[string]any{"id": "resp-test7-long", "status": "completed"},
	})

	capture := gwtesting.SessionCapture{
		Version:  gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{Name: "openai", Model: "gpt-realtime-2.1"},
		Session:  gwtesting.SessionMetadata{ID: "sess-test7-long", StartedAtUTC: "2026-09-02T19:41:55.000000Z"},
		Records:  records,
	}
	data, err := json.Marshal(capture)
	if err != nil {
		t.Fatalf("marshal test7 replay capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write test7 replay capture: %v", err)
	}
}
