package integration

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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	serverVADInterruptedAudioFixture = "audio/openai-server-vad-interrupted-output.base64"
	serverVADInterruptedAudioBytes   = 1440
	serverVADInterruptedAudioSHA256  = "bf9f2ae334b63a8f5e4f6868cebfbe3d64a07a34bfefcbef1c0cfac4806c78d6"
)

// TestAgentBinaryOpenAIServerVADBargeInUsesRemoteAudioDevice starts both
// shipped binaries as child processes. The agent sees only an OpenAI replay
// socket and the public --audio-device-server flag; assertions inspect the
// provider wire contract and device-server evidence rather than Go internals.
func TestAgentBinaryOpenAIServerVADBargeInUsesRemoteAudioDevice(t *testing.T) {
	endpoint, stopServer := startAudioDeviceServerBinary(t, true)
	defer stopServer()

	delta := loadServerVADInterruptedAudio(t)
	capturePath := filepath.Join(t.TempDir(), "openai-server-vad-barge-in-before-first-callback.session.json")
	writeServerVADBargeInCapture(t, capturePath, delta)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, agentBinaryPath,
		"session",
		"--replay", capturePath,
		"--prompt", "replay server VAD barge in",
		"--audio-device-server", endpoint,
		"--audio-out-device=",
	)
	command.Stdout = io.Discard
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("run agent binary with remote audio-device server: %v; stderr=%q", err, stderr.String())
	}

	snapshot, err := devicegw.ReadRemoteDeviceServerSnapshot(ctx, endpoint)
	if err != nil {
		t.Fatalf("read cross-process device evidence: %v", err)
	}
	if len(snapshot.RenderedSamples) != 0 {
		t.Fatalf("rendered samples before first callback = %d, want zero", len(snapshot.RenderedSamples))
	}
	if snapshot.Playback.QueuedSamples != 0 || snapshot.Playback.DroppedSamples != 0 {
		t.Fatalf("playback queue after server VAD = %+v", snapshot.Playback)
	}
}

func TestAudioDeviceServerBinaryDefaultClockRunsWithoutController(t *testing.T) {
	endpoint, stopServer := startAudioDeviceServerBinary(t, false)
	defer stopServer()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		snapshot, err := devicegw.ReadRemoteDeviceServerSnapshot(ctx, endpoint)
		if err != nil {
			t.Fatalf("read realtime device snapshot: %v", err)
		}
		if snapshot.Playback.CallbackCount > 0 && snapshot.Capture.CompletedFrames > 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("default device clock did not advance: playback=%+v capture=%+v", snapshot.Playback, snapshot.Capture)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func startAudioDeviceServerBinary(t *testing.T, manualClock bool) (string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	arguments := []string{"--listen", "127.0.0.1:0", "--sample-rate", "16000", "--render-quantum", "480", "--capture-quantum", "480"}
	if manualClock {
		arguments = append(arguments, "--manual-clock")
	}
	command := exec.CommandContext(ctx, audioDeviceServerBinaryPath, arguments...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatalf("open audio-device server stdout: %v", err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		cancel()
		t.Fatalf("start audio-device server: %v", err)
	}
	ready := make(chan []byte, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadBytes('\n')
		ready <- line
	}()
	var line []byte
	select {
	case line = <-ready:
	case <-time.After(5 * time.Second):
		cancel()
		_ = command.Wait()
		t.Fatalf("audio-device server did not become ready; stderr=%q", stderr.String())
	}
	var announcement struct {
		Endpoint string `json:"endpoint"`
		Input    string `json:"input_device"`
		Output   string `json:"output_device"`
	}
	if err := json.Unmarshal(line, &announcement); err != nil {
		cancel()
		_ = command.Wait()
		t.Fatalf("decode audio-device server ready line %q: %v; stderr=%q", line, err, stderr.String())
	}
	if announcement.Endpoint == "" || announcement.Input != "simulated-duplex:input" || announcement.Output != "simulated-duplex:output" {
		cancel()
		_ = command.Wait()
		t.Fatalf("audio-device server announcement = %+v", announcement)
	}
	return announcement.Endpoint, func() {
		cancel()
		if err := command.Wait(); err != nil && ctx.Err() == nil {
			t.Errorf("wait for audio-device server: %v; stderr=%q", err, stderr.String())
		}
	}
}

func loadServerVADInterruptedAudio(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(locateCLIFixture(t, serverVADInterruptedAudioFixture))
	if err != nil {
		t.Fatalf("read server-VAD audio fixture: %v", err)
	}
	encoded := strings.TrimSpace(string(data))
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode server-VAD audio fixture: %v", err)
	}
	if len(decoded) != serverVADInterruptedAudioBytes {
		t.Fatalf("server-VAD fixture bytes = %d, want %d", len(decoded), serverVADInterruptedAudioBytes)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(decoded)); got != serverVADInterruptedAudioSHA256 {
		t.Fatalf("server-VAD fixture SHA-256 = %s, want %s", got, serverVADInterruptedAudioSHA256)
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
			t.Fatalf("decode server-VAD replay event type %d: %v", sequence, err)
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
			"id": "sess-server-vad-binary", "type": "realtime", "model": "gpt-realtime-2.1-mini",
			"audio": map[string]any{"output": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}}},
		},
	})
	add(gwtesting.DirectionClientToServer, map[string]any{
		"type": "conversation.item.create", "item": map[string]any{
			"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "replay server VAD barge in"}},
		},
	})
	add(gwtesting.DirectionClientToServer, map[string]any{"type": "response.create"})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.created", "response": map[string]any{"id": "resp-server-vad-binary", "status": "in_progress"},
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.output_audio.delta", "response_id": "resp-server-vad-binary", "item_id": "item-server-vad-binary",
		"output_index": 0, "content_index": 0, "delta": delta,
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "input_audio_buffer.speech_started", "audio_start_ms": 7156, "item_id": "item-user-server-vad",
	})
	add(gwtesting.DirectionClientToServer, map[string]any{
		"type": "conversation.item.truncate", "item_id": "item-server-vad-binary", "content_index": 0, "audio_end_ms": 0,
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.output_audio.done", "response_id": "resp-server-vad-binary", "item_id": "item-server-vad-binary",
		"output_index": 0, "content_index": 0,
	})
	add(gwtesting.DirectionServerToClient, map[string]any{
		"type": "response.done", "response": map[string]any{
			"id": "resp-server-vad-binary", "status": "cancelled", "status_details": map[string]any{"type": "cancelled"},
		},
	})
	capture := gwtesting.SessionCapture{
		Version:  gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{Name: "openai", Model: "gpt-realtime-2.1-mini"},
		Session:  gwtesting.SessionMetadata{ID: "sess-server-vad-binary", StartedAtUTC: "2026-09-01T23:11:01.000000Z"},
		Records:  records,
	}
	data, err := json.Marshal(capture)
	if err != nil {
		t.Fatalf("marshal server-VAD replay capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write server-VAD replay capture: %v", err)
	}
}
