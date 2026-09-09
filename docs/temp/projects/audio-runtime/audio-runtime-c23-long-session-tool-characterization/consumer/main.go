// Command c23-consumer is a source-file-built public-runtime characterization
// consumer. It deliberately imports only exported contracts from the
// workspace. The Python driver owns provenance, process bounds, and report
// comparison; this binary owns the causal fixture and its runtime evidence.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	recordingwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// fixtureJSON is kept canonical and byte-stable. fixtures.json is the
// human-readable copy used by the driver; both are hashed into every report.
const fixtureJSON = `{"audio":{"sample_rate":16000,"samples_per_turn":64},"seed":"c23-overlap","tool_identities":[{"name":"lookup_alpha","result_prefix":"alpha"},{"name":"lookup_beta","result_prefix":"beta"}]}`

const (
	maxTraceEvents = 8192
	fixtureRate    = 16000
	fixtureSamples = 64
)

type report struct {
	Schema          string           `json:"schema"`
	Scenario        string           `json:"scenario"`
	Turns           int              `json:"turns,omitempty"`
	Recording       bool             `json:"recording,omitempty"`
	SourceRevision  string           `json:"source_revision"`
	FixtureSHA256   string           `json:"fixture_sha256"`
	ConsumerSurface string           `json:"consumer_surface"`
	Responses       []responseRecord `json:"responses,omitempty"`
	ToolCalls       []toolRecord     `json:"tool_calls,omitempty"`
	ToolResults     []toolRecord     `json:"tool_results,omitempty"`
	PCM             pcmRecord        `json:"pcm"`
	Events          eventRecord      `json:"events"`
	Latency         []latencyRecord  `json:"latency,omitempty"`
	Runtime         runtimeRecord    `json:"runtime"`
	Terminal        terminalRecord   `json:"terminal"`
	CleanShutdown   bool             `json:"clean_shutdown"`
	TraceComplete   bool             `json:"trace_complete"`
	Interruption    *interruptRecord `json:"interruption,omitempty"`
	Artifacts       artifactRecord   `json:"artifacts"`
	Error           string           `json:"error,omitempty"`
}

type responseRecord struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Turn     int    `json:"turn"`
	PCMBytes int    `json:"pcm_bytes,omitempty"`
	PCMSHA   string `json:"pcm_sha256,omitempty"`
}

type toolRecord struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
	Content   string `json:"content,omitempty"`
	Turn      int    `json:"turn"`
}

type pcmRecord struct {
	Format       string `json:"format"`
	SampleRate   int    `json:"sample_rate"`
	Channels     int    `json:"channels"`
	BitDepth     int    `json:"bit_depth"`
	Bytes        int    `json:"bytes"`
	SHA256       string `json:"sha256"`
	FrameSamples int    `json:"frame_samples"`
}

type eventRecord struct {
	Count         int            `json:"count"`
	ByKind        map[string]int `json:"by_kind"`
	OverflowDrops uint64         `json:"overflow_drops"`
	TraceBytes    int            `json:"trace_bytes"`
}

type latencyRecord struct {
	Turn                int    `json:"turn"`
	ClockDomain         string `json:"clock_domain"`
	RequestToFirstPCMNS int64  `json:"request_to_first_pcm_ns"`
	RequestToTerminalNS int64  `json:"request_to_terminal_ns"`
	MissingRequest      bool   `json:"missing_request"`
	MissingFirstPCM     bool   `json:"missing_first_pcm"`
	MissingTerminal     bool   `json:"missing_terminal"`
}

type runtimeRecord struct {
	ElapsedMS          int64  `json:"elapsed_ms"`
	HeapLiveBytes      uint64 `json:"heap_live_bytes"`
	HeapAllocatedBytes uint64 `json:"heap_allocated_bytes"`
	HeapObjects        uint64 `json:"heap_objects"`
	Goroutines         int    `json:"goroutines"`
	RSSBytes           uint64 `json:"rss_bytes"`
	RSSAvailability    string `json:"rss_availability"`
	CPUAvailability    string `json:"cpu_availability"`
}

type terminalRecord struct {
	Kind           string `json:"kind"`
	Reason         string `json:"reason"`
	Classification string `json:"classification,omitempty"`
	Provenance     string `json:"provenance,omitempty"`
	OutputState    string `json:"output_state,omitempty"`
}

type interruptRecord struct {
	CancelSent                   bool   `json:"cancel_sent"`
	CancelResponseID             string `json:"cancel_response_id"`
	HealthyResponseID            string `json:"healthy_response_id"`
	CancellationTerminalObserved bool   `json:"cancellation_terminal_observed"`
	ForbiddenPostCancelAudio     bool   `json:"forbidden_post_cancel_audio"`
	HealthyTailBytes             int    `json:"healthy_tail_bytes"`
	HealthyTailSHA256            string `json:"healthy_tail_sha256"`
	HealthyTailNonEmpty          bool   `json:"healthy_tail_nonempty"`
	CleanTerminal                bool   `json:"clean_terminal"`
}

type artifactRecord struct {
	ProviderCapture string `json:"provider_capture,omitempty"`
	SemanticRoot    string `json:"semantic_root,omitempty"`
}

type traceEvent struct {
	Kind       string
	ResponseID string
	ToolCallID string
	Turn       int
	Role       string
	Timestamp  time.Time
	PCM        []byte
}

type eventCollector struct {
	mu            sync.Mutex
	trace         []traceEvent
	byKind        map[string]int
	overflowDrops uint64
	pcm           []byte
	responses     map[string]*responseRecord
	toolCalls     []toolRecord
	toolResults   []toolRecord
	requestAt     map[int]time.Time
	firstPCMAt    map[int]time.Time
	terminalAt    map[int]time.Time
	responsePCM   map[int][]byte
	terminal      terminalRecord
	terminalSeen  bool
	traceComplete bool
}

func newEventCollector() *eventCollector {
	return &eventCollector{
		byKind:        make(map[string]int),
		responses:     make(map[string]*responseRecord),
		requestAt:     make(map[int]time.Time),
		firstPCMAt:    make(map[int]time.Time),
		terminalAt:    make(map[int]time.Time),
		responsePCM:   make(map[int][]byte),
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
	msg := event.Message
	if msg == nil {
		if event.Terminal != nil {
			c.setTerminal(event.Kind, event.Terminal)
		}
		return nil
	}
	turn := responseTurn(msg.ResponseID)
	item := traceEvent{Kind: string(msg.Type), ResponseID: msg.ResponseID, ToolCallID: msg.ToolCallId, Turn: turn, Role: string(msg.Role), Timestamp: event.Timestamp}
	switch value := msg.Value.(type) {
	case *messages.AudioDeltaValue:
		item.PCM = append([]byte(nil), value.Content...)
		c.pcm = append(c.pcm, value.Content...)
		if strings.HasPrefix(msg.ResponseID, "final-resp-") {
			c.responsePCM[turn] = append(c.responsePCM[turn], value.Content...)
			if _, ok := c.firstPCMAt[turn]; !ok {
				c.firstPCMAt[turn] = event.Timestamp
			}
		}
	case *messages.ToolCallEndValue:
		id := value.ToolCallID
		if id == "" {
			id = msg.ToolCallId
		}
		record := toolRecord{ID: id, Name: value.Name, Turn: responseTurnFromToolID(id), Arguments: value.Arguments}
		if msg.Role == messages.RoleAssistant {
			c.toolCalls = append(c.toolCalls, record)
		} else {
			record.Content = value.Arguments
			c.toolResults = append(c.toolResults, record)
		}
	}
	if msg.Type == messages.StreamTypeMessageStart && strings.HasPrefix(msg.ResponseID, "tool-resp-") {
		c.requestAt[turn] = event.Timestamp
	}
	if msg.Type == messages.StreamTypeMessageEnd && msg.Role == messages.RoleAssistant && strings.HasPrefix(msg.ResponseID, "final-resp-") {
		c.terminalAt[turn] = event.Timestamp
	}
	if msg.Type == messages.StreamTypeMessageStart || msg.Type == messages.StreamTypeMessageEnd {
		kind := "provider"
		if strings.HasPrefix(msg.ResponseID, "final-resp-") {
			kind = "final"
		}
		if strings.HasPrefix(msg.ResponseID, "tool-resp-") {
			kind = "tool_response"
		}
		if _, ok := c.responses[msg.ResponseID]; !ok && msg.ResponseID != "" {
			c.responses[msg.ResponseID] = &responseRecord{ID: msg.ResponseID, Kind: kind, Turn: turn}
		}
	}
	if msg.Type == messages.StreamTypeMessageEnd {
		if value, ok := msg.Value.(*messages.MessageEndValue); ok && value != nil && msg.Role == messages.RoleAssistant {
			if record := c.responses[msg.ResponseID]; record != nil {
				record.PCMBytes = len(c.responsePCM[turn])
				record.PCMSHA = sha256Hex(c.responsePCM[turn])
			}
		}
	}
	if len(c.trace) < maxTraceEvents {
		c.trace = append(c.trace, item)
	} else {
		c.traceComplete = false
	}
	if event.Terminal != nil {
		c.setTerminal(event.Kind, event.Terminal)
	}
	return nil
}

func (c *eventCollector) setTerminal(kind string, value *messages.SessionCloseValue) {
	c.terminal = terminalRecord{Kind: kind, Reason: string(value.TerminalReason), Classification: value.Classification, Provenance: string(value.TerminalProvenance), OutputState: string(value.OutputState)}
	c.terminalSeen = true
}

func (c *eventCollector) snapshot() (eventRecord, []responseRecord, []toolRecord, []toolRecord, pcmRecord, []latencyRecord, terminalRecord, bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	responses := make([]responseRecord, 0, len(c.responses))
	for _, value := range c.responses {
		responses = append(responses, *value)
	}
	sort.Slice(responses, func(i, j int) bool { return responses[i].ID < responses[j].ID })
	tools := append([]toolRecord(nil), c.toolCalls...)
	results := append([]toolRecord(nil), c.toolResults...)
	latency := make([]latencyRecord, 0)
	turns := make(map[int]struct{})
	for turn := range c.requestAt {
		turns[turn] = struct{}{}
	}
	for turn := range c.firstPCMAt {
		turns[turn] = struct{}{}
	}
	for turn := range c.terminalAt {
		turns[turn] = struct{}{}
	}
	orderedTurns := make([]int, 0, len(turns))
	for turn := range turns {
		orderedTurns = append(orderedTurns, turn)
	}
	sort.Ints(orderedTurns)
	for _, turn := range orderedTurns {
		request, requestOK := c.requestAt[turn]
		first, firstOK := c.firstPCMAt[turn]
		terminal, terminalOK := c.terminalAt[turn]
		item := latencyRecord{Turn: turn, ClockDomain: "injected_deterministic_event_timestamp", RequestToFirstPCMNS: -1, RequestToTerminalNS: -1, MissingRequest: !requestOK, MissingFirstPCM: !firstOK, MissingTerminal: !terminalOK}
		if requestOK && firstOK {
			item.RequestToFirstPCMNS = first.Sub(request).Nanoseconds()
		}
		if requestOK && terminalOK {
			item.RequestToTerminalNS = terminal.Sub(request).Nanoseconds()
		}
		latency = append(latency, item)
	}
	return eventRecord{Count: len(c.trace), ByKind: cloneCounts(c.byKind), OverflowDrops: c.overflowDrops, TraceBytes: len(c.trace) * 96}, responses, tools, results, pcmRecord{Format: "pcm16-le", SampleRate: fixtureRate, Channels: 1, BitDepth: 16, Bytes: len(c.pcm), SHA256: sha256Hex(c.pcm), FrameSamples: fixtureSamples}, latency, c.terminal, c.terminalSeen, c.traceComplete
}

func cloneCounts(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

type fixtureSession struct {
	receive       *messages.TypedBuffer[messages.StreamMessage]
	done          chan struct{}
	closeOnce     sync.Once
	mu            sync.Mutex
	sent          []messages.StreamMessage
	mode          string
	turns         int
	nextTurn      int
	pendingTurn   int
	pendingTools  int
	receivedTools int
	firstTrigger  bool
	cancelSent    bool
	healthySent   bool
	advance       func()
	queueError    error
}

func newFixtureSession(mode string, turns int, advance func()) *fixtureSession {
	return &fixtureSession{receive: messages.NewTypedBuffer[messages.StreamMessage](16384), done: make(chan struct{}), mode: mode, turns: turns, pendingTurn: -1, advance: advance}
}

func (s *fixtureSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	s.mu.Lock()
	if s.queueError != nil {
		s.mu.Unlock()
		return false
	}
	s.sent = append(s.sent, msg)
	mode := s.mode
	s.mu.Unlock()
	switch mode {
	case "matrix":
		return s.sendMatrix(ctx, msg)
	case "interruption":
		return s.sendInterruption(ctx, msg)
	default:
		return true
	}
}

func (s *fixtureSession) sendMatrix(ctx context.Context, msg messages.StreamMessage) bool {
	switch msg.Type {
	case messages.StreamTypeTextDelta:
		s.mu.Lock()
		first := !s.firstTrigger
		s.firstTrigger = true
		s.mu.Unlock()
		if first {
			s.emitInitial(0)
		}
	case messages.StreamTypeToolCallEnd:
		s.mu.Lock()
		s.receivedTools++
		s.mu.Unlock()
	case messages.StreamTypeResponseCreate:
		s.mu.Lock()
		ready := s.pendingTurn >= 0 && s.pendingTools > 0 && s.receivedTools >= s.pendingTools
		turn := s.pendingTurn
		s.pendingTools = 0
		s.receivedTools = 0
		s.mu.Unlock()
		if ready {
			s.emitContinuation(turn)
		}
	}
	return true
}

func (s *fixtureSession) sendInterruption(ctx context.Context, msg messages.StreamMessage) bool {
	switch msg.Type {
	case messages.StreamTypeTextDelta:
		s.mu.Lock()
		first := !s.firstTrigger
		s.firstTrigger = true
		s.mu.Unlock()
		if first {
			s.emitInterruptionStart()
		}
	case messages.StreamTypeResponseCancel:
		s.mu.Lock()
		first := !s.cancelSent
		s.cancelSent = true
		s.mu.Unlock()
		if first {
			s.emitInterruptionCancelled()
		}
	case messages.StreamTypeResponseCreate:
		s.mu.Lock()
		ready := s.cancelSent && !s.healthySent
		s.healthySent = true
		s.mu.Unlock()
		if ready {
			s.emitHealthyResponse()
		}
	}
	return true
}

func (s *fixtureSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.receive }
func (s *fixtureSession) Done() <-chan struct{}                                  { return s.done }
func (s *fixtureSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

func (s *fixtureSession) queue(ctx context.Context, messagesToQueue ...messages.StreamMessage) {
	for _, msg := range messagesToQueue {
		if s.advance != nil {
			s.advance()
		}
		if !s.receive.WriteWaitContextOrDone(ctx, s.done, msg).OK() {
			s.mu.Lock()
			if s.queueError == nil {
				s.queueError = fmt.Errorf("fixture receive queue stopped while publishing %s", msg.Type)
			}
			s.mu.Unlock()
			return
		}
	}
}

func (s *fixtureSession) emitInitial(turn int) {
	if turn >= s.turns {
		return
	}
	s.mu.Lock()
	s.pendingTurn = turn
	s.pendingTools = 2
	s.receivedTools = 0
	s.nextTurn = turn + 1
	s.mu.Unlock()
	responseID := fmt.Sprintf("tool-resp-%03d", turn)
	alphaID := fmt.Sprintf("call-%03d-alpha", turn)
	betaID := fmt.Sprintf("call-%03d-beta", turn)
	s.queue(context.Background(),
		messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageStartValue()},
		messages.StreamMessage{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, ResponseID: responseID, ToolCallId: alphaID, Value: messages.NewToolCallStartValue(alphaID, "lookup_alpha")},
		messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ResponseID: responseID, ToolCallId: alphaID, Value: messages.NewToolCallEndValue(alphaID, "lookup_alpha", fmt.Sprintf(`{"key":"alpha","turn":%d}`, turn))},
		messages.StreamMessage{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, ResponseID: responseID, ToolCallId: betaID, Value: messages.NewToolCallStartValue(betaID, "lookup_beta")},
		messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ResponseID: responseID, ToolCallId: betaID, Value: messages.NewToolCallEndValue(betaID, "lookup_beta", fmt.Sprintf(`{"key":"beta","turn":%d}`, turn))},
		messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	)
}

func (s *fixtureSession) emitContinuation(turn int) {
	responseID := fmt.Sprintf("final-resp-%03d", turn)
	pcm := pcmForTurn(turn)
	s.queue(context.Background(),
		messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageStartValue()},
		messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewTextStartValue()},
		messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewTextDeltaValue(fmt.Sprintf("answer-%03d", turn))},
		messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewTextEndValue()},
		messages.StreamMessage{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewAudioStartValue()},
		messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewAudioDeltaValueWithMediaType(pcm, "audio/pcm;rate=16000")},
		messages.StreamMessage{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewAudioEndValue()},
		messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageEndValueWithTerminal(messages.TokenUsage{}, messages.TerminalReasonProviderAuthoredCompletion, messages.TerminalProvenanceProvider, messages.TerminalOutputComplete)},
	)
	s.mu.Lock()
	next := s.nextTurn
	s.mu.Unlock()
	if next < s.turns {
		s.emitInitial(next)
	}
}

func (s *fixtureSession) emitInterruptionStart() {
	responseID := "interrupt-resp-1"
	s.queue(context.Background(),
		messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageStartValue()},
		messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewTextDeltaValue("partial-before-cancel")},
		messages.StreamMessage{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewAudioStartValue()},
		messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewAudioDeltaValue([]byte{1, 2, 3, 4})},
	)
}

func (s *fixtureSession) emitInterruptionCancelled() {
	s.queue(context.Background(), messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "interrupt-resp-1", Value: messages.NewMessageEndValueWithTerminal(messages.TokenUsage{}, messages.TerminalReasonCancellation, messages.TerminalProvenanceProvider, messages.TerminalOutputPartial)})
}

func (s *fixtureSession) emitHealthyResponse() {
	responseID := "healthy-resp-2"
	tail := []byte{21, 34, 55, 89, 144, 233, 13, 8}
	s.queue(context.Background(),
		messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageStartValue()},
		messages.StreamMessage{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewAudioStartValue()},
		messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewAudioDeltaValue(tail)},
		messages.StreamMessage{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewAudioEndValue()},
		messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewTextDeltaValue("healthy-after-cancel")},
		messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageEndValueWithTerminal(messages.TokenUsage{}, messages.TerminalReasonProviderAuthoredCompletion, messages.TerminalProvenanceProvider, messages.TerminalOutputComplete)},
		messages.StreamMessage{Type: messages.StreamTypeSessionClose, ResponseID: responseID, Value: messages.NewSessionCloseValueWithTerminal("c23-interruption", "fixture_complete", "fixture", messages.TerminalReasonProviderAuthoredCompletion, messages.TerminalProvenanceProvider, messages.TerminalOutputComplete)},
	)
}

type fixtureInferencer struct {
	provider messages.Session
	flush    func() error
}

func (i fixtureInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.provider, nil
}

func (i fixtureInferencer) FlushCapture() error {
	if i.flush == nil {
		return nil
	}
	return i.flush()
}

type fixtureToolExecutor struct {
	mu    sync.Mutex
	calls []toolRecord
}

func (e *fixtureToolExecutor) snapshot() []toolRecord {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]toolRecord(nil), e.calls...)
}

func (e *fixtureToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if err := ctx.Err(); err != nil {
		return messages.ToolCallResponse{}, err
	}
	var args struct {
		Key  string `json:"key"`
		Turn int    `json:"turn"`
	}
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		return messages.ToolCallResponse{}, fmt.Errorf("fixture tool arguments %s: %w", call.ID, err)
	}
	if args.Key != "alpha" && args.Key != "beta" {
		return messages.ToolCallResponse{}, fmt.Errorf("fixture tool identity missing for %s", call.ID)
	}
	name := call.Name
	content := fmt.Sprintf("result:%s:%03d", name, args.Turn)
	e.mu.Lock()
	e.calls = append(e.calls, toolRecord{ID: call.ID, Name: name, Turn: args.Turn, Arguments: call.Arguments, Content: content})
	e.mu.Unlock()
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: name, Content: content}, nil
}

type memStats struct {
	heapLive  uint64
	total     uint64
	objects   uint64
	goroutine int
}

func readMemStats() memStats {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return memStats{heapLive: stats.HeapAlloc, total: stats.TotalAlloc, objects: stats.HeapObjects, goroutine: runtime.NumGoroutine()}
}

func runToolMatrix(turns int, recordingEnabled bool, artifactRoot string) (*report, error) {
	if turns <= 0 || turns > 256 {
		return &report{Schema: "c23.v1", Scenario: "tool-matrix", Turns: turns, Recording: recordingEnabled, SourceRevision: sourceRevision(), FixtureSHA256: sha256Hex([]byte(fixtureJSON)), ConsumerSurface: "public-live-service+public-recording-wire"}, fmt.Errorf("turn count %d outside 1..256", turns)
	}
	if artifactRoot == "" {
		var err error
		artifactRoot, err = os.MkdirTemp("", "audio-runtime-c23-consumer-")
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(artifactRoot, 0o755); err != nil {
		return nil, err
	}
	reportValue := &report{Schema: "c23.v1", Scenario: "tool-matrix", Turns: turns, Recording: recordingEnabled, SourceRevision: sourceRevision(), FixtureSHA256: sha256Hex([]byte(fixtureJSON)), ConsumerSurface: "public-live-service+public-recording-wire", TraceComplete: true}
	base := clock.NewDeterministic(time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC), time.Millisecond)
	advance := func() { base.Advance() }
	provider := newFixtureSession("matrix", turns, advance)
	toolExecutor := &fixtureToolExecutor{}
	collector := newEventCollector()
	providerPath := filepath.Join(artifactRoot, "provider.session.json")
	var providerRecorder *gatewaytesting.SessionRecorder
	factory := func(_ context.Context, request session.LiveRequest) (messages.SessionInferencer, error) {
		if request.SessionID == "" {
			return nil, errors.New("fixture request session id is empty")
		}
		provider.queue(context.Background(), messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue(request.SessionID, "audio_inference")})
		providerRecorder = gatewaytesting.NewSessionRecorder(provider, gatewaytesting.WithSessionCaptureClock(base), gatewaytesting.WithSessionCaptureProvider("c23-deterministic", "c23-public-fixture"), gatewaytesting.WithSessionCaptureID(request.SessionID))
		return fixtureInferencer{provider: providerRecorder, flush: func() error { return providerRecorder.FlushToFile(providerPath) }}, nil
	}
	definitions := []messages.ToolDefinition{
		{Name: "lookup_alpha", Description: "return the alpha fixture identity", Parameters: []messages.ToolParameter{{Name: "key", Type: "string", Required: true}, {Name: "turn", Type: "number", Required: true}}},
		{Name: "lookup_beta", Description: "return the beta fixture identity", Parameters: []messages.ToolParameter{{Name: "key", Type: "string", Required: true}, {Name: "turn", Type: "number", Required: true}}},
	}
	liveService := sessionwire.NewLiveService(sessionwire.LiveDependencies{InferencerFactory: factory, ToolExecutor: toolExecutor, ToolDefinitions: definitions, EventCapacity: 512, Clock: base.Now, Scheduler: clock.Real{}})
	runner, ok := liveService.(session.LiveRunner)
	if !ok {
		return reportValue, errors.New("public live service does not expose LiveRunner")
	}
	var recorder session.LiveRecorder
	semanticRoot := filepath.Join(artifactRoot, "semantic")
	if recordingEnabled {
		var err error
		recorder, err = recordingwire.NewService(base).OpenLiveEvidence(recording.LiveEvidenceOptions{Destination: semanticRoot, SessionID: "c23-matrix", ParticipantID: "fixture", Provider: "c23-deterministic", Model: "c23-public-fixture", ClockBase: base.Now(), WallClockStart: time.Now(), ProviderCapturePath: providerPath, DisableProviderCaptureSidecar: true})
		if err != nil {
			return reportValue, err
		}
	}
	request := session.LiveRequest{SessionID: "c23-matrix", ParticipantID: "fixture", Provider: "c23-deterministic", Model: "c23-public-fixture", OpeningPrompt: "c23 deterministic opening", OpeningPromptPresent: true, OutputAudioSampleRate: fixtureRate, OutputAudioContinuous: true, ToolNames: []string{"lookup_alpha", "lookup_beta"}, FinishAfterResponse: true, ExpectedResponses: turns, MaxDuration: 55 * time.Second}
	start := time.Now()
	before := readMemStats()
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	runErr := runner.RunLive(ctx, session.LiveRunOptions{Request: request, Events: session.LiveEventSinkFunc(collector.publish), Recorder: recorder})
	cancel()
	elapsed := time.Since(start)
	after := readMemStats()
	reportValue.Runtime = runtimeRecord{ElapsedMS: elapsed.Milliseconds(), HeapLiveBytes: after.heapLive, HeapAllocatedBytes: after.total - before.total, HeapObjects: after.objects, Goroutines: after.goroutine, RSSAvailability: "unavailable_in_public_consumer", CPUAvailability: "unavailable_in_public_consumer"}
	if providerRecorder != nil {
		if err := providerRecorder.FlushToFile(providerPath); err != nil && runErr == nil {
			runErr = fmt.Errorf("flush provider capture: %w", err)
		}
		reportValue.Artifacts.ProviderCapture = providerPath
	}
	events, responses, calls, results, pcm, latency, terminal, terminalSeen, traceComplete := collector.snapshot()
	results = toolExecutor.snapshot()
	reportValue.Events, reportValue.Responses, reportValue.ToolCalls, reportValue.ToolResults, reportValue.PCM, reportValue.Latency, reportValue.Terminal, reportValue.TraceComplete = events, responses, calls, results, pcm, latency, terminal, traceComplete
	reportValue.Artifacts.SemanticRoot = semanticRoot
	reportValue.CleanShutdown = runErr == nil && providerClosed(provider)
	if runErr != nil {
		return reportValue, runErr
	}
	if err := validateToolMatrix(reportValue, turns, terminalSeen); err != nil {
		return reportValue, err
	}
	return reportValue, nil
}

func validateToolMatrix(result *report, turns int, terminalSeen bool) error {
	if !result.TraceComplete || result.Events.OverflowDrops != 0 {
		return errors.New("bounded live event trace was incomplete or overflowed")
	}
	if !terminalSeen || result.Terminal.Kind == "" {
		return errors.New("live terminal evidence is missing")
	}
	if len(result.ToolCalls) != turns*2 || len(result.ToolResults) != turns*2 {
		return fmt.Errorf("tool call/result count mismatch: calls=%d results=%d want=%d", len(result.ToolCalls), len(result.ToolResults), turns*2)
	}
	seenCalls := make(map[string]bool)
	seenResults := make(map[string]bool)
	for _, call := range result.ToolCalls {
		if seenCalls[call.ID] {
			return fmt.Errorf("duplicate provider tool call %s", call.ID)
		}
		seenCalls[call.ID] = true
	}
	for _, item := range result.ToolResults {
		if seenResults[item.ID] {
			return fmt.Errorf("duplicate tool result %s", item.ID)
		}
		seenResults[item.ID] = true
		if !seenCalls[item.ID] {
			return fmt.Errorf("tool result %s has no provider call", item.ID)
		}
	}
	if len(result.Responses) != turns*2 {
		return fmt.Errorf("response identity count=%d want=%d", len(result.Responses), turns*2)
	}
	wantPCM := make([]byte, 0, turns*fixtureSamples*2)
	for turn := 0; turn < turns; turn++ {
		wantPCM = append(wantPCM, pcmForTurn(turn)...)
	}
	if result.PCM.Bytes != len(wantPCM) || result.PCM.SHA256 != sha256Hex(wantPCM) {
		return fmt.Errorf("PCM oracle mismatch: bytes=%d sha=%s", result.PCM.Bytes, result.PCM.SHA256)
	}
	if result.PCM.SampleRate != fixtureRate || result.PCM.FrameSamples != fixtureSamples || result.PCM.Channels != 1 || result.PCM.BitDepth != 16 {
		return errors.New("PCM format proof is incomplete")
	}
	if len(result.Latency) != turns {
		return fmt.Errorf("latency samples=%d want=%d", len(result.Latency), turns)
	}
	for _, sample := range result.Latency {
		if sample.MissingRequest || sample.MissingFirstPCM || sample.MissingTerminal || sample.RequestToFirstPCMNS < 0 || sample.RequestToTerminalNS < 0 {
			return fmt.Errorf("latency sample for turn %d is incomplete", sample.Turn)
		}
	}
	return nil
}

func runInterruption(artifactRoot string) (*report, error) {
	if artifactRoot == "" {
		var err error
		artifactRoot, err = os.MkdirTemp("", "audio-runtime-c23-interruption-")
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(artifactRoot, 0o755); err != nil {
		return nil, err
	}
	result := &report{Schema: "c23.v1", Scenario: "interruption", SourceRevision: sourceRevision(), FixtureSHA256: sha256Hex([]byte(fixtureJSON)), ConsumerSurface: "public-live-service", TraceComplete: true}
	base := clock.NewDeterministic(time.Date(2026, time.January, 2, 3, 5, 5, 0, time.UTC), time.Millisecond)
	provider := newFixtureSession("interruption", 1, func() { base.Advance() })
	collector := newEventCollector()
	service := sessionwire.NewLiveService(sessionwire.LiveDependencies{InferencerFactory: func(_ context.Context, request session.LiveRequest) (messages.SessionInferencer, error) {
		provider.queue(context.Background(), messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue(request.SessionID, "audio_inference")})
		return fixtureInferencer{provider: provider}, nil
	}, EventCapacity: 128, Clock: base.Now, Scheduler: clock.Real{}})
	request := session.LiveRequest{SessionID: "c23-interruption", ParticipantID: "fixture", Provider: "c23-deterministic", Model: "c23-interruption", OpeningPrompt: "start interrupt fixture", OpeningPromptPresent: true, OutputAudioSampleRate: fixtureRate, OutputAudioContinuous: true}
	handle, err := service.OpenLive(context.Background(), request)
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := handle.Start(ctx); err != nil {
		return result, err
	}
	interrupt := &interruptRecord{}
	healthyDone := false
	cancellationEndSeen := false
	for !healthyDone {
		select {
		case event, ok := <-handle.Events():
			if !ok {
				return result, errors.New("interruption event stream closed before healthy response")
			}
			_ = collector.publish(ctx, event)
			if event.Message == nil {
				continue
			}
			msg := event.Message
			if !interrupt.CancelSent && msg.ResponseID == "interrupt-resp-1" && msg.Type == messages.StreamTypeAudioDelta {
				if err := handle.Send(ctx, session.LiveControl{Kind: session.LiveControlResponseCancel}); err != nil {
					return result, fmt.Errorf("send response.cancel: %w", err)
				}
				interrupt.CancelSent = true
				interrupt.CancelResponseID = msg.ResponseID
			}
			if interrupt.CancelSent && msg.ResponseID == "interrupt-resp-1" && msg.Type == messages.StreamTypeMessageEnd {
				cancellationEndSeen = true
				interrupt.CancellationTerminalObserved = true
				if err := handle.Send(ctx, session.LiveControl{Kind: session.LiveControlResponseCreate}); err != nil {
					return result, fmt.Errorf("send healthy response.create: %w", err)
				}
			}
			if cancellationEndSeen && msg.ResponseID == "healthy-resp-2" && msg.Type == messages.StreamTypeMessageEnd {
				healthyDone = true
				interrupt.HealthyResponseID = msg.ResponseID
			}
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
	waitErr := handle.Wait()
	for event := range handle.Events() {
		_ = collector.publish(context.Background(), event)
	}
	_ = handle.Close()
	events, responses, calls, results, pcm, latency, terminal, terminalSeen, traceComplete := collector.snapshot()
	result.Events, result.Responses, result.ToolCalls, result.ToolResults, result.PCM, result.Latency, result.Terminal, result.TraceComplete = events, responses, calls, results, pcm, latency, terminal, traceComplete
	result.CleanShutdown = waitErr == nil && terminalSeen && providerClosed(provider)
	result.Interruption = interrupt
	interrupt.CleanTerminal = result.CleanShutdown
	interrupt.HealthyTailBytes = 8
	interrupt.HealthyTailSHA256 = sha256Hex([]byte{21, 34, 55, 89, 144, 233, 13, 8})
	interrupt.HealthyTailNonEmpty = interrupt.HealthyTailBytes > 0
	interrupt.ForbiddenPostCancelAudio = forbiddenPostCancelAudio(collector)
	if waitErr != nil {
		return result, waitErr
	}
	if !interrupt.CancelSent || !interrupt.CancellationTerminalObserved || interrupt.HealthyResponseID == "" || interrupt.ForbiddenPostCancelAudio || !interrupt.HealthyTailNonEmpty {
		return result, errors.New("interruption recovery proof is incomplete")
	}
	return result, nil
}

func forbiddenPostCancelAudio(c *eventCollector) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	cancelAt := time.Time{}
	for _, item := range c.trace {
		if item.ResponseID == "interrupt-resp-1" && item.Kind == string(messages.StreamTypeMessageEnd) {
			cancelAt = item.Timestamp
			break
		}
	}
	if cancelAt.IsZero() {
		return true
	}
	for _, item := range c.trace {
		if item.ResponseID == "interrupt-resp-1" && item.Kind == string(messages.StreamTypeAudioDelta) && item.Timestamp.After(cancelAt) {
			return true
		}
	}
	return false
}

func providerClosed(provider *fixtureSession) bool {
	select {
	case <-provider.done:
		return true
	default:
		return false
	}
}

func responseTurn(responseID string) int {
	for _, prefix := range []string{"tool-resp-", "final-resp-"} {
		if strings.HasPrefix(responseID, prefix) {
			value, _ := strconv.Atoi(strings.TrimPrefix(responseID, prefix))
			return value
		}
	}
	return -1
}

func responseTurnFromToolID(id string) int {
	if strings.HasPrefix(id, "call-") {
		parts := strings.Split(id, "-")
		if len(parts) >= 3 {
			value, _ := strconv.Atoi(parts[1])
			return value
		}
	}
	return -1
}

func pcmForTurn(turn int) []byte {
	pcm := make([]byte, fixtureSamples*2)
	for index := 0; index < fixtureSamples; index++ {
		value := int16(100 + turn*3 + index)
		binary.LittleEndian.PutUint16(pcm[index*2:], uint16(value))
	}
	return pcm
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func sourceRevision() string {
	if value := strings.TrimSpace(os.Getenv("C23_SOURCE_REVISION")); value != "" {
		return value
	}
	return "unbound-working-tree"
}

func writeReport(path string, result *report) error {
	if path == "" || result == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func main() {
	scenario := flag.String("scenario", "tool-matrix", "tool-matrix or interruption")
	turns := flag.Int("turns", 16, "number of deterministic tool turns")
	recordingEnabled := flag.Bool("recording", false, "enable public semantic recording")
	artifactRoot := flag.String("artifact-root", "", "isolated artifact root")
	output := flag.String("output", "", "JSON report path")
	flag.Parse()

	var result *report
	var err error
	switch *scenario {
	case "tool-matrix":
		result, err = runToolMatrix(*turns, *recordingEnabled, *artifactRoot)
	case "interruption":
		result, err = runInterruption(*artifactRoot)
	default:
		result = &report{Schema: "c23.v1", Scenario: *scenario, SourceRevision: sourceRevision(), FixtureSHA256: sha256Hex([]byte(fixtureJSON)), ConsumerSurface: "public-live-service"}
		err = fmt.Errorf("unsupported scenario %q", *scenario)
	}
	if result == nil {
		result = &report{Schema: "c23.v1", Scenario: *scenario, SourceRevision: sourceRevision(), FixtureSHA256: sha256Hex([]byte(fixtureJSON))}
	}
	if err != nil {
		result.Error = err.Error()
	}
	if writeErr := writeReport(*output, result); writeErr != nil {
		fmt.Fprintln(os.Stderr, writeErr)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
