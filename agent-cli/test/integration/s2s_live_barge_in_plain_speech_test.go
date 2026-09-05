package integration

import servicetest "github.com/portpowered/go-agent-harness/agent-cli/internal/services/servicetest"

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

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const (
	plainSpeechInputCount      = 3
	plainSpeechResponseCount   = 3
	plainSpeechFrameBytes      = audio.FrameSize * 2
	plainSpeechRunTimeout      = 5 * time.Second
	plainSpeechCommandJoinWait = 500 * time.Millisecond
)

// plainSpeechTrace observes the normalized stream delivered by the generated
// CLI. It is deliberately only a gate: the wire capture below is the source of
// identity and ordering evidence.
type plainSpeechTrace struct {
	mu sync.Mutex

	responseOrdinal int
	responseOpen    bool
	created         chan int
	audio           chan int
	done            chan int
	events          []plainSpeechStreamEvent
}

type plainSpeechStreamEvent struct {
	Ordinal int
	Type    messages.StreamMessageType
	Bytes   int
}

func newPlainSpeechTrace() *plainSpeechTrace {
	return &plainSpeechTrace{
		created: make(chan int, plainSpeechResponseCount),
		audio:   make(chan int, plainSpeechResponseCount),
		done:    make(chan int, plainSpeechResponseCount),
	}
}

func (t *plainSpeechTrace) observe(msg messages.StreamMessage) {
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
	t.events = append(t.events, plainSpeechStreamEvent{Ordinal: ordinal, Type: msg.Type, Bytes: bytes})
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

func (t *plainSpeechTrace) waitFor(ctx context.Context, signal <-chan int, minimum int) error {
	if ctx == nil {
		return errors.New("plain-speech trace wait requires a context")
	}
	for {
		select {
		case ordinal := <-signal:
			if ordinal >= minimum {
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (t *plainSpeechTrace) waitForAudio(ctx context.Context, minimum int) error {
	return t.waitFor(ctx, t.audio, minimum)
}

func (t *plainSpeechTrace) waitForCreated(ctx context.Context, minimum int) error {
	return t.waitFor(ctx, t.created, minimum)
}

func (t *plainSpeechTrace) waitForDone(ctx context.Context, minimum int) error {
	return t.waitFor(ctx, t.done, minimum)
}

func (t *plainSpeechTrace) snapshot() []plainSpeechStreamEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]plainSpeechStreamEvent(nil), t.events...)
}

// plainSpeechAudioReader feeds three one-frame utterances through the shipped
// --audio-in - path. The second frame cannot be read until non-empty assistant
// audio for response 1 has crossed the CLI observer. The third frame waits for
// response 2 to complete, proving that the same session remains usable.
type plainSpeechAudioReader struct {
	mu       sync.Mutex
	segments []plainSpeechAudioSegment
	segment  int
	frame    bool
	gateUsed bool
	marker   bool
}

type plainSpeechAudioSegment struct {
	frame     []byte
	gate      func(context.Context) error
	endOfTurn bool
}

func newPlainSpeechAudioReader(trace *plainSpeechTrace) *plainSpeechAudioReader {
	return &plainSpeechAudioReader{
		segments: []plainSpeechAudioSegment{
			{frame: plainSpeechFrame(1), endOfTurn: true},
			{frame: plainSpeechFrame(2), endOfTurn: true, gate: func(ctx context.Context) error {
				return trace.waitForAudio(ctx, 1)
			}},
			{frame: plainSpeechFrame(3), gate: func(ctx context.Context) error {
				return trace.waitForDone(ctx, 2)
			}},
		},
	}
}

func plainSpeechFrame(seed byte) []byte {
	frame := make([]byte, plainSpeechFrameBytes)
	for index := range frame {
		frame[index] = seed + byte(index%29)
	}
	return frame
}

func (r *plainSpeechAudioReader) Read(p []byte) (int, error) {
	return r.ReadContext(context.Background(), p)
}

func (r *plainSpeechAudioReader) ReadContext(ctx context.Context, p []byte) (int, error) {
	if len(p) != plainSpeechFrameBytes {
		return 0, fmt.Errorf("plain-speech reader received %d bytes, want %d", len(p), plainSpeechFrameBytes)
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
		if !r.frame {
			r.frame = true
			r.mu.Unlock()
			copy(p, segment.frame)
			return len(p), nil
		}
		if segment.endOfTurn && !r.marker {
			r.marker = true
			r.mu.Unlock()
			return 0, audio.ErrEndOfTurn
		}
		r.segment++
		r.frame = false
		r.gateUsed = false
		r.marker = false
		r.mu.Unlock()
	}
}

func (*plainSpeechAudioReader) Close() error { return nil }

type plainSpeechServer struct {
	mu sync.Mutex

	events    chan []byte
	closed    chan struct{}
	closeOnce sync.Once
	dialOnce  sync.Once
	dialCount int

	turnHasAudio    bool
	commits         int
	responses       []*plainSpeechServerResponse
	active          *plainSpeechServerResponse
	pendingCancel   *plainSpeechServerResponse
	holdFirstOutput bool
	protocolErrs    []string
}

type plainSpeechServerResponse struct {
	ID           string
	CancelCount  int
	TerminalSent bool
}

func newPlainSpeechServer() *plainSpeechServer {
	return &plainSpeechServer{
		events: make(chan []byte, 128),
		closed: make(chan struct{}),
	}
}

func newTurnStartPlainSpeechServer() *plainSpeechServer {
	server := newPlainSpeechServer()
	server.holdFirstOutput = true
	return server
}

func (s *plainSpeechServer) Dial(_ string, _ map[string]string) (transport.Conn, error) {
	s.mu.Lock()
	s.dialCount++
	s.mu.Unlock()
	s.dialOnce.Do(func() {
		s.sendEvent(`{"type":"session.created","session":{"id":"session-plain-speech","model":"gpt-realtime"}}`)
		s.sendEvent(`{"type":"session.updated","session":{"id":"session-plain-speech","model":"gpt-realtime"}}`)
	})
	return &plainSpeechConn{server: s}, nil
}

func (s *plainSpeechServer) sendEvent(payload string) {
	select {
	case s.events <- []byte(payload):
	case <-s.closed:
	}
}

func (s *plainSpeechServer) shutdown() {
	s.closeOnce.Do(func() { close(s.closed) })
}

func (s *plainSpeechServer) snapshot() (int, []*plainSpeechServerResponse, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	responses := make([]*plainSpeechServerResponse, len(s.responses))
	for index, response := range s.responses {
		copyOf := *response
		responses[index] = &copyOf
	}
	return s.dialCount, responses, append([]string(nil), s.protocolErrs...)
}

type plainSpeechConn struct{ server *plainSpeechServer }

func (c *plainSpeechConn) ReadMessage() (int, []byte, error) {
	select {
	case payload := <-c.server.events:
		return 1, payload, nil
	case <-c.server.closed:
		return 0, nil, errors.New("plain-speech provider connection closed")
	}
}

func (c *plainSpeechConn) WriteMessage(_ int, payload []byte) error {
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
		// The collision is independent of session instruction composition.
	case "input_audio_buffer.append":
		decoded, err := base64.StdEncoding.DecodeString(envelope.Audio)
		if envelope.Audio == "" {
			s.protocolErrs = append(s.protocolErrs, "empty input_audio_buffer.append")
		} else if err != nil || len(decoded) == 0 {
			s.protocolErrs = append(s.protocolErrs, "input_audio_buffer.append was not non-empty base64 audio")
		} else if !s.turnHasAudio {
			s.turnHasAudio = true
			events = append(events, `{"type":"input_audio_buffer.speech_started"}`)
		}
		if s.pendingCancel != nil {
			response := s.pendingCancel
			response.TerminalSent = true
			s.pendingCancel = nil
			events = append(events, fmt.Sprintf(`{"type":"response.done","response":{"id":%q,"status":"cancelled"}}`, response.ID))
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
			fmt.Sprintf(`{"type":"conversation.item.created","item":{"id":"item-plain-%d","role":"user"}}`, s.commits),
		)
	case "response.create":
		if s.active != nil {
			s.protocolErrs = append(s.protocolErrs, fmt.Sprintf("response.create while %q is active", s.active.ID))
		}
		ordinal := len(s.responses) + 1
		response := &plainSpeechServerResponse{ID: fmt.Sprintf("response-plain-%d", ordinal)}
		s.responses = append(s.responses, response)
		s.active = response
		events = append(events, fmt.Sprintf(`{"type":"response.created","response":{"id":%q}}`, response.ID))
		switch ordinal {
		case 1:
			if !s.holdFirstOutput {
				events = append(events, plainSpeechAudioDelta(response.ID, 1))
			}
		case 2, 3:
			events = append(events,
				plainSpeechAudioDelta(response.ID, byte(ordinal)),
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
			s.protocolErrs = append(s.protocolErrs, fmt.Sprintf("response %q received duplicate cancellation", response.ID))
		}
		s.active = nil
		s.pendingCancel = response
	}
	s.mu.Unlock()

	for _, event := range events {
		s.sendEvent(event)
	}
	return nil
}

func plainSpeechAudioDelta(responseID string, seed byte) string {
	audio := base64.StdEncoding.EncodeToString([]byte{seed, 0, seed + 20, 0})
	return fmt.Sprintf(`{"type":"response.output_audio.delta","response_id":%q,"delta":%q,"format":"pcm16"}`, responseID, audio)
}

func (c *plainSpeechConn) Close() error {
	c.server.shutdown()
	return nil
}

type plainSpeechRun struct {
	capture gwtesting.SessionCapture
	trace   *plainSpeechTrace
	server  *plainSpeechServer
	err     error
}

func runPlainSpeechCLI(t *testing.T) plainSpeechRun {
	t.Helper()
	trace := newPlainSpeechTrace()
	server := newPlainSpeechServer()
	t.Cleanup(server.shutdown)
	recorder := gwtesting.NewRecordingWebSocketDialer(server, "openai", "gpt-realtime")
	sessionInferencer, err := servicetest.NewOpenAIRealtimeSessionInferencerWithOptions(
		config.OpenAIConfig{APIKey: "test-key", Model: "gpt-realtime", BaseURL: "wss://hermetic.openai.test/v1/realtime"},
		oaiprovider.WithWebSocketDialer(recorder),
		oaiprovider.WithClientOwnedAudioTurnBoundaries(),
	)
	if err != nil {
		t.Fatalf("create hermetic OpenAI session inferencer: %v", err)
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
	root.SetIn(newPlainSpeechAudioReader(trace))
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
		"--max-duration", plainSpeechRunTimeout.String(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), plainSpeechRunTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()

	var runErr error
	select {
	case runErr = <-done:
	case <-ctx.Done():
		timer := time.NewTimer(plainSpeechCommandJoinWait)
		select {
		case runErr = <-done:
			timer.Stop()
		case <-timer.C:
			runErr = fmt.Errorf("plain-speech CLI command await timed out at %s: %w", plainSpeechRunTimeout, probe.ErrBargeInWait)
		}
	}
	return plainSpeechRun{capture: recorder.Capture(), trace: trace, server: server, err: runErr}
}

func plainSpeechContract() probe.BargeInContract {
	return probe.BargeInContract{
		Inputs: []probe.BargeInInputExpectation{
			{ID: "input-1", TurnID: "turn-1"},
			{ID: "input-2", TurnID: "turn-2"},
			{ID: "input-3", TurnID: "turn-3"},
		},
		Responses: []probe.BargeInResponseExpectation{
			{ID: "response-1", InputID: "input-1", TurnID: "turn-1", Disposition: probe.BargeInDispositionCancelled, RequireCancel: true, RequireOutput: true},
			{ID: "response-2", InputID: "input-2", TurnID: "turn-2", Disposition: probe.BargeInDispositionCompleted, ForbidCancel: true, RequireOutput: true, RequireContinuation: true},
			{ID: "response-3", InputID: "input-3", TurnID: "turn-3", Disposition: probe.BargeInDispositionCompleted, ForbidCancel: true, RequireOutput: true, RequireContinuation: true},
		},
		RequireSessionTerminal: true,
	}
}

// normalizePlainSpeechCapture is the OpenAI transport adapter for this proof.
// It maps provider wire fields to safe stable identities before recording them
// in the provider-neutral oracle. Provider IDs never enter ledger diagnostics.
func normalizePlainSpeechCapture(capture gwtesting.SessionCapture, omitContinuation string) *probe.BargeInLedger {
	adapter := plainSpeechCaptureAdapter{
		ledger:              probe.NewBargeInLedger(),
		providerResponses:   make(map[string]plainSpeechResponseIdentity),
		responseByProvider:  make(map[string]string),
		omitContinuationFor: omitContinuation,
	}
	for _, record := range capture.Records {
		adapter.observe(record)
	}
	adapter.ledger.Observe(probe.BargeInEvent{
		Sequence:    adapter.nextEventSequence(),
		Kind:        probe.BargeInEventSessionTerminal,
		Disposition: probe.BargeInDispositionClean,
		Clean:       true,
	})
	return adapter.ledger
}

type plainSpeechResponseIdentity struct {
	stable   string
	inputID  string
	turnID   string
	ordinal  int
	terminal bool
}

type plainSpeechCaptureAdapter struct {
	ledger *probe.BargeInLedger

	nextSequence        int
	inputOrdinal        int
	currentInput        string
	lastCommittedInput  string
	responseOrdinal     int
	providerResponses   map[string]plainSpeechResponseIdentity
	responseByProvider  map[string]string
	omitContinuationFor string
}

func (a *plainSpeechCaptureAdapter) observe(record gwtesting.CapturedSessionEvent) {
	payload := plainSpeechRecordPayload(record)
	switch record.Type {
	case "input_audio_buffer.append":
		if record.Direction != gwtesting.DirectionClientToServer {
			return
		}
		if a.currentInput == "" {
			a.inputOrdinal++
			a.currentInput = plainSpeechInputID(a.inputOrdinal)
		}
		decoded, _ := base64.StdEncoding.DecodeString(plainSpeechJSONField(payload, "audio"))
		a.ledger.Observe(probe.BargeInEvent{
			Sequence:      a.nextEventSequence(),
			Kind:          probe.BargeInEventInputAppend,
			InputID:       a.currentInput,
			TurnID:        plainSpeechTurnID(a.currentInput),
			AppendGroupID: a.currentInput,
			Bytes:         len(decoded),
			NonEmpty:      len(decoded) > 0,
		})
	case "input_audio_buffer.commit":
		if record.Direction != gwtesting.DirectionClientToServer {
			return
		}
		a.ledger.Observe(probe.BargeInEvent{
			Sequence: a.nextEventSequence(),
			Kind:     probe.BargeInEventInputCommit,
			InputID:  a.currentInput,
			TurnID:   plainSpeechTurnID(a.currentInput),
		})
		a.lastCommittedInput = a.currentInput
	case "conversation.item.created":
		if record.Direction != gwtesting.DirectionServerToClient || plainSpeechJSONField(payload, "item.role") != "user" {
			return
		}
		a.ledger.Observe(probe.BargeInEvent{
			Sequence: a.nextEventSequence(),
			Kind:     probe.BargeInEventUserTurn,
			InputID:  a.currentInput,
			TurnID:   plainSpeechTurnID(a.currentInput),
		})
		a.currentInput = ""
	case "response.created":
		if record.Direction != gwtesting.DirectionServerToClient {
			return
		}
		a.responseOrdinal++
		providerID := plainSpeechJSONField(payload, "response.id", "response_id")
		stableID := plainSpeechResponseID(a.responseOrdinal)
		owner := a.lastCommittedInput
		identity := plainSpeechResponseIdentity{
			stable:  stableID,
			inputID: owner,
			turnID:  plainSpeechTurnID(owner),
			ordinal: a.responseOrdinal,
		}
		a.providerResponses[providerID] = identity
		a.responseByProvider[providerID] = stableID
		a.ledger.Observe(probe.BargeInEvent{
			Sequence:   a.nextEventSequence(),
			Kind:       probe.BargeInEventResponseCreated,
			InputID:    owner,
			TurnID:     identity.turnID,
			ResponseID: stableID,
		})
		if a.responseOrdinal > 1 && stableID != a.omitContinuationFor {
			a.ledger.Observe(probe.BargeInEvent{
				Sequence:   a.nextEventSequence(),
				Kind:       probe.BargeInEventContinuation,
				InputID:    owner,
				TurnID:     identity.turnID,
				ResponseID: stableID,
			})
		}
	case "response.output_audio.delta":
		if record.Direction != gwtesting.DirectionServerToClient {
			return
		}
		providerID := plainSpeechJSONField(payload, "response_id", "response.id")
		stableID := a.responseByProvider[providerID]
		decoded, _ := base64.StdEncoding.DecodeString(plainSpeechJSONField(payload, "delta"))
		a.ledger.Observe(probe.BargeInEvent{
			Sequence:   a.nextEventSequence(),
			Kind:       probe.BargeInEventResponseOutput,
			ResponseID: stableID,
			Bytes:      len(decoded),
			NonEmpty:   len(decoded) > 0,
		})
	case "response.cancel":
		if record.Direction != gwtesting.DirectionClientToServer {
			return
		}
		identity := a.activeResponse()
		interruptingInput := ""
		if identity.ordinal > 0 {
			interruptingInput = plainSpeechInputID(identity.ordinal + 1)
		}
		a.ledger.Observe(probe.BargeInEvent{
			Sequence:   a.nextEventSequence(),
			Kind:       probe.BargeInEventResponseCancel,
			InputID:    interruptingInput,
			TurnID:     plainSpeechTurnID(interruptingInput),
			ResponseID: identity.stable,
		})
	case "response.done":
		if record.Direction != gwtesting.DirectionServerToClient {
			return
		}
		providerID := plainSpeechJSONField(payload, "response.id", "response_id")
		identity := a.providerResponses[providerID]
		stableID := a.responseByProvider[providerID]
		status := plainSpeechJSONField(payload, "response.status", "status")
		a.ledger.Observe(probe.BargeInEvent{
			Sequence:    a.nextEventSequence(),
			Kind:        probe.BargeInEventResponseTerminal,
			ResponseID:  stableID,
			Disposition: plainSpeechDisposition(status),
			Reason:      status,
		})
		identity.terminal = true
		a.providerResponses[providerID] = identity
	}
}

func (a *plainSpeechCaptureAdapter) nextEventSequence() int {
	a.nextSequence++
	return a.nextSequence
}

func (a *plainSpeechCaptureAdapter) activeResponse() plainSpeechResponseIdentity {
	for ordinal := a.responseOrdinal; ordinal > 0; ordinal-- {
		for _, identity := range a.providerResponses {
			if identity.ordinal == ordinal && !identity.terminal {
				return identity
			}
		}
	}
	return plainSpeechResponseIdentity{}
}

func plainSpeechDisposition(status string) probe.BargeInDisposition {
	switch strings.ToLower(status) {
	case "completed":
		return probe.BargeInDispositionCompleted
	case "cancelled", "canceled":
		return probe.BargeInDispositionCancelled
	case "failed", "incomplete":
		return probe.BargeInDispositionFailed
	default:
		return probe.BargeInDisposition(status)
	}
}

func validatePlainSpeechCapture(capture gwtesting.SessionCapture, omitContinuation string) error {
	return normalizePlainSpeechCapture(capture, omitContinuation).Validate(plainSpeechContract())
}

func plainSpeechInputID(ordinal int) string {
	if ordinal <= 0 {
		return ""
	}
	return fmt.Sprintf("input-%d", ordinal)
}

func plainSpeechTurnID(inputID string) string {
	if inputID == "" {
		return ""
	}
	return "turn-" + strings.TrimPrefix(inputID, "input-")
}

func plainSpeechResponseID(ordinal int) string {
	if ordinal <= 0 {
		return ""
	}
	return fmt.Sprintf("response-%d", ordinal)
}

func plainSpeechRecordPayload(record gwtesting.CapturedSessionEvent) []byte {
	if len(record.Payload) > 0 {
		return record.Payload
	}
	return record.Data
}

func plainSpeechJSONField(payload []byte, paths ...string) string {
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

func clonePlainSpeechCapture(capture gwtesting.SessionCapture) gwtesting.SessionCapture {
	clone := capture
	clone.Records = make([]gwtesting.CapturedSessionEvent, len(capture.Records))
	for index, record := range capture.Records {
		clone.Records[index] = record
		clone.Records[index].Payload = append([]byte(nil), record.Payload...)
		clone.Records[index].Data = append([]byte(nil), record.Data...)
	}
	return clone
}

func renumberPlainSpeechCapture(capture *gwtesting.SessionCapture) {
	for index := range capture.Records {
		capture.Records[index].Sequence = index + 1
		capture.Records[index].TimestampMs = int64(index + 1)
	}
}

func insertPlainSpeechRecordAfter(capture *gwtesting.SessionCapture, index int, record gwtesting.CapturedSessionEvent) {
	withDuplicate := make([]gwtesting.CapturedSessionEvent, 0, len(capture.Records)+1)
	withDuplicate = append(withDuplicate, capture.Records[:index+1]...)
	withDuplicate = append(withDuplicate, record)
	withDuplicate = append(withDuplicate, capture.Records[index+1:]...)
	capture.Records = withDuplicate
	renumberPlainSpeechCapture(capture)
}

func removePlainSpeechRecords(capture *gwtesting.SessionCapture, remove func(gwtesting.CapturedSessionEvent) bool) {
	filtered := make([]gwtesting.CapturedSessionEvent, 0, len(capture.Records))
	for _, record := range capture.Records {
		if remove(record) {
			continue
		}
		filtered = append(filtered, record)
	}
	capture.Records = filtered
	renumberPlainSpeechCapture(capture)
}

func plainSpeechRecordIndex(capture gwtesting.SessionCapture, match func(gwtesting.CapturedSessionEvent) bool, occurrence int) int {
	seen := 0
	for index, record := range capture.Records {
		if !match(record) {
			continue
		}
		if seen == occurrence {
			return index
		}
		seen++
	}
	return -1
}

func TestS2SLiveBargeInPlainSpeechCLIUsesObservedAudioGate(t *testing.T) {
	run := runPlainSpeechCLI(t)
	if run.err != nil {
		dialCount, responses, protocolErrs := run.server.snapshot()
		t.Fatalf("plain-speech CLI returned %v; dial_count=%d responses=%v protocol_errors=%v stream=%v", run.err, dialCount, responses, protocolErrs, run.trace.snapshot())
	}
	if err := validatePlainSpeechCapture(run.capture, ""); err != nil {
		t.Fatalf("plain-speech identity-aware ledger failed: %v; stream=%v", err, run.trace.snapshot())
	}

	firstAudio := plainSpeechRecordIndex(run.capture, func(record gwtesting.CapturedSessionEvent) bool {
		return record.Direction == gwtesting.DirectionServerToClient && record.Type == "response.output_audio.delta" && plainSpeechJSONField(plainSpeechRecordPayload(record), "response_id") == "response-plain-1"
	}, 0)
	secondAppend := plainSpeechRecordIndex(run.capture, func(record gwtesting.CapturedSessionEvent) bool {
		return record.Direction == gwtesting.DirectionClientToServer && record.Type == "input_audio_buffer.append"
	}, 1)
	firstCancel := plainSpeechRecordIndex(run.capture, func(record gwtesting.CapturedSessionEvent) bool {
		return record.Direction == gwtesting.DirectionClientToServer && record.Type == "response.cancel"
	}, 0)
	firstTerminal := plainSpeechRecordIndex(run.capture, func(record gwtesting.CapturedSessionEvent) bool {
		return record.Direction == gwtesting.DirectionServerToClient && record.Type == "response.done" && plainSpeechJSONField(plainSpeechRecordPayload(record), "response.id") == "response-plain-1"
	}, 0)
	if firstAudio < 0 || secondAppend < 0 || firstCancel < 0 || firstTerminal < 0 {
		t.Fatalf("plain-speech collision boundary is incomplete: first_audio=%d second_append=%d cancel=%d terminal=%d records=%v", firstAudio, secondAppend, firstCancel, firstTerminal, run.capture.Records)
	}
	if !(firstAudio < secondAppend && secondAppend < firstTerminal && firstAudio < firstCancel && firstCancel < firstTerminal) {
		t.Fatalf("interrupting input was not released after active response audio and before its terminal: first_audio=%d second_append=%d cancel=%d terminal=%d", firstAudio, secondAppend, firstCancel, firstTerminal)
	}

	dialCount, responses, protocolErrs := run.server.snapshot()
	if dialCount != 1 || len(responses) != plainSpeechResponseCount || len(protocolErrs) != 0 {
		t.Fatalf("plain-speech provider observations = dials:%d responses:%d protocol_errors:%v; want one session, three responses, and no protocol errors", dialCount, len(responses), protocolErrs)
	}
	for index, response := range responses {
		wantCancel := 0
		if index == 0 {
			wantCancel = 1
		}
		if response.CancelCount != wantCancel || !response.TerminalSent {
			t.Fatalf("provider response %q = cancel:%d terminal:%t, want cancel:%d and terminal", response.ID, response.CancelCount, response.TerminalSent, wantCancel)
		}
	}
}

func TestS2SLiveBargeInPlainSpeechOracleRejectsNamedMutations(t *testing.T) {
	run := runPlainSpeechCLI(t)
	if run.err != nil {
		t.Fatalf("build positive plain-speech capture: %v; stream=%v", run.err, run.trace.snapshot())
	}
	cases := []struct {
		name             string
		mutate           func(*gwtesting.SessionCapture)
		omitContinuation string
		want             string
	}{
		{
			name: "dropped input",
			mutate: func(capture *gwtesting.SessionCapture) {
				index := plainSpeechRecordIndex(*capture, func(record gwtesting.CapturedSessionEvent) bool {
					return record.Direction == gwtesting.DirectionClientToServer && record.Type == "input_audio_buffer.append"
				}, 1)
				if index >= 0 {
					capture.Records = append(capture.Records[:index], capture.Records[index+1:]...)
					renumberPlainSpeechCapture(capture)
				}
			},
			want: `missing input "input-3"`,
		},
		{
			name: "duplicate cancellation",
			mutate: func(capture *gwtesting.SessionCapture) {
				index := plainSpeechRecordIndex(*capture, func(record gwtesting.CapturedSessionEvent) bool {
					return record.Direction == gwtesting.DirectionClientToServer && record.Type == "response.cancel"
				}, 0)
				if index < 0 {
					return
				}
				insertPlainSpeechRecordAfter(capture, index, capture.Records[index])
			},
			want: `response "response-1" received duplicate cancellation`,
		},
		{
			name: "stale output",
			mutate: func(capture *gwtesting.SessionCapture) {
				cancelIndex := plainSpeechRecordIndex(*capture, func(record gwtesting.CapturedSessionEvent) bool {
					return record.Direction == gwtesting.DirectionClientToServer && record.Type == "response.cancel"
				}, 0)
				outputIndex := plainSpeechRecordIndex(*capture, func(record gwtesting.CapturedSessionEvent) bool {
					return record.Direction == gwtesting.DirectionServerToClient && record.Type == "response.output_audio.delta" && plainSpeechJSONField(plainSpeechRecordPayload(record), "response_id") == "response-plain-1"
				}, 0)
				if cancelIndex < 0 || outputIndex < 0 {
					return
				}
				insertPlainSpeechRecordAfter(capture, cancelIndex, capture.Records[outputIndex])
			},
			want: `response "response-1" emitted stale output after cancellation`,
		},
		{
			name: "misattributed response",
			mutate: func(capture *gwtesting.SessionCapture) {
				index := plainSpeechRecordIndex(*capture, func(record gwtesting.CapturedSessionEvent) bool {
					return record.Direction == gwtesting.DirectionServerToClient && record.Type == "response.done"
				}, 1)
				if index < 0 {
					return
				}
				payload := map[string]any{}
				if json.Unmarshal(plainSpeechRecordPayload(capture.Records[index]), &payload) != nil {
					return
				}
				response, _ := payload["response"].(map[string]any)
				if response == nil {
					response = map[string]any{}
					payload["response"] = response
				}
				response["id"] = "response-plain-1"
				encoded, _ := json.Marshal(payload)
				capture.Records[index].Payload = encoded
			},
			want: `response "response-1" received duplicate terminal disposition`,
		},
		{
			name: "missing replacement",
			mutate: func(capture *gwtesting.SessionCapture) {
				removePlainSpeechRecords(capture, func(record gwtesting.CapturedSessionEvent) bool {
					return plainSpeechRecordResponseID(record) == "response-plain-2"
				})
			},
			want: `missing response "response-3"`,
		},
		{
			name:             "missing continuation",
			omitContinuation: "response-2",
			want:             `response "response-2" has no continuation identity`,
		},
		{
			name: "clean unresolved close",
			mutate: func(capture *gwtesting.SessionCapture) {
				index := plainSpeechRecordIndex(*capture, func(record gwtesting.CapturedSessionEvent) bool {
					return record.Direction == gwtesting.DirectionServerToClient && record.Type == "response.done"
				}, 2)
				if index >= 0 {
					capture.Records = append(capture.Records[:index], capture.Records[index+1:]...)
					renumberPlainSpeechCapture(capture)
				}
			},
			want: `response "response-3" has unresolved terminal disposition`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			capture := clonePlainSpeechCapture(run.capture)
			if testCase.mutate != nil {
				testCase.mutate(&capture)
			}
			err := validatePlainSpeechCapture(capture, testCase.omitContinuation)
			if err == nil {
				t.Fatal("mutation unexpectedly passed the identity-aware ledger")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("mutation error = %v, want detail %q", err, testCase.want)
			}
		})
	}
}

func plainSpeechRecordResponseID(record gwtesting.CapturedSessionEvent) string {
	return plainSpeechJSONField(plainSpeechRecordPayload(record), "response_id", "response.id")
}

func TestS2SLiveBargeInPlainSpeechWaitForNoEventIsBounded(t *testing.T) {
	ledger := probe.NewBargeInLedger()
	ledger.Observe(probe.BargeInEvent{
		Sequence: 1, Kind: probe.BargeInEventInputAppend,
		InputID: "input-wait", TurnID: "turn-wait", AppendGroupID: "input-wait",
		Bytes: 2, NonEmpty: true,
	})
	start := time.Now()
	err := ledger.WaitFor(context.Background(), "plain-speech assistant audio", make(chan struct{}), 20*time.Millisecond)
	if err == nil {
		t.Fatal("missing assistant-audio gate unexpectedly passed")
	}
	var waitErr *probe.BargeInWaitError
	if !errors.As(err, &waitErr) || !errors.Is(err, probe.ErrBargeInWait) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait error = %v, want bounded barge-in wait with deadline identity", err)
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("missing assistant-audio gate took %s, want a bounded return", elapsed)
	}
	for _, want := range []string{"plain-speech assistant audio", "1:input.append", "input-wait:commit", "session:terminal"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("wait error = %v, want diagnostic %q", err, want)
		}
	}
}
