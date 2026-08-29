package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
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
					"format": map[string]any{"type": "audio/pcm", "rate": 24000},
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

func TestRunRoomWithResult_SilenceCadenceDoesNotCancelActiveResponse(t *testing.T) {
	const (
		peerID   = "peer"
		targetID = "target"
		model    = openAIRealtimeDefaultModel
	)

	silence := []byte{0, 0, 0, 0}
	responsePCM := []byte{0x34, 0x12, 0x78, 0x56}
	responseAudioBase64 := base64.StdEncoding.EncodeToString(responsePCM)
	silenceBase64 := base64.StdEncoding.EncodeToString(silence)

	peerCapture := roomRealtimeReplayCapture(model,
		roomRealtimeReplayEvent(1, gwtesting.DirectionClientToServer, "session.update", roomRealtimeReplaySessionUpdate(t, model, "peer system")),
		roomRealtimeReplayEvent(2, gwtesting.DirectionServerToClient, "session.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "session.created", "session": map[string]any{"id": "sess-peer", "model": model},
		})),
		roomRealtimeReplayEvent(3, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.created", "response": map[string]any{"id": "resp-peer"},
		})),
		roomRealtimeReplayEvent(4, gwtesting.DirectionServerToClient, "response.output_text.delta", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_text.delta", "delta": "peer response",
		})),
		roomRealtimeReplayEvent(5, gwtesting.DirectionServerToClient, "response.output_text.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_text.done",
		})),
		roomRealtimeReplayEvent(6, gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.done",
		})),
	)
	targetCapture := roomRealtimeReplayCapture(model,
		roomRealtimeReplayEvent(1, gwtesting.DirectionClientToServer, "session.update", roomRealtimeReplaySessionUpdate(t, model, "target system")),
		roomRealtimeReplayEvent(2, gwtesting.DirectionServerToClient, "session.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "session.created", "session": map[string]any{"id": "sess-target", "model": model},
		})),
		// The first zero frame is idle input. It unlocks response.created; the
		// output delta then proves the target response is active before the
		// remaining zero frames are advanced.
		roomRealtimeReplayEvent(3, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": silenceBase64,
		})),
		roomRealtimeReplayEvent(4, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.created", "response": map[string]any{"id": "resp-target"},
		})),
		roomRealtimeReplayEvent(5, gwtesting.DirectionServerToClient, "response.output_audio.delta", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.delta", "delta": responseAudioBase64,
		})),
		// Keep the provider response open behind three exact silence appends.
		// A response.cancel at any of these positions is a strict replay error.
		roomRealtimeReplayEvent(6, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": silenceBase64,
		})),
		roomRealtimeReplayEvent(7, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": silenceBase64,
		})),
		roomRealtimeReplayEvent(8, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": silenceBase64,
		})),
		roomRealtimeReplayEvent(9, gwtesting.DirectionServerToClient, "response.output_audio.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.done",
		})),
		roomRealtimeReplayEvent(10, gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.done",
		})),
	)
	harness := newRoomRealtimeReplayHarness(t, map[string]gwtesting.SessionCapture{
		peerID:   peerCapture,
		targetID: targetCapture,
	})

	configDir := t.TempDir()
	writeSessionConfigFile(t, configDir, "model:\n  provider: openai\n")
	credentials := map[string]string{
		"ROOM_PEER_KEY":   "room-peer-test-key",
		"ROOM_TARGET_KEY": "room-target-test-key",
	}
	manifest := room.Manifest{
		SchemaVersion: room.SchemaVersion,
		Room:          room.Room{MaxTurns: 1},
		Participants: []room.Participant{
			{ID: peerID, SystemPrompt: "peer system", Provider: config.ProviderOpenAI, Model: model, APIKeyEnv: "ROOM_PEER_KEY", Tools: []string{}},
			{ID: targetID, SystemPrompt: "target system", Provider: config.ProviderOpenAI, Model: model, APIKeyEnv: "ROOM_TARGET_KEY", Tools: []string{}},
		},
	}

	cadenceReady := make(chan *roomRealtimeReplayCadence, len(manifest.Participants))
	mixerConfig := room.PCM16MixerConfig{
		Format:            room.PCM16Format{SampleRate: 100, Channels: 1, FrameDuration: 20 * time.Millisecond},
		InputQueueFrames:  8,
		OutputQueueFrames: 8,
		CadenceFactory: func(time.Duration) room.PCM16Cadence {
			cadence := newRoomRealtimeReplayCadence()
			cadenceReady <- cadence
			return cadence
		},
	}
	targetAudio := make(chan []byte, 1)
	opened := make(chan string, len(manifest.Participants))
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
		onParticipantSessionOpen: func(participantID string) {
			opened <- participantID
		},
		OnAudioOutput: func(participantID string, pcm []byte) error {
			if participantID == targetID {
				targetAudio <- append([]byte(nil), pcm...)
			}
			return nil
		},
	}

	runDone := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(roomCtx, io.Discard, opts)
		runDone <- roomTestRunOutcome{result: result, err: err}
	}()

	var peerCadence, targetCadence *roomRealtimeReplayCadence
	select {
	case peerCadence = <-cadenceReady:
	case <-roomCtx.Done():
		t.Fatalf("peer mixer cadence was not created: %v", roomCtx.Err())
	}
	select {
	case targetCadence = <-cadenceReady:
	case <-roomCtx.Done():
		t.Fatalf("target mixer cadence was not created: %v", roomCtx.Err())
	}
	if peerCadence == nil || targetCadence == nil {
		t.Fatal("room did not create both deterministic mixer cadences")
	}
	for range manifest.Participants {
		select {
		case <-opened:
		case <-roomCtx.Done():
			t.Fatalf("room sessions did not open: %v", roomCtx.Err())
		}
	}

	// No peer audio is ever queued, so this frame is deterministically all
	// zeroes. It starts the scripted response without asserting on scheduling.
	targetCadence.Advance()
	firstAppend := harness.participant(targetID).awaitOutbound(t, "input_audio_buffer.append")
	if !bytes.Equal(firstAppend.Payload, roomRealtimeReplayJSON(t, map[string]any{
		"type": "input_audio_buffer.append", "audio": silenceBase64,
	})) {
		t.Fatalf("target idle append payload = %s, want exact silence append", firstAppend.Payload)
	}
	select {
	case got := <-targetAudio:
		if !bytes.Equal(got, responsePCM) {
			t.Fatalf("target response audio = %v, want %v", got, responsePCM)
		}
	case <-roomCtx.Done():
		t.Fatalf("target response did not become active: %v", roomCtx.Err())
	}

	for activeFrame := 1; activeFrame <= 3; activeFrame++ {
		targetCadence.Advance()
		appendMessage := harness.participant(targetID).awaitOutbound(t, "input_audio_buffer.append")
		wantPayload := roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": silenceBase64,
		})
		if !bytes.Equal(appendMessage.Payload, wantPayload) {
			t.Fatalf("target active silence frame %d payload = %s, want exact silence append", activeFrame, appendMessage.Payload)
		}
	}

	var outcome roomTestRunOutcome
	select {
	case outcome = <-runDone:
	case <-roomCtx.Done():
		t.Fatalf("room did not reach explicit max-turn boundary after silence: %v", roomCtx.Err())
	}
	if outcome.err != nil {
		t.Fatalf("silence room replay: %v", outcome.err)
	}
	if outcome.result.Reason != RoomTerminationMaxTurnsReached {
		t.Fatalf("room termination reason = %q, want %q", outcome.result.Reason, RoomTerminationMaxTurnsReached)
	}
	for _, participantID := range []string{peerID, targetID} {
		participantResult, ok := outcome.result.Participants[participantID]
		if !ok {
			t.Fatalf("room result missing participant %q", participantID)
		}
		if !participantResult.Connected || participantResult.TurnsCompleted != 1 {
			t.Fatalf("participant %q result = %+v, want connected with one completed turn", participantID, participantResult)
		}
		if err := harness.participant(participantID).dialer.Err(); err != nil {
			t.Fatalf("participant %q strict wire: %v", participantID, err)
		}
	}

	peerWrites := harness.participant(peerID).outboundSnapshot()
	if len(peerWrites) != 1 || peerWrites[0].Type != "session.update" {
		t.Fatalf("peer outbound raw wire = %+v, want one session.update", peerWrites)
	}
	targetWrites := harness.participant(targetID).outboundSnapshot()
	if len(targetWrites) != 5 || targetWrites[0].Type != "session.update" {
		t.Fatalf("target outbound raw wire = %+v, want session.update plus four silence appends", targetWrites)
	}
	for index, write := range targetWrites[1:] {
		if write.Type != "input_audio_buffer.append" || !bytes.Equal(write.Payload, roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": silenceBase64,
		})) {
			t.Fatalf("target outbound raw wire step %d = %+v, want silence append and no response.cancel", index+2, write)
		}
	}
}

type roomSpeechOverlapFanout struct {
	sourceID string
	targetID string
	pcm      []byte
}

type roomSpeechOverlapScenario struct {
	harness       *roomRealtimeReplayHarness
	peerCadence   *roomRealtimeReplayCadence
	targetCadence *roomRealtimeReplayCadence
	targetAudio   <-chan []byte
	fanouts       <-chan roomSpeechOverlapFanout
	diagnostic    <-chan string
	targetEnds    <-chan struct{}
	runDone       <-chan roomTestRunOutcome
	ctx           context.Context
	cancel        context.CancelFunc

	silence        []byte
	expectedSpeech []byte
	peerOutput     []byte
	targetOutput   []byte
	followupOutput []byte
}

func newRoomSpeechOverlapScenario(t *testing.T, peerOutput []byte) *roomSpeechOverlapScenario {
	t.Helper()
	const (
		peerID   = "speaker"
		targetID = "target"
		model    = openAIRealtimeDefaultModel
	)

	silence := []byte{0, 0, 0, 0}
	expectedSpeech := []byte{0x20, 0x03, 0xe0, 0xfc}
	targetOutput := []byte{0x34, 0x12, 0x78, 0x56}
	followupOutput := []byte{0x56, 0x34, 0x12, 0x78}
	peerOutput = append([]byte(nil), peerOutput...)
	silenceBase64 := base64.StdEncoding.EncodeToString(silence)
	expectedSpeechBase64 := base64.StdEncoding.EncodeToString(expectedSpeech)
	targetOutputBase64 := base64.StdEncoding.EncodeToString(targetOutput)
	peerOutputBase64 := base64.StdEncoding.EncodeToString(peerOutput)
	followupOutputBase64 := base64.StdEncoding.EncodeToString(followupOutput)

	peerCapture := roomRealtimeReplayCapture(model,
		roomRealtimeReplayEvent(1, gwtesting.DirectionClientToServer, "session.update", roomRealtimeReplaySessionUpdate(t, model, "peer system")),
		roomRealtimeReplayEvent(2, gwtesting.DirectionServerToClient, "session.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "session.created", "session": map[string]any{"id": "sess-peer-overlap", "model": model},
		})),
		// The peer receives the target's first response through its real mixer
		// before its scripted response emits the overlapping speech frames.
		roomRealtimeReplayEvent(3, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": targetOutputBase64,
		})),
		roomRealtimeReplayEvent(4, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.created", "response": map[string]any{"id": "resp-peer-overlap"},
		})),
		roomRealtimeReplayEvent(5, gwtesting.DirectionServerToClient, "response.output_audio.delta", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.delta", "response_id": "resp-peer-overlap", "delta": peerOutputBase64,
		})),
		roomRealtimeReplayEvent(6, gwtesting.DirectionServerToClient, "response.output_audio.delta", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.delta", "response_id": "resp-peer-overlap", "delta": peerOutputBase64,
		})),
		roomRealtimeReplayEvent(7, gwtesting.DirectionServerToClient, "response.output_audio.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.done", "response_id": "resp-peer-overlap",
		})),
		roomRealtimeReplayEvent(8, gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.done", "response": map[string]any{"id": "resp-peer-overlap", "status": "completed"},
		})),
	)
	targetCapture := roomRealtimeReplayCapture(model,
		roomRealtimeReplayEvent(1, gwtesting.DirectionClientToServer, "session.update", roomRealtimeReplaySessionUpdate(t, model, "target system")),
		roomRealtimeReplayEvent(2, gwtesting.DirectionServerToClient, "session.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "session.created", "session": map[string]any{"id": "sess-target-overlap", "model": model},
		})),
		// This first idle frame establishes the active response without
		// creating a cancellation candidate.
		roomRealtimeReplayEvent(3, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": silenceBase64,
		})),
		roomRealtimeReplayEvent(4, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.created", "response": map[string]any{"id": "resp-target-overlap-1"},
		})),
		roomRealtimeReplayEvent(5, gwtesting.DirectionServerToClient, "response.output_audio.delta", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.delta", "response_id": "resp-target-overlap-1", "delta": targetOutputBase64,
		})),
		// The first contentful overlap must cancel the active response before
		// its append. The second append remains in this same overlap and must
		// not produce another cancellation.
		roomRealtimeReplayEvent(6, gwtesting.DirectionClientToServer, "response.cancel", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.cancel",
		})),
		roomRealtimeReplayEvent(7, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": expectedSpeechBase64,
		})),
		roomRealtimeReplayEvent(8, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": expectedSpeechBase64,
		})),
		// A cancelled provider response still has a wire terminal event. The
		// runtime must keep the boundary but exclude it from completed turns.
		roomRealtimeReplayEvent(9, gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.done", "response": map[string]any{"id": "resp-target-overlap-1", "status": "cancelled"},
		})),
		// A later idle frame starts a normal response so max-turn shutdown is
		// an explicit room boundary rather than timeout or transport EOF.
		roomRealtimeReplayEvent(10, gwtesting.DirectionClientToServer, "input_audio_buffer.append", roomRealtimeReplayJSON(t, map[string]any{
			"type": "input_audio_buffer.append", "audio": silenceBase64,
		})),
		roomRealtimeReplayEvent(11, gwtesting.DirectionServerToClient, "response.created", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.created", "response": map[string]any{"id": "resp-target-overlap-2"},
		})),
		roomRealtimeReplayEvent(12, gwtesting.DirectionServerToClient, "response.output_audio.delta", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.delta", "response_id": "resp-target-overlap-2", "delta": followupOutputBase64,
		})),
		roomRealtimeReplayEvent(13, gwtesting.DirectionServerToClient, "response.output_audio.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.output_audio.done", "response_id": "resp-target-overlap-2",
		})),
		roomRealtimeReplayEvent(14, gwtesting.DirectionServerToClient, "response.done", roomRealtimeReplayJSON(t, map[string]any{
			"type": "response.done", "response": map[string]any{"id": "resp-target-overlap-2", "status": "completed"},
		})),
	)
	harness := newRoomRealtimeReplayHarness(t, map[string]gwtesting.SessionCapture{
		peerID:   peerCapture,
		targetID: targetCapture,
	})

	configDir := t.TempDir()
	writeSessionConfigFile(t, configDir, "model:\n  provider: openai\n")
	credentials := map[string]string{
		"ROOM_SPEAKER_KEY": "room-speaker-overlap-key",
		"ROOM_TARGET_KEY":  "room-target-overlap-key",
	}
	manifest := room.Manifest{
		SchemaVersion: room.SchemaVersion,
		Room:          room.Room{MaxTurns: 1},
		Participants: []room.Participant{
			{ID: peerID, SystemPrompt: "peer system", Provider: config.ProviderOpenAI, Model: model, APIKeyEnv: "ROOM_SPEAKER_KEY", Tools: []string{}},
			{ID: targetID, SystemPrompt: "target system", Provider: config.ProviderOpenAI, Model: model, APIKeyEnv: "ROOM_TARGET_KEY", Tools: []string{}},
		},
	}

	cadenceReady := make(chan *roomRealtimeReplayCadence, len(manifest.Participants))
	mixerConfig := room.PCM16MixerConfig{
		Format:            room.PCM16Format{SampleRate: 100, Channels: 1, FrameDuration: 20 * time.Millisecond},
		InputQueueFrames:  8,
		OutputQueueFrames: 8,
		CadenceFactory: func(time.Duration) room.PCM16Cadence {
			cadence := newRoomRealtimeReplayCadence()
			cadenceReady <- cadence
			return cadence
		},
	}
	targetAudio := make(chan []byte, 8)
	fanouts := make(chan roomSpeechOverlapFanout, 16)
	diagnosticTurns := make(chan string, 8)
	opened := make(chan string, len(manifest.Participants))
	targetEnds := make(chan struct{}, 4)
	roomCtx, cancel := context.WithTimeout(context.Background(), roomRealtimeReplayTestTimeout)
	t.Cleanup(cancel)

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
		onParticipantSessionOpen: func(participantID string) {
			// The startup barrier below observes this through the channel so no
			// cadence can be released before all real sessions are admitted.
			opened <- participantID
		},
		onParticipantAudioFanned: func(sourceID, targetID string, pcm []byte) {
			fanouts <- roomSpeechOverlapFanout{
				sourceID: sourceID,
				targetID: targetID,
				pcm:      append([]byte(nil), pcm...),
			}
		},
		onParticipantStream: func(participantID string, msg messages.StreamMessage) {
			if participantID == targetID && msg.Type == messages.StreamTypeMessageEnd {
				targetEnds <- struct{}{}
			}
		},
		OnAudioOutput: func(participantID string, pcm []byte) error {
			if participantID == targetID {
				targetAudio <- append([]byte(nil), pcm...)
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

	var peerCadence, targetCadence *roomRealtimeReplayCadence
	select {
	case peerCadence = <-cadenceReady:
	case <-roomCtx.Done():
		t.Fatalf("peer overlap mixer cadence was not created: %v", roomCtx.Err())
	}
	select {
	case targetCadence = <-cadenceReady:
	case <-roomCtx.Done():
		t.Fatalf("target overlap mixer cadence was not created: %v", roomCtx.Err())
	}
	for range manifest.Participants {
		select {
		case <-opened:
		case <-roomCtx.Done():
			t.Fatalf("overlap room sessions did not open: %v", roomCtx.Err())
		}
	}

	return &roomSpeechOverlapScenario{
		harness:        harness,
		peerCadence:    peerCadence,
		targetCadence:  targetCadence,
		targetAudio:    targetAudio,
		fanouts:        fanouts,
		diagnostic:     diagnosticTurns,
		targetEnds:     targetEnds,
		runDone:        runDone,
		ctx:            roomCtx,
		cancel:         cancel,
		silence:        silence,
		expectedSpeech: expectedSpeech,
		peerOutput:     peerOutput,
		targetOutput:   targetOutput,
		followupOutput: followupOutput,
	}
}

func TestRunRoomWithResult_SpeechOverlapCancelsExactlyOnce(t *testing.T) {
	t.Run("speech overlap", func(t *testing.T) {
		scenario := newRoomSpeechOverlapScenario(t, []byte{0x20, 0x03, 0xe0, 0xfc})

		scenario.targetCadence.Advance()
		assertRoomSpeechOverlapAppend(t, scenario.harness.participant("target"), scenario.silence)
		awaitRoomSpeechOverlapAudio(t, scenario.targetAudio, scenario.targetOutput)
		awaitRoomSpeechOverlapFanout(t, scenario.fanouts, "target", "speaker", scenario.targetOutput)

		// The speaker's first mixer frame is the target's response audio. Its
		// scripted response then emits two speech-shaped frames to the target.
		scenario.peerCadence.Advance()
		assertRoomSpeechOverlapAppend(t, scenario.harness.participant("speaker"), scenario.targetOutput)
		awaitRoomSpeechOverlapFanout(t, scenario.fanouts, "speaker", "target", scenario.peerOutput)
		awaitRoomSpeechOverlapFanout(t, scenario.fanouts, "speaker", "target", scenario.peerOutput)

		scenario.targetCadence.Advance()
		cancelMessage := scenario.harness.participant("target").awaitOutbound(t, "response.cancel")
		if !bytes.Equal(cancelMessage.Payload, roomRealtimeReplayJSON(t, map[string]any{"type": "response.cancel"})) {
			t.Fatalf("target response.cancel payload = %s, want exact response.cancel", cancelMessage.Payload)
		}
		assertRoomSpeechOverlapAppend(t, scenario.harness.participant("target"), scenario.expectedSpeech)

		// A second contentful frame from the same overlap must be forwarded
		// without another RESPONSE.CANCEL.
		scenario.targetCadence.Advance()
		assertRoomSpeechOverlapAppend(t, scenario.harness.participant("target"), scenario.expectedSpeech)

		// The cancelled response.done is consumed before this idle frame opens
		// the normal follow-up response that reaches the room max-turn boundary.
		awaitRoomSpeechOverlapTargetEnd(t, scenario.targetEnds)
		scenario.targetCadence.Advance()
		assertRoomSpeechOverlapAppend(t, scenario.harness.participant("target"), scenario.silence)
		awaitRoomSpeechOverlapAudio(t, scenario.targetAudio, scenario.followupOutput)

		outcome := awaitRoomSpeechOverlapRun(t, scenario)
		if outcome.err != nil {
			t.Fatalf("speech-overlap room replay: %v", outcome.err)
		}
		if outcome.result.Reason != RoomTerminationMaxTurnsReached {
			t.Fatalf("speech-overlap room termination = %q, want %q", outcome.result.Reason, RoomTerminationMaxTurnsReached)
		}
		for _, participantID := range []string{"speaker", "target"} {
			participantResult, ok := outcome.result.Participants[participantID]
			if !ok {
				t.Fatalf("speech-overlap result missing participant %q", participantID)
			}
			if !participantResult.Connected || participantResult.TurnsCompleted != 1 {
				t.Fatalf("speech-overlap participant %q result = %+v, want one normal completed turn", participantID, participantResult)
			}
			if err := scenario.harness.participant(participantID).dialer.Err(); err != nil {
				t.Fatalf("speech-overlap participant %q strict wire: %v", participantID, err)
			}
		}

		diagnosticCounts := map[string]int{}
		for range []int{0, 1} {
			select {
			case participantID := <-scenario.diagnostic:
				diagnosticCounts[participantID]++
			case <-scenario.ctx.Done():
				t.Fatalf("speech-overlap diagnostics did not report both normal turns: %v", scenario.ctx.Err())
			}
		}
		if diagnosticCounts["speaker"] != 1 || diagnosticCounts["target"] != 1 {
			t.Fatalf("speech-overlap diagnostic turns = %v, want one per participant and none for cancelled response", diagnosticCounts)
		}

		targetWrites := scenario.harness.participant("target").outboundSnapshot()
		wantTypes := []string{"session.update", "input_audio_buffer.append", "response.cancel", "input_audio_buffer.append", "input_audio_buffer.append", "input_audio_buffer.append"}
		gotTypes := make([]string, 0, len(targetWrites))
		for _, write := range targetWrites {
			gotTypes = append(gotTypes, write.Type)
		}
		if !sameRoomReplayStrings(gotTypes, wantTypes) {
			t.Fatalf("target overlap outbound types = %v, want %v", gotTypes, wantTypes)
		}
		wantAppends := [][]byte{scenario.silence, scenario.expectedSpeech, scenario.expectedSpeech, scenario.silence}
		appendWriteIndexes := []int{1, 3, 4, 5}
		for index, wantPCM := range wantAppends {
			assertRoomSpeechOverlapWireAppendPayload(t, targetWrites[appendWriteIndexes[index]], wantPCM)
		}
		appendCount := 0
		for _, write := range targetWrites {
			if write.Type == "input_audio_buffer.append" {
				appendCount++
			}
		}
		if got := appendCount; got != len(wantAppends) {
			t.Fatalf("target overlap append count = %d, want %d", got, len(wantAppends))
		}
		if got := len(scenario.harness.participant("speaker").outboundSnapshot()); got != 2 {
			t.Fatalf("speaker overlap outbound count = %d, want session.update plus one append", got)
		}
		if got := scenario.harness.participant("target").inboundTypes(); !sameRoomReplayStrings(got, []string{
			"session.created", "response.created", "response.output_audio.delta", "response.done",
			"response.created", "response.output_audio.delta", "response.output_audio.done", "response.done",
		}) {
			t.Fatalf("target overlap inbound provider events = %v", got)
		}
	})

	t.Run("digital silence negative control", func(t *testing.T) {
		// The target provider script is unchanged: replacing the two peer
		// frames with digital silence must fail at the exact response.cancel
		// slot instead of falsely satisfying the speech-overlap expectation.
		scenario := newRoomSpeechOverlapScenario(t, []byte{0, 0, 0, 0})

		scenario.targetCadence.Advance()
		assertRoomSpeechOverlapAppend(t, scenario.harness.participant("target"), scenario.silence)
		awaitRoomSpeechOverlapAudio(t, scenario.targetAudio, scenario.targetOutput)
		awaitRoomSpeechOverlapFanout(t, scenario.fanouts, "target", "speaker", scenario.targetOutput)
		scenario.peerCadence.Advance()
		assertRoomSpeechOverlapAppend(t, scenario.harness.participant("speaker"), scenario.targetOutput)
		awaitRoomSpeechOverlapFanout(t, scenario.fanouts, "speaker", "target", scenario.peerOutput)
		awaitRoomSpeechOverlapFanout(t, scenario.fanouts, "speaker", "target", scenario.peerOutput)

		scenario.targetCadence.Advance()
		select {
		case <-scenario.harness.participant("target").dialer.Done():
		case <-scenario.ctx.Done():
			t.Fatalf("digital-silence negative control did not reach strict replay mismatch: %v", scenario.ctx.Err())
		}
		error := scenario.harness.participant("target").dialer.Err()
		if error == nil {
			t.Fatal("digital-silence negative control did not retain strict replay mismatch")
		}
		scenario.cancel()
		outcome := awaitRoomSpeechOverlapRun(t, scenario)
		if outcome.err != nil || outcome.result.Reason != RoomTerminationStopped {
			t.Fatalf("digital-silence negative control cleanup outcome = (%v, %q), want stopped room after strict mismatch", outcome.err, outcome.result.Reason)
		}
		errorText := error.Error()
		for _, want := range []string{`participant "target"`, "response.cancel", "input_audio_buffer.append"} {
			if !strings.Contains(errorText, want) {
				t.Fatalf("digital-silence negative control error = %q, want %q", errorText, want)
			}
		}
		writes := scenario.harness.participant("target").outboundSnapshot()
		if len(writes) != 2 || writes[0].Type != "session.update" || writes[1].Type != "input_audio_buffer.append" {
			t.Fatalf("digital-silence target successful wire = %+v, want no response.cancel", writes)
		}
		if err := scenario.harness.participant("target").dialer.Err(); err == nil {
			t.Fatal("digital-silence target replay did not retain strict mismatch")
		}
	})
}

func assertRoomSpeechOverlapAppend(t *testing.T, participant *roomRealtimeReplayParticipant, wantPCM []byte) {
	t.Helper()
	message := participant.awaitOutbound(t, "input_audio_buffer.append")
	assertRoomSpeechOverlapWireAppendPayload(t, message, wantPCM)
}

func assertRoomSpeechOverlapWireAppendPayload(t *testing.T, message roomRealtimeReplayWireMessage, wantPCM []byte) {
	t.Helper()
	want := roomRealtimeReplayJSON(t, map[string]any{
		"type":  "input_audio_buffer.append",
		"audio": base64.StdEncoding.EncodeToString(wantPCM),
	})
	if message.Type != "input_audio_buffer.append" || !bytes.Equal(message.Payload, want) {
		t.Fatalf("raw input_audio_buffer.append = %s, want exact PCM %v", message.Payload, wantPCM)
	}
}

func awaitRoomSpeechOverlapAudio(t *testing.T, audio <-chan []byte, want []byte) {
	t.Helper()
	timer := time.NewTimer(roomRealtimeReplayTestTimeout)
	defer timer.Stop()
	select {
	case got := <-audio:
		if !bytes.Equal(got, want) {
			t.Fatalf("room overlap output audio = %v, want %v", got, want)
		}
	case <-timer.C:
		t.Fatalf("room overlap did not emit expected output audio %v", want)
	}
}

func awaitRoomSpeechOverlapFanout(t *testing.T, fanouts <-chan roomSpeechOverlapFanout, sourceID, targetID string, want []byte) {
	t.Helper()
	timer := time.NewTimer(roomRealtimeReplayTestTimeout)
	defer timer.Stop()
	for {
		select {
		case fanout := <-fanouts:
			if fanout.sourceID != sourceID || fanout.targetID != targetID {
				continue
			}
			if !bytes.Equal(fanout.pcm, want) {
				t.Fatalf("room overlap fanout %s -> %s = %v, want %v", sourceID, targetID, fanout.pcm, want)
			}
			return
		case <-timer.C:
			t.Fatalf("room overlap did not fan out %s -> %s", sourceID, targetID)
		}
	}
}

func awaitRoomSpeechOverlapRun(t *testing.T, scenario *roomSpeechOverlapScenario) roomTestRunOutcome {
	t.Helper()
	timer := time.NewTimer(roomRealtimeReplayTestTimeout)
	defer timer.Stop()
	select {
	case outcome := <-scenario.runDone:
		return outcome
	case <-timer.C:
		t.Fatalf("room overlap run did not terminate: %v; target replay_err=%v target_outbound=%+v speaker_outbound=%+v", scenario.ctx.Err(), scenario.harness.participant("target").dialer.Err(), scenario.harness.participant("target").outboundSnapshot(), scenario.harness.participant("speaker").outboundSnapshot())
		return roomTestRunOutcome{}
	}
}

func awaitRoomSpeechOverlapTargetEnd(t *testing.T, ends <-chan struct{}) {
	t.Helper()
	timer := time.NewTimer(roomRealtimeReplayTestTimeout)
	defer timer.Stop()
	select {
	case <-ends:
	case <-timer.C:
		t.Fatal("room overlap did not observe the cancelled response boundary")
	}
}

func TestRoomRealtimeReplayDialer_ReportsParticipantAndScriptPosition(t *testing.T) {
	tests := []struct {
		name       string
		records    []gwtesting.CapturedSessionEvent
		drive      func(transport.Conn) error
		wantStep   string
		wantActual string
	}{
		{
			name: "out of order outbound",
			records: []gwtesting.CapturedSessionEvent{
				roomRealtimeReplayEvent(1, gwtesting.DirectionServerToClient, "session.created", []byte(`{"type":"session.created"}`)),
			},
			drive: func(conn transport.Conn) error {
				return conn.WriteMessage(1, []byte(`{"type":"response.create"}`))
			},
			wantStep:   "sequence 1",
			wantActual: "response.create",
		},
		{
			name: "missing outbound on close",
			records: []gwtesting.CapturedSessionEvent{
				roomRealtimeReplayEvent(1, gwtesting.DirectionServerToClient, "session.created", []byte(`{"type":"session.created"}`)),
				roomRealtimeReplayEvent(2, gwtesting.DirectionClientToServer, "response.create", []byte(`{"type":"response.create"}`)),
			},
			drive: func(conn transport.Conn) error {
				if _, _, err := conn.ReadMessage(); err != nil {
					return err
				}
				return conn.Close()
			},
			wantStep:   "sequence 2",
			wantActual: "connection close",
		},
		{
			name: "duplicate outbound",
			records: []gwtesting.CapturedSessionEvent{
				roomRealtimeReplayEvent(1, gwtesting.DirectionClientToServer, "response.create", []byte(`{"type":"response.create"}`)),
			},
			drive: func(conn transport.Conn) error {
				if err := conn.WriteMessage(1, []byte(`{"type":"response.create"}`)); err != nil {
					return err
				}
				return conn.WriteMessage(1, []byte(`{"type":"response.create"}`))
			},
			wantStep:   "script position 2",
			wantActual: "response.create",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capture := roomRealtimeReplayCapture(openAIRealtimeDefaultModel, tt.records...)
			dialer, err := newRoomRealtimeReplayDialer("subject", capture)
			if err != nil {
				t.Fatalf("new strict dialer: %v", err)
			}
			conn, err := dialer.Dial("wss://room-replay.invalid", nil)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			err = tt.drive(conn)
			if err == nil {
				t.Fatal("expected strict replay divergence")
			}
			for _, want := range []string{`participant "subject"`, tt.wantStep, "expected", "actual", tt.wantActual} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("divergence = %q, want substring %q", err, want)
				}
			}
			if got := dialer.Err(); got == nil {
				t.Fatal("dialer did not retain strict replay divergence")
			}
		})
	}
}

func sameRoomReplayStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

type roomRealtimeReplayCadence struct {
	ticks   chan time.Time
	stopped chan struct{}
	once    sync.Once
}

func newRoomRealtimeReplayCadence() *roomRealtimeReplayCadence {
	return &roomRealtimeReplayCadence{
		ticks:   make(chan time.Time, 4),
		stopped: make(chan struct{}),
	}
}

func (c *roomRealtimeReplayCadence) C() <-chan time.Time { return c.ticks }

func (c *roomRealtimeReplayCadence) Stop() {
	c.once.Do(func() { close(c.stopped) })
}

func (c *roomRealtimeReplayCadence) Advance() {
	select {
	case c.ticks <- time.Time{}:
	case <-c.stopped:
	}
}
