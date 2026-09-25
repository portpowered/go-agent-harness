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
	payload := mustMarshalFixture(map[string]string{
		"type":  rtEventOutputAudioDelta,
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
	payload := mustMarshalFixture(map[string]string{
		"type":  rtEventInputAudioAppend,
		"audio": base64.StdEncoding.EncodeToString(audioBytes),
	})
	return string(payload)
}

func functionCallOutputPayload() string {
	payload := mustMarshalFixture(map[string]any{
		"type": rtEventConversationItemCreate,
		"item": map[string]string{
			"type":    rtItemFunctionCallOutput,
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
	userPayload := mustMarshalFixture(map[string]any{
		"type": rtEventConversationItemCreate,
		"item": map[string]any{
			"type":    rtItemMessage,
			"role":    rtRoleUser,
			"content": []map[string]string{{"type": "input_text", "text": asyncCollisionPrompt}},
		},
	})
	// The positional prompt is the first user turn and causes the outstanding
	// tool call. The CLI delays --audio-in-turn input until this response ends,
	// making the second response a real later turn rather than an unsolicited
	// provider frame.
	add(gwtesting.DirectionClientToServer, rtEventConversationItemCreate, string(userPayload))
	add(gwtesting.DirectionClientToServer, rtEventResponseCreate, `{"type":"response.create"}`)
	add(gwtesting.DirectionServerToClient, rtEventResponseCreated, `{"type":"response.created","response":{"id":"`+asyncCollisionResponseOne+`"}}`)
	add(gwtesting.DirectionServerToClient, rtEventOutputItemAdded, `{"type":"response.output_item.added","item":{"type":"function_call","call_id":"`+asyncCollisionCallID+`","name":"`+asyncCollisionToolName+`"}}`)
	add(gwtesting.DirectionServerToClient, rtEventFunctionCallArgumentsDone, `{"type":"response.function_call_arguments.done","call_id":"`+asyncCollisionCallID+`","name":"`+asyncCollisionToolName+`","arguments":`+strconvQuote(asyncCollisionToolArgs)+`}`)
	add(gwtesting.DirectionServerToClient, rtEventResponseDone, `{"type":"response.done","response":{"id":"`+asyncCollisionResponseOne+`","status":"completed"}}`)

	// The result is the only outbound work eligible after the tool-call response.
	// The grounded continuation must complete before the scheduled audio turn is
	// allowed onto the provider wire.
	add(gwtesting.DirectionClientToServer, rtEventConversationItemCreate, functionCallOutputPayload())
	add(gwtesting.DirectionClientToServer, rtEventResponseCreate, `{"type":"response.create"}`)

	add(gwtesting.DirectionServerToClient, rtEventResponseCreated, `{"type":"response.created","response":{"id":"`+asyncCollisionResponseThree+`"}}`)
	for _, delta := range continuation {
		add(gwtesting.DirectionServerToClient, rtEventOutputAudioDelta, audioDeltaPayload(delta))
	}
	add(gwtesting.DirectionServerToClient, "response.output_audio.done", `{"type":"response.output_audio.done"}`)
	add(gwtesting.DirectionServerToClient, rtEventResponseDone, `{"type":"response.done","response":{"id":"`+asyncCollisionResponseThree+`","status":"completed"}}`)

	// The scheduled audio is a distinct later user turn. Its provider-facing
	// append is rejected by the gated connection if it arrives before the
	// result-driven continuation's terminal MESSAGE.END.
	add(gwtesting.DirectionClientToServer, rtEventInputAudioAppend, inputAudioPayload(inputAudio))
	add(gwtesting.DirectionClientToServer, rtEventInputAudioCommit, `{"type":"input_audio_buffer.commit"}`)
	add(gwtesting.DirectionClientToServer, rtEventResponseCreate, `{"type":"response.create"}`)
	add(gwtesting.DirectionServerToClient, rtEventResponseCreated, `{"type":"response.created","response":{"id":"`+asyncCollisionResponseTwo+`"}}`)
	for _, delta := range collision {
		add(gwtesting.DirectionServerToClient, rtEventOutputAudioDelta, audioDeltaPayload(delta))
	}
	add(gwtesting.DirectionServerToClient, "response.output_audio.done", `{"type":"response.output_audio.done"}`)
	add(gwtesting.DirectionServerToClient, rtEventResponseDone, `{"type":"response.done","response":{"id":"`+asyncCollisionResponseTwo+`","status":"completed"}}`)
	add(gwtesting.DirectionServerToClient, rtEventSessionClosed, `{"type":"session.closed","session_id":"`+asyncCollisionSessionID+`","reason":"`+asyncCollisionCloseReason+`"}`)

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

// asyncContinuationIndices records where each continuation-relevant outbound
// event sits in the provider exchange.
type asyncContinuationIndices struct {
	userTurns, responseCreates, providerResults []int
	inputAppend, inputCommit                    int
}

func collectAsyncContinuationIndices(outbound []asyncCollisionOutbound, expectedInputAudio []byte) (asyncContinuationIndices, error) {
	indices := asyncContinuationIndices{inputAppend: -1, inputCommit: -1}
	for index, event := range outbound {
		switch event.Type {
		case rtEventConversationItemCreate:
			if err := indices.observeItem(index, event.Payload); err != nil {
				return indices, err
			}
		case rtEventResponseCreate:
			indices.responseCreates = append(indices.responseCreates, index)
		case rtEventInputAudioAppend:
			if indices.inputAppend >= 0 {
				return indices, fmt.Errorf("provider exchange contains multiple input_audio_buffer.append events")
			}
			if err := validateAsyncOutboundInputAudio(event.Payload, expectedInputAudio); err != nil {
				return indices, err
			}
			indices.inputAppend = index
		case rtEventInputAudioCommit:
			if indices.inputCommit >= 0 {
				return indices, fmt.Errorf("provider exchange contains multiple input_audio_buffer.commit events")
			}
			indices.inputCommit = index
		}
	}
	return indices, nil
}

func (indices *asyncContinuationIndices) observeItem(index int, raw []byte) error {
	var payload struct {
		Item struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
		} `json:"item"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("decode outbound continuation item: %w", err)
	}
	if payload.Item.Type == rtItemMessage {
		indices.userTurns = append(indices.userTurns, index)
	} else if payload.Item.Type == rtItemFunctionCallOutput && payload.Item.CallID == asyncCollisionCallID {
		indices.providerResults = append(indices.providerResults, index)
	}
	return nil
}

func validateAsyncProviderResultPlacement(indices asyncContinuationIndices, expectProviderResult bool) error {
	if !expectProviderResult {
		if len(indices.providerResults) != 0 {
			return fmt.Errorf("result-loss control still carried %d provider results for %q", len(indices.providerResults), asyncCollisionCallID)
		}
		return nil
	}
	if len(indices.providerResults) != 1 {
		return fmt.Errorf("%s provider result for %q was correlated %d times, want exactly one", asyncCollisionDisposition, asyncCollisionCallID, len(indices.providerResults))
	}
	if !strictlyIncreasing(indices.responseCreates[0], indices.providerResults[0], indices.responseCreates[1]) {
		return fmt.Errorf("%s provider result for %q was not sent between the initial tool response and its continuation response.create", asyncCollisionDisposition, asyncCollisionCallID)
	}
	return nil
}
