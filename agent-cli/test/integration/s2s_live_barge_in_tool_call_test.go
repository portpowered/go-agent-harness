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
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const (
	toolBargeInCallID        = "call-tool-barge-in"
	toolBargeInToolName      = "slow_lookup"
	toolBargeInToolArguments = `{"query":"bounded"}`
	toolBargeInToolResult    = `{"status":"ready","source":"hermetic"}`
	toolBargeInResponseOne   = "response-tool-barge-in-1"
	toolBargeInResponseTwo   = "response-tool-barge-in-2"
	toolBargeInResponseThree = "response-tool-barge-in-3"
	toolBargeInSessionID     = "session-tool-barge-in"
	toolBargeInGateTimeout   = 2 * time.Second
)

// toolBargeInTrace counts only assistant response boundaries. A tool runner
// emits its own RoleTool MESSAGE.START/END pair while the provider call is
// being executed; counting that pair as spoken model output would make a
// source gate depend on an implementation detail instead of observable
// assistant audio.
type toolBargeInTrace struct {
	mu               sync.Mutex
	responseOrdinal  int
	responseOpen     bool
	responseDone     [4]chan struct{}
	responseDoneOnce [4]sync.Once
	events           []toolBargeInStreamEvent
}

type toolBargeInStreamEvent struct {
	Ordinal int
	Type    messages.StreamMessageType
	Bytes   int
}

func newToolBargeInTrace() *toolBargeInTrace {
	trace := &toolBargeInTrace{}
	for ordinal := 1; ordinal < len(trace.responseDone); ordinal++ {
		trace.responseDone[ordinal] = make(chan struct{})
	}
	return trace
}

func (t *toolBargeInTrace) observe(msg messages.StreamMessage) {
	if t == nil || msg.Role == messages.RoleTool {
		return
	}

	doneOrdinal := 0
	t.mu.Lock()
	if msg.Type == messages.StreamTypeMessageStart {
		t.responseOrdinal++
		t.responseOpen = true
	}
	ordinal := t.responseOrdinal
	bytes := 0
	if value, ok := msg.Value.(*messages.AudioDeltaValue); ok && value != nil {
		bytes = len(value.Content)
	}
	if msg.Type == messages.StreamTypeMessageEnd && t.responseOpen {
		doneOrdinal = ordinal
		t.responseOpen = false
	}
	t.events = append(t.events, toolBargeInStreamEvent{Ordinal: ordinal, Type: msg.Type, Bytes: bytes})
	t.mu.Unlock()

	if doneOrdinal > 0 && doneOrdinal < len(t.responseDone) {
		t.responseDoneOnce[doneOrdinal].Do(func() { close(t.responseDone[doneOrdinal]) })
	}
}

func (t *toolBargeInTrace) waitForDone(ctx context.Context, ordinal int) error {
	if ordinal <= 0 || ordinal >= len(t.responseDone) {
		return fmt.Errorf("invalid assistant response ordinal %d", ordinal)
	}
	return probe.NewBargeInLedger().WaitFor(
		ctx,
		fmt.Sprintf("assistant response %d terminal", ordinal),
		t.responseDone[ordinal],
		toolBargeInGateTimeout,
	)
}

func (t *toolBargeInTrace) snapshot() []toolBargeInStreamEvent {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]toolBargeInStreamEvent(nil), t.events...)
}

// toolBargeInAudioReader makes the collision causal. The second non-empty
// frame is not read until the provider tool call has started and its owning
// response is terminal. After that frame has crossed the provider boundary,
// the executor is released and the reader waits for the result-driven spoken
// continuation before committing the second audio turn.
type toolBargeInAudioReader struct {
	mu             sync.Mutex
	segments       []toolBargeInAudioSegment
	segment        int
	frame          bool
	gateUsed       bool
	marker         bool
	afterFrameUsed bool
}

type toolBargeInAudioSegment struct {
	frame      []byte
	gate       func(context.Context) error
	afterFrame func(context.Context) error
	endOfTurn  bool
}

func newToolBargeInAudioReader(server *toolBargeInServer, executor *toolBargeInExecutor, trace *toolBargeInTrace) *toolBargeInAudioReader {
	return &toolBargeInAudioReader{
		segments: []toolBargeInAudioSegment{
			{frame: plainSpeechFrame(41), endOfTurn: true},
			{
				frame: plainSpeechFrame(42),
				gate: func(ctx context.Context) error {
					if err := waitToolBargeInSignal(ctx, "named tool executor start", executor.started); err != nil {
						return err
					}
					return trace.waitForDone(ctx, 1)
				},
				afterFrame: func(ctx context.Context) error {
					if err := waitToolBargeInSignal(ctx, "customer speech while named tool call is outstanding", server.speechSignalCh); err != nil {
						return err
					}
					executor.releaseResult()
					return trace.waitForDone(ctx, 2)
				},
				// Let EOF emit the single final end-of-turn marker. Marking this
				// segment as an end-of-turn as well would commit the same audio
				// twice (once at the marker and once again at EOF).
				endOfTurn: false,
			},
		},
	}
}

func (r *toolBargeInAudioReader) Read(p []byte) (int, error) {
	return r.ReadContext(context.Background(), p)
}

func (r *toolBargeInAudioReader) ReadContext(ctx context.Context, p []byte) (int, error) {
	if len(p) != plainSpeechFrameBytes {
		return 0, fmt.Errorf("tool barge-in reader received %d bytes, want %d", len(p), plainSpeechFrameBytes)
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
		if !r.afterFrameUsed {
			r.afterFrameUsed = true
			afterFrame := segment.afterFrame
			r.mu.Unlock()
			if afterFrame != nil {
				if err := afterFrame(ctx); err != nil {
					return 0, err
				}
			}
			continue
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
		r.afterFrameUsed = false
		r.mu.Unlock()
	}
}

func (*toolBargeInAudioReader) Close() error { return nil }

func waitToolBargeInSignal(ctx context.Context, boundary string, signal <-chan struct{}) error {
	return probe.NewBargeInLedger().WaitFor(ctx, boundary, signal, toolBargeInGateTimeout)
}

type toolBargeInServer struct {
	mu sync.Mutex

	events    chan []byte
	closed    chan struct{}
	closeOnce sync.Once
	dialOnce  sync.Once
	dialCount int

	turnHasAudio     bool
	commits          int
	responses        []*toolBargeInServerResponse
	active           *toolBargeInServerResponse
	toolOutstanding  bool
	toolResultCount  int
	toolResultCallID string
	toolResultOutput string

	speechWhileToolOutstanding bool
	speechOverlapOnce          sync.Once
	speechSignalCh             chan struct{}
	protocolErrs               []string
	milestones                 []string
	clientClosePending         bool
}

type toolBargeInServerResponse struct {
	ID           string
	CancelCount  int
	TerminalSent bool
}

func newToolBargeInServer() *toolBargeInServer {
	return &toolBargeInServer{
		events:         make(chan []byte, 128),
		closed:         make(chan struct{}),
		speechSignalCh: make(chan struct{}),
	}
}

func (s *toolBargeInServer) Dial(_ string, _ map[string]string) (transport.Conn, error) {
	s.mu.Lock()
	s.dialCount++
	s.mu.Unlock()
	s.dialOnce.Do(func() {
		s.sendEvent(`{"type":"session.created","session":{"id":"` + toolBargeInSessionID + `","model":"gpt-realtime"}}`)
		s.sendEvent(`{"type":"session.updated","session":{"id":"` + toolBargeInSessionID + `"}}`)
	})
	return &toolBargeInConn{server: s}, nil
}

func (s *toolBargeInServer) sendEvent(payload string) {
	select {
	case s.events <- []byte(payload):
	case <-s.closed:
	}
}

func (s *toolBargeInServer) shutdown() {
	s.closeOnce.Do(func() { close(s.closed) })
}

func (s *toolBargeInServer) recordMilestoneLocked(milestone string) {
	s.milestones = append(s.milestones, milestone)
}

func (s *toolBargeInServer) snapshot() (int, []*toolBargeInServerResponse, int, bool, bool, []string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	responses := make([]*toolBargeInServerResponse, len(s.responses))
	for index, response := range s.responses {
		copyOf := *response
		responses[index] = &copyOf
	}
	return s.dialCount,
		responses,
		s.toolResultCount,
		s.speechWhileToolOutstanding,
		s.clientClosePending,
		append([]string(nil), s.protocolErrs...),
		append([]string(nil), s.milestones...)
}

func (s *toolBargeInServer) observeTransportClose() {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := s.toolResultCount != 1 || s.active != nil
	for _, response := range s.responses {
		if !response.TerminalSent {
			pending = true
		}
	}
	s.clientClosePending = pending
	s.recordMilestoneLocked("transport_close")
}

type toolBargeInConn struct{ server *toolBargeInServer }

func (c *toolBargeInConn) ReadMessage() (int, []byte, error) {
	select {
	case payload := <-c.server.events:
		return 1, payload, nil
	case <-c.server.closed:
		return 0, nil, errors.New("tool barge-in provider connection closed")
	}
}

func (c *toolBargeInConn) WriteMessage(_ int, payload []byte) error {
	var envelope struct {
		Type  string `json:"type"`
		Audio string `json:"audio"`
		Item  struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Output string `json:"output"`
		} `json:"item"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("decode tool barge-in client event: %w", err)
	}

	events := make([]string, 0, 8)
	s := c.server
	s.mu.Lock()
	switch envelope.Type {
	case "session.update":
		// The provider-specific configuration is not part of this collision.
	case "input_audio_buffer.append":
		decoded, err := base64.StdEncoding.DecodeString(envelope.Audio)
		if envelope.Audio == "" || err != nil || len(decoded) == 0 {
			s.protocolErrs = append(s.protocolErrs, "input_audio_buffer.append was not non-empty base64 audio")
		} else {
			if !s.turnHasAudio {
				s.turnHasAudio = true
				events = append(events, `{"type":"input_audio_buffer.speech_started"}`)
			}
			if s.toolOutstanding && s.toolResultCount == 0 {
				s.speechWhileToolOutstanding = true
				s.recordMilestoneLocked("speech_overlap")
				s.speechOverlapOnce.Do(func() { close(s.speechSignalCh) })
			}
		}
	case "input_audio_buffer.commit":
		if !s.turnHasAudio {
			s.protocolErrs = append(s.protocolErrs, "input commit without non-empty audio")
		}
		s.commits++
		if s.commits == 2 {
			s.recordMilestoneLocked("later_input_committed")
		}
		s.turnHasAudio = false
		events = append(events,
			`{"type":"input_audio_buffer.speech_stopped"}`,
			`{"type":"input_audio_buffer.committed"}`,
			fmt.Sprintf(`{"type":"conversation.item.created","item":{"id":"item-tool-barge-in-%d","role":"user"}}`, s.commits),
		)
	case "conversation.item.create":
		if envelope.Item.Type != "function_call_output" {
			break
		}
		s.toolResultCount++
		s.toolResultCallID = envelope.Item.CallID
		s.toolResultOutput = envelope.Item.Output
		if s.toolResultCount > 1 {
			s.protocolErrs = append(s.protocolErrs, "function_call_output was delivered more than once")
		}
		if envelope.Item.CallID != toolBargeInCallID || envelope.Item.Output != toolBargeInToolResult {
			s.protocolErrs = append(s.protocolErrs, fmt.Sprintf("function_call_output correlation = {call_id:%q output:%q}", envelope.Item.CallID, envelope.Item.Output))
		} else {
			s.recordMilestoneLocked("tool_result_received")
			s.toolOutstanding = false
		}
	case "response.create":
		ordinal := len(s.responses) + 1
		response := &toolBargeInServerResponse{ID: toolBargeInResponseID(ordinal)}
		s.responses = append(s.responses, response)
		s.active = response
		events = append(events, fmt.Sprintf(`{"type":"response.created","response":{"id":%q}}`, response.ID))
		switch ordinal {
		case 1:
			events = append(events,
				plainSpeechAudioDelta(response.ID, 51),
				fmt.Sprintf(`{"type":"response.output_audio.done","response_id":%q}`, response.ID),
				fmt.Sprintf(`{"type":"response.output_item.added","response_id":%q,"item":{"id":"item-tool-call","type":"function_call","call_id":%q,"name":%q}}`, response.ID, toolBargeInCallID, toolBargeInToolName),
				fmt.Sprintf(`{"type":"response.function_call_arguments.done","response_id":%q,"call_id":%q,"name":%q,"arguments":%q}`, response.ID, toolBargeInCallID, toolBargeInToolName, toolBargeInToolArguments),
				fmt.Sprintf(`{"type":"response.done","response":{"id":%q,"status":"completed"}}`, response.ID),
			)
			response.TerminalSent = true
			s.active = nil
			s.toolOutstanding = true
			s.recordMilestoneLocked("tool_call_issued")
		case 2:
			if s.toolResultCount != 1 {
				s.protocolErrs = append(s.protocolErrs, "continuation response created without exactly one tool result")
			}
			s.recordMilestoneLocked("continuation_issued")
			events = append(events,
				plainSpeechAudioDelta(response.ID, 52),
				fmt.Sprintf(`{"type":"response.output_audio.done","response_id":%q}`, response.ID),
				fmt.Sprintf(`{"type":"response.done","response":{"id":%q,"status":"completed"}}`, response.ID),
			)
			response.TerminalSent = true
			s.active = nil
		case 3:
			s.recordMilestoneLocked("later_response_issued")
			events = append(events,
				plainSpeechAudioDelta(response.ID, 53),
				fmt.Sprintf(`{"type":"response.output_audio.done","response_id":%q}`, response.ID),
				fmt.Sprintf(`{"type":"response.done","response":{"id":%q,"status":"completed"}}`, response.ID),
			)
			response.TerminalSent = true
			s.active = nil
		default:
			s.protocolErrs = append(s.protocolErrs, fmt.Sprintf("unexpected response ordinal %d", ordinal))
		}
	case "response.cancel":
		if s.active == nil {
			s.protocolErrs = append(s.protocolErrs, "response.cancel without active response")
			break
		}
		response := s.active
		response.CancelCount++
		s.active = nil
		events = append(events, fmt.Sprintf(`{"type":"response.done","response":{"id":%q,"status":"cancelled"}}`, response.ID))
		response.TerminalSent = true
	default:
		s.protocolErrs = append(s.protocolErrs, fmt.Sprintf("unexpected client event %q", envelope.Type))
	}
	s.mu.Unlock()

	for _, event := range events {
		s.sendEvent(event)
	}
	return nil
}

func (c *toolBargeInConn) Close() error {
	c.server.observeTransportClose()
	c.server.shutdown()
	return nil
}

type toolBargeInRun struct {
	capture  gwtesting.SessionCapture
	trace    *toolBargeInTrace
	server   *toolBargeInServer
	executor *toolBargeInExecutor
	err      error
}

type toolBargeInExecutor struct {
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
	releaseOnce sync.Once

	mu       sync.Mutex
	calls    []messages.ToolCall
	returned []messages.ToolCallResponse
}

func newToolBargeInExecutor() *toolBargeInExecutor {
	return &toolBargeInExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (e *toolBargeInExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.mu.Lock()
	e.calls = append(e.calls, call)
	e.mu.Unlock()
	e.startedOnce.Do(func() { close(e.started) })
	select {
	case <-e.release:
		response := messages.ToolCallResponse{
			ToolCallID: call.ID,
			Name:       call.Name,
			Content:    toolBargeInToolResult,
		}
		e.mu.Lock()
		e.returned = append(e.returned, response)
		e.mu.Unlock()
		return response, nil
	case <-ctx.Done():
		return messages.ToolCallResponse{}, ctx.Err()
	}
}

func (e *toolBargeInExecutor) releaseResult() {
	e.releaseOnce.Do(func() { close(e.release) })
}

func (e *toolBargeInExecutor) snapshot() ([]messages.ToolCall, []messages.ToolCallResponse) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]messages.ToolCall(nil), e.calls...), append([]messages.ToolCallResponse(nil), e.returned...)
}

func runToolBargeInCLI(t *testing.T) toolBargeInRun {
	t.Helper()
	trace := newToolBargeInTrace()
	server := newToolBargeInServer()
	executor := newToolBargeInExecutor()
	t.Cleanup(server.shutdown)
	recorder := gwtesting.NewRecordingWebSocketDialer(server, "openai", "gpt-realtime")
	sessionInferencer, err := services.NewOpenAIRealtimeSessionInferencerWithOptions(
		config.OpenAIConfig{APIKey: "test-key", Model: "gpt-realtime", BaseURL: "wss://hermetic.openai.test/v1/realtime"},
		oaiprovider.WithWebSocketDialer(recorder),
		oaiprovider.WithClientOwnedAudioTurnBoundaries(),
	)
	if err != nil {
		t.Fatalf("create hermetic OpenAI session inferencer: %v", err)
	}
	agentCLI, err := wire.InitializeMockAgentCLIWithSessionInferencer(
		executor,
		&mockInferencer{response: "stateless inferencer should not be called"},
		sessionInferencer,
	)
	if err != nil {
		t.Fatalf("initialize tool barge-in CLI: %v", err)
	}
	agentCLI.SetSessionStreamObserver(func(msg messages.StreamMessage) {
		trace.observe(msg)
	})
	root := agentCLI.Generate()
	root.SetIn(newToolBargeInAudioReader(server, executor, trace))
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
			runErr = fmt.Errorf("tool-call barge-in CLI command await timed out at %s: %w", plainSpeechRunTimeout, probe.ErrBargeInWait)
		}
	}
	return toolBargeInRun{
		capture:  recorder.Capture(),
		trace:    trace,
		server:   server,
		executor: executor,
		err:      runErr,
	}
}

func toolBargeInResponseID(ordinal int) string {
	switch ordinal {
	case 1:
		return toolBargeInResponseOne
	case 2:
		return toolBargeInResponseTwo
	case 3:
		return toolBargeInResponseThree
	default:
		return fmt.Sprintf("response-tool-barge-in-%d", ordinal)
	}
}

func toolBargeInContract() probe.BargeInContract {
	return probe.BargeInContract{
		Inputs: []probe.BargeInInputExpectation{
			{ID: "input-1", TurnID: "turn-1"},
			{ID: "input-2", TurnID: "turn-2"},
		},
		Responses: []probe.BargeInResponseExpectation{
			{
				ID: toolBargeInResponseOne, InputID: "input-1", TurnID: "turn-1",
				Disposition: probe.BargeInDispositionCompleted, ForbidCancel: true, RequireOutput: true,
			},
			{
				ID: toolBargeInResponseTwo, InputID: "input-1", TurnID: "turn-1",
				Disposition: probe.BargeInDispositionCompleted, ForbidCancel: true, RequireOutput: true, RequireContinuation: true,
			},
			{
				ID: toolBargeInResponseThree, InputID: "input-2", TurnID: "turn-2",
				Disposition: probe.BargeInDispositionCompleted, ForbidCancel: true, RequireOutput: true, RequireContinuation: true,
			},
		},
		Tools: []probe.BargeInToolExpectation{
			{
				ID: toolBargeInCallID, ResponseID: toolBargeInResponseOne, TurnID: "turn-1",
				Disposition: probe.BargeInDispositionDelivered, ForbidResultAfterCancel: true,
			},
		},
		RequireSessionTerminal: true,
	}
}

type toolBargeInResponseIdentity struct {
	stable   string
	inputID  string
	turnID   string
	ordinal  int
	terminal bool
}

type toolBargeInToolIdentity struct {
	responseID string
	turnID     string
}

// normalizeToolBargeInCapture translates raw OpenAI records at the adapter
// boundary. The ledger sees only stable identities; provider response and call
// IDs are used for lookup and never appear in oracle diagnostics.
type toolBargeInCaptureAdapter struct {
	ledger *probe.BargeInLedger

	nextSequence       int
	inputOrdinal       int
	currentInput       string
	lastCommittedInput string
	responseOrdinal    int
	providerResponses  map[string]toolBargeInResponseIdentity
	responseByProvider map[string]string
	tools              map[string]toolBargeInToolIdentity
}

func normalizeToolBargeInCapture(capture gwtesting.SessionCapture) *probe.BargeInLedger {
	adapter := toolBargeInCaptureAdapter{
		ledger:             probe.NewBargeInLedger(),
		providerResponses:  make(map[string]toolBargeInResponseIdentity),
		responseByProvider: make(map[string]string),
		tools:              make(map[string]toolBargeInToolIdentity),
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

func (a *toolBargeInCaptureAdapter) nextEventSequence() int {
	a.nextSequence++
	return a.nextSequence
}

func (a *toolBargeInCaptureAdapter) observe(record gwtesting.CapturedSessionEvent) {
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
		stableID := toolBargeInResponseID(a.responseOrdinal)
		owner := a.lastCommittedInput
		identity := toolBargeInResponseIdentity{
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
		if a.responseOrdinal > 1 {
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
	case "response.output_item.added":
		if record.Direction != gwtesting.DirectionServerToClient || plainSpeechJSONField(payload, "item.type") != "function_call" {
			return
		}
		providerResponseID := plainSpeechJSONField(payload, "response_id", "response.id")
		stableResponseID := a.responseByProvider[providerResponseID]
		identity := a.providerResponses[providerResponseID]
		callID := plainSpeechJSONField(payload, "item.call_id", "item.id")
		a.tools[callID] = toolBargeInToolIdentity{responseID: stableResponseID, turnID: identity.turnID}
		a.ledger.Observe(probe.BargeInEvent{
			Sequence:   a.nextEventSequence(),
			Kind:       probe.BargeInEventToolCall,
			ResponseID: stableResponseID,
			TurnID:     identity.turnID,
			ToolCallID: callID,
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
	case "conversation.item.create":
		if record.Direction != gwtesting.DirectionClientToServer || plainSpeechJSONField(payload, "item.type") != "function_call_output" {
			return
		}
		callID := plainSpeechJSONField(payload, "item.call_id")
		tool := a.tools[callID]
		a.ledger.Observe(probe.BargeInEvent{
			Sequence:    a.nextEventSequence(),
			Kind:        probe.BargeInEventToolResult,
			ResponseID:  tool.responseID,
			TurnID:      tool.turnID,
			ToolCallID:  callID,
			Disposition: probe.BargeInDispositionDelivered,
		})
	}
}

func (a *toolBargeInCaptureAdapter) activeResponse() toolBargeInResponseIdentity {
	for ordinal := a.responseOrdinal; ordinal > 0; ordinal-- {
		for _, identity := range a.providerResponses {
			if identity.ordinal == ordinal && !identity.terminal {
				return identity
			}
		}
	}
	return toolBargeInResponseIdentity{}
}

func validateToolBargeInCapture(capture gwtesting.SessionCapture) error {
	return normalizeToolBargeInCapture(capture).Validate(toolBargeInContract())
}

func toolBargeInRecordIndex(capture gwtesting.SessionCapture, match func(gwtesting.CapturedSessionEvent) bool, occurrence int) int {
	return plainSpeechRecordIndex(capture, match, occurrence)
}

func toolBargeInClientRecordIndex(capture gwtesting.SessionCapture, eventType string, occurrence int) int {
	return toolBargeInRecordIndex(capture, func(record gwtesting.CapturedSessionEvent) bool {
		return record.Direction == gwtesting.DirectionClientToServer && record.Type == eventType
	}, occurrence)
}

func toolBargeInResponseRecordIndex(capture gwtesting.SessionCapture, eventType, responseID string) int {
	return toolBargeInRecordIndex(capture, func(record gwtesting.CapturedSessionEvent) bool {
		return record.Direction == gwtesting.DirectionServerToClient && record.Type == eventType && plainSpeechRecordResponseID(record) == responseID
	}, 0)
}

func toolBargeInResultRecordIndex(capture gwtesting.SessionCapture, occurrence int) int {
	return toolBargeInRecordIndex(capture, func(record gwtesting.CapturedSessionEvent) bool {
		return record.Direction == gwtesting.DirectionClientToServer && record.Type == "conversation.item.create" && plainSpeechJSONField(plainSpeechRecordPayload(record), "item.type") == "function_call_output"
	}, occurrence)
}

func toolBargeInResponseDoneWithStatus(capture *gwtesting.SessionCapture, responseID, status string) bool {
	index := toolBargeInResponseRecordIndex(*capture, "response.done", responseID)
	if index < 0 {
		return false
	}
	value := map[string]any{}
	if json.Unmarshal(plainSpeechRecordPayload(capture.Records[index]), &value) != nil {
		return false
	}
	response, _ := value["response"].(map[string]any)
	if response == nil {
		response = map[string]any{}
		value["response"] = response
	}
	response["status"] = status
	encoded, err := json.Marshal(value)
	if err != nil {
		return false
	}
	capture.Records[index].Payload = encoded
	return true
}

func toolBargeInInsertCancellationBeforeTerminal(capture *gwtesting.SessionCapture) bool {
	callIndex := toolBargeInRecordIndex(*capture, func(record gwtesting.CapturedSessionEvent) bool {
		return record.Direction == gwtesting.DirectionServerToClient && record.Type == "response.function_call_arguments.done" && plainSpeechRecordResponseID(record) == toolBargeInResponseOne
	}, 0)
	if callIndex < 0 {
		return false
	}
	cancel := gwtesting.CapturedSessionEvent{
		Direction:   gwtesting.DirectionClientToServer,
		Type:        "response.cancel",
		PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
		Payload:     json.RawMessage(`{"type":"response.cancel"}`),
	}
	insertPlainSpeechRecordAfter(capture, callIndex, cancel)
	return true
}

func countToolBargeInAudio(events []toolBargeInStreamEvent, ordinal int) int {
	count := 0
	for _, event := range events {
		if event.Ordinal == ordinal && event.Type == messages.StreamTypeAudioDelta && event.Bytes > 0 {
			count++
		}
	}
	return count
}

func toolBargeInMilestoneOrder(milestones []string, names ...string) error {
	positions := make(map[string]int, len(milestones))
	for index, milestone := range milestones {
		if _, exists := positions[milestone]; !exists {
			positions[milestone] = index
		}
	}
	for index, name := range names {
		position, exists := positions[name]
		if !exists {
			return fmt.Errorf("provider milestone %q was not observed: %v", name, milestones)
		}
		if index > 0 {
			previous := positions[names[index-1]]
			if previous >= position {
				return fmt.Errorf("provider milestone order = %v, want %q before %q", milestones, names[index-1], name)
			}
		}
	}
	return nil
}

func TestS2SLiveBargeInOutstandingToolCallThroughCLI(t *testing.T) {
	run := runToolBargeInCLI(t)
	if run.err != nil {
		dialCount, responses, resultCount, overlap, pending, protocolErrs, milestones := run.server.snapshot()
		t.Fatalf("tool-call barge-in CLI returned %v; dials=%d responses=%v result_count=%d overlap=%t close_pending=%t protocol_errors=%v milestones=%v stream=%v", run.err, dialCount, responses, resultCount, overlap, pending, protocolErrs, milestones, run.trace.snapshot())
	}
	if err := validateToolBargeInCapture(run.capture); err != nil {
		t.Fatalf("tool-call barge-in identity-aware ledger failed: %v; stream=%v", err, run.trace.snapshot())
	}

	calls, returned := run.executor.snapshot()
	if len(calls) != 1 || calls[0].ID != toolBargeInCallID || calls[0].Name != toolBargeInToolName || calls[0].Arguments != toolBargeInToolArguments {
		t.Fatalf("tool calls = %+v, want exactly one named correlated call", calls)
	}
	if len(returned) != 1 || returned[0].ToolCallID != toolBargeInCallID || returned[0].Content != toolBargeInToolResult {
		t.Fatalf("tool results = %+v, want exactly one correlated sentinel result", returned)
	}

	dialCount, responses, resultCount, overlap, pending, protocolErrs, milestones := run.server.snapshot()
	if dialCount != 1 || len(responses) != 3 || resultCount != 1 || !overlap || pending || len(protocolErrs) != 0 {
		t.Fatalf("provider observations = dials:%d responses:%d result_count:%d overlap:%t close_pending:%t protocol_errors:%v; want one session, three terminal responses, one result, overlap, and no pending close", dialCount, len(responses), resultCount, overlap, pending, protocolErrs)
	}
	for _, response := range responses {
		if response.CancelCount != 0 || !response.TerminalSent {
			t.Fatalf("provider response %q = cancel:%d terminal:%t, want completion without cancellation", response.ID, response.CancelCount, response.TerminalSent)
		}
	}
	if err := toolBargeInMilestoneOrder(milestones, "tool_call_issued", "speech_overlap", "tool_result_received", "continuation_issued", "later_input_committed", "later_response_issued", "transport_close"); err != nil {
		t.Fatal(err)
	}
	if countToolBargeInAudio(run.trace.snapshot(), 2) == 0 || countToolBargeInAudio(run.trace.snapshot(), 3) == 0 {
		t.Fatalf("spoken continuation/later response audio = %v, want non-empty output for responses 2 and 3", run.trace.snapshot())
	}

	toolCallIndex := toolBargeInRecordIndex(run.capture, func(record gwtesting.CapturedSessionEvent) bool {
		return record.Direction == gwtesting.DirectionServerToClient && record.Type == "response.output_item.added" && plainSpeechJSONField(plainSpeechRecordPayload(record), "item.type") == "function_call"
	}, 0)
	overlapAppendIndex := toolBargeInClientRecordIndex(run.capture, "input_audio_buffer.append", 1)
	firstTerminalIndex := toolBargeInResponseRecordIndex(run.capture, "response.done", toolBargeInResponseOne)
	if toolCallIndex < 0 || overlapAppendIndex < 0 || firstTerminalIndex < 0 || !(toolCallIndex < firstTerminalIndex && firstTerminalIndex < overlapAppendIndex) {
		t.Fatalf("wire collision order = tool_call:%d first_terminal:%d overlap_append:%d; want tool call < owning terminal < non-empty interrupting speech", toolCallIndex, firstTerminalIndex, overlapAppendIndex)
	}
}

func TestS2SLiveBargeInOutstandingToolCallOracleRejectsMutations(t *testing.T) {
	run := runToolBargeInCLI(t)
	if run.err != nil {
		t.Fatalf("build positive tool-call capture: %v; stream=%v", run.err, run.trace.snapshot())
	}
	cases := []struct {
		name     string
		mutate   func(*gwtesting.SessionCapture)
		contract func() probe.BargeInContract
		want     string
	}{
		{
			name: "premature clean close",
			mutate: func(capture *gwtesting.SessionCapture) {
				removePlainSpeechRecords(capture, func(record gwtesting.CapturedSessionEvent) bool {
					return record.Direction == gwtesting.DirectionServerToClient && record.Type == "response.done" && plainSpeechRecordResponseID(record) == toolBargeInResponseThree
				})
			},
			want: `response "response-tool-barge-in-3" has unresolved terminal disposition`,
		},
		{
			name: "lost result",
			mutate: func(capture *gwtesting.SessionCapture) {
				removePlainSpeechRecords(capture, func(record gwtesting.CapturedSessionEvent) bool {
					return record.Direction == gwtesting.DirectionClientToServer && record.Type == "conversation.item.create" && plainSpeechJSONField(plainSpeechRecordPayload(record), "item.type") == "function_call_output"
				})
			},
			want: `tool call "call-tool-barge-in" has unresolved result disposition`,
		},
		{
			name: "orphan result ID",
			mutate: func(capture *gwtesting.SessionCapture) {
				index := toolBargeInResultRecordIndex(*capture, 0)
				if index < 0 {
					return
				}
				value := map[string]any{}
				if json.Unmarshal(plainSpeechRecordPayload(capture.Records[index]), &value) != nil {
					return
				}
				item, _ := value["item"].(map[string]any)
				item["call_id"] = "call-orphaned"
				encoded, _ := json.Marshal(value)
				capture.Records[index].Payload = encoded
			},
			want: `tool result references unknown call "call-orphaned"`,
		},
		{
			name: "duplicate delivery",
			mutate: func(capture *gwtesting.SessionCapture) {
				index := toolBargeInResultRecordIndex(*capture, 0)
				if index >= 0 {
					insertPlainSpeechRecordAfter(capture, index, capture.Records[index])
				}
			},
			want: `tool call "call-tool-barge-in" received duplicate result disposition`,
		},
		{
			name: "post-cancel delivery",
			mutate: func(capture *gwtesting.SessionCapture) {
				if !toolBargeInInsertCancellationBeforeTerminal(capture) {
					return
				}
				toolBargeInResponseDoneWithStatus(capture, toolBargeInResponseOne, "cancelled")
			},
			contract: func() probe.BargeInContract {
				contract := toolBargeInContract()
				contract.Responses[0].Disposition = probe.BargeInDispositionCancelled
				contract.Responses[0].RequireCancel = true
				contract.Responses[0].ForbidCancel = false
				return contract
			},
			want: `tool result for "call-tool-barge-in" was delivered after response cancellation`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			capture := clonePlainSpeechCapture(run.capture)
			if testCase.mutate != nil {
				testCase.mutate(&capture)
			}
			contract := toolBargeInContract()
			if testCase.contract != nil {
				contract = testCase.contract()
			}
			err := normalizeToolBargeInCapture(capture).Validate(contract)
			if err == nil {
				t.Fatal("mutation unexpectedly passed the identity-aware ledger")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("mutation error = %v, want detail %q", err, testCase.want)
			}
		})
	}
}

func TestS2SLiveBargeInOutstandingToolCallWaitIsBounded(t *testing.T) {
	ledger := probe.NewBargeInLedger()
	ledger.Observe(probe.BargeInEvent{
		Sequence: 1, Kind: probe.BargeInEventInputAppend,
		InputID: "input-tool-wait", TurnID: "turn-tool-wait", AppendGroupID: "input-tool-wait",
		Bytes: 2, NonEmpty: true,
	})
	start := time.Now()
	err := ledger.WaitFor(context.Background(), "named tool continuation", make(chan struct{}), 20*time.Millisecond)
	if err == nil {
		t.Fatal("missing named-tool continuation gate unexpectedly passed")
	}
	var waitErr *probe.BargeInWaitError
	if !errors.As(err, &waitErr) || !errors.Is(err, probe.ErrBargeInWait) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait error = %v, want bounded barge-in wait with deadline identity", err)
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("named-tool continuation gate took %s, want a bounded return", elapsed)
	}
	for _, want := range []string{"named tool continuation", "1:input.append", "input-tool-wait:commit", "session:terminal"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("wait error = %v, want diagnostic %q", err, want)
		}
	}
}
