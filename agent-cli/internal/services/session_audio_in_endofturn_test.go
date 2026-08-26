package services_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

// sessionAudioInTestWAVPCM returns the raw PCM16 payload of a committed WAV
// fixture so synthetic wire fixtures can mirror its frame stream exactly.
func sessionAudioInTestWAVPCM(t *testing.T, wavPath string) []byte {
	t.Helper()
	wavBytes, err := os.ReadFile(wavPath)
	if err != nil {
		t.Fatalf("read WAV fixture: %v", err)
	}
	_, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse WAV fixture: %v", err)
	}
	pcm := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(sample))
	}
	return pcm
}

// TestSessionCommandAudioInputRecordsPostCommitResponse drives the real
// session command against a synthetic wire fixture: the finite WAV source is
// appended frame by frame, then input_audio_buffer.commit and response.create
// are emitted, after which the provider delivers a spoken response. The test
// asserts the response audio received after commit is recorded non-empty.
func TestSessionCommandAudioInputRecordsPostCommitResponse(t *testing.T) {
	wavPath := committedSessionAudioInputWAVPath(t)
	baseCapture, err := gwtesting.LoadSessionCapture(filepath.Join("..", "..", "test", "integration", "testdata", "openai_realtime_smoke.session.json"))
	if err != nil {
		t.Fatalf("load replay base fixture: %v", err)
	}
	records := []gwtesting.CapturedSessionEvent{baseCapture.Records[0], baseCapture.Records[1]}

	appendChunk := func(audio []byte) {
		t.Helper()
		payload, marshalErr := json.Marshal(map[string]string{
			"type":  "input_audio_buffer.append",
			"audio": base64.StdEncoding.EncodeToString(audio),
		})
		if marshalErr != nil {
			t.Fatalf("marshal append event: %v", marshalErr)
		}
		records = append(records, gwtesting.CapturedSessionEvent{
			Sequence:    len(records) + 1,
			Direction:   gwtesting.DirectionClientToServer,
			TimestampMs: int64(len(records)),
			Type:        "input_audio_buffer.append",
			PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
			Payload:     payload,
		})
	}

	// The committed WAV fixture streams in whole frames; reuse its raw PCM16
	// payload by re-encoding deterministic frames from the file itself.
	pcm := sessionAudioInTestWAVPCM(t, wavPath)
	const chunkSize = 960
	for start := 0; start < len(pcm); start += chunkSize {
		end := start + chunkSize
		if end > len(pcm) {
			end = len(pcm)
		}
		appendChunk(pcm[start:end])
	}

	records = append(records,
		gwtesting.CapturedSessionEvent{
			Sequence:    len(records) + 1,
			Direction:   gwtesting.DirectionClientToServer,
			TimestampMs: int64(len(records)),
			Type:        "input_audio_buffer.commit",
			PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
			Payload:     json.RawMessage(`{"type":"input_audio_buffer.commit"}`),
		},
		gwtesting.CapturedSessionEvent{
			Sequence:    len(records) + 1,
			Direction:   gwtesting.DirectionClientToServer,
			TimestampMs: int64(len(records)),
			Type:        "response.create",
			PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
			Payload:     json.RawMessage(`{"type":"response.create"}`),
		},
	)

	serverEvent := func(eventType string, payload string) gwtesting.CapturedSessionEvent {
		return gwtesting.CapturedSessionEvent{
			Sequence:    len(records) + 1,
			Direction:   gwtesting.DirectionServerToClient,
			TimestampMs: int64(len(records)),
			Type:        eventType,
			PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
			Payload:     json.RawMessage(payload),
		}
	}

	responseAudio := []byte("spoken-response-pcm-bytes-0001")
	audioDeltaPayload, err := json.Marshal(map[string]string{
		"type":  "response.output_audio.delta",
		"delta": base64.StdEncoding.EncodeToString(responseAudio),
	})
	if err != nil {
		t.Fatalf("marshal audio delta: %v", err)
	}
	records = append(records,
		serverEvent("response.created", `{"type":"response.created","response":{"id":"resp_1"}}`),
		serverEvent("response.output_audio_transcript.done", `{"type":"response.output_audio_transcript.done","transcript":"Hello there."}`),
		gwtesting.CapturedSessionEvent{
			Sequence:    len(records) + 1,
			Direction:   gwtesting.DirectionServerToClient,
			TimestampMs: int64(len(records)),
			Type:        "response.output_audio.delta",
			PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
			Payload:     audioDeltaPayload,
		},
		serverEvent("response.output_audio.done", `{"type":"response.output_audio.done"}`),
		serverEvent("response.done", `{"type":"response.done","response":{"id":"resp_1","status":"completed"}}`),
		serverEvent("session.closed", `{"type":"session.closed","session_id":"sess_audio_record_fixture","reason":"fixture_complete"}`),
	)

	baseCapture.Session.ID = "sess_audio_record_fixture"
	baseCapture.Session.FixtureProvenance = gwtesting.SessionFixtureProvenanceSynthetic
	baseCapture.Records = records
	wirePath := filepath.Join(t.TempDir(), "audio-record.session.json")
	wireData, err := json.MarshalIndent(baseCapture, "", "  ")
	if err != nil {
		t.Fatalf("marshal wire fixture: %v", err)
	}
	if err := os.WriteFile(wirePath, wireData, 0o600); err != nil {
		t.Fatalf("write wire fixture: %v", err)
	}

	outputPath := filepath.Join(t.TempDir(), "response.wav")
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{"--replay", wirePath, "--audio-in", wavPath, "--audio-out", outputPath})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("session command with --audio-in and --audio-out: %v", err)
	}

	info, statErr := os.Stat(outputPath)
	if statErr != nil {
		t.Fatalf("stat recorded response audio: %v", statErr)
	}
	// A WAV sink writes a 44-byte header plus the received response audio.
	if info.Size() <= 44 {
		t.Fatalf("recorded response audio is %d bytes; want non-empty audio after commit", info.Size())
	}
	recorded, readErr := os.ReadFile(outputPath)
	if readErr != nil {
		t.Fatalf("read recorded response audio: %v", readErr)
	}
	if len(recorded) <= 44 {
		t.Fatalf("recorded response audio holds no samples")
	}
	payloadOnly := recorded[44:]
	if len(payloadOnly) != len(responseAudio) {
		t.Fatalf("recorded response audio length = %d, want %d", len(payloadOnly), len(responseAudio))
	}
	for i := range responseAudio {
		if payloadOnly[i] != responseAudio[i] {
			t.Fatalf("recorded response audio byte %d changed against the scripted response", i)
		}
	}
}
