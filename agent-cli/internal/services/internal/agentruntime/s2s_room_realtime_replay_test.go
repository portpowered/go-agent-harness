package agentruntime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const roomRealtimeReplayTestTimeout = 2 * time.Second

// roomRealtimeReplayHarness is the room-level composition helper used by the
// real-path replay stories. Each participant owns one strict raw WebSocket
// dialer; no messages.Session or SessionInferencer is injected into the room.
type roomRealtimeReplayHarness struct {
	participants map[string]*roomRealtimeReplayParticipant
}

type roomRealtimeReplayParticipant struct {
	id     string
	dialer *roomRealtimeReplayDialer
}

type roomRealtimeReplayDialer struct {
	participantID string
	inner         *gwtesting.ReplayWebSocketDialer
	events        []gwtesting.CapturedSessionEvent
	writes        chan roomRealtimeReplayWireMessage

	mu         sync.Mutex
	conn       *roomRealtimeReplayConn
	written    []roomRealtimeReplayWireMessage
	dialedOnce bool
}

type roomRealtimeReplayConn struct {
	owner *roomRealtimeReplayDialer
	inner transport.Conn

	mu        sync.Mutex
	completed int
}

type roomRealtimeReplayWireMessage struct {
	Type    string
	Payload []byte
}

var _ transport.Dialer = (*roomRealtimeReplayDialer)(nil)
var _ transport.Conn = (*roomRealtimeReplayConn)(nil)

func newRoomRealtimeReplayHarness(t *testing.T, captures map[string]gwtesting.SessionCapture) *roomRealtimeReplayHarness {
	t.Helper()
	harness := &roomRealtimeReplayHarness{
		participants: make(map[string]*roomRealtimeReplayParticipant, len(captures)),
	}
	for participantID, capture := range captures {
		dialer, err := newRoomRealtimeReplayDialer(participantID, capture)
		if err != nil {
			t.Fatalf("participant %q replay dialer: %v", participantID, err)
		}
		harness.participants[participantID] = &roomRealtimeReplayParticipant{
			id:     participantID,
			dialer: dialer,
		}
	}
	return harness
}

func newRoomRealtimeReplayDialer(participantID string, capture gwtesting.SessionCapture) (*roomRealtimeReplayDialer, error) {
	sealed, err := gwtesting.SealSessionCapture(capture)
	if err != nil {
		return nil, err
	}
	capture = sealed
	inner, err := gwtesting.NewReplayWebSocketDialerFromCapture(capture)
	if err != nil {
		return nil, err
	}
	events := append([]gwtesting.CapturedSessionEvent(nil), capture.Records...)
	return &roomRealtimeReplayDialer{
		participantID: participantID,
		inner:         inner,
		events:        events,
		// Accepted outbound traffic cannot exceed the script length. The extra
		// slot keeps diagnostics non-blocking if a script is deliberately
		// exercised after its expected terminal event.
		writes:  make(chan roomRealtimeReplayWireMessage, len(events)+1),
		written: make([]roomRealtimeReplayWireMessage, 0, len(events)),
	}, nil
}

// DialerFactory is passed directly to RoomRunOptions so the room's normal
// live-session constructor receives a distinct transport for every member.
func (h *roomRealtimeReplayHarness) DialerFactory(participant room.Participant) transport.Dialer {
	if h != nil {
		if configured := h.participants[participant.ID]; configured != nil {
			return configured.dialer
		}
	}
	return missingRoomRealtimeReplayDialer{participantID: participant.ID}
}

func (h *roomRealtimeReplayHarness) participant(id string) *roomRealtimeReplayParticipant {
	if h == nil {
		return nil
	}
	return h.participants[id]
}

func (p *roomRealtimeReplayParticipant) awaitOutbound(t *testing.T, eventType string) roomRealtimeReplayWireMessage {
	t.Helper()
	if p == nil || p.dialer == nil {
		t.Fatalf("participant %q has no replay dialer", pID(p))
	}
	timer := time.NewTimer(roomRealtimeReplayTestTimeout)
	defer timer.Stop()
	for {
		select {
		case message := <-p.dialer.writes:
			if message.Type == eventType {
				return message
			}
		case <-timer.C:
			t.Fatalf("participant %q did not emit outbound %q: replay_err=%v outbound=%+v", p.id, eventType, p.dialer.Err(), p.outboundSnapshot())
		}
	}
}

func (p *roomRealtimeReplayParticipant) outboundSnapshot() []roomRealtimeReplayWireMessage {
	if p == nil || p.dialer == nil {
		return nil
	}
	return p.dialer.outboundSnapshot()
}

func (p *roomRealtimeReplayParticipant) inboundTypes() []string {
	if p == nil || p.dialer == nil {
		return nil
	}
	types := make([]string, 0, len(p.dialer.events))
	for _, event := range p.dialer.events {
		if event.Direction == gwtesting.DirectionServerToClient {
			types = append(types, event.Type)
		}
	}
	return types
}

func (d *roomRealtimeReplayDialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	conn, err := d.inner.Dial(endpoint, headers)
	if err != nil {
		return nil, d.annotate(1, err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dialedOnce {
		_ = conn.Close()
		return nil, fmt.Errorf("participant %q replay script position 1: duplicate Dial", d.participantID)
	}
	d.dialedOnce = true
	wrapped := &roomRealtimeReplayConn{owner: d, inner: conn}
	d.conn = wrapped
	return wrapped, nil
}

func (d *roomRealtimeReplayDialer) outboundSnapshot() []roomRealtimeReplayWireMessage {
	d.mu.Lock()
	defer d.mu.Unlock()
	result := make([]roomRealtimeReplayWireMessage, len(d.written))
	for i, message := range d.written {
		result[i] = roomRealtimeReplayWireMessage{
			Type:    message.Type,
			Payload: append([]byte(nil), message.Payload...),
		}
	}
	return result
}

func (d *roomRealtimeReplayDialer) Done() <-chan struct{} {
	if d == nil || d.inner == nil {
		return nil
	}
	return d.inner.Done()
}

func (d *roomRealtimeReplayDialer) Err() error {
	if d == nil || d.inner == nil {
		return nil
	}
	err := d.inner.Err()
	if err == nil {
		return nil
	}
	position := 1
	d.mu.Lock()
	conn := d.conn
	d.mu.Unlock()
	if conn != nil {
		position = conn.nextPosition()
	}
	return d.annotate(position, err)
}

func (d *roomRealtimeReplayDialer) annotate(position int, err error) error {
	if err == nil {
		return nil
	}
	expected, actual := "replay operation", "replay error"
	var mismatch *gateway.ReplayMismatchError
	var incomplete *gateway.ReplayIncompleteError
	if errors.As(err, &mismatch) {
		expected, actual = mismatch.Expected, mismatch.Actual
	} else if errors.As(err, &incomplete) {
		expected, actual = incomplete.Expected, incomplete.Actual
	}
	if position <= 0 {
		position = 1
	}
	return fmt.Errorf("participant %q replay script position %d: expected %s, actual %s: %w", d.participantID, position, expected, actual, err)
}

func (c *roomRealtimeReplayConn) ReadMessage() (int, []byte, error) {
	position := c.nextPosition()
	messageType, payload, err := c.inner.ReadMessage()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return messageType, payload, err
		}
		return messageType, payload, c.owner.annotate(position, err)
	}
	c.markCompleted()
	return messageType, payload, nil
}

func (c *roomRealtimeReplayConn) WriteMessage(messageType int, payload []byte) error {
	position := c.nextPosition()
	if err := c.inner.WriteMessage(messageType, payload); err != nil {
		return c.owner.annotate(position, err)
	}
	c.markCompleted()
	message := roomRealtimeReplayWireMessage{
		Type:    roomRealtimeReplayPayloadType(payload),
		Payload: append([]byte(nil), payload...),
	}
	c.owner.mu.Lock()
	c.owner.written = append(c.owner.written, message)
	c.owner.mu.Unlock()
	select {
	case c.owner.writes <- message:
	default:
	}
	return nil
}

func (c *roomRealtimeReplayConn) Close() error {
	priorErr := c.owner.inner.Err()
	err := c.inner.Close()
	if err != nil {
		return c.owner.annotate(c.nextPosition(), err)
	}
	// A mismatch is already latched and exposed through the dialer's Err
	// method. Do not report that same error as a second cleanup failure when a
	// caller explicitly cancels the room after observing the mismatch. An error
	// created by Close itself (such as an incomplete capture) remains visible.
	var mismatch *gateway.ReplayMismatchError
	if errors.As(priorErr, &mismatch) {
		return nil
	}
	if replayErr := c.owner.inner.Err(); replayErr != nil {
		return c.owner.annotate(c.nextPosition(), replayErr)
	}
	return nil
}

func (c *roomRealtimeReplayConn) nextPosition() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.completed + 1
}

func (c *roomRealtimeReplayConn) markCompleted() {
	c.mu.Lock()
	c.completed++
	c.mu.Unlock()
}

type missingRoomRealtimeReplayDialer struct {
	participantID string
}

func (d missingRoomRealtimeReplayDialer) Dial(string, map[string]string) (transport.Conn, error) {
	return nil, fmt.Errorf("participant %q replay script has no configured dialer", d.participantID)
}

func roomRealtimeReplayPayloadType(payload []byte) string {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Type == "" {
		return "websocket.message"
	}
	return envelope.Type
}

func roomRealtimeReplayCapture(model string, records ...gwtesting.CapturedSessionEvent) gwtesting.SessionCapture {
	return gwtesting.SessionCapture{
		Version: gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{
			Name:  config.ProviderOpenAI,
			Model: model,
		},
		Session: gwtesting.SessionMetadata{
			FixtureProvenance: gwtesting.SessionFixtureProvenanceSynthetic,
		},
		Records: records,
	}
}

func roomRealtimeReplayEvent(sequence int, direction gwtesting.SessionEventDirection, eventType string, payload []byte) gwtesting.CapturedSessionEvent {
	return gwtesting.CapturedSessionEvent{
		Sequence:    sequence,
		Direction:   direction,
		Type:        eventType,
		PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
		Payload:     append([]byte(nil), payload...),
	}
}

func roomRealtimeReplayJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal replay JSON: %v", err)
	}
	return data
}

func roomRealtimeReplaySessionUpdate(t *testing.T, model, instructions string) []byte {
	return roomRealtimeReplayJSON(t, map[string]any{
		"type": "session.update",
		"session": map[string]any{
			"type":              "realtime",
			"model":             model,
			"output_modalities": []string{"audio"},
			"instructions":      instructions,
			"audio": map[string]any{
				"input": map[string]any{
					"format":        map[string]any{"type": "audio/pcm", "rate": 24000},
					"transcription": map[string]any{"model": "gpt-live-transcribe"},
				},
				"output": map[string]any{
					"format": map[string]any{"type": "audio/pcm", "rate": 24000},
				},
			},
		},
	})
}

func pID(participant *roomRealtimeReplayParticipant) string {
	if participant == nil {
		return "<nil>"
	}
	return participant.id
}

func TestRunRoomWithResult_UsesRealRealtimeStackAndStrictParticipantWires(t *testing.T) {
	const (
		speakerID    = "speaker"
		listenerID   = "listener"
		model        = openAIRealtimeDefaultModel
		speakerText  = "speaker system"
		listenerText = "listener system"
	)

	speakerPCM := []byte{0x34, 0x12, 0x78, 0x56}
	speakerAudioBase64 := base64.StdEncoding.EncodeToString(speakerPCM)
	speakerCapture := roomRealtimeReplayCapture(model,
		roomRealtimeReplayEvent(1, gwtesting.DirectionClientToServer, "session.update", roomRealtimeReplaySessionUpdate(t, model, speakerText)),
		roomRealtimeReplayEvent(2, gwtesting.DirectionServerToClient, "session.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "session.created", "session": map[string]any{"id": "sess-speaker", "model": model},
		})),
		roomRealtimeReplayEvent(3, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.created", "response": map[string]any{"id": "resp-speaker"},
		})),
		roomRealtimeReplayEvent(4, gwtesting.DirectionServerToClient, "response.output_audio.delta", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.delta", "delta": speakerAudioBase64,
		})),
		roomRealtimeReplayEvent(5, gwtesting.DirectionServerToClient, "response.output_audio.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.done",
		})),
		roomRealtimeReplayEvent(6, gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.done",
		})),
	)
	listenerCapture := roomRealtimeReplayCapture(model,
		roomRealtimeReplayEvent(1, gwtesting.DirectionClientToServer, "session.update", roomRealtimeReplaySessionUpdate(t, model, listenerText)),
		roomRealtimeReplayEvent(2, gwtesting.DirectionServerToClient, "session.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "session.created", "session": map[string]any{"id": "sess-listener", "model": model},
		})),
		// The listener cannot receive its response until the room has sent the
		// mixed speaker frame. ReplayWebSocketDialer blocks this read until the
		// exact append below has been accepted.
		roomRealtimeReplayEvent(3, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": speakerAudioBase64,
		})),
		roomRealtimeReplayEvent(4, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.created", "response": map[string]any{"id": "resp-listener"},
		})),
		roomRealtimeReplayEvent(5, gwtesting.DirectionServerToClient, "response.output_text.delta", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_text.delta", "delta": "listener response",
		})),
		roomRealtimeReplayEvent(6, gwtesting.DirectionServerToClient, "response.output_text.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_text.done",
		})),
		roomRealtimeReplayEvent(7, gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.done",
		})),
	)
	harness := newRoomRealtimeReplayHarness(t, map[string]gwtesting.SessionCapture{
		speakerID:  speakerCapture,
		listenerID: listenerCapture,
	})

	configDir := t.TempDir()
	writeSessionConfigFile(t, configDir, "model:\n  provider: openai\n")
	credentials := map[string]string{
		"ROOM_SPEAKER_KEY":  "room-speaker-test-key",
		"ROOM_LISTENER_KEY": "room-listener-test-key",
	}
	manifest := room.Manifest{
		SchemaVersion: room.SchemaVersion,
		Room:          room.Room{MaxTurns: 1},
		Participants: []room.Participant{
			{ID: speakerID, SystemPrompt: speakerText, Provider: config.ProviderOpenAI, Model: model, APIKeyEnv: "ROOM_SPEAKER_KEY", Tools: []string{}},
			{ID: listenerID, SystemPrompt: listenerText, Provider: config.ProviderOpenAI, Model: model, APIKeyEnv: "ROOM_LISTENER_KEY", Tools: []string{}},
		},
	}

	cadenceReady := make(chan *roomRealtimeReplayCadence, len(manifest.Participants))
	mixerConfig := room.PCM16MixerConfig{
		Format:            room.PCM16Format{SampleRate: 100, Channels: 1, FrameDuration: 20 * time.Millisecond},
		InputQueueFrames:  4,
		OutputQueueFrames: 4,
		CadenceFactory: func(time.Duration) room.PCM16Cadence {
			cadence := newRoomRealtimeReplayCadence()
			cadenceReady <- cadence
			return cadence
		},
	}
	audioOutput := make(chan roomRealtimeReplayWireMessage, 1)
	allowFanout := make(chan struct{})
	diagnosticTurns := make(chan string, 4)
	roomCtx, cancel := context.WithTimeout(context.Background(), roomRealtimeReplayTestTimeout)
	defer cancel()

	opts := RoomRunOptions{
		Manifest:    manifest,
		ConfigDir:   configDir,
		BaseURL:     "wss://room-replay.invalid/v1/realtime",
		MixerConfig: mixerConfig,
		CredentialLookup: func(name string) (string, bool) {
			value, ok := credentials[name]
			return value, ok
		},
		WebSocketDialerFactory: harness.DialerFactory,
		OnAudioOutput: func(participantID string, pcm []byte) error {
			if participantID == speakerID {
				audioOutput <- roomRealtimeReplayWireMessage{Type: participantID, Payload: append([]byte(nil), pcm...)}
				<-allowFanout
			}
			return nil
		},
		OnDiagnostic: func(participantID string, record SessionDiagnosticRecord) {
			if record.Event == SessionDiagnosticEventTurn {
				diagnosticTurns <- participantID
			}
		},
	}

	runDone := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(roomCtx, io.Discard, opts)
		runDone <- roomTestRunOutcome{result: result, err: err}
	}()

	// The factory is called once for every participant before any session is
	// admitted. Manifest order makes the two returned controls identity-stable.
	var speakerCadence, listenerCadence *roomRealtimeReplayCadence
	select {
	case speakerCadence = <-cadenceReady:
	case <-roomCtx.Done():
		t.Fatalf("speaker mixer cadence was not created: %v", roomCtx.Err())
	}
	select {
	case listenerCadence = <-cadenceReady:
	case <-roomCtx.Done():
		t.Fatalf("listener mixer cadence was not created: %v", roomCtx.Err())
	}
	if speakerCadence == nil || listenerCadence == nil {
		t.Fatal("room did not create both deterministic mixer cadences")
	}

	select {
	case output := <-audioOutput:
		if output.Type != speakerID || !bytes.Equal(output.Payload, speakerPCM) {
			t.Fatalf("decoded speaker audio = %v, want %v", output.Payload, speakerPCM)
		}
	case <-roomCtx.Done():
		t.Fatalf("speaker response was not decoded through the room: %v", roomCtx.Err())
	}
	close(allowFanout)

	select {
	case participantID := <-diagnosticTurns:
		if participantID != speakerID {
			t.Fatalf("first completed participant = %q, want %q", participantID, speakerID)
		}
	case <-roomCtx.Done():
		t.Fatalf("speaker response did not reach a completed turn: %v", roomCtx.Err())
	}

	listenerCadence.Advance()
	appendMessage := harness.participant(listenerID).awaitOutbound(t, "input_audio_buffer.append")
	var appendPayload struct {
		Type  string `json:"type"`
		Audio string `json:"audio"`
	}
	if err := json.Unmarshal(appendMessage.Payload, &appendPayload); err != nil {
		t.Fatalf("decode listener raw append JSON: %v", err)
	}
	if appendPayload.Type != "input_audio_buffer.append" {
		t.Fatalf("listener append type = %q, want input_audio_buffer.append", appendPayload.Type)
	}
	gotPCM, err := base64.StdEncoding.DecodeString(appendPayload.Audio)
	if err != nil {
		t.Fatalf("decode listener append audio: %v", err)
	}
	if !bytes.Equal(gotPCM, speakerPCM) {
		t.Fatalf("listener raw append PCM = %v, want byte-exact %v", gotPCM, speakerPCM)
	}

	var outcome roomTestRunOutcome
	select {
	case outcome = <-runDone:
	case <-roomCtx.Done():
		t.Fatalf("room did not reach its explicit max-turn boundary: %v", roomCtx.Err())
	}
	if outcome.err != nil {
		t.Fatalf("real realtime room run: %v", outcome.err)
	}
	if outcome.result.Reason != RoomTerminationMaxTurnsReached {
		t.Fatalf("room termination reason = %q, want %q", outcome.result.Reason, RoomTerminationMaxTurnsReached)
	}
	for _, participantID := range []string{speakerID, listenerID} {
		participantResult, ok := outcome.result.Participants[participantID]
		if !ok {
			t.Fatalf("room result missing participant %q", participantID)
		}
		if !participantResult.Connected || participantResult.TurnsCompleted != 1 {
			t.Fatalf("participant %q result = %+v, want connected with one completed turn", participantID, participantResult)
		}
	}
	select {
	case participantID := <-diagnosticTurns:
		if participantID != listenerID {
			t.Fatalf("second completed participant = %q, want %q", participantID, listenerID)
		}
	case <-roomCtx.Done():
		t.Fatalf("listener response did not reach a completed turn: %v", roomCtx.Err())
	}

	for _, participantID := range []string{speakerID, listenerID} {
		participant := harness.participant(participantID)
		if err := participant.dialer.Err(); err != nil {
			t.Fatalf("participant %q strict wire: %v", participantID, err)
		}
	}
	if got := harness.participant(speakerID).inboundTypes(); !sameRoomReplayStrings(got, []string{"session.created", "response.created", "response.output_audio.delta", "response.output_audio.done", "response.done"}) {
		t.Fatalf("speaker inbound provider events = %v", got)
	}
	if got := harness.participant(listenerID).inboundTypes(); !sameRoomReplayStrings(got, []string{"session.created", "response.created", "response.output_text.delta", "response.output_text.done", "response.done"}) {
		t.Fatalf("listener inbound provider events = %v", got)
	}
	if writes := harness.participant(speakerID).outboundSnapshot(); len(writes) != 1 || writes[0].Type != "session.update" {
		t.Fatalf("speaker outbound raw wire = %+v, want one session.update", writes)
	}
	if writes := harness.participant(listenerID).outboundSnapshot(); len(writes) != 2 || writes[0].Type != "session.update" || writes[1].Type != "input_audio_buffer.append" {
		t.Fatalf("listener outbound raw wire = %+v, want session.update then append", writes)
	}
}
