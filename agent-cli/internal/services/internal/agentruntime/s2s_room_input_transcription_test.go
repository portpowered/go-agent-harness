package agentruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const roomInputTranscriptionTestTimeout = 2 * time.Second

// TestRunRoomWithResult_EnablesInputTranscriptionOncePerParticipant exercises
// the room's normal live composition rather than injecting SessionInferencers.
// Each fake provider connection observes the generated handshake and emits a
// participant-specific transcript so the test proves both request isolation
// and role attribution at the room stream seam.
func TestRunRoomWithResult_EnablesInputTranscriptionOncePerParticipant(t *testing.T) {
	const (
		model          = openAIRealtimeDefaultModel
		alphaID        = "alpha"
		betaID         = "beta"
		alphaSecret    = "room-alpha-key"
		betaSecret     = "room-beta-key"
		alphaUser      = "alpha customer words"
		betaUser       = "beta customer words"
		alphaAssistant = "alpha assistant words"
		betaAssistant  = "beta assistant words"
	)

	servers := map[string]*roomInputTranscriptionServer{
		alphaID: newRoomInputTranscriptionServer(alphaID, model, alphaUser, alphaAssistant),
		betaID:  newRoomInputTranscriptionServer(betaID, model, betaUser, betaAssistant),
	}
	configDir := t.TempDir()
	writeSessionConfigFile(t, configDir, "model:\n  provider: openai\n")

	manifest := room.Manifest{
		SchemaVersion: room.SchemaVersion,
		Room:          room.Room{MaxTurns: 1},
		Participants: []room.Participant{
			{ID: alphaID, SystemPrompt: "alpha system", Provider: config.ProviderOpenAI, Model: model, APIKeyEnv: "ROOM_ALPHA_KEY", Tools: []string{}},
			{ID: betaID, SystemPrompt: "beta system", Provider: config.ProviderOpenAI, Model: model, APIKeyEnv: "ROOM_BETA_KEY", Tools: []string{}},
		},
	}

	observations := make(chan roomTranscriptObservation, 16)
	credentials := map[string]string{
		"ROOM_ALPHA_KEY": alphaSecret,
		"ROOM_BETA_KEY":  betaSecret,
	}
	cadenceReady := make(chan *roomRealtimeReplayCadence, len(servers))
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
	opened := make(chan string, len(servers))
	opts := RoomRunOptions{
		Manifest:  manifest,
		ConfigDir: configDir, ModelCatalog: testModelCatalog(), MixerConfig: mixerConfig,
		BaseURL: "wss://room-input-transcription.invalid/v1/realtime",
		CredentialLookup: func(name string) (string, bool) {
			value, ok := credentials[name]
			return value, ok
		},
		WebSocketDialerFactory: func(participant room.Participant) transport.Dialer {
			return servers[participant.ID]
		},
		onParticipantSessionOpen: func(participantID string) {
			opened <- participantID
		},
		onParticipantStream: func(participantID string, msg messages.StreamMessage) {
			if msg.Type != messages.StreamTypeTranscriptDelta && msg.Type != messages.StreamTypeTranscriptEnd {
				return
			}
			observation := roomTranscriptObservation{participant: participantID, role: msg.Role, kind: msg.Type}
			switch value := msg.Value.(type) {
			case *messages.TranscriptDeltaValue:
				observation.text = value.Text
			case *messages.TranscriptEndValue:
				observation.text = value.FullText
			default:
				observation.text = fmt.Sprintf("unexpected transcript value %T", msg.Value)
			}
			observations <- observation
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), roomInputTranscriptionTestTimeout)
	defer cancel()
	runDone := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(ctx, io.Discard, opts)
		runDone <- roomTestRunOutcome{result: result, err: err}
	}()

	cadences := make([]*roomRealtimeReplayCadence, 0, len(servers))
	for range servers {
		select {
		case cadence := <-cadenceReady:
			cadences = append(cadences, cadence)
		case <-ctx.Done():
			t.Fatalf("room mixer cadence was not created: %v", ctx.Err())
		}
	}
	openedParticipants := make(map[string]struct{}, len(servers))
	for range servers {
		select {
		case participantID := <-opened:
			if _, duplicate := openedParticipants[participantID]; duplicate {
				t.Fatalf("participant %q observed duplicate session.open", participantID)
			}
			if _, known := servers[participantID]; !known {
				t.Fatalf("unknown participant observed session.open: %q", participantID)
			}
			openedParticipants[participantID] = struct{}{}
		case <-ctx.Done():
			t.Fatalf("room sessions did not observe session.open: %v", ctx.Err())
		}
	}
	// The fake provider withholds its response completion until it receives
	// media. Advancing every cadence only after both SESSION.OPEN observations
	// forces the real room mixer to produce the post-handshake append that CI
	// observed, without a wall-clock sleep or a production synchronization seam.
	for _, cadence := range cadences {
		cadence.Advance()
	}
	for participantID, server := range servers {
		select {
		case <-server.appendSeen:
		case <-ctx.Done():
			t.Fatalf("participant %q did not receive deterministic post-handshake media: %v", participantID, ctx.Err())
		}
	}

	var outcome roomTestRunOutcome
	select {
	case outcome = <-runDone:
	case <-ctx.Done():
		t.Fatalf("RunRoomWithResult: %v", ctx.Err())
	}
	if outcome.err != nil {
		t.Fatalf("RunRoomWithResult: %v", outcome.err)
	}
	result := outcome.result
	if result.Reason != RoomTerminationMaxTurnsReached {
		t.Fatalf("room termination reason = %q, want %q", result.Reason, RoomTerminationMaxTurnsReached)
	}
	if len(result.Participants) != 2 {
		t.Fatalf("participant results = %d, want two", len(result.Participants))
	}

	wantTranscripts := map[string]roomTranscriptExpectation{
		alphaID + ":delta":           {role: messages.RoleUser, kind: messages.StreamTypeTranscriptDelta, text: alphaUser},
		alphaID + ":end":             {role: messages.RoleUser, kind: messages.StreamTypeTranscriptEnd, text: alphaUser},
		alphaID + ":assistant-delta": {role: messages.RoleAssistant, kind: messages.StreamTypeTranscriptDelta, text: alphaAssistant},
		alphaID + ":assistant-end":   {role: messages.RoleAssistant, kind: messages.StreamTypeTranscriptEnd, text: alphaAssistant},
		betaID + ":delta":            {role: messages.RoleUser, kind: messages.StreamTypeTranscriptDelta, text: betaUser},
		betaID + ":end":              {role: messages.RoleUser, kind: messages.StreamTypeTranscriptEnd, text: betaUser},
		betaID + ":assistant-delta":  {role: messages.RoleAssistant, kind: messages.StreamTypeTranscriptDelta, text: betaAssistant},
		betaID + ":assistant-end":    {role: messages.RoleAssistant, kind: messages.StreamTypeTranscriptEnd, text: betaAssistant},
	}
	seen := make(map[string]struct{}, len(wantTranscripts))
	deadline := time.NewTimer(roomInputTranscriptionTestTimeout)
	defer deadline.Stop()
	for len(seen) < len(wantTranscripts) {
		select {
		case observation := <-observations:
			key := transcriptObservationKey(observation)
			want, ok := wantTranscripts[key]
			if !ok {
				t.Fatalf("unexpected transcript observation = %+v", observation)
			}
			if observation.role != want.role || observation.kind != want.kind || observation.text != want.text {
				t.Fatalf("transcript %q = %+v, want role=%q kind=%q text=%q", key, observation, want.role, want.kind, want.text)
			}
			seen[key] = struct{}{}
		case <-deadline.C:
			t.Fatalf("timed out waiting for participant transcript observations: seen=%v", seen)
		}
	}

	for participantID, server := range servers {
		if calls := server.dialCallsSnapshot(); calls != 1 {
			t.Fatalf("participant %q provider connections = %d, want exactly one", participantID, calls)
		}
		updates := server.sessionUpdatesSnapshot()
		if len(updates) != 1 {
			t.Fatalf("participant %q session.update writes = %d, want exactly one: %s", participantID, len(updates), server.writesSnapshot())
		}
		assertRoomInputTranscriptionHandshake(t, participantID, updates[0], model)
		writes := server.writeTypesSnapshot()
		if err := validateRoomInputTranscriptionWire(writes); err != nil {
			t.Fatalf("participant %q outbound event types = %v: %v", participantID, writes, err)
		}
	}
}

type roomTranscriptObservation struct {
	participant string
	role        messages.Role
	kind        messages.StreamMessageType
	text        string
}

type roomTranscriptExpectation struct {
	role messages.Role
	kind messages.StreamMessageType
	text string
}

func transcriptObservationKey(observation roomTranscriptObservation) string {
	label := ""
	if observation.role == messages.RoleAssistant {
		label = "assistant-"
	}
	suffix := "delta"
	if observation.kind == messages.StreamTypeTranscriptEnd {
		suffix = "end"
	}
	return observation.participant + ":" + label + suffix
}

func validateRoomInputTranscriptionWire(writes []string) error {
	if len(writes) == 0 {
		return fmt.Errorf("missing initial session.update")
	}
	if writes[0] != "session.update" {
		return fmt.Errorf("first outbound event is %q, want session.update", writes[0])
	}
	updates := 0
	appends := 0
	for index, writeType := range writes {
		switch writeType {
		case "session.update":
			updates++
			if index != 0 {
				return fmt.Errorf("duplicate or out-of-order session.update at wire index %d", index)
			}
		case "input_audio_buffer.append":
			appends++
		default:
			return fmt.Errorf("unexpected outbound event %q at wire index %d", writeType, index)
		}
	}
	if updates != 1 {
		return fmt.Errorf("got %d session.update events, want exactly one", updates)
	}
	if appends == 0 {
		return fmt.Errorf("missing post-handshake input_audio_buffer.append")
	}
	return nil
}

func TestRoomInputTranscriptionWireContractRejectsDuplicateOrOutOfOrderHandshake(t *testing.T) {
	tests := []struct {
		name   string
		writes []string
	}{
		{name: "missing handshake", writes: []string{"input_audio_buffer.append"}},
		{name: "duplicate handshake", writes: []string{"session.update", "session.update", "input_audio_buffer.append"}},
		{name: "out of order handshake", writes: []string{"input_audio_buffer.append", "session.update"}},
		{name: "unexpected control", writes: []string{"session.update", "response.create"}},
		{name: "missing media", writes: []string{"session.update"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateRoomInputTranscriptionWire(test.writes); err == nil {
				t.Fatalf("validateRoomInputTranscriptionWire(%v) unexpectedly passed", test.writes)
			}
		})
	}
	if err := validateRoomInputTranscriptionWire([]string{"session.update", "input_audio_buffer.append", "input_audio_buffer.append"}); err != nil {
		t.Fatalf("valid post-handshake media sequence rejected: %v", err)
	}
}

func assertRoomInputTranscriptionHandshake(t *testing.T, participantID string, payload []byte, model string) {
	t.Helper()
	var envelope struct {
		Type    string `json:"type"`
		Session struct {
			Model string `json:"model"`
			Audio struct {
				Input struct {
					Transcription *struct {
						Model string `json:"model"`
					} `json:"transcription"`
				} `json:"input"`
			} `json:"audio"`
		} `json:"session"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("participant %q decode session.update: %v", participantID, err)
	}
	if envelope.Type != "session.update" || envelope.Session.Model != model {
		t.Fatalf("participant %q handshake identity = type:%q model:%q, want session.update/%q", participantID, envelope.Type, envelope.Session.Model, model)
	}
	if envelope.Session.Audio.Input.Transcription == nil {
		t.Fatalf("participant %q handshake omitted audio.input.transcription", participantID)
	}
	if envelope.Session.Audio.Input.Transcription.Model != "gpt-live-transcribe" {
		t.Fatalf("participant %q transcription model = %q, want gpt-live-transcribe", participantID, envelope.Session.Audio.Input.Transcription.Model)
	}
}

type roomInputTranscriptionServer struct {
	participantID string
	model         string
	userText      string
	assistantText string

	mu             sync.Mutex
	dialCalls      int
	writes         []string
	sessionUpdates [][]byte
	events         chan []byte
	closed         chan struct{}
	closeOnce      sync.Once
	appendSeen     chan struct{}
	appendOnce     sync.Once
	responseOnce   sync.Once
}

func newRoomInputTranscriptionServer(participantID, model, userText, assistantText string) *roomInputTranscriptionServer {
	return &roomInputTranscriptionServer{
		participantID: participantID,
		model:         model,
		userText:      userText,
		assistantText: assistantText,
		events:        make(chan []byte, 16),
		closed:        make(chan struct{}),
		appendSeen:    make(chan struct{}),
	}
}

var _ transport.Dialer = (*roomInputTranscriptionServer)(nil)

func (s *roomInputTranscriptionServer) Dial(string, map[string]string) (transport.Conn, error) {
	s.mu.Lock()
	s.dialCalls++
	calls := s.dialCalls
	s.mu.Unlock()
	if calls != 1 {
		return nil, fmt.Errorf("participant %q received duplicate Dial", s.participantID)
	}
	return &roomInputTranscriptionConn{server: s}, nil
}

func (s *roomInputTranscriptionServer) dialCallsSnapshot() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dialCalls
}

func (s *roomInputTranscriptionServer) writesSnapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprint(s.writes)
}

func (s *roomInputTranscriptionServer) writeTypesSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.writes...)
}

func (s *roomInputTranscriptionServer) sessionUpdatesSnapshot() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	updates := make([][]byte, len(s.sessionUpdates))
	for index, update := range s.sessionUpdates {
		updates[index] = append([]byte(nil), update...)
	}
	return updates
}

func (s *roomInputTranscriptionServer) enqueue(value string) {
	select {
	case s.events <- []byte(value):
	case <-s.closed:
	}
}

type roomInputTranscriptionConn struct {
	server *roomInputTranscriptionServer
}

var _ transport.Conn = (*roomInputTranscriptionConn)(nil)

func (c *roomInputTranscriptionConn) ReadMessage() (int, []byte, error) {
	select {
	case payload := <-c.server.events:
		return 1, payload, nil
	case <-c.server.closed:
		return 0, nil, io.EOF
	}
}

func (c *roomInputTranscriptionConn) WriteMessage(_ int, payload []byte) error {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}
	c.server.mu.Lock()
	c.server.writes = append(c.server.writes, envelope.Type)
	if envelope.Type == "session.update" {
		c.server.sessionUpdates = append(c.server.sessionUpdates, append([]byte(nil), payload...))
	}
	c.server.mu.Unlock()
	if envelope.Type == "input_audio_buffer.append" {
		c.server.appendOnce.Do(func() { close(c.server.appendSeen) })
		c.server.responseOnce.Do(func() {
			c.server.enqueue(`{"type":"response.created","response":{"id":"response-room"}}`)
			c.server.enqueue(fmt.Sprintf(`{"type":"response.output_audio_transcript.delta","delta":%q}`, c.server.assistantText))
			c.server.enqueue(fmt.Sprintf(`{"type":"response.output_audio_transcript.done","transcript":%q}`, c.server.assistantText))
			c.server.enqueue(`{"type":"response.done","response":{"id":"response-room","status":"completed"}}`)
		})
		return nil
	}
	if envelope.Type != "session.update" {
		return nil
	}

	c.server.enqueue(fmt.Sprintf(`{"type":"session.created","session":{"id":"room-%s","model":%q}}`, c.server.participantID, c.server.model))
	c.server.enqueue(fmt.Sprintf(`{"type":"conversation.item.input_audio_transcription.delta","delta":%q}`, c.server.userText))
	c.server.enqueue(fmt.Sprintf(`{"type":"conversation.item.input_audio_transcription.completed","transcript":%q}`, c.server.userText))
	return nil
}

func (c *roomInputTranscriptionConn) Close() error {
	c.server.closeOnce.Do(func() { close(c.server.closed) })
	return nil
}
