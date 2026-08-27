package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const (
	deterministicBargeInTurns       = 4
	deterministicBargeInCancels     = 2
	deterministicBargeInFrameBytes  = audio.FrameSize * 2
	deterministicBargeInRunTimeout  = 5 * time.Second
	deterministicBargeInResponseOne = "resp-s2s-v2-1"
)

// deterministicBargeInTrace is fed by the generated CLI's public stream
// observer. Its channels are the only timing gates used by the raw stdin
// source below; the test never sleeps to manufacture a collision.
type deterministicBargeInTrace struct {
	mu sync.Mutex

	responseOrdinal int
	responseOpen    bool
	created         chan int
	audio           chan int
	done            chan int
	events          []deterministicBargeInStreamEvent
}

type deterministicBargeInStreamEvent struct {
	Ordinal int
	Type    messages.StreamMessageType
	Bytes   int
}

func newDeterministicBargeInTrace() *deterministicBargeInTrace {
	return &deterministicBargeInTrace{
		created: make(chan int, deterministicBargeInTurns),
		audio:   make(chan int, deterministicBargeInTurns),
		done:    make(chan int, deterministicBargeInTurns),
	}
}

func (t *deterministicBargeInTrace) observe(msg messages.StreamMessage) {
	if t == nil {
		return
	}
	var created, audio, done int
	t.mu.Lock()
	if msg.Type == messages.StreamTypeMessageStart {
		t.responseOrdinal++
		t.responseOpen = true
		created = t.responseOrdinal
	}
	ordinal := t.responseOrdinal
	bytes := 0
	if value, ok := msg.Value.(*messages.AudioDeltaValue); ok && value != nil {
		bytes = len(value.Content)
		if bytes > 0 && t.responseOpen {
			audio = ordinal
		}
	}
	if msg.Type == messages.StreamTypeMessageEnd && t.responseOpen {
		done = ordinal
		t.responseOpen = false
	}
	t.events = append(t.events, deterministicBargeInStreamEvent{
		Ordinal: ordinal,
		Type:    msg.Type,
		Bytes:   bytes,
	})
	t.mu.Unlock()

	for channel, value := range map[chan int]int{
		t.created: created,
		t.audio:   audio,
		t.done:    done,
	} {
		if value == 0 {
			continue
		}
		select {
		case channel <- value:
		default:
		}
	}
}

func (t *deterministicBargeInTrace) waitFor(channel <-chan int, minimum int, ctx context.Context) error {
	for {
		select {
		case ordinal := <-channel:
			if ordinal >= minimum {
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (t *deterministicBargeInTrace) waitForAudio(ctx context.Context, minimum int) error {
	return t.waitFor(t.audio, minimum, ctx)
}

func (t *deterministicBargeInTrace) waitForCreated(ctx context.Context, minimum int) error {
	return t.waitFor(t.created, minimum, ctx)
}

func (t *deterministicBargeInTrace) waitForDone(ctx context.Context, minimum int) error {
	return t.waitFor(t.done, minimum, ctx)
}

func (t *deterministicBargeInTrace) streamSnapshot() []deterministicBargeInStreamEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]deterministicBargeInStreamEvent(nil), t.events...)
}

// deterministicBargeInAudioReader feeds four one-frame utterances through the
// shipped --audio-in - path. ErrEndOfTurn is the same raw-stream boundary the
// production FileSource recognizes, so each frame sequence becomes one input
// commit and response.create. Gates are ordered as follows:
//
//   - turn 2 waits for response 1's first output audio, then cancels response 1;
//   - turn 3 waits for response 2 creation before its first output, then
//     cancels response 2;
//   - turn 4 waits for response 3 completion, proving the no-cancel race;
//   - response 4 is the final usable continuation.
type deterministicBargeInAudioReader struct {
	mu       sync.Mutex
	segments []deterministicBargeInAudioSegment
	segment  int
	frame    int
	gateUsed bool
	marker   bool
}

type deterministicBargeInAudioSegment struct {
	frames    [][]byte
	gate      func(context.Context) error
	endOfTurn bool
}

func newDeterministicBargeInAudioReader(trace *deterministicBargeInTrace) *deterministicBargeInAudioReader {
	return &deterministicBargeInAudioReader{
		segments: []deterministicBargeInAudioSegment{
			{frames: [][]byte{deterministicBargeInFrame(1)}, endOfTurn: true},
			{frames: [][]byte{deterministicBargeInFrame(2)}, gate: func(ctx context.Context) error {
				return trace.waitForAudio(ctx, 1)
			}, endOfTurn: true},
			{frames: [][]byte{deterministicBargeInFrame(3)}, gate: func(ctx context.Context) error {
				return trace.waitForCreated(ctx, 2)
			}, endOfTurn: true},
			{frames: [][]byte{deterministicBargeInFrame(4)}, gate: func(ctx context.Context) error {
				return trace.waitForDone(ctx, 3)
			}},
		},
	}
}

func deterministicBargeInFrame(seed byte) []byte {
	frame := make([]byte, deterministicBargeInFrameBytes)
	for index := range frame {
		frame[index] = seed + byte(index%31)
	}
	return frame
}

func (r *deterministicBargeInAudioReader) Read(p []byte) (int, error) {
	return r.ReadContext(context.Background(), p)
}

func (r *deterministicBargeInAudioReader) ReadContext(ctx context.Context, p []byte) (int, error) {
	if len(p) != deterministicBargeInFrameBytes {
		return 0, fmt.Errorf("deterministic barge-in reader received %d bytes, want %d", len(p), deterministicBargeInFrameBytes)
	}
	for {
		r.mu.Lock()
		if r.segment >= len(r.segments) {
			r.mu.Unlock()
			return 0, io.EOF
		}
		segment := r.segments[r.segment]
		if !r.gateUsed {
			r.gateUsed = true
			gate := segment.gate
			r.mu.Unlock()
			if gate != nil {
				if err := gate(ctx); err != nil {
					return 0, err
				}
			}
			continue
		}
		if r.frame < len(segment.frames) {
			frame := segment.frames[r.frame]
			r.frame++
			r.mu.Unlock()
			copy(p, frame)
			return len(p), nil
		}
		if !segment.endOfTurn {
			r.segment = len(r.segments)
			r.mu.Unlock()
			return 0, io.EOF
		}
		if !r.marker {
			r.marker = true
			r.mu.Unlock()
			return 0, audio.ErrEndOfTurn
		}
		r.segment++
		r.frame = 0
		r.gateUsed = false
		r.marker = false
		r.mu.Unlock()
	}
}

func (*deterministicBargeInAudioReader) Close() error { return nil }

type deterministicBargeInServer struct {
	mu sync.Mutex

	events       chan []byte
	eventBatches chan []string
	closed       chan struct{}
	closeOnce    sync.Once
	dialOnce     sync.Once
	dialCount    int

	turnHasAudio bool
	commits      int
	responses    []*deterministicBargeInServerResponse
	active       *deterministicBargeInServerResponse
	protocolErrs []string
}

type deterministicBargeInServerResponse struct {
	ID           string
	CancelCount  int
	TerminalSent bool
}

func newDeterministicBargeInServer() *deterministicBargeInServer {
	return &deterministicBargeInServer{
		events:       make(chan []byte, 128),
		eventBatches: make(chan []string, 32),
		closed:       make(chan struct{}),
	}
}

func (s *deterministicBargeInServer) Dial(_ string, _ map[string]string) (transport.Conn, error) {
	s.mu.Lock()
	s.dialCount++
	s.mu.Unlock()
	s.dialOnce.Do(func() { go s.serve() })
	return &deterministicBargeInConn{server: s}, nil
}

func (s *deterministicBargeInServer) serve() {
	s.sendEvent(`{"type":"session.created","session":{"id":"sess-s2s-v2","model":"gpt-realtime"}}`)
	s.sendEvent(`{"type":"session.updated","session":{"id":"sess-s2s-v2","model":"gpt-realtime"}}`)
	for {
		select {
		case batch := <-s.eventBatches:
			for _, event := range batch {
				s.sendEvent(event)
			}
		case <-s.closed:
			return
		}
	}
}

func (s *deterministicBargeInServer) sendEvent(payload string) {
	select {
	case s.events <- []byte(payload):
	case <-s.closed:
	}
}

func (s *deterministicBargeInServer) shutdown() {
	s.closeOnce.Do(func() { close(s.closed) })
}

func (s *deterministicBargeInServer) snapshot() (int, []*deterministicBargeInServerResponse, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	responses := make([]*deterministicBargeInServerResponse, len(s.responses))
	for index, response := range s.responses {
		copyOf := *response
		responses[index] = &copyOf
	}
	return s.dialCount, responses, append([]string(nil), s.protocolErrs...)
}

type deterministicBargeInConn struct {
	server *deterministicBargeInServer
}

func (c *deterministicBargeInConn) ReadMessage() (int, []byte, error) {
	select {
	case payload := <-c.server.events:
		return 1, payload, nil
	case <-c.server.closed:
		return 0, nil, errors.New("deterministic barge-in connection closed")
	}
}

func (c *deterministicBargeInConn) WriteMessage(_ int, payload []byte) error {
	var envelope struct {
		Type  string `json:"type"`
		Audio string `json:"audio"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}

	var events []string
	s := c.server
	s.mu.Lock()
	switch envelope.Type {
	case "session.update":
		// The deterministic server accepts either current Realtime session
		// configuration shape; the test's collision order is independent of it.
	case "input_audio_buffer.append":
		if envelope.Audio == "" {
			s.protocolErrs = append(s.protocolErrs, "empty input_audio_buffer.append")
		} else if _, err := base64.StdEncoding.DecodeString(envelope.Audio); err != nil {
			s.protocolErrs = append(s.protocolErrs, fmt.Sprintf("invalid input audio: %v", err))
		}
		if !s.turnHasAudio {
			s.turnHasAudio = true
			events = append(events, `{"type":"input_audio_buffer.speech_started"}`)
		}
	case "input_audio_buffer.commit":
		if !s.turnHasAudio {
			s.protocolErrs = append(s.protocolErrs, "input commit without non-empty audio")
		}
		s.commits++
		s.turnHasAudio = false
		events = append(events,
			`{"type":"input_audio_buffer.speech_stopped"}`,
			`{"type":"input_audio_buffer.committed"}`,
			fmt.Sprintf(`{"type":"conversation.item.created","item":{"id":"item-s2s-v2-%d","role":"user"}}`, s.commits),
		)
	case "response.create":
		if s.active != nil {
			s.protocolErrs = append(s.protocolErrs, fmt.Sprintf("response.create while %q is active", s.active.ID))
		}
		responseOrdinal := len(s.responses) + 1
		response := &deterministicBargeInServerResponse{ID: fmt.Sprintf("resp-s2s-v2-%d", responseOrdinal)}
		s.responses = append(s.responses, response)
		s.active = response
		events = append(events, fmt.Sprintf(`{"type":"response.created","response":{"id":%q}}`, response.ID))
		switch responseOrdinal {
		case 1:
			events = append(events, deterministicBargeInAudioDelta(response.ID, 1))
		case 2:
			// Hold the first output delta until the client either cancels or
			// proves the provider response was already terminal.
		case 3, 4:
			events = append(events,
				deterministicBargeInAudioDelta(response.ID, byte(responseOrdinal)),
				fmt.Sprintf(`{"type":"response.output_audio.done","response_id":%q}`, response.ID),
				fmt.Sprintf(`{"type":"response.done","response":{"id":%q,"status":"completed"}}`, response.ID),
			)
			response.TerminalSent = true
			s.active = nil
		}
	case "response.cancel":
		if s.active == nil {
			s.protocolErrs = append(s.protocolErrs, "response.cancel without active response")
			break
		}
		response := s.active
		response.CancelCount++
		if response.CancelCount > 1 {
			s.protocolErrs = append(s.protocolErrs, fmt.Sprintf("response %q was cancelled more than once", response.ID))
		}
		response.TerminalSent = true
		s.active = nil
		events = append(events, fmt.Sprintf(`{"type":"response.done","response":{"id":%q,"status":"cancelled"}}`, response.ID))
	}
	s.mu.Unlock()

	if len(events) > 0 {
		select {
		case s.eventBatches <- events:
		case <-s.closed:
		}
	}
	return nil
}

func deterministicBargeInAudioDelta(responseID string, seed byte) string {
	audio := base64.StdEncoding.EncodeToString([]byte{seed, 0, seed + 20, 0})
	return fmt.Sprintf(`{"type":"response.output_audio.delta","response_id":%q,"delta":%q,"format":"pcm16"}`, responseID, audio)
}

func (c *deterministicBargeInConn) Close() error {
	c.server.shutdown()
	return nil
}

// runDeterministicBargeInCLI drives the normal generated command graph with a
// provider-shaped transport. The raw capture is kept in memory so the
// identity-aware validator below examines exactly the wire events the CLI
// consumed and emitted.
func runDeterministicBargeInCLI(t *testing.T) (gwtesting.SessionCapture, *deterministicBargeInTrace, *deterministicBargeInServer, error) {
	t.Helper()
	trace := newDeterministicBargeInTrace()
	server := newDeterministicBargeInServer()
	t.Cleanup(server.shutdown)
	recorder := gwtesting.NewRecordingWebSocketDialer(server, "openai", "gpt-realtime")
	sessionInferencer, err := services.NewOpenAIRealtimeSessionInferencerWithOptions(
		config.OpenAIConfig{APIKey: "test-key", Model: "gpt-realtime", BaseURL: "wss://hermetic.openai.test/v1/realtime"},
		oaiprovider.WithWebSocketDialer(recorder),
		oaiprovider.WithClientOwnedAudioTurnBoundaries(),
	)
	if err != nil {
		t.Fatalf("create deterministic OpenAI session inferencer: %v", err)
	}
	agentCLI, err := wire.InitializeMockAgentCLIWithSessionInferencer(
		&mockToolExecutor{},
		&mockInferencer{response: "stateless inferencer should not be called"},
		sessionInferencer,
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}
	agentCLI.SetSessionStreamObserver(trace.observe)
	root := agentCLI.Generate()
	root.SetIn(newDeterministicBargeInAudioReader(trace))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{
		"--config-dir", filepath.Join(t.TempDir(), "config"),
		"session",
		"--record-dir", filepath.Join(t.TempDir(), "recording"),
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--api-key", "test-key",
		"--system-prompt", "none",
		"--audio-in", "-",
		"--max-duration", deterministicBargeInRunTimeout.String(),
	})
	ctx, cancel := context.WithTimeout(context.Background(), deterministicBargeInRunTimeout)
	defer cancel()
	runErr := root.ExecuteContext(ctx)
	return recorder.Capture(), trace, server, runErr
}

type deterministicBargeInResponse struct {
	ID            string
	OwnerTurn     int
	CreatedSeq    int
	FirstAudioSeq int
	AudioDeltas   int
	CancelSeq     int
	DoneSeq       int
	DoneStatus    string
	PostCancel    int
	PostTerminal  int
}

type deterministicBargeInLedger struct {
	Responses      []*deterministicBargeInResponse
	ByID           map[string]*deterministicBargeInResponse
	InputAppends   int
	InputBytes     int
	InputCommits   int
	SpeechStarts   int
	SpeechStops    int
	UserItems      int
	ProtocolErrors []string
	Violations     []string
}

func (l *deterministicBargeInLedger) evidence() string {
	responses := make([]string, 0, len(l.Responses))
	for _, response := range l.Responses {
		responses = append(responses, fmt.Sprintf("%s{turn=%d,audio=%d,cancel=%d,done=%d:%s,post_cancel=%d,post_terminal=%d}",
			response.ID,
			response.OwnerTurn,
			response.AudioDeltas,
			response.CancelSeq,
			response.DoneSeq,
			response.DoneStatus,
			response.PostCancel,
			response.PostTerminal,
		))
	}
	return fmt.Sprintf("responses=[%s] appends=%d input_bytes=%d commits=%d speech_started=%d speech_stopped=%d user_items=%d protocol_errors=%v violations=%v",
		strings.Join(responses, ";"),
		l.InputAppends,
		l.InputBytes,
		l.InputCommits,
		l.SpeechStarts,
		l.SpeechStops,
		l.UserItems,
		l.ProtocolErrors,
		l.Violations,
	)
}

func validateDeterministicBargeInCapture(capture gwtesting.SessionCapture, clean bool) (deterministicBargeInLedger, error) {
	ledger := deterministicBargeInLedger{ByID: make(map[string]*deterministicBargeInResponse)}
	if capture.Version != gwtesting.SessionCaptureVersion {
		ledger.Violations = append(ledger.Violations, fmt.Sprintf("capture version = %d, want %d", capture.Version, gwtesting.SessionCaptureVersion))
	}
	if capture.Provider.Name != "openai" || capture.Provider.Model != "gpt-realtime" {
		ledger.Violations = append(ledger.Violations, fmt.Sprintf("capture provider/model = %q/%q, want openai/gpt-realtime", capture.Provider.Name, capture.Provider.Model))
	}
	lastSeq := 0
	appendsSinceCommit := 0
	for index, record := range capture.Records {
		seq := record.Sequence
		if seq <= lastSeq {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("record %d sequence %d is not after %d", index, seq, lastSeq))
		}
		lastSeq = seq
		if record.PayloadType != gwtesting.SessionPayloadTypeWebSocketMessage {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("record %d payload type = %q, want websocket_message", seq, record.PayloadType))
		}
		payload := record.Payload
		if len(payload) == 0 {
			payload = record.Data
		}
		switch record.Type {
		case "input_audio_buffer.append":
			requireDeterministicDirection(&ledger, record, gwtesting.DirectionClientToServer)
			encoded := deterministicJSONField(payload, "audio")
			if encoded == "" {
				ledger.Violations = append(ledger.Violations, fmt.Sprintf("input append at sequence %d has empty audio", seq))
				continue
			}
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil || len(decoded) == 0 {
				ledger.Violations = append(ledger.Violations, fmt.Sprintf("input append at sequence %d is not non-empty base64 audio", seq))
				continue
			}
			ledger.InputAppends++
			ledger.InputBytes += len(decoded)
			appendsSinceCommit++
		case "input_audio_buffer.speech_started":
			requireDeterministicDirection(&ledger, record, gwtesting.DirectionServerToClient)
			ledger.SpeechStarts++
		case "input_audio_buffer.speech_stopped":
			requireDeterministicDirection(&ledger, record, gwtesting.DirectionServerToClient)
			ledger.SpeechStops++
		case "input_audio_buffer.commit":
			requireDeterministicDirection(&ledger, record, gwtesting.DirectionClientToServer)
			ledger.InputCommits++
			if appendsSinceCommit == 0 {
				ledger.Violations = append(ledger.Violations, fmt.Sprintf("input commit at sequence %d had no input append", seq))
			}
			appendsSinceCommit = 0
		case "conversation.item.created":
			requireDeterministicDirection(&ledger, record, gwtesting.DirectionServerToClient)
			if deterministicJSONField(payload, "item.role") != "user" {
				continue
			}
			if id := deterministicJSONField(payload, "item.id"); id == "" {
				ledger.Violations = append(ledger.Violations, fmt.Sprintf("user item at sequence %d has no item identity", seq))
			} else {
				ledger.UserItems++
			}
		case "response.created":
			requireDeterministicDirection(&ledger, record, gwtesting.DirectionServerToClient)
			id := deterministicJSONField(payload, "response.id", "response_id")
			if id == "" {
				ledger.Violations = append(ledger.Violations, fmt.Sprintf("response.created at sequence %d has no response identity", seq))
				continue
			}
			if _, duplicate := ledger.ByID[id]; duplicate {
				ledger.Violations = append(ledger.Violations, fmt.Sprintf("response %q was created more than once", id))
				continue
			}
			if len(ledger.Responses) > 0 {
				previous := ledger.Responses[len(ledger.Responses)-1]
				if previous.DoneSeq == 0 {
					ledger.Violations = append(ledger.Violations, fmt.Sprintf("response %q was created while response %q remained unresolved", id, previous.ID))
				}
			}
			response := &deterministicBargeInResponse{
				ID:         id,
				OwnerTurn:  ledger.InputCommits,
				CreatedSeq: seq,
			}
			if response.OwnerTurn == 0 {
				ledger.Violations = append(ledger.Violations, fmt.Sprintf("response %q was created before an input commit", id))
			}
			ledger.Responses = append(ledger.Responses, response)
			ledger.ByID[id] = response
		case "response.output_audio.delta", "response.audio.delta":
			requireDeterministicDirection(&ledger, record, gwtesting.DirectionServerToClient)
			response := ledger.responseForEvent(payload, seq)
			if response == nil {
				continue
			}
			response.AudioDeltas++
			if response.FirstAudioSeq == 0 {
				response.FirstAudioSeq = seq
			}
			if response.CancelSeq > 0 {
				response.PostCancel++
			}
			if response.DoneSeq > 0 {
				response.PostTerminal++
			}
		case "response.cancel":
			requireDeterministicDirection(&ledger, record, gwtesting.DirectionClientToServer)
			var active *deterministicBargeInResponse
			for _, response := range ledger.Responses {
				if response.DoneSeq == 0 {
					active = response
				}
			}
			if active == nil {
				ledger.Violations = append(ledger.Violations, fmt.Sprintf("response.cancel at sequence %d had no live response identity", seq))
				continue
			}
			if active.CancelSeq > 0 {
				ledger.Violations = append(ledger.Violations, fmt.Sprintf("response %q received duplicate response.cancel at sequence %d", active.ID, seq))
				continue
			}
			active.CancelSeq = seq
		case "response.done":
			requireDeterministicDirection(&ledger, record, gwtesting.DirectionServerToClient)
			id := deterministicJSONField(payload, "response.id", "response_id")
			response := ledger.ByID[id]
			if response == nil {
				ledger.Violations = append(ledger.Violations, fmt.Sprintf("response.done at sequence %d references unknown response %q", seq, id))
				continue
			}
			if response.DoneSeq > 0 {
				ledger.Violations = append(ledger.Violations, fmt.Sprintf("response %q received duplicate response.done at sequence %d", id, seq))
				continue
			}
			response.DoneSeq = seq
			response.DoneStatus = deterministicJSONField(payload, "response.status", "status")
			if response.DoneStatus == "" {
				ledger.Violations = append(ledger.Violations, fmt.Sprintf("response %q terminal event has no status", id))
			}
			if response.CancelSeq > 0 && response.DoneStatus != "cancelled" {
				ledger.Violations = append(ledger.Violations, fmt.Sprintf("response %q was cancelled but completed with status %q", id, response.DoneStatus))
			}
			if response.CancelSeq == 0 && response.DoneStatus == "cancelled" {
				ledger.Violations = append(ledger.Violations, fmt.Sprintf("response %q reported cancelled without a preceding response.cancel", id))
			}
		case "response.output_audio.done", "response.audio.done":
			requireDeterministicDirection(&ledger, record, gwtesting.DirectionServerToClient)
		}
	}

	if ledger.InputAppends != deterministicBargeInTurns {
		ledger.Violations = append(ledger.Violations, fmt.Sprintf("input appends: expected %d, actual %d", deterministicBargeInTurns, ledger.InputAppends))
	}
	if ledger.InputCommits != deterministicBargeInTurns {
		ledger.Violations = append(ledger.Violations, fmt.Sprintf("input commits: expected %d, actual %d", deterministicBargeInTurns, ledger.InputCommits))
	}
	if ledger.SpeechStarts != deterministicBargeInTurns || ledger.SpeechStops != deterministicBargeInTurns {
		ledger.Violations = append(ledger.Violations, fmt.Sprintf("speech boundaries: expected %d/%d, actual %d/%d", deterministicBargeInTurns, deterministicBargeInTurns, ledger.SpeechStarts, ledger.SpeechStops))
	}
	if ledger.UserItems != deterministicBargeInTurns {
		ledger.Violations = append(ledger.Violations, fmt.Sprintf("user items: expected %d, actual %d", deterministicBargeInTurns, ledger.UserItems))
	}
	if len(ledger.Responses) != deterministicBargeInTurns {
		ledger.Violations = append(ledger.Violations, fmt.Sprintf("responses: expected %d, actual %d", deterministicBargeInTurns, len(ledger.Responses)))
	}
	for index, response := range ledger.Responses {
		wantTurn := index + 1
		if response.OwnerTurn != wantTurn {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("response %q owner turn: expected %d, actual %d", response.ID, wantTurn, response.OwnerTurn))
		}
		if response.DoneSeq == 0 {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("response %q has unresolved terminal disposition", response.ID))
		}
		if index < deterministicBargeInCancels {
			if response.CancelSeq == 0 || response.DoneStatus != "cancelled" {
				ledger.Violations = append(ledger.Violations, fmt.Sprintf("response %q must be cancelled exactly once, got cancel=%d status=%q", response.ID, response.CancelSeq, response.DoneStatus))
			}
		} else if response.CancelSeq != 0 || response.DoneStatus != "completed" {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("response %q must complete without cancel, got cancel=%d status=%q", response.ID, response.CancelSeq, response.DoneStatus))
		}
		if index == 0 && response.AudioDeltas == 0 {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("response %q did not prove active assistant audio", response.ID))
		}
		if index == 1 && response.AudioDeltas != 0 {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("response %q emitted audio before its cancellation boundary", response.ID))
		}
		if index >= 2 && response.AudioDeltas == 0 {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("response %q did not prove continuation audio", response.ID))
		}
		if response.PostCancel != 0 {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("response %q emitted %d audio deltas after cancel", response.ID, response.PostCancel))
		}
		if response.PostTerminal != 0 {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("response %q emitted %d audio deltas after terminality", response.ID, response.PostTerminal))
		}
	}
	if clean && len(ledger.Violations) > 0 {
		return ledger, fmt.Errorf("clean session outcome has unresolved or invalid barge-in ledger: %s", ledger.evidence())
	}
	if len(ledger.Violations) > 0 {
		return ledger, errors.New(strings.Join(ledger.Violations, "; "))
	}
	return ledger, nil
}

func (l *deterministicBargeInLedger) responseForEvent(payload []byte, seq int) *deterministicBargeInResponse {
	id := deterministicJSONField(payload, "response_id", "response.id")
	if id == "" {
		l.Violations = append(l.Violations, fmt.Sprintf("response output at sequence %d has no response identity", seq))
		return nil
	}
	response := l.ByID[id]
	if response == nil {
		l.Violations = append(l.Violations, fmt.Sprintf("response output at sequence %d references unknown response %q", seq, id))
	}
	return response
}

func requireDeterministicDirection(ledger *deterministicBargeInLedger, record gwtesting.CapturedSessionEvent, want gwtesting.SessionEventDirection) {
	if record.Direction != want {
		ledger.Violations = append(ledger.Violations, fmt.Sprintf("%s at sequence %d direction = %q, want %q", record.Type, record.Sequence, record.Direction, want))
	}
}

func deterministicJSONField(payload []byte, paths ...string) string {
	var value any
	if json.Unmarshal(payload, &value) != nil {
		return ""
	}
	for _, path := range paths {
		current := value
		for _, part := range strings.Split(path, ".") {
			object, ok := current.(map[string]any)
			if !ok {
				current = nil
				break
			}
			current = object[part]
		}
		if text, ok := current.(string); ok && text != "" {
			return text
		}
	}
	return ""
}

func cloneDeterministicCapture(capture gwtesting.SessionCapture) gwtesting.SessionCapture {
	clone := capture
	clone.Records = make([]gwtesting.CapturedSessionEvent, len(capture.Records))
	for index, record := range capture.Records {
		clone.Records[index] = record
		clone.Records[index].Payload = append([]byte(nil), record.Payload...)
		clone.Records[index].Data = append([]byte(nil), record.Data...)
	}
	return clone
}

func renumberDeterministicCapture(capture *gwtesting.SessionCapture) {
	for index := range capture.Records {
		capture.Records[index].Sequence = index + 1
		capture.Records[index].TimestampMs = int64(index + 1)
	}
}

func mutateDeterministicRecord(capture *gwtesting.SessionCapture, eventType string, occurrence int, mutate func(*gwtesting.CapturedSessionEvent)) bool {
	seen := 0
	for index := range capture.Records {
		if capture.Records[index].Type != eventType {
			continue
		}
		if seen == occurrence {
			mutate(&capture.Records[index])
			return true
		}
		seen++
	}
	return false
}

func deterministicPayloadWithResponseID(payload []byte, id string) []byte {
	var value map[string]any
	if json.Unmarshal(payload, &value) != nil {
		return payload
	}
	response, ok := value["response"].(map[string]any)
	if !ok {
		response = map[string]any{}
		value["response"] = response
	}
	response["id"] = id
	mutated, err := json.Marshal(value)
	if err != nil {
		return payload
	}
	return mutated
}

func TestSessionCommand_S2SLiveShapedBargeInV2DeterministicMatrix(t *testing.T) {
	capture, trace, server, runErr := runDeterministicBargeInCLI(t)
	if runErr != nil {
		ledger, validationErr := validateDeterministicBargeInCapture(capture, false)
		t.Fatalf("deterministic barge-in CLI returned %v; validation=%v; evidence=%s; stream=%v", runErr, validationErr, ledger.evidence(), trace.streamSnapshot())
	}
	ledger, err := validateDeterministicBargeInCapture(capture, true)
	if err != nil {
		t.Fatalf("deterministic barge-in matrix failed: %v; evidence=%s; stream=%v", err, ledger.evidence(), trace.streamSnapshot())
	}
	dials, responses, protocolErrs := server.snapshot()
	if dials != 1 {
		t.Fatalf("deterministic session dial count = %d, want 1", dials)
	}
	if len(responses) != deterministicBargeInTurns {
		t.Fatalf("deterministic provider response count = %d, want %d", len(responses), deterministicBargeInTurns)
	}
	if len(protocolErrs) != 0 {
		t.Fatalf("deterministic provider protocol errors = %v", protocolErrs)
	}
	for index, response := range responses {
		wantCancel := 0
		if index < deterministicBargeInCancels {
			wantCancel = 1
		}
		if response.CancelCount != wantCancel {
			t.Fatalf("provider response %q cancel count = %d, want %d", response.ID, response.CancelCount, wantCancel)
		}
	}
}

func TestSessionCommand_S2SLiveShapedBargeInV2NegativeControls(t *testing.T) {
	base, _, _, runErr := runDeterministicBargeInCLI(t)
	if runErr != nil {
		t.Fatalf("build positive capture for negative controls: %v", runErr)
	}
	cases := []struct {
		name   string
		mutate func(*gwtesting.SessionCapture) bool
		want   string
	}{
		{
			name: "dropped input frame",
			mutate: func(capture *gwtesting.SessionCapture) bool {
				found := mutateDeterministicRecord(capture, "input_audio_buffer.append", 1, func(record *gwtesting.CapturedSessionEvent) {
					record.Payload = json.RawMessage(`{"type":"input_audio_buffer.append","audio":""}`)
				})
				renumberDeterministicCapture(capture)
				return found
			},
			want: "empty audio",
		},
		{
			name: "duplicated input frame",
			mutate: func(capture *gwtesting.SessionCapture) bool {
				for index, record := range capture.Records {
					if record.Type != "input_audio_buffer.append" || index == 0 {
						continue
					}
					duplicate := cloneDeterministicCapture(*capture).Records[index]
					capture.Records = append(capture.Records[:index], append([]gwtesting.CapturedSessionEvent{duplicate}, capture.Records[index:]...)...)
					renumberDeterministicCapture(capture)
					return true
				}
				return false
			},
			want: "input appends: expected 4, actual 5",
		},
		{
			name: "non-terminal wait",
			mutate: func(capture *gwtesting.SessionCapture) bool {
				for index := len(capture.Records) - 1; index >= 0; index-- {
					if capture.Records[index].Type == "response.done" {
						capture.Records = append(capture.Records[:index], capture.Records[index+1:]...)
						renumberDeterministicCapture(capture)
						return true
					}
				}
				return false
			},
			want: "unresolved terminal disposition",
		},
		{
			name: "missing cancel",
			mutate: func(capture *gwtesting.SessionCapture) bool {
				for index, record := range capture.Records {
					if record.Type != "response.cancel" {
						continue
					}
					capture.Records = append(capture.Records[:index], capture.Records[index+1:]...)
					renumberDeterministicCapture(capture)
					return true
				}
				return false
			},
			want: "reported cancelled without a preceding response.cancel",
		},
		{
			name: "duplicate cancel",
			mutate: func(capture *gwtesting.SessionCapture) bool {
				for index, record := range capture.Records {
					if record.Type != "response.cancel" {
						continue
					}
					duplicate := record
					capture.Records = append(capture.Records[:index+1], append([]gwtesting.CapturedSessionEvent{duplicate}, capture.Records[index+1:]...)...)
					renumberDeterministicCapture(capture)
					return true
				}
				return false
			},
			want: "duplicate response.cancel",
		},
		{
			name: "dropped replacement",
			mutate: func(capture *gwtesting.SessionCapture) bool {
				filtered := make([]gwtesting.CapturedSessionEvent, 0, len(capture.Records))
				removed := false
				for _, record := range capture.Records {
					id := deterministicJSONField(record.Payload, "response.id", "response_id")
					if id == "resp-s2s-v2-4" {
						removed = true
						continue
					}
					filtered = append(filtered, record)
				}
				if !removed {
					return false
				}
				capture.Records = filtered
				renumberDeterministicCapture(capture)
				return true
			},
			want: "responses: expected 4, actual 3",
		},
		{
			name: "stale output after cancel",
			mutate: func(capture *gwtesting.SessionCapture) bool {
				var stale gwtesting.CapturedSessionEvent
				if !mutateDeterministicRecord(capture, "response.output_audio.delta", 0, func(record *gwtesting.CapturedSessionEvent) {
					stale = *record
					stale.Payload = deterministicPayloadWithResponseID(stale.Payload, deterministicBargeInResponseOne)
				}) {
					return false
				}
				for index, record := range capture.Records {
					if record.Type == "response.done" && deterministicJSONField(record.Payload, "response.id") == deterministicBargeInResponseOne {
						capture.Records = append(capture.Records[:index+1], append([]gwtesting.CapturedSessionEvent{stale}, capture.Records[index+1:]...)...)
						renumberDeterministicCapture(capture)
						return true
					}
				}
				return false
			},
			want: "emitted 1 audio deltas after cancel",
		},
		{
			name: "misattributed terminal identity",
			mutate: func(capture *gwtesting.SessionCapture) bool {
				found := mutateDeterministicRecord(capture, "response.done", 1, func(record *gwtesting.CapturedSessionEvent) {
					record.Payload = deterministicPayloadWithResponseID(record.Payload, deterministicBargeInResponseOne)
				})
				renumberDeterministicCapture(capture)
				return found
			},
			want: "duplicate response.done",
		},
		{
			name: "clean unresolved outcome",
			mutate: func(capture *gwtesting.SessionCapture) bool {
				for index := len(capture.Records) - 1; index >= 0; index-- {
					if capture.Records[index].Type == "response.done" {
						capture.Records = append(capture.Records[:index], capture.Records[index+1:]...)
						renumberDeterministicCapture(capture)
						return true
					}
				}
				return false
			},
			want: "clean session outcome has unresolved",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			capture := cloneDeterministicCapture(base)
			if !testCase.mutate(&capture) {
				t.Fatal("negative control did not find its target event")
			}
			clean := testCase.name == "clean unresolved outcome"
			ledger, err := validateDeterministicBargeInCapture(capture, clean)
			if err == nil {
				t.Fatalf("negative control unexpectedly passed: %s", ledger.evidence())
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("negative control error = %v, want detail %q; evidence=%s", err, testCase.want, ledger.evidence())
			}
		})
	}
}
