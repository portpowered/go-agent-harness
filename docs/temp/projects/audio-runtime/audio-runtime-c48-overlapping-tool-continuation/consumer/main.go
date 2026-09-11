// Command c48-consumer is a source-file-built public-runtime overlap probe.
// It intentionally imports only exported workspace contracts; the fixture is
// a deterministic Session implementation rather than a Realtime provider.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const (
	maxTraceEvents = 4096
	maxReportBytes = 262144
)

type callSpec struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Result    string `json:"result"`
}

var expectedCalls = []callSpec{
	{ID: "call-000-alpha", Name: "lookup_alpha", Arguments: `{"key":"alpha","turn":0}`, Result: "result:lookup_alpha:000"},
	{ID: "call-000-beta", Name: "lookup_beta", Arguments: `{"key":"beta","turn":0}`, Result: "result:lookup_beta:000"},
	{ID: "call-001-alpha", Name: "lookup_alpha", Arguments: `{"key":"alpha","turn":1}`, Result: "result:lookup_alpha:001"},
	{ID: "call-001-beta", Name: "lookup_beta", Arguments: `{"key":"beta","turn":1}`, Result: "result:lookup_beta:001"},
}

type fixtureFile struct {
	Schema   string     `json:"schema"`
	Scenario string     `json:"scenario"`
	Calls    []callSpec `json:"calls"`
}

type toolRecord struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Result    string `json:"result"`
}

type wireRecord struct {
	Sequence   int    `json:"sequence"`
	Direction  string `json:"direction"`
	Type       string `json:"type"`
	ResponseID string `json:"response_id,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

type overlapReport struct {
	ActiveResponseIDs              []string `json:"active_response_ids"`
	BothResponsesBeforeFirstResult bool     `json:"both_responses_before_first_result"`
	FirstResultAfterAllCalls       bool     `json:"first_result_after_all_calls"`
	CrossRoutingVerified           bool     `json:"cross_routing_verified"`
	ExactlyOnceVerified            bool     `json:"exactly_once_verified"`
}

type eventReport struct {
	Count         int            `json:"count"`
	ByKind        map[string]int `json:"by_kind"`
	OverflowDrops uint64         `json:"overflow_drops"`
}

type terminalReport struct {
	Reason      string `json:"reason,omitempty"`
	Provenance  string `json:"provenance,omitempty"`
	OutputState string `json:"output_state,omitempty"`
}

type report struct {
	Schema                string         `json:"schema"`
	Scenario              string         `json:"scenario"`
	Control               string         `json:"control,omitempty"`
	SourceRevision        string         `json:"source_revision"`
	FixtureSHA256         string         `json:"fixture_sha256"`
	ConsumerSurface       string         `json:"consumer_surface"`
	Responses             []string       `json:"responses,omitempty"`
	ToolCalls             []toolRecord   `json:"tool_calls,omitempty"`
	ToolResults           []toolRecord   `json:"tool_results,omitempty"`
	OutboundToolResultIDs []string       `json:"outbound_tool_result_ids,omitempty"`
	ReverseCompletion     []string       `json:"reverse_completion,omitempty"`
	WireTrace             []wireRecord   `json:"wire_trace,omitempty"`
	Events                eventReport    `json:"events"`
	Terminal              terminalReport `json:"terminal"`
	Overlap               overlapReport  `json:"overlap"`
	CleanShutdown         bool           `json:"clean_shutdown"`
	TraceComplete         bool           `json:"trace_complete"`
	Error                 string         `json:"error,omitempty"`
}

type eventCollector struct {
	mu            sync.Mutex
	responses     []string
	responseSet   map[string]struct{}
	byKind        map[string]int
	traceCount    int
	overflowDrops uint64
	traceComplete bool
	terminal      terminalReport
}

func newEventCollector() *eventCollector {
	return &eventCollector{
		responseSet:   make(map[string]struct{}),
		byKind:        make(map[string]int),
		traceComplete: true,
	}
}

func (c *eventCollector) publish(_ context.Context, event session.LiveEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byKind[event.Kind]++
	if event.Kind == string(session.LiveEventOverflow) {
		c.overflowDrops += event.Dropped
	}
	if c.traceCount >= maxTraceEvents {
		c.traceComplete = false
		return nil
	}
	c.traceCount++
	if event.Message != nil && event.Message.Type == messages.StreamTypeMessageStart && event.Message.ResponseID != "" {
		if _, seen := c.responseSet[event.Message.ResponseID]; !seen {
			c.responseSet[event.Message.ResponseID] = struct{}{}
			c.responses = append(c.responses, event.Message.ResponseID)
		}
	}
	if event.Terminal != nil {
		c.terminal = terminalReport{
			Reason:      string(event.Terminal.TerminalReason),
			Provenance:  string(event.Terminal.TerminalProvenance),
			OutputState: string(event.Terminal.OutputState),
		}
	}
	return nil
}

func (c *eventCollector) snapshot() (eventReport, []string, terminalReport, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	responses := append([]string(nil), c.responses...)
	return eventReport{Count: c.traceCount, ByKind: cloneCounts(c.byKind), OverflowDrops: c.overflowDrops}, responses, c.terminal, c.traceComplete
}

func cloneCounts(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

type fixtureSession struct {
	receive   *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	closeOnce sync.Once

	mu                       sync.Mutex
	control                  string
	wire                     []wireRecord
	inboundCalls             []toolRecord
	outboundResults          []toolRecord
	seenResults              map[string]struct{}
	activeIDs                []string
	activeSet                map[string]struct{}
	receivedByID             map[string]int
	firstResultSeen          bool
	firstResultAfterAllCalls bool
	finalZeroSent            bool
	finalOneSent             bool
	err                      error
}

func newFixtureSession(control string) *fixtureSession {
	return &fixtureSession{
		receive:      messages.NewTypedBuffer[messages.StreamMessage](128),
		done:         make(chan struct{}),
		control:      control,
		seenResults:  make(map[string]struct{}),
		activeSet:    make(map[string]struct{}),
		receivedByID: make(map[string]int),
	}
}

func (s *fixtureSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	if s.hasError() {
		return false
	}
	s.recordWire("client_to_server", msg)
	switch msg.Type {
	case messages.StreamTypeTextDelta:
		if s.callCount() == 0 {
			s.emitInitial()
		}
	case messages.StreamTypeToolCallEnd:
		if err := s.acceptToolResult(msg); err != nil {
			s.setError(err)
			return false
		}
	case messages.StreamTypeResponseCreate:
		if err := s.emitReadyContinuation(); err != nil {
			s.setError(err)
			return false
		}
	}
	return !s.hasError()
}

func (s *fixtureSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.receive }
func (s *fixtureSession) Done() <-chan struct{}                                  { return s.done }

func (s *fixtureSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

func (s *fixtureSession) recordWire(direction string, msg messages.StreamMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.wire) >= maxTraceEvents {
		if s.err == nil {
			s.err = fmt.Errorf("fixture wire trace exceeded %d events", maxTraceEvents)
		}
		return
	}
	record := wireRecord{Sequence: len(s.wire) + 1, Direction: direction, Type: string(msg.Type), ResponseID: msg.ResponseID, ToolCallID: msg.ToolCallId}
	if value, ok := msg.Value.(*messages.ToolCallEndValue); ok && value != nil && record.ToolCallID == "" {
		record.ToolCallID = value.ToolCallID
	}
	s.wire = append(s.wire, record)
}

func (s *fixtureSession) queue(messagesToQueue ...messages.StreamMessage) {
	for _, msg := range messagesToQueue {
		if s.hasError() {
			return
		}
		s.recordWire("server_to_client", msg)
		s.recordInbound(msg)
		if !s.receive.WriteWaitContextOrDone(context.Background(), s.done, msg).OK() {
			s.setError(fmt.Errorf("fixture receive queue stopped while publishing %s", msg.Type))
			return
		}
	}
}

func (s *fixtureSession) emitInitial() {
	s.queue(
		messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "tool-resp-000", Value: messages.NewMessageStartValue()},
		toolCallMessage("tool-resp-000", expectedCalls[0]),
		toolCallMessage("tool-resp-000", expectedCalls[1]),
		messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "tool-resp-001", Value: messages.NewMessageStartValue()},
		toolCallMessage("tool-resp-001", expectedCalls[2]),
		toolCallMessage("tool-resp-001", expectedCalls[3]),
		messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "tool-resp-000", Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	)
}

func toolCallMessage(responseID string, spec callSpec) messages.StreamMessage {
	return messages.StreamMessage{
		Type:       messages.StreamTypeToolCallEnd,
		Role:       messages.RoleAssistant,
		ResponseID: responseID,
		ToolCallId: spec.ID,
		Value:      messages.NewToolCallEndValue(spec.ID, spec.Name, spec.Arguments),
	}
}

func (s *fixtureSession) acceptToolResult(msg messages.StreamMessage) error {
	value, ok := msg.Value.(*messages.ToolCallEndValue)
	if !ok || value == nil {
		return errors.New("tool result omitted ToolCallEndValue")
	}
	id := strings.TrimSpace(value.ToolCallID)
	if id == "" {
		id = strings.TrimSpace(msg.ToolCallId)
	}
	spec, ok := specForID(id)
	if !ok {
		return fmt.Errorf("unexpected tool result %q", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.firstResultSeen {
		s.firstResultSeen = true
		s.firstResultAfterAllCalls = len(s.inboundCalls) == len(expectedCalls)
	}
	if s.control == "swapped" {
		return fmt.Errorf("swapped result rejected for %s: expected another call identity", id)
	}
	if s.control == "duplicate" {
		return fmt.Errorf("duplicate result rejected for %s", id)
	}
	if _, seen := s.seenResults[id]; seen {
		return fmt.Errorf("duplicate tool result %s", id)
	}
	s.seenResults[id] = struct{}{}
	s.receivedByID[id]++
	s.outboundResults = append(s.outboundResults, toolRecord{ID: id, Name: spec.Name, Result: value.Arguments})
	return nil
}

func (s *fixtureSession) emitReadyContinuation() error {
	s.mu.Lock()
	if s.control == "missing" && !s.finalZeroSent {
		callID := expectedCalls[1].ID
		s.mu.Unlock()
		return fmt.Errorf("missing result rejected for %s", callID)
	}
	readyZero := s.receivedByID[expectedCalls[0].ID] == 1 && s.receivedByID[expectedCalls[1].ID] == 1 && !s.finalZeroSent
	readyOne := s.receivedByID[expectedCalls[2].ID] == 1 && s.receivedByID[expectedCalls[3].ID] == 1 && !s.finalOneSent
	if readyZero {
		s.finalZeroSent = true
		s.mu.Unlock()
		s.emitFinal("final-resp-000", "answer-000")
		// Response 1 remains open until response 0's continuation has ended.
		s.queue(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "tool-resp-001", Value: messages.NewMessageEndValue(messages.TokenUsage{})})
		return nil
	}
	if readyOne {
		s.finalOneSent = true
		s.mu.Unlock()
		s.emitFinal("final-resp-001", "answer-001")
		return nil
	}
	s.mu.Unlock()
	return errors.New("response create arrived before its two exact tool results")
}

func (s *fixtureSession) emitFinal(responseID, text string) {
	s.queue(
		messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageStartValue()},
		messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewTextStartValue()},
		messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewTextDeltaValue(text)},
		messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewTextEndValue()},
		messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageEndValueWithTerminal(messages.TokenUsage{}, messages.TerminalReasonProviderAuthoredCompletion, messages.TerminalProvenanceProvider, messages.TerminalOutputComplete)},
	)
}

func (s *fixtureSession) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.inboundCalls)
}

func (s *fixtureSession) recordInbound(msg messages.StreamMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if msg.Type == messages.StreamTypeMessageStart && strings.HasPrefix(msg.ResponseID, "tool-resp-") {
		if _, seen := s.activeSet[msg.ResponseID]; !seen {
			s.activeSet[msg.ResponseID] = struct{}{}
			s.activeIDs = append(s.activeIDs, msg.ResponseID)
		}
	}
	if msg.Type != messages.StreamTypeToolCallEnd || msg.Role != messages.RoleAssistant {
		return
	}
	value, ok := msg.Value.(*messages.ToolCallEndValue)
	if !ok || value == nil {
		return
	}
	spec, ok := specForID(value.ToolCallID)
	if !ok {
		return
	}
	if _, seen := s.activeSet[msg.ResponseID]; !seen && strings.HasPrefix(msg.ResponseID, "tool-resp-") {
		s.activeSet[msg.ResponseID] = struct{}{}
		s.activeIDs = append(s.activeIDs, msg.ResponseID)
	}
	s.inboundCalls = append(s.inboundCalls, toolRecord{ID: spec.ID, Name: spec.Name, Arguments: spec.Arguments, Result: spec.Result})
}

func (s *fixtureSession) hasError() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err != nil
}

func (s *fixtureSession) setError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
}

func (s *fixtureSession) snapshot() ([]wireRecord, []toolRecord, []toolRecord, []string, bool, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wire := append([]wireRecord(nil), s.wire...)
	calls := append([]toolRecord(nil), s.inboundCalls...)
	results := append([]toolRecord(nil), s.outboundResults...)
	active := append([]string(nil), s.activeIDs...)
	crossRouting := len(results) == len(expectedCalls)
	for index, result := range results {
		if index >= len(expectedCalls) || result.ID != expectedCalls[index].ID || result.Name != expectedCalls[index].Name || result.Result != expectedCalls[index].Result {
			crossRouting = false
			break
		}
	}
	return wire, calls, results, active, s.firstResultAfterAllCalls, crossRouting, s.err
}

type fixtureInferencer struct{ provider messages.Session }

func (i fixtureInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.provider, nil
}

type reverseToolExecutor struct {
	mu         sync.Mutex
	betaReady  map[int]chan struct{}
	betaClosed map[int]bool
	completed  []string
}

func newReverseToolExecutor() *reverseToolExecutor {
	return &reverseToolExecutor{betaReady: make(map[int]chan struct{}), betaClosed: make(map[int]bool)}
}

func (e *reverseToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	turn, suffix, err := parseCallID(call.ID)
	if err != nil {
		return messages.ToolCallResponse{}, err
	}
	e.mu.Lock()
	ready := e.betaReady[turn]
	if ready == nil {
		ready = make(chan struct{})
		e.betaReady[turn] = ready
	}
	if suffix == "beta" && !e.betaClosed[turn] {
		e.betaClosed[turn] = true
		close(ready)
	}
	e.mu.Unlock()
	if suffix == "alpha" {
		select {
		case <-ready:
		case <-ctx.Done():
			return messages.ToolCallResponse{}, ctx.Err()
		}
	}
	spec, ok := specForID(call.ID)
	if !ok {
		return messages.ToolCallResponse{}, fmt.Errorf("unknown tool call %s", call.ID)
	}
	e.mu.Lock()
	e.completed = append(e.completed, call.ID)
	e.mu.Unlock()
	return messages.ToolCallResponse{ToolCallID: call.ID, ContentParts: []messages.ContentPart{messages.TextPart{Text: spec.Result}}}, nil
}

func (e *reverseToolExecutor) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.completed...)
}

func parseCallID(id string) (int, string, error) {
	parts := strings.Split(id, "-")
	if len(parts) != 3 || parts[0] != "call" {
		return 0, "", fmt.Errorf("invalid tool call ID %q", id)
	}
	turn, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, "", fmt.Errorf("invalid tool call turn %q", id)
	}
	return turn, parts[2], nil
}

func specForID(id string) (callSpec, bool) {
	for _, spec := range expectedCalls {
		if spec.ID == id {
			return spec, true
		}
	}
	return callSpec{}, false
}

func loadFixture(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read fixture: %w", err)
	}
	var fixture fixtureFile
	if err := json.Unmarshal(raw, &fixture); err != nil {
		return "", fmt.Errorf("decode fixture: %w", err)
	}
	if fixture.Schema != "audio-runtime-c48.v1" || fixture.Scenario != "overlapping_tool_continuation" || len(fixture.Calls) != len(expectedCalls) {
		return "", errors.New("fixture schema or exact-call contract mismatch")
	}
	for index := range expectedCalls {
		if fixture.Calls[index] != expectedCalls[index] {
			return "", fmt.Errorf("fixture call %d mismatch", index)
		}
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func sourceRevision(value string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return "unspecified"
}

func run(fixturePath, source, control, artifactRoot string) (*report, error) {
	fixtureSHA, err := loadFixture(fixturePath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(artifactRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create artifact root: %w", err)
	}
	provider := newFixtureSession(control)
	executor := newReverseToolExecutor()
	collector := newEventCollector()
	base := clock.NewDeterministic(time.Date(2026, time.September, 10, 0, 0, 0, 0, time.UTC), time.Millisecond)
	service := sessionwire.NewLiveService(sessionwire.LiveDependencies{
		InferencerFactory: func(_ context.Context, request session.LiveRequest) (messages.SessionInferencer, error) {
			provider.queue(messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue(request.SessionID, "audio_inference")})
			return fixtureInferencer{provider: provider}, nil
		},
		ToolExecutor: executor,
		ToolDefinitions: []messages.ToolDefinition{
			{Name: "lookup_alpha", Description: "return the alpha fixture identity", Parameters: []messages.ToolParameter{{Name: "key", Type: "string", Required: true}, {Name: "turn", Type: "number", Required: true}}},
			{Name: "lookup_beta", Description: "return the beta fixture identity", Parameters: []messages.ToolParameter{{Name: "key", Type: "string", Required: true}, {Name: "turn", Type: "number", Required: true}}},
		},
		EventCapacity: 128,
		Clock:         base.Now,
		Scheduler:     clock.Real{},
	})
	runner, ok := service.(session.LiveRunner)
	if !ok {
		return nil, errors.New("public live service does not expose LiveRunner")
	}
	request := session.LiveRequest{
		SessionID:            "c48-overlap",
		ParticipantID:        "fixture",
		Provider:             "c48-deterministic",
		Model:                "c48-public-fixture",
		OpeningPrompt:        "c48 deterministic opening",
		OpeningPromptPresent: true,
		ToolNames:            []string{"lookup_alpha", "lookup_beta"},
		FinishAfterResponse:  true,
		ExpectedResponses:    2,
		MaxDuration:          3 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runErr := runner.RunLive(ctx, session.LiveRunOptions{Request: request, Events: session.LiveEventSinkFunc(collector.publish)})
	wire, calls, results, active, firstResultAfterAllCalls, crossRouting, fixtureErr := provider.snapshot()
	events, responses, terminal, traceComplete := collector.snapshot()
	value := &report{
		Schema:                "audio-runtime-c48.v1",
		Scenario:              "overlapping_tool_continuation",
		Control:               control,
		SourceRevision:        sourceRevision(source),
		FixtureSHA256:         fixtureSHA,
		ConsumerSurface:       "public-live-service+public-session-contracts",
		Responses:             responses,
		ToolCalls:             calls,
		ToolResults:           results,
		OutboundToolResultIDs: resultIDs(results),
		ReverseCompletion:     executor.snapshot(),
		WireTrace:             wire,
		Events:                events,
		Terminal:              terminal,
		TraceComplete:         traceComplete,
		CleanShutdown:         runErr == nil && fixtureErr == nil,
		Overlap: overlapReport{
			ActiveResponseIDs:              active,
			BothResponsesBeforeFirstResult: len(active) == 2 && active[0] == "tool-resp-000" && active[1] == "tool-resp-001" && firstResultAfterAllCalls,
			FirstResultAfterAllCalls:       firstResultAfterAllCalls,
			CrossRoutingVerified:           crossRouting,
			ExactlyOnceVerified:            exactlyOnce(results),
		},
	}
	if fixtureErr != nil {
		runErr = errors.Join(runErr, fixtureErr)
	}
	if control == "" {
		if runErr != nil {
			return value, runErr
		}
		if err := validatePositive(value); err != nil {
			return value, err
		}
		return value, nil
	}
	if runErr == nil {
		return value, fmt.Errorf("negative control %s unexpectedly passed", control)
	}
	return value, runErr
}

func resultIDs(results []toolRecord) []string {
	ids := make([]string, 0, len(results))
	for _, result := range results {
		ids = append(ids, result.ID)
	}
	return ids
}

func exactlyOnce(results []toolRecord) bool {
	if len(results) != len(expectedCalls) {
		return false
	}
	seen := make(map[string]struct{}, len(results))
	for _, result := range results {
		if _, ok := seen[result.ID]; ok {
			return false
		}
		seen[result.ID] = struct{}{}
	}
	return true
}

func validatePositive(value *report) error {
	if !value.CleanShutdown || !value.TraceComplete || value.Events.OverflowDrops != 0 {
		return errors.New("positive live run did not provide clean bounded public evidence")
	}
	if !value.Overlap.BothResponsesBeforeFirstResult || !value.Overlap.FirstResultAfterAllCalls || !value.Overlap.CrossRoutingVerified || !value.Overlap.ExactlyOnceVerified {
		return errors.New("positive overlap barrier or result correlation failed")
	}
	if len(value.ToolCalls) != len(expectedCalls) || len(value.ToolResults) != len(expectedCalls) {
		return fmt.Errorf("positive call/result count mismatch: calls=%d results=%d", len(value.ToolCalls), len(value.ToolResults))
	}
	for index, expected := range expectedCalls {
		if value.ToolCalls[index] != (toolRecord{ID: expected.ID, Name: expected.Name, Arguments: expected.Arguments, Result: expected.Result}) {
			return fmt.Errorf("positive tool call %d mismatch: %+v", index, value.ToolCalls[index])
		}
		if value.ToolResults[index] != (toolRecord{ID: expected.ID, Name: expected.Name, Result: expected.Result}) {
			return fmt.Errorf("positive tool result %d mismatch: %+v", index, value.ToolResults[index])
		}
	}
	if value.Responses == nil || len(value.Responses) != 4 || value.Responses[0] != "tool-resp-000" || value.Responses[1] != "tool-resp-001" || value.Responses[2] != "final-resp-000" || value.Responses[3] != "final-resp-001" {
		return fmt.Errorf("public response identity sequence mismatch: %v", value.Responses)
	}
	if value.Terminal.Reason != string(messages.TerminalReasonProviderAuthoredCompletion) || value.Terminal.Provenance != string(messages.TerminalProvenanceProvider) || value.Terminal.OutputState != string(messages.TerminalOutputComplete) {
		return fmt.Errorf("public terminal mismatch: %+v", value.Terminal)
	}
	return nil
}

func writeReport(path string, value *report) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if len(raw) > maxReportBytes {
		return fmt.Errorf("report exceeds %d bytes", maxReportBytes)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

func main() {
	fixturePath := flag.String("fixture", "", "fixture JSON path")
	outputPath := flag.String("output", "", "report JSON path")
	artifactRoot := flag.String("artifact-root", "", "bounded artifact directory")
	source := flag.String("source-revision", "", "source revision supplied by the driver")
	control := flag.String("control", "", "negative control: missing, duplicate, or swapped")
	flag.Parse()
	if *fixturePath == "" || *outputPath == "" || *artifactRoot == "" {
		fmt.Fprintln(os.Stderr, "fixture, output, and artifact-root are required")
		os.Exit(2)
	}
	value, err := run(*fixturePath, *source, *control, *artifactRoot)
	if value == nil {
		value = &report{Schema: "audio-runtime-c48.v1", Scenario: "overlapping_tool_continuation", Control: *control, SourceRevision: sourceRevision(*source)}
	}
	if err != nil {
		value.Error = err.Error()
	}
	if writeErr := writeReport(*outputPath, value); writeErr != nil {
		fmt.Fprintln(os.Stderr, writeErr)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
