package integration

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// asyncCollisionSignals owns the close-once channels used by the replay
// connection. A separate struct avoids exposing writable channels through the
// control values shared with the provider adapter.

type asyncCollisionSignals struct {
	sessionUpdateReady   chan struct{}
	initialResponseReady chan struct{}
	laterResponseReady   chan struct{}
	continuationReady    chan struct{}
	terminalReady        chan struct{}
}

func newAsyncCollisionSignals() *asyncCollisionSignals {
	return &asyncCollisionSignals{
		sessionUpdateReady:   make(chan struct{}),
		initialResponseReady: make(chan struct{}),
		laterResponseReady:   make(chan struct{}),
		continuationReady:    make(chan struct{}),
		terminalReady:        make(chan struct{}),
	}
}

func (s *asyncCollisionSignals) control(continuationCompleted, collisionResponseComplete <-chan struct{}, expectedInputAudio []byte) asyncCollisionReplayControl {
	return asyncCollisionReplayControl{
		signals:                   s,
		continuationRequested:     s.continuationReady,
		continuationCompleted:     continuationCompleted,
		collisionResponseComplete: collisionResponseComplete,
		expectedInputAudio:        append([]byte(nil), expectedInputAudio...),
	}
}

func (s *asyncCollisionSignals) markSessionUpdate() {
	closeIfOpen(s.sessionUpdateReady)
}

func (s *asyncCollisionSignals) markInitialResponse() {
	closeIfOpen(s.initialResponseReady)
}

func (s *asyncCollisionSignals) markLaterResponse() {
	closeIfOpen(s.laterResponseReady)
}

func (s *asyncCollisionSignals) markContinuation() {
	closeIfOpen(s.continuationReady)
}

func closeIfOpen(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func validateAsyncOutboundInputAudio(payload []byte, want []byte) error {
	var envelope struct {
		Audio string `json:"audio"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("decode outbound input_audio_buffer.append: %w", err)
	}
	got, err := base64.StdEncoding.DecodeString(envelope.Audio)
	if err != nil {
		return fmt.Errorf("decode outbound input audio base64: %w", err)
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("later-turn input audio differs from the gated fixture: got %d bytes want %d", len(got), len(want))
	}
	return nil
}

func audioDeltaPayload(samples []int16) string {
	payload, _ := json.Marshal(map[string]string{
		"type":  "response.output_audio.delta",
		"delta": base64.StdEncoding.EncodeToString(pcm16LEBytes(samples)),
	})
	return string(payload)
}

func asyncCollisionAudio(t *testing.T) (collision, continuation [][]int16) {
	t.Helper()
	wavBytes, err := os.ReadFile(toolSingleCallWAVPath(t))
	if err != nil {
		t.Fatalf("read committed corpus WAV: %v", err)
	}
	_, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse committed corpus WAV: %v", err)
	}
	window := loudestWindowSamplesIntegration(t, samples, asyncCollisionDeltaSamples)
	all := make([][]int16, asyncCollisionDeltaCount*2)
	for i := range all {
		all[i] = make([]int16, len(window))
		shift := int16(i + 1)
		if i >= asyncCollisionDeltaCount {
			shift = int16(31 + i)
		}
		for j, sample := range window {
			all[i][j] = sample + shift
		}
	}
	return all[:asyncCollisionDeltaCount], all[asyncCollisionDeltaCount:]
}

func asyncCollisionInputAudio() []byte {
	samples := make([]int16, asyncCollisionInputSamples)
	for i := range samples {
		samples[i] = int16(700 + (i % 29))
	}
	return pcm16LEBytes(samples)
}

func writeAsyncCollisionInputWAV(t *testing.T, path string, inputAudio []byte) {
	t.Helper()
	samples := make([]int16, len(inputAudio)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(inputAudio[index*2:]))
	}
	var wav bytes.Buffer
	if err := wavio.Write(&wav, wavio.Rate24kHz, samples); err != nil {
		t.Fatalf("encode async collision input fixture: %v", err)
	}
	if err := os.WriteFile(path, wav.Bytes(), 0o600); err != nil {
		t.Fatalf("write async collision input fixture: %v", err)
	}
}

func inputAudioPayload(audioBytes []byte) string {
	payload, _ := json.Marshal(map[string]string{
		"type":  "input_audio_buffer.append",
		"audio": base64.StdEncoding.EncodeToString(audioBytes),
	})
	return string(payload)
}

func functionCallOutputPayload() string {
	payload, _ := json.Marshal(map[string]any{
		"type": "conversation.item.create",
		"item": map[string]string{
			"type":    "function_call_output",
			"call_id": asyncCollisionCallID,
			"output":  asyncCollisionResult,
		},
	})
	return string(payload)
}

func buildAsyncCollisionFixture(t *testing.T, collision, continuation [][]int16, inputAudio []byte) (string, gwtesting.SessionCapture) {
	t.Helper()
	base, err := gwtesting.LoadSessionCapture(filepath.Join("testdata", "openai_realtime_smoke.session.json"))
	if err != nil {
		t.Fatalf("load OpenAI replay baseline: %v", err)
	}
	// Keep the provider and artifact media at the negotiated 24 kHz boundary so
	// the byte-exact audio oracle measures ordering and retention, not sink DSP.
	base.Records[0].Payload = json.RawMessage(`{"type":"session.update","session":{"model":"gpt-realtime","type":"realtime","audio":{"input":{"format":{"rate":24000}},"output":{"format":{"rate":24000}}}}}`)
	records := []gwtesting.CapturedSessionEvent{base.Records[0], base.Records[1]}
	add := func(direction gwtesting.SessionEventDirection, eventType, payload string) {
		records = append(records, gwtesting.CapturedSessionEvent{
			Sequence:    len(records) + 1,
			Direction:   direction,
			TimestampMs: int64(len(records)),
			Type:        eventType,
			PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
			Payload:     json.RawMessage(payload),
		})
	}
	userPayload, _ := json.Marshal(map[string]any{
		"type": "conversation.item.create",
		"item": map[string]any{
			"type":    "message",
			"role":    "user",
			"content": []map[string]string{{"type": "input_text", "text": asyncCollisionPrompt}},
		},
	})
	// The positional prompt is the first user turn and causes the outstanding
	// tool call. The CLI delays --audio-in-turn input until this response ends,
	// making the second response a real later turn rather than an unsolicited
	// provider frame.
	add(gwtesting.DirectionClientToServer, "conversation.item.create", string(userPayload))
	add(gwtesting.DirectionClientToServer, "response.create", `{"type":"response.create"}`)
	add(gwtesting.DirectionServerToClient, "response.created", `{"type":"response.created","response":{"id":"`+asyncCollisionResponseOne+`"}}`)
	add(gwtesting.DirectionServerToClient, "response.output_item.added", `{"type":"response.output_item.added","item":{"type":"function_call","call_id":"`+asyncCollisionCallID+`","name":"`+asyncCollisionToolName+`"}}`)
	add(gwtesting.DirectionServerToClient, "response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","call_id":"`+asyncCollisionCallID+`","name":"`+asyncCollisionToolName+`","arguments":`+strconvQuote(asyncCollisionToolArgs)+`}`)
	add(gwtesting.DirectionServerToClient, "response.done", `{"type":"response.done","response":{"id":"`+asyncCollisionResponseOne+`","status":"completed"}}`)

	// The result is the only outbound work eligible after the tool-call response.
	// The grounded continuation must complete before the scheduled audio turn is
	// allowed onto the provider wire.
	add(gwtesting.DirectionClientToServer, "conversation.item.create", functionCallOutputPayload())
	add(gwtesting.DirectionClientToServer, "response.create", `{"type":"response.create"}`)

	add(gwtesting.DirectionServerToClient, "response.created", `{"type":"response.created","response":{"id":"`+asyncCollisionResponseThree+`"}}`)
	for _, delta := range continuation {
		add(gwtesting.DirectionServerToClient, "response.output_audio.delta", audioDeltaPayload(delta))
	}
	add(gwtesting.DirectionServerToClient, "response.output_audio.done", `{"type":"response.output_audio.done"}`)
	add(gwtesting.DirectionServerToClient, "response.done", `{"type":"response.done","response":{"id":"`+asyncCollisionResponseThree+`","status":"completed"}}`)

	// The scheduled audio is a distinct later user turn. Its provider-facing
	// append is rejected by the gated connection if it arrives before the
	// result-driven continuation's terminal MESSAGE.END.
	add(gwtesting.DirectionClientToServer, "input_audio_buffer.append", inputAudioPayload(inputAudio))
	add(gwtesting.DirectionClientToServer, "input_audio_buffer.commit", `{"type":"input_audio_buffer.commit"}`)
	add(gwtesting.DirectionClientToServer, "response.create", `{"type":"response.create"}`)
	add(gwtesting.DirectionServerToClient, "response.created", `{"type":"response.created","response":{"id":"`+asyncCollisionResponseTwo+`"}}`)
	for _, delta := range collision {
		add(gwtesting.DirectionServerToClient, "response.output_audio.delta", audioDeltaPayload(delta))
	}
	add(gwtesting.DirectionServerToClient, "response.output_audio.done", `{"type":"response.output_audio.done"}`)
	add(gwtesting.DirectionServerToClient, "response.done", `{"type":"response.done","response":{"id":"`+asyncCollisionResponseTwo+`","status":"completed"}}`)
	add(gwtesting.DirectionServerToClient, "session.closed", `{"type":"session.closed","session_id":"`+asyncCollisionSessionID+`","reason":"`+asyncCollisionCloseReason+`"}`)

	base.Session.ID = asyncCollisionSessionID
	base.Session.FixtureProvenance = gwtesting.SessionFixtureProvenanceSynthetic
	base.Records = records
	data, err := json.MarshalIndent(base, "", "  ")
	if err != nil {
		t.Fatalf("marshal async collision fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "async-tool-result-interrupts-speech.session.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write async collision fixture: %v", err)
	}
	if _, err := gwtesting.NewReplayWebSocketDialer(path); err != nil {
		t.Fatalf("validate async collision fixture with shared replay validator: %v", err)
	}
	return path, base
}
