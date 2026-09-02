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
	test13OpenAIAudioFixture      = "testdata/test13-openai-audio.base64"
	test13FullProviderPacketBytes = 19_200
	test13ProviderPacketCount     = 25
	test13ProviderBytes           = 468_000
	test13SeedSHA256              = "4178a70df57de1e70701ceb83553f611ebf041c7b501f1ff9d30d2e0af5a68ad"
	test13ProviderSHA256          = "b00851d38de650d2a12ebd7b2bec4b4aef6de7a7ca0457962c3729d2b0914808"
)

// TestSessionCommandReplaysTest13AudioBurstWithoutDeviceLoss preserves the
// completed 25-packet response shape from test13.json. OpenAI supplied twenty-
// four 400 ms PCM16/24 kHz packets and one 150 ms tail in about two seconds,
// while the output device consumed the resulting 9.75 seconds at 16 kHz.
// Exact loopback equality catches every dropped, duplicated, or reordered
// sample at the provider-to-device boundary.
func TestSessionCommandReplaysTest13AudioBurstWithoutDeviceLoss(t *testing.T) {
	deltas, providerPCM := loadTest13ProviderPackets(t)
	runCapturedOpenAIAudioToVirtualDevice(t, "test13", deltas, providerPCM)
}

func runCapturedOpenAIAudioToVirtualDevice(t *testing.T, fixtureName string, deltas []string, providerPCM []byte) {
	t.Helper()

	providerSamples := pcm16Bytes(t, providerPCM)
	want, err := wavio.Resample(providerSamples, wavio.Rate24kHz, audio.SampleRate)
	if err != nil {
		t.Fatalf("independently resample %s PCM: %v", fixtureName, err)
	}

	capturePath := filepath.Join(t.TempDir(), fixtureName+"-audio-device.session.json")
	writeOpenAIAudioBurstCapture(t, capturePath, fixtureName, deltas)

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
			t.Fatalf("read %s loopback at sample %d; stderr=%q: %v", fixtureName, len(got), stderr.String(), err)
		}
		got = append(got, batch...)
	}
	if err := <-runErr; err != nil {
		t.Fatalf("execute %s OpenAI edge replay: %v; stderr=%q", fixtureName, err, stderr.String())
	}
	if !equalPCM16(got, want) {
		t.Fatalf("%s device PCM differs: got %d samples, want %d exact samples", fixtureName, len(got), len(want))
	}
	stats := observer.PlaybackStats()
	if stats.DroppedSamples != 0 || stats.OverflowEvents != 0 || stats.QueuedSamples != 0 {
		t.Fatalf("%s device lost or retained samples: %+v", fixtureName, stats)
	}
	t.Logf("%s preserved %d provider samples as %d exact 16 kHz device samples; playback=%+v", fixtureName, len(providerSamples), len(got), stats)
}

func loadTest13ProviderPackets(t *testing.T) ([]string, []byte) {
	t.Helper()

	file, err := os.Open(test13OpenAIAudioFixture)
	if err != nil {
		t.Fatalf("open test13 OpenAI audio fixture: %v", err)
	}
	defer func() { _ = file.Close() }()

	var seeds [][]byte
	var encoded strings.Builder
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "--packet--":
			packet, decodeErr := base64.StdEncoding.DecodeString(encoded.String())
			if decodeErr != nil {
				t.Fatalf("decode test13 OpenAI packet %d: %v", len(seeds), decodeErr)
			}
			seeds = append(seeds, packet)
			encoded.Reset()
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		default:
			encoded.WriteString(line)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan test13 OpenAI audio fixture: %v", err)
	}
	if encoded.Len() != 0 {
		t.Fatal("test13 OpenAI fixture ended without a packet delimiter")
	}
	if len(seeds) != 5 {
		t.Fatalf("test13 OpenAI seed packets = %d, want four full packets and one tail", len(seeds))
	}
	for index, packet := range seeds {
		wantBytes := test13FullProviderPacketBytes
		if index == len(seeds)-1 {
			wantBytes = 7_200
		}
		if len(packet) != wantBytes {
			t.Fatalf("test13 OpenAI packet %d bytes = %d, want %d", index, len(packet), wantBytes)
		}
	}
	seedPCM := bytes.Join(seeds[:4], nil)
	if got := fmt.Sprintf("%x", sha256.Sum256(seedPCM)); got != test13SeedSHA256 {
		t.Fatalf("test13 OpenAI seed SHA-256 = %s, want %s", got, test13SeedSHA256)
	}

	deltas := make([]string, 0, test13ProviderPacketCount)
	providerPCM := make([]byte, 0, test13ProviderBytes)
	for index := 0; index < test13ProviderPacketCount-1; index++ {
		packet := seeds[index%4]
		deltas = append(deltas, base64.StdEncoding.EncodeToString(packet))
		providerPCM = append(providerPCM, packet...)
	}
	deltas = append(deltas, base64.StdEncoding.EncodeToString(seeds[4]))
	providerPCM = append(providerPCM, seeds[4]...)
	if len(providerPCM) != test13ProviderBytes {
		t.Fatalf("test13 reconstructed provider PCM bytes = %d, want %d", len(providerPCM), test13ProviderBytes)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(providerPCM)); got != test13ProviderSHA256 {
		t.Fatalf("test13 reconstructed provider SHA-256 = %s, want %s", got, test13ProviderSHA256)
	}
	return deltas, providerPCM
}

func pcm16Bytes(t *testing.T, pcm []byte) []int16 {
	t.Helper()
	if len(pcm)%2 != 0 {
		t.Fatalf("PCM byte count = %d, want even", len(pcm))
	}
	samples := make([]int16, len(pcm)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(pcm[index*2:]))
	}
	return samples
}

func writeOpenAIAudioBurstCapture(t *testing.T, path, fixtureName string, deltas []string) {
	t.Helper()
	sequence := 0
	var records []gwtesting.CapturedSessionEvent
	add := func(direction gwtesting.SessionEventDirection, payload any) {
		sequence++
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal %s replay event %d: %v", fixtureName, sequence, err)
		}
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatalf("decode %s replay event type %d: %v", fixtureName, sequence, err)
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
			"id": "sess-" + fixtureName, "type": "realtime", "model": "gpt-realtime-2.1",
			"audio": map[string]any{"output": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}}},
		},
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.created", "response": map[string]any{"id": "resp-" + fixtureName, "status": "in_progress"},
	})
	for _, delta := range deltas {
		add(gwtesting.DirectionServerToClient, map[string]any{
			"type": "response.output_audio.delta", "response_id": "resp-" + fixtureName, "item_id": "item-" + fixtureName,
			"output_index": 0, "content_index": 0, "delta": delta,
		})
	}
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.output_audio.done", "response_id": "resp-" + fixtureName, "item_id": "item-" + fixtureName,
		"output_index": 0, "content_index": 0,
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.done", "response": map[string]any{"id": "resp-" + fixtureName, "status": "completed"},
	})

	capture, err := gwtesting.SealSessionCapture(gwtesting.SessionCapture{
		Version:  gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{Name: "openai", Model: "gpt-realtime-2.1"},
		Session:  gwtesting.SessionMetadata{ID: "sess-" + fixtureName, StartedAtUTC: "2026-09-02T00:00:00Z"},
		Records:  records,
	})
	if err != nil {
		t.Fatalf("seal %s replay capture: %v", fixtureName, err)
	}
	data, err := json.Marshal(capture)
	if err != nil {
		t.Fatalf("marshal %s replay capture: %v", fixtureName, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s replay capture: %v", fixtureName, err)
	}
}
