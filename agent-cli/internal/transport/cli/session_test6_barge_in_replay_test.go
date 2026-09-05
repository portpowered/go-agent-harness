package cli

import sessionclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"

import sessionservicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

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
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const test6OpenAIBargeInFixture = "testdata/test6-openai-barge-in.base64"

// TestSessionCommandReplaysTest6CustomerBargeInThenNewAssistantAudio preserves
// the provider-edge ordering observed in test6.json. Server VAD speech is the
// customer barge-in: it prevents the first assistant response from remaining
// queued (discarding any prefix already admitted to the device) and emits a
// truncate at the device-heard cursor. The following output_audio.delta is a
// new ChatGPT response, not more customer input, and must reach the device
// byte-for-byte after the real 24-to-16 kHz conversion.
func TestSessionCommandReplaysTest6CustomerBargeInThenNewAssistantAudio(t *testing.T) {
	providerAudio := loadTest6OpenAIBargeInAudio(t)
	wantInterrupted := resampleTest6PCM(t, providerAudio[0])
	wantNewAssistant := resampleTest6PCM(t, providerAudio[1])

	capturePath := filepath.Join(t.TempDir(), "test6-customer-barge-in.session.json")
	writeTest6BargeInCapture(t, capturePath, providerAudio)

	device := newTest6RecordingPlaybackDevice(t)
	registry := &test6PlaybackRegistry{device: device}
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	command := NewSessionCommand(flags.NewAskFlags(), globalFlags, newTestSessionService(sessionservicewire.SessionDependencies{Clock: sessionclock.Real{}, DeviceRegistry: registry}), nil).Generate()
	command.SetOut(io.Discard)
	var stderr bytes.Buffer
	command.SetErr(&stderr)
	command.SetArgs([]string{
		"--replay", capturePath,
		"--prompt", "test6 customer barge in",
		"--audio-out-device", test6PlaybackDeviceID,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := command.ExecuteContext(ctx); err != nil {
		t.Fatalf("execute test6 barge-in replay: %v; stderr=%q", err, stderr.String())
	}

	queued, accepted, discarded := device.snapshot()
	if !equalPCM16(queued, wantNewAssistant) {
		t.Fatalf("new assistant device PCM differs after customer barge-in: got %d samples, want %d exact samples", len(queued), len(wantNewAssistant))
	}
	if discarded > len(wantInterrupted) {
		t.Fatalf("customer barge-in discarded %d queued assistant samples, want within 0..%d", discarded, len(wantInterrupted))
	}
	// The streaming resampler may retain one response-final remainder when VAD
	// interrupts before output_audio.done. Only its already-emitted prefix can
	// have reached the device and therefore be discarded there.
	wantAccepted := append(append([]int16(nil), wantInterrupted[:discarded]...), wantNewAssistant...)
	if !equalPCM16(accepted, wantAccepted) {
		t.Fatalf("device accepted stream differs from captured ChatGPT audio: got %d samples, want %d", len(accepted), len(wantAccepted))
	}
	t.Logf("test6 replay: interrupted_assistant_discarded=%d new_assistant_exact=%d total_device_accepted=%d", discarded, len(wantNewAssistant), len(accepted))
}

func loadTest6OpenAIBargeInAudio(t *testing.T) [][]byte {
	t.Helper()
	file, err := os.Open(test6OpenAIBargeInFixture)
	if err != nil {
		t.Fatalf("open test6 OpenAI audio fixture: %v", err)
	}
	defer func() { _ = file.Close() }()

	wantBytes := []int{7200, 2400}
	wantSHA := []string{
		"1317f3c423ac6e2d19ae38ec0da06b80c1246bcef4087d27b0771fb4d1ae30e3",
		"ce2b9a756d4c71c1e3d07e5302edaae9bf786a58eb3d53dc02ea559565c35df0",
	}
	var audioPCM [][]byte
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 32*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pcm, decodeErr := base64.StdEncoding.DecodeString(line)
		if decodeErr != nil {
			t.Fatalf("decode test6 OpenAI audio %d: %v", len(audioPCM), decodeErr)
		}
		audioPCM = append(audioPCM, pcm)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan test6 OpenAI audio fixture: %v", err)
	}
	if len(audioPCM) != 2 {
		t.Fatalf("test6 OpenAI audio segments = %d, want 2", len(audioPCM))
	}
	for index := range audioPCM {
		if len(audioPCM[index]) != wantBytes[index] {
			t.Fatalf("test6 OpenAI audio %d bytes = %d, want %d", index, len(audioPCM[index]), wantBytes[index])
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(audioPCM[index])); got != wantSHA[index] {
			t.Fatalf("test6 OpenAI audio %d SHA-256 = %s, want %s", index, got, wantSHA[index])
		}
	}
	return audioPCM
}

func resampleTest6PCM(t *testing.T, pcm []byte) []int16 {
	t.Helper()
	if len(pcm)%2 != 0 {
		t.Fatalf("test6 PCM byte count = %d, want even PCM16", len(pcm))
	}
	samples := make([]int16, len(pcm)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(pcm[index*2:]))
	}
	return mustResampleStream(t, [][]int16{samples}, wavio.Rate24kHz, audio.SampleRate)
}

func writeTest6BargeInCapture(t *testing.T, path string, providerAudio [][]byte) {
	t.Helper()
	sequence := 0
	var records []gwtesting.CapturedSessionEvent
	add := func(direction gwtesting.SessionEventDirection, payload any) {
		sequence++
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal test6 replay event %d: %v", sequence, err)
		}
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatalf("decode test6 replay event type %d: %v", sequence, err)
		}
		records = append(records, gwtesting.CapturedSessionEvent{
			Sequence: sequence, Direction: direction, TimestampMs: int64(sequence), Type: envelope.Type,
			PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage, Payload: data,
		})
	}

	add(gwtesting.DirectionClientToServer, map[string]any{
		"type": "session.update", "session": map[string]any{
			"model": "gpt-realtime-2.1-mini",
			"audio": map[string]any{
				"input":  map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}},
				"output": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}},
			},
		},
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "session.created", "session": map[string]any{
			"id": "sess-test6-barge-in", "type": "realtime", "model": "gpt-realtime-2.1-mini",
			"audio": map[string]any{"output": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}}},
		},
	})
	add(gwtesting.DirectionClientToServer, map[string]any{
		"type": "conversation.item.create", "item": map[string]any{
			"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "test6 customer barge in"}},
		},
	})
	add(gwtesting.DirectionClientToServer, map[string]any{"type": "response.create"})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.created", "response": map[string]any{"id": "resp-test6-interrupted", "status": "in_progress"},
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.output_audio.delta", "response_id": "resp-test6-interrupted", "item_id": "item-test6-interrupted",
		"output_index": 0, "content_index": 0, "delta": base64.StdEncoding.EncodeToString(providerAudio[0]),
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "input_audio_buffer.speech_started", "audio_start_ms": 98092, "item_id": "item-test6-customer",
	})
	add(gwtesting.DirectionClientToServer, map[string]any{
		"type": "conversation.item.truncate", "item_id": "item-test6-interrupted", "content_index": 0, "audio_end_ms": 0,
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "conversation.item.truncated", "item_id": "item-test6-interrupted", "content_index": 0, "audio_end_ms": 0,
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.output_audio.done", "response_id": "resp-test6-interrupted", "item_id": "item-test6-interrupted",
		"output_index": 0, "content_index": 0,
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.done", "response": map[string]any{
			"id": "resp-test6-interrupted", "status": "cancelled", "status_details": map[string]any{"type": "cancelled"},
		},
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.created", "response": map[string]any{"id": "resp-test6-new-assistant", "status": "in_progress"},
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.output_audio.delta", "response_id": "resp-test6-new-assistant", "item_id": "item-test6-new-assistant",
		"output_index": 0, "content_index": 0, "delta": base64.StdEncoding.EncodeToString(providerAudio[1]),
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.output_audio.done", "response_id": "resp-test6-new-assistant", "item_id": "item-test6-new-assistant",
		"output_index": 0, "content_index": 0,
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.done", "response": map[string]any{"id": "resp-test6-new-assistant", "status": "completed"},
	})

	capture := gwtesting.SessionCapture{
		Version:  gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{Name: "openai", Model: "gpt-realtime-2.1-mini"},
		Session:  gwtesting.SessionMetadata{ID: "sess-test6-barge-in", StartedAtUTC: "2026-09-02T18:49:17.635745Z"},
		Records:  records,
	}
	data, err := json.Marshal(capture)
	if err != nil {
		t.Fatalf("marshal test6 replay capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write test6 replay capture: %v", err)
	}
}

const test6PlaybackDeviceID = "test6:output"

type test6PlaybackRegistry struct{ device *test6RecordingPlaybackDevice }

func (r *test6PlaybackRegistry) List() ([]devicegw.Device, error) {
	return []devicegw.Device{r.device.metadata}, nil
}
func (r *test6PlaybackRegistry) Default(direction devicegw.Direction) (devicegw.Device, error) {
	if direction != devicegw.DirectionOutput {
		return devicegw.Device{}, devicegw.NewNoDefaultDeviceError(direction)
	}
	return r.device.metadata, nil
}
func (r *test6PlaybackRegistry) Open(id devicegw.DeviceID) (devicegw.OpenedDevice, error) {
	if id != test6PlaybackDeviceID {
		return nil, devicegw.NewDeviceNotFoundError(id)
	}
	return r.device, nil
}

type test6RecordingPlaybackDevice struct {
	mu        sync.Mutex
	metadata  devicegw.Device
	queued    []int16
	accepted  []int16
	discarded int
}

func newTest6RecordingPlaybackDevice(t *testing.T) *test6RecordingPlaybackDevice {
	t.Helper()
	metadata, err := devicegw.NewDevice("test6", "output", "test6 recording playback", devicegw.DirectionOutput)
	if err != nil {
		t.Fatal(err)
	}
	return &test6RecordingPlaybackDevice{metadata: metadata}
}

func (d *test6RecordingPlaybackDevice) WriteFrame(ctx context.Context, samples []int16) error {
	return d.WriteSamples(ctx, samples)
}
func (d *test6RecordingPlaybackDevice) WriteSamples(ctx context.Context, samples []int16) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	d.queued = append(d.queued, samples...)
	d.accepted = append(d.accepted, samples...)
	d.mu.Unlock()
	return nil
}
func (d *test6RecordingPlaybackDevice) DiscardPlayback() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	discarded := len(d.queued)
	d.discarded += discarded
	d.queued = nil
	return discarded
}
func (*test6RecordingPlaybackDevice) Close() error { return nil }
func (d *test6RecordingPlaybackDevice) snapshot() (queued, accepted []int16, discarded int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]int16(nil), d.queued...), append([]int16(nil), d.accepted...), d.discarded
}

var _ devicegw.DeviceRegistry = (*test6PlaybackRegistry)(nil)
var _ audio.PlaybackDiscarder = (*test6RecordingPlaybackDevice)(nil)
