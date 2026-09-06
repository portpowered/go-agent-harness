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

	events             chan []byte
	closed             chan struct{}
	closeOnce          sync.Once
	transportClosed    chan struct{}
	transportCloseOnce sync.Once
	dialOnce           sync.Once
	dialCount          int

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
		events:          make(chan []byte, 128),
		closed:          make(chan struct{}),
		transportClosed: make(chan struct{}),
		speechSignalCh:  make(chan struct{}),
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
	pending := s.toolResultCount != 1 || s.active != nil
	for _, response := range s.responses {
		if !response.TerminalSent {
			pending = true
		}
	}
	s.clientClosePending = pending
	s.recordMilestoneLocked("transport_close")
	s.mu.Unlock()
	s.transportCloseOnce.Do(func() { close(s.transportClosed) })
}

func (s *toolBargeInServer) waitForTransportClose(ctx context.Context) error {
	return probe.NewBargeInLedger().WaitFor(
		ctx,
		"transport close observation",
		s.transportClosed,
		toolBargeInGateTimeout,
	)
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
	sessionInferencer, err := servicetest.NewOpenAIRealtimeSessionInferencerWithOptions(
		config.OpenAIConfig{APIKey: "test-key", Model: "gpt-realtime", BaseURL: "wss://hermetic.openai.test/v1/realtime"},
		oaiprovider.WithWebSocketDialer(recorder),
		oaiprovider.WithClientOwnedAudioTurnBoundaries(),
	)
	if err != nil {
		t.Fatalf("create hermetic OpenAI session inferencer: %v", err)
	}
	agentCLI, err := wire.InitializeMockAgentCLIWithPorts(
		wire.NewToolServicePort(toolBargeInCapabilities(executor)),
		wire.NewPortSwap(wire.PortInferencer, &mockInferencer{response: "stateless inferencer should not be called"}),
		wire.NewPortSwap(wire.PortSessionInferencer, sessionInferencer),
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
	closeCtx, closeCancel := context.WithTimeout(context.Background(), toolBargeInGateTimeout)
	closeErr := server.waitForTransportClose(closeCtx)
	closeCancel()
	if closeErr != nil {
		if runErr == nil {
			runErr = closeErr
		} else {
			runErr = errors.Join(runErr, closeErr)
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
