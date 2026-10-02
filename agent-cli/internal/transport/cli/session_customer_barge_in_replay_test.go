package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	bargeInReplayOpenAIBargeInFixture = "testdata/openai-customer-barge-in.base64"
	// The device-bound session installs its playback controller after the
	// provider handshake returns. Model that bounded startup gap before the
	// first client turn so server audio cannot outrun the device boundary.
	bargeInReplayPlaybackStartupGapMS int64 = 100
)

// TestSessionCommandReplaysCustomerBargeInThenNewAssistantAudio preserves
// the provider-edge ordering observed in a captured OpenAI session. Server VAD speech is the
// customer barge-in: it prevents the first assistant response from remaining
// queued (discarding any prefix already admitted to the device) and emits a
// truncate at the device-heard cursor. The following output_audio.delta is a
// new ChatGPT response, not more customer input, and must reach the device
// byte-for-byte after the real 24-to-16 kHz conversion.
func TestSessionCommandReplaysCustomerBargeInThenNewAssistantAudio(t *testing.T) {
	providerAudio := loadBargeInReplayOpenAIBargeInAudio(t)
	wantInterrupted := resampleBargeInReplayPCM(t, providerAudio[0])
	wantNewAssistant := resampleBargeInReplayPCM(t, providerAudio[1])

	capturePath := filepath.Join(t.TempDir(), "barge-in-customer-barge-in.session.json")
	writeBargeInReplayBargeInCapture(t, capturePath, providerAudio)

	device := newBargeInReplayRecordingPlaybackDevice(t)
	registry := &bargeInReplayPlaybackRegistry{device: device}
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	command := newTestSessionCommand(flags.NewAskFlags(), globalFlags, testSessionDeps{Registry: registry}).Generate()
	command.SetOut(io.Discard)
	var stderr bytes.Buffer
	command.SetErr(&stderr)
	command.SetArgs([]string{
		"--replay", capturePath,
		"--replay-timing", "recorded",
		"--prompt", "barge-in replay customer barge in",
		"--audio-out-device", bargeInReplayPlaybackDeviceID,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := command.ExecuteContext(ctx); err != nil {
		t.Fatalf("execute barge-in replay barge-in replay: %v; stderr=%q", err, stderr.String())
	}

	queued, accepted, discarded := device.snapshot()
	if !slices.Equal(queued, wantNewAssistant) {
		t.Fatalf("new assistant device PCM differs after customer barge-in: got %d samples, want %d exact samples", len(queued), len(wantNewAssistant))
	}
	if discarded > len(wantInterrupted) {
		t.Fatalf("customer barge-in discarded %d queued assistant samples, want within 0..%d", discarded, len(wantInterrupted))
	}
	// The streaming resampler may retain one response-final remainder when VAD
	// interrupts before output_audio.done. Only its already-emitted prefix can
	// have reached the device and therefore be discarded there.
	wantAccepted := append(append([]int16(nil), wantInterrupted[:discarded]...), wantNewAssistant...)
	if !slices.Equal(accepted, wantAccepted) {
		t.Fatalf("device accepted stream differs from captured ChatGPT audio: got %d samples, want %d", len(accepted), len(wantAccepted))
	}
	t.Logf("barge-in replay replay: interrupted_assistant_discarded=%d new_assistant_exact=%d total_device_accepted=%d", discarded, len(wantNewAssistant), len(accepted))
}

func loadBargeInReplayOpenAIBargeInAudio(t *testing.T) [][]byte {
	t.Helper()
	file, err := os.Open(bargeInReplayOpenAIBargeInFixture)
	if err != nil {
		t.Fatalf("open barge-in replay OpenAI audio fixture: %v", err)
	}
	defer closeForTest(t, file.Close)

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
			t.Fatalf("decode barge-in replay OpenAI audio %d: %v", len(audioPCM), decodeErr)
		}
		audioPCM = append(audioPCM, pcm)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan barge-in replay OpenAI audio fixture: %v", err)
	}
	if len(audioPCM) != 2 {
		t.Fatalf("barge-in replay OpenAI audio segments = %d, want 2", len(audioPCM))
	}
	for index := range audioPCM {
		if len(audioPCM[index]) != wantBytes[index] {
			t.Fatalf("barge-in replay OpenAI audio %d bytes = %d, want %d", index, len(audioPCM[index]), wantBytes[index])
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(audioPCM[index])); got != wantSHA[index] {
			t.Fatalf("barge-in replay OpenAI audio %d SHA-256 = %s, want %s", index, got, wantSHA[index])
		}
	}
	return audioPCM
}

func resampleBargeInReplayPCM(t *testing.T, pcm []byte) []int16 {
	t.Helper()
	if len(pcm)%2 != 0 {
		t.Fatalf("barge-in replay PCM byte count = %d, want even PCM16", len(pcm))
	}
	samples := codec.PCM16Samples(pcm)
	return mustResampleProviderToDevice(t, [][]int16{samples})
}

func writeBargeInReplayBargeInCapture(t *testing.T, path string, providerAudio [][]byte) {
	t.Helper()
	sequence := 0
	var records []gwtesting.CapturedSessionEvent
	add := func(direction gwtesting.SessionEventDirection, payload any) {
		sequence++
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal barge-in replay replay event %d: %v", sequence, err)
		}
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatalf("decode barge-in replay replay event type %d: %v", sequence, err)
		}
		timestampMs := int64(sequence)
		if sequence >= 3 {
			timestampMs += bargeInReplayPlaybackStartupGapMS
		}
		records = append(records, gwtesting.CapturedSessionEvent{
			Sequence: sequence, Direction: direction, TimestampMs: timestampMs, Type: envelope.Type,
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
			"id": "sess-barge-in-barge-in", "type": "realtime", "model": "gpt-realtime-2.1-mini",
			"audio": map[string]any{"output": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}}},
		},
	})
	add(gwtesting.DirectionClientToServer, map[string]any{
		"type": "conversation.item.create", "item": map[string]any{
			"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "barge-in replay customer barge in"}},
		},
	})
	add(gwtesting.DirectionClientToServer, map[string]any{"type": "response.create"})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.created", "response": map[string]any{"id": "resp-barge-in-interrupted", "status": "in_progress"},
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.output_audio.delta", "response_id": "resp-barge-in-interrupted", "item_id": "item-barge-in-interrupted",
		"output_index": 0, "content_index": 0, "delta": base64.StdEncoding.EncodeToString(providerAudio[0]),
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "input_audio_buffer.speech_started", "audio_start_ms": 98092, "item_id": "item-barge-in-customer",
	})
	add(gwtesting.DirectionClientToServer, map[string]any{
		"type": "conversation.item.truncate", "item_id": "item-barge-in-interrupted", "content_index": 0, "audio_end_ms": 0,
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "conversation.item.truncated", "item_id": "item-barge-in-interrupted", "content_index": 0, "audio_end_ms": 0,
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.output_audio.done", "response_id": "resp-barge-in-interrupted", "item_id": "item-barge-in-interrupted",
		"output_index": 0, "content_index": 0,
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.done", "response": map[string]any{
			"id": "resp-barge-in-interrupted", "status": "cancelled", "status_details": map[string]any{"type": "cancelled"},
		},
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.created", "response": map[string]any{"id": "resp-barge-in-new-assistant", "status": "in_progress"},
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.output_audio.delta", "response_id": "resp-barge-in-new-assistant", "item_id": "item-barge-in-new-assistant",
		"output_index": 0, "content_index": 0, "delta": base64.StdEncoding.EncodeToString(providerAudio[1]),
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.output_audio.done", "response_id": "resp-barge-in-new-assistant", "item_id": "item-barge-in-new-assistant",
		"output_index": 0, "content_index": 0,
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.done", "response": map[string]any{"id": "resp-barge-in-new-assistant", "status": "completed"},
	})

	capture := gwtesting.SessionCapture{
		Version:  gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{Name: "openai", Model: "gpt-realtime-2.1-mini"},
		Session:  gwtesting.SessionMetadata{ID: "sess-barge-in-barge-in", StartedAtUTC: "2026-09-02T18:49:17.635745Z"},
		Records:  records,
	}
	data, err := json.Marshal(capture)
	if err != nil {
		t.Fatalf("marshal barge-in replay replay capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write barge-in replay replay capture: %v", err)
	}
}

const bargeInReplayPlaybackDeviceID = "barge-in-replay:output"

type bargeInReplayPlaybackRegistry struct {
	device *bargeInReplayRecordingPlaybackDevice
}

func (r *bargeInReplayPlaybackRegistry) List() ([]devicegw.Device, error) {
	return []devicegw.Device{r.device.metadata}, nil
}
func (r *bargeInReplayPlaybackRegistry) Default(direction devicegw.Direction) (devicegw.Device, error) {
	if direction != devicegw.DirectionOutput {
		return devicegw.Device{}, devicegw.NewNoDefaultDeviceError(direction)
	}
	return r.device.metadata, nil
}
func (r *bargeInReplayPlaybackRegistry) Open(id devicegw.DeviceID) (devicegw.OpenedDevice, error) {
	if id != bargeInReplayPlaybackDeviceID {
		return nil, devicegw.NewDeviceNotFoundError(id)
	}
	return r.device, nil
}

type bargeInReplayRecordingPlaybackDevice struct {
	mu        sync.Mutex
	metadata  devicegw.Device
	queued    []int16
	accepted  []int16
	discarded int
}

func newBargeInReplayRecordingPlaybackDevice(t *testing.T) *bargeInReplayRecordingPlaybackDevice {
	t.Helper()
	metadata, err := devicegw.NewDevice("barge-in-replay", "output", "barge-in replay recording playback", devicegw.DirectionOutput)
	if err != nil {
		t.Fatal(err)
	}
	return &bargeInReplayRecordingPlaybackDevice{metadata: metadata}
}

func (d *bargeInReplayRecordingPlaybackDevice) WriteFrame(ctx context.Context, samples []int16) error {
	return d.WriteSamples(ctx, samples)
}
func (d *bargeInReplayRecordingPlaybackDevice) WriteSamples(ctx context.Context, samples []int16) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	d.queued = append(d.queued, samples...)
	d.accepted = append(d.accepted, samples...)
	d.mu.Unlock()
	return nil
}
func (d *bargeInReplayRecordingPlaybackDevice) DiscardPlayback() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	discarded := len(d.queued)
	d.discarded += discarded
	d.queued = nil
	return discarded
}
func (*bargeInReplayRecordingPlaybackDevice) Close() error { return nil }
func (d *bargeInReplayRecordingPlaybackDevice) snapshot() (queued, accepted []int16, discarded int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]int16(nil), d.queued...), append([]int16(nil), d.accepted...), d.discarded
}

var _ devicegw.DeviceRegistry = (*bargeInReplayPlaybackRegistry)(nil)
var _ devicegw.PlaybackDiscarder = (*bargeInReplayRecordingPlaybackDevice)(nil)
