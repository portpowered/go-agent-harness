// Command c23-consumer is a source-file-built public-runtime characterization
// consumer. It deliberately imports only exported contracts from the
// workspace. The Python driver owns provenance, process bounds, and report
// comparison; this binary owns the causal fixture and its runtime evidence.
package main

import (
	"bytes"
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

const (
	maxTraceEvents = 8192
	// The finite matrix reaches 256 turns. Keep the transcript bound finite but
	// leave headroom for the public recording wire's per-event JSON envelope.
	maxRecordingTranscriptBytes = 32 << 20
	maxRecordingTranscriptItems = 32768
	maxRecordingAudioBytes      = 4 << 20
	maxRecordingAudioItems      = 8192
	maxRecordingSidecarBytes    = 256 << 10
	maxRecordingSidecarItems    = 64
	maxRecordingMetadataBytes   = 256 << 10
	maxRecordingMetadataItems   = 1024
	maxRecordingTerminalBytes   = 128 << 10
	maxRecordingTerminalItems   = 16
	maxRecordingProviderBytes   = 4 << 20
	maxRecordingProviderItems   = 8192
	recordingFixturePace        = 2 * time.Millisecond
)

type fixtureDocument struct {
	Audio struct {
		SampleRate     int `json:"sample_rate"`
		SamplesPerTurn int `json:"samples_per_turn"`
	} `json:"audio"`
	Interruption struct {
		HealthyTail []int `json:"healthy_tail"`
	} `json:"interruption"`
}

var loadedFixture fixtureDocument
var loadedFixtureDigest string

func fixtureSampleRate() int     { return loadedFixture.Audio.SampleRate }
func fixtureSamplesPerTurn() int { return loadedFixture.Audio.SamplesPerTurn }

type report struct {
	Schema          string               `json:"schema"`
	Scenario        string               `json:"scenario"`
	Turns           int                  `json:"turns,omitempty"`
	Recording       bool                 `json:"recording,omitempty"`
	SourceRevision  string               `json:"source_revision"`
	FixtureSHA256   string               `json:"fixture_sha256"`
	ConsumerSurface string               `json:"consumer_surface"`
	Responses       []responseRecord     `json:"responses,omitempty"`
	ToolCalls       []toolRecord         `json:"tool_calls,omitempty"`
	ToolResults     []toolRecord         `json:"tool_results,omitempty"`
	Trace           []traceRecord        `json:"trace,omitempty"`
	PCM             pcmRecord            `json:"pcm"`
	Events          eventRecord          `json:"events"`
	Latency         []latencyRecord      `json:"latency,omitempty"`
	Runtime         runtimeRecord        `json:"runtime"`
	Terminal        terminalRecord       `json:"terminal"`
	CleanShutdown   bool                 `json:"clean_shutdown"`
	TraceComplete   bool                 `json:"trace_complete"`
	Interruption    *interruptRecord     `json:"interruption,omitempty"`
	Lifecycle       lifecycleRecord      `json:"lifecycle"`
	Artifacts       artifactRecord       `json:"artifacts"`
	RecordingUsage  recordingUsageRecord `json:"recording_usage"`
	Overlap         overlapRecord        `json:"overlap"`
	Control         *controlObservation  `json:"control_observation,omitempty"`
	ToolControl     string               `json:"tool_control,omitempty"`
	Error           string               `json:"error,omitempty"`
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
	ElapsedMS          int64             `json:"elapsed_ms"`
	HeapLiveBytes      uint64            `json:"heap_live_bytes"`
	HeapAllocatedBytes uint64            `json:"heap_allocated_bytes"`
	HeapObjects        uint64            `json:"heap_objects"`
	Goroutines         int               `json:"goroutines"`
	RSSBytes           uint64            `json:"rss_bytes"`
	RSSAvailability    string            `json:"rss_availability"`
	CPUAvailability    string            `json:"cpu_availability"`
	Baseline           runtimeSnapshot   `json:"baseline"`
	After              runtimeSnapshot   `json:"after"`
	Delta              runtimeDelta      `json:"delta"`
	State              stateRecord       `json:"state"`
	Measurement        measurementPolicy `json:"measurement"`
}

type runtimeSnapshot struct {
	HeapLiveBytes   uint64 `json:"heap_live_bytes"`
	TotalAllocBytes uint64 `json:"total_allocated_bytes"`
	HeapObjects     uint64 `json:"heap_objects"`
	Goroutines      int    `json:"goroutines"`
}

type runtimeDelta struct {
	HeapLiveBytes   int64 `json:"heap_live_bytes"`
	TotalAllocBytes int64 `json:"total_allocated_bytes"`
	HeapObjects     int64 `json:"heap_objects"`
	Goroutines      int   `json:"goroutines"`
}

type stateRecord struct {
	ConversationItemsObserved int               `json:"conversation_items_observed"`
	ToolCallsObserved         int               `json:"tool_calls_observed"`
	ToolResultsObserved       int               `json:"tool_results_observed"`
	RecordingItemsObserved    *int64            `json:"recording_items_observed,omitempty"`
	Availability              map[string]string `json:"availability"`
	Scope                     string            `json:"scope"`
}

type measurementPolicy struct {
	GC       string `json:"gc"`
	Observer string `json:"observer"`
	Clock    string `json:"clock"`
}

type lifecycleRecord struct {
	Opened      bool   `json:"opened"`
	Started     bool   `json:"started"`
	CloseCalled bool   `json:"close_called"`
	WaitCalled  bool   `json:"wait_called"`
	CloseError  string `json:"close_error,omitempty"`
	WaitError   string `json:"wait_error,omitempty"`
}

type terminalRecord struct {
	Kind           string `json:"kind"`
	Reason         string `json:"reason"`
	Classification string `json:"classification,omitempty"`
	Provenance     string `json:"provenance,omitempty"`
	OutputState    string `json:"output_state,omitempty"`
}

type interruptRecord struct {
	CancelSent                     bool   `json:"cancel_sent"`
	CancelResponseID               string `json:"cancel_response_id"`
	CancelBoundarySequence         int    `json:"cancel_boundary_sequence"`
	HealthyResponseID              string `json:"healthy_response_id"`
	CancellationTerminalObserved   bool   `json:"cancellation_terminal_observed"`
	PostCancelAudioObserved        bool   `json:"post_cancel_audio_observed"`
	PostCancelAudioBytes           int    `json:"post_cancel_audio_bytes"`
	PostCancelAudioPubliclyEmitted bool   `json:"post_cancel_audio_publicly_emitted"`
	CancelledOutputRejected        bool   `json:"cancelled_output_rejected"`
	ForbiddenPostCancelAudio       bool   `json:"forbidden_post_cancel_audio"`
	HealthyTailBytes               int    `json:"healthy_tail_bytes"`
	HealthyTailSHA256              string `json:"healthy_tail_sha256"`
	HealthyTailNonEmpty            bool   `json:"healthy_tail_nonempty"`
	CleanTerminal                  bool   `json:"clean_terminal"`
}

type artifactRecord struct {
	ProviderCapture string `json:"provider_capture,omitempty"`
	SemanticRoot    string `json:"semantic_root,omitempty"`
}

type traceRecord struct {
	Sequence   int    `json:"sequence"`
	Kind       string `json:"kind"`
	ResponseID string `json:"response_id,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Turn       int    `json:"turn"`
	Role       string `json:"role,omitempty"`
	PCMBytes   int    `json:"pcm_bytes,omitempty"`
}

type recordingUsageRecord struct {
	Enabled              bool              `json:"enabled"`
	Available            bool              `json:"available"`
	Limits               map[string]int64  `json:"limits"`
	Usage                map[string]int64  `json:"usage,omitempty"`
	ProviderCapture      bool              `json:"provider_capture_available"`
	ProviderCaptureUsage map[string]int64  `json:"provider_capture_usage,omitempty"`
	Drops                map[string]uint64 `json:"drops"`
}

type overlapRecord struct {
	PrefetchedResponseIDs          []string `json:"prefetched_response_ids,omitempty"`
	PrefetchBeforeFirstToolResult  bool     `json:"prefetch_before_first_tool_result"`
	ActiveResponseIDs              []string `json:"active_response_ids,omitempty"`
	CrossRoutingVerified           bool     `json:"cross_routing_verified"`
	ExactlyOnceToolResultsVerified bool     `json:"exactly_once_tool_results_verified"`
}

type controlObservation struct {
	Name              string   `json:"name"`
	Observed          bool     `json:"observed"`
	Boundary          string   `json:"boundary"`
	Detail            string   `json:"detail"`
	UnresolvedCallIDs []string `json:"unresolved_call_ids,omitempty"`
}

type traceEvent struct {
	Kind       string
	ResponseID string
	ToolCallID string
	Turn       int
	Role       string
	Timestamp  time.Time
	PCM        []byte
	PCMBytes   int
}

type eventCollector struct {
	mu                sync.Mutex
	trace             []traceEvent
	byKind            map[string]int
	overflowDrops     uint64
	pcm               []byte
	responses         map[string]*responseRecord
	toolCalls         []toolRecord
	toolResults       []toolRecord
	requestAt         map[int]time.Time
	firstPCMAt        map[int]time.Time
	terminalAt        map[int]time.Time
	responsePCM       map[int][]byte
	responseAudioByID map[string][]byte
	terminal          terminalRecord
	terminalSeen      bool
	traceComplete     bool
}

func newEventCollector() *eventCollector {
	return &eventCollector{
		byKind:            make(map[string]int),
		responses:         make(map[string]*responseRecord),
		requestAt:         make(map[int]time.Time),
		firstPCMAt:        make(map[int]time.Time),
		terminalAt:        make(map[int]time.Time),
		responsePCM:       make(map[int][]byte),
		responseAudioByID: make(map[string][]byte),
		traceComplete:     true,
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
		c.appendTrace(traceEvent{Kind: event.Kind, Timestamp: event.Timestamp, Turn: -1})
		return nil
	}
	turn := responseTurn(msg.ResponseID)
	item := traceEvent{Kind: string(msg.Type), ResponseID: msg.ResponseID, ToolCallID: msg.ToolCallId, Turn: turn, Role: string(msg.Role), Timestamp: event.Timestamp}
	switch value := msg.Value.(type) {
	case *messages.AudioDeltaValue:
		item.PCM = append([]byte(nil), value.Content...)
		item.PCMBytes = len(value.Content)
		c.pcm = append(c.pcm, value.Content...)
		c.responseAudioByID[msg.ResponseID] = append(c.responseAudioByID[msg.ResponseID], value.Content...)
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
	c.appendTrace(item)
	if event.Terminal != nil {
		c.setTerminal(event.Kind, event.Terminal)
	}
	return nil
}

func (c *eventCollector) appendTrace(item traceEvent) {
	if len(c.trace) < maxTraceEvents {
		c.trace = append(c.trace, item)
	} else {
		c.traceComplete = false
	}
}

func (c *eventCollector) setTerminal(kind string, value *messages.SessionCloseValue) {
	c.terminal = terminalRecord{Kind: kind, Reason: string(value.TerminalReason), Classification: value.Classification, Provenance: string(value.TerminalProvenance), OutputState: string(value.OutputState)}
	c.terminalSeen = true
}

func (c *eventCollector) snapshot() (eventRecord, []responseRecord, []toolRecord, []toolRecord, []traceRecord, pcmRecord, []latencyRecord, terminalRecord, bool, bool) {
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
	trace := make([]traceRecord, 0, len(c.trace))
	for sequence, item := range c.trace {
		trace = append(trace, traceRecord{Sequence: sequence, Kind: item.Kind, ResponseID: item.ResponseID, ToolCallID: item.ToolCallID, Turn: item.Turn, Role: item.Role, PCMBytes: item.PCMBytes})
	}
	return eventRecord{Count: len(c.trace), ByKind: cloneCounts(c.byKind), OverflowDrops: c.overflowDrops, TraceBytes: len(c.trace) * 96}, responses, tools, results, trace, pcmRecord{Format: "pcm16-le", SampleRate: fixtureSampleRate(), Channels: 1, BitDepth: 16, Bytes: len(c.pcm), SHA256: sha256Hex(c.pcm), FrameSamples: fixtureSamplesPerTurn()}, latency, c.terminal, c.terminalSeen, c.traceComplete
}

func (c *eventCollector) responseAudio(responseID string) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.responseAudioByID[responseID]...)
}

func (c *eventCollector) traceCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.trace)
}

func cloneCounts(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

type fixtureSession struct {
	receive                        *messages.TypedBuffer[messages.StreamMessage]
	done                           chan struct{}
	closeOnce                      sync.Once
	mu                             sync.Mutex
	sent                           []messages.StreamMessage
	mode                           string
	toolControl                    string
	turns                          int
	nextTurn                       int
	pendingTools                   map[int]int
	receivedTools                  map[int]int
	continued                      map[int]bool
	firstTrigger                   bool
	cancelSent                     bool
	healthySent                    bool
	prefetchedIDs                  []string
	activeIDs                      []string
	prefetchBeforeFirstToolResult  bool
	firstToolResultSeen            bool
	prefetchedEndQueued            bool
	interruptionDelayedAudioQueued bool
	interruptionDelayedAudioBytes  int
	advance                        func()
	pace                           func()
	queueError                     error
	toolResults                    []toolRecord
	resultIDs                      map[string]bool
}

func newFixtureSession(mode string, turns int, advance func(), pace func(), toolControl string) *fixtureSession {
	return &fixtureSession{receive: messages.NewTypedBuffer[messages.StreamMessage](16384), done: make(chan struct{}), mode: mode, toolControl: toolControl, turns: turns, pendingTools: make(map[int]int), receivedTools: make(map[int]int), continued: make(map[int]bool), advance: advance, pace: pace, resultIDs: make(map[string]bool)}
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
		result := toolResultFromMessage(msg)
		s.mu.Lock()
		if result.ID == "" {
			if s.queueError == nil {
				s.queueError = errors.New("fixture control missing tool result identity")
			}
			s.mu.Unlock()
			return false
		}
		if s.resultIDs[result.ID] {
			if s.queueError == nil {
				s.queueError = fmt.Errorf("fixture control duplicate tool result %s", result.ID)
			}
			s.mu.Unlock()
			return false
		}
		s.resultIDs[result.ID] = true
		s.toolResults = append(s.toolResults, result)
		turn := responseTurnFromToolID(result.ID)
		s.receivedTools[turn]++
		s.firstToolResultSeen = true
		s.mu.Unlock()
	case messages.StreamTypeResponseCreate:
		s.mu.Lock()
		turn := -1
		for candidate := 0; candidate < s.turns; candidate++ {
			if s.pendingTools[candidate] > 0 && !s.continued[candidate] && s.receivedTools[candidate] >= s.pendingTools[candidate] {
				turn = candidate
				break
			}
		}
		ready := turn >= 0
		if ready {
			s.continued[turn] = true
		}
		s.mu.Unlock()
		if ready {
			s.emitContinuation(turn)
		}
	}
	return true
}

func toolResultFromMessage(msg messages.StreamMessage) toolRecord {
	result := toolRecord{ID: msg.ToolCallId, Turn: responseTurnFromToolID(msg.ToolCallId)}
	if value, ok := msg.Value.(*messages.ToolCallEndValue); ok && value != nil {
		if value.ToolCallID != "" {
			result.ID = value.ToolCallID
		}
		result.Name = value.Name
		result.Content = value.Arguments
		result.Turn = responseTurnFromToolID(result.ID)
	}
	return result
}

func runtimeControlObservation(name string, runErr error) *controlObservation {
	if strings.TrimSpace(name) == "" || runErr == nil {
		return nil
	}
	var unresolved *session.LiveUnresolvedToolResultsError
	if !errors.As(runErr, &unresolved) || unresolved == nil {
		return nil
	}
	callIDs := unresolved.UnresolvedCallIDs()
	return &controlObservation{
		Name:              name,
		Observed:          len(callIDs) > 0,
		Boundary:          "runtime_tool_result_forwarder",
		Detail:            fmt.Sprintf("public live runner rejected %s before provider admission: %s", name, unresolved.Error()),
		UnresolvedCallIDs: callIDs,
	}
}

func (s *fixtureSession) snapshotToolResults() []toolRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]toolRecord(nil), s.toolResults...)
}

func (s *fixtureSession) snapshotInterruptionDelayedAudio() (bool, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.interruptionDelayedAudioQueued, s.interruptionDelayedAudioBytes
}

func (s *fixtureSession) snapshotOverlap() overlapRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return overlapRecord{
		PrefetchedResponseIDs:         append([]string(nil), s.prefetchedIDs...),
		PrefetchBeforeFirstToolResult: s.prefetchBeforeFirstToolResult,
		ActiveResponseIDs:             append([]string(nil), s.activeIDs...),
	}
}

func (s *fixtureSession) queueErrorValue() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queueError
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
		if s.pace != nil {
			s.pace()
		}
	}
}

func (s *fixtureSession) emitInitial(turn int) {
	if turn >= s.turns {
		return
	}
	if turn == 0 && s.toolControl == "" && s.turns > 1 {
		s.emitToolResponse(0, true)
		s.emitToolResponse(1, false)
		s.mu.Lock()
		s.nextTurn = 2
		s.prefetchedIDs = []string{"tool-resp-000", "tool-resp-001"}
		s.activeIDs = append([]string(nil), s.prefetchedIDs...)
		s.prefetchBeforeFirstToolResult = !s.firstToolResultSeen
		s.mu.Unlock()
		return
	}
	s.emitToolResponse(turn, true)
	s.mu.Lock()
	s.nextTurn = turn + 1
	s.mu.Unlock()
}

func (s *fixtureSession) emitToolResponse(turn int, complete bool) {
	s.mu.Lock()
	s.pendingTools[turn] = 2
	s.receivedTools[turn] = 0
	s.mu.Unlock()
	responseID := fmt.Sprintf("tool-resp-%03d", turn)
	alphaID := fmt.Sprintf("call-%03d-alpha", turn)
	betaID := fmt.Sprintf("call-%03d-beta", turn)
	messagesToQueue := []messages.StreamMessage{
		messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageStartValue()},
		messages.StreamMessage{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, ResponseID: responseID, ToolCallId: alphaID, Value: messages.NewToolCallStartValue(alphaID, "lookup_alpha")},
		messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ResponseID: responseID, ToolCallId: alphaID, Value: messages.NewToolCallEndValue(alphaID, "lookup_alpha", fmt.Sprintf(`{"key":"alpha","turn":%d}`, turn))},
		messages.StreamMessage{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, ResponseID: responseID, ToolCallId: betaID, Value: messages.NewToolCallStartValue(betaID, "lookup_beta")},
		messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ResponseID: responseID, ToolCallId: betaID, Value: messages.NewToolCallEndValue(betaID, "lookup_beta", fmt.Sprintf(`{"key":"beta","turn":%d}`, turn))},
	}
	if complete {
		messagesToQueue = append(messagesToQueue, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	}
	s.queue(context.Background(), messagesToQueue...)
}

func (s *fixtureSession) emitToolResponseEnd(turn int) {
	s.queue(context.Background(), messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: fmt.Sprintf("tool-resp-%03d", turn), Value: messages.NewMessageEndValue(messages.TokenUsage{})})
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
	deferPrefetchedEnd := turn == 0 && s.prefetchBeforeFirstToolResult && !s.prefetchedEndQueued
	if deferPrefetchedEnd {
		s.prefetchedEndQueued = true
	}
	s.mu.Unlock()
	if deferPrefetchedEnd {
		// Keep the second response active until the first continuation has
		// produced output. This is a real overlap boundary while preserving
		// the provider's response lifecycle for the public runtime.
		s.emitToolResponseEnd(1)
	} else if next < s.turns {
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
	// This delayed provider audio is intentionally queued after the cancel
	// control and before the cancellation terminal. The public runtime must
	// reject it from the canceled response's accepted output.
	s.mu.Lock()
	s.interruptionDelayedAudioQueued = true
	s.interruptionDelayedAudioBytes = 4
	s.mu.Unlock()
	s.queue(context.Background(),
		messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, ResponseID: "interrupt-resp-1", Value: messages.NewAudioDeltaValue([]byte{9, 9, 9, 9})},
		messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "interrupt-resp-1", Value: messages.NewMessageEndValueWithTerminal(messages.TokenUsage{}, messages.TerminalReasonCancellation, messages.TerminalProvenanceProvider, messages.TerminalOutputPartial)},
	)
}

func (s *fixtureSession) emitHealthyResponse() {
	responseID := "healthy-resp-2"
	tail := fixtureHealthyTail()
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

var loadedFixtureValue map[string]any

func loadFixture(path string) error {
	if path == "" {
		return errors.New("fixture path is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read fixture: %w", err)
	}
	if err := json.Unmarshal(raw, &loadedFixtureValue); err != nil {
		return fmt.Errorf("decode fixture: %w", err)
	}
	if err := json.Unmarshal(raw, &loadedFixture); err != nil {
		return fmt.Errorf("decode fixture schema: %w", err)
	}
	if loadedFixture.Audio.SampleRate <= 0 || loadedFixture.Audio.SamplesPerTurn <= 0 || len(loadedFixture.Interruption.HealthyTail) == 0 {
		return errors.New("fixture audio/interruption values are incomplete")
	}
	canonical, err := json.Marshal(loadedFixtureValue)
	if err != nil {
		return fmt.Errorf("canonicalize fixture: %w", err)
	}
	loadedFixtureDigest = sha256Hex(canonical)
	return nil
}

func fixtureHealthyTail() []byte {
	tail := make([]byte, len(loadedFixture.Interruption.HealthyTail))
	for index, value := range loadedFixture.Interruption.HealthyTail {
		if value < 0 || value > 255 {
			return nil
		}
		tail[index] = byte(value)
	}
	return tail
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

type providerCaptureSession struct {
	inner      messages.Session
	sink       recording.ProviderCaptureSink
	capture    gatewaytesting.SessionCapture
	source     clock.Source
	startAt    time.Time
	inbound    *messages.TypedBuffer[messages.StreamMessage]
	relayCtx   context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
	sequence   int
	captureErr error
	flushOnce  sync.Once
	flushErr   error
}

func newProviderCaptureSession(inner messages.Session, sink recording.ProviderCaptureSink, source clock.Source, sessionID string) *providerCaptureSession {
	startAt := source.Now()
	ctx, cancel := context.WithCancel(context.Background())
	result := &providerCaptureSession{
		inner:    inner,
		sink:     sink,
		source:   source,
		startAt:  startAt,
		inbound:  messages.NewTypedBuffer[messages.StreamMessage](inner.Receive().Cap()),
		relayCtx: ctx,
		cancel:   cancel,
		capture: gatewaytesting.SessionCapture{
			Version:  gatewaytesting.SessionCaptureVersion,
			Provider: gatewaytesting.SessionProviderMetadata{Name: "c23-deterministic", Model: "c23-public-fixture"},
			Session:  gatewaytesting.SessionMetadata{ID: sessionID, StartedAtUTC: startAt.UTC().Format(time.RFC3339Nano), FixtureProvenance: "c23-public-fixture"},
		},
	}
	go result.relayInbound()
	return result
}

func (s *providerCaptureSession) relayInbound() {
	for {
		select {
		case msg := <-s.inner.Receive().Chan():
			sequence, ok := s.admit(gatewaytesting.DirectionServerToClient, msg)
			if ok {
				if !s.inbound.WriteWaitContextOrDone(s.relayCtx, s.inner.Done(), msg).OK() {
					return
				}
				if err := s.commit(sequence); err != nil {
					return
				}
			} else if !s.inbound.WriteWaitContextOrDone(s.relayCtx, s.inner.Done(), msg).OK() {
				return
			}
		case <-s.inner.Done():
			s.cancel()
			return
		case <-s.relayCtx.Done():
			return
		}
	}
}

func (s *providerCaptureSession) admit(direction gatewaytesting.SessionEventDirection, msg messages.StreamMessage) (int, bool) {
	payload, err := gatewaytesting.MarshalStreamMessage(msg)
	if err != nil {
		s.latchCaptureError(err)
		return 0, false
	}
	s.mu.Lock()
	s.sequence++
	sequence := s.sequence
	s.mu.Unlock()
	event := gatewaytesting.CapturedSessionEvent{
		Sequence:    sequence,
		Direction:   direction,
		TimestampMs: s.source.Now().Sub(s.startAt).Milliseconds(),
		Type:        string(msg.Type),
		PayloadType: gatewaytesting.SessionPayloadTypeStreamMessage,
		Payload:     payload,
	}
	if err := s.sink.Append(event); err != nil {
		s.latchCaptureError(err)
		return sequence, false
	}
	return sequence, true
}

func (s *providerCaptureSession) commit(sequence int) error {
	if err := s.sink.Commit(sequence); err != nil {
		s.latchCaptureError(err)
		return err
	}
	return nil
}

func (s *providerCaptureSession) discard(sequence int) {
	if err := s.sink.Discard(sequence); err != nil {
		s.latchCaptureError(err)
	}
}

func (s *providerCaptureSession) latchCaptureError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	s.captureErr = errors.Join(s.captureErr, err)
	s.mu.Unlock()
}

func (s *providerCaptureSession) captureError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.captureErr
}

func (s *providerCaptureSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	sequence, admitted := s.admit(gatewaytesting.DirectionClientToServer, msg)
	if !admitted {
		return false
	}
	ok := s.inner.Send(ctx, msg)
	if ok {
		if err := s.commit(sequence); err != nil {
			return false
		}
	} else {
		s.discard(sequence)
	}
	return ok
}

func (s *providerCaptureSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.inbound
}
func (s *providerCaptureSession) Done() <-chan struct{} { return s.inner.Done() }

func (s *providerCaptureSession) Close() error {
	s.cancel()
	return s.inner.Close()
}

func (s *providerCaptureSession) FlushCapture(path string) error {
	s.flushOnce.Do(func() {
		if err := s.captureError(); err != nil {
			s.flushErr = errors.Join(err, s.sink.Abort())
			return
		}
		s.flushErr = s.sink.FlushToFile(path, s.capture)
	})
	return s.flushErr
}

type fixtureToolExecutor struct {
	mu      sync.Mutex
	calls   []toolRecord
	control string
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
	if e.control == "missing-result" && args.Key == "beta" {
		// Return a malformed result through the public tool-executor contract.
		// The public runtime's result-forwarding boundary must suppress the
		// missing identity and report the unresolved provider call.
		return messages.ToolCallResponse{Name: name, Content: content}, nil
	}
	e.mu.Lock()
	e.calls = append(e.calls, toolRecord{ID: call.ID, Name: name, Turn: args.Turn, Arguments: call.Arguments, Content: content})
	e.mu.Unlock()
	resultID := call.ID
	if e.control == "duplicate-result" && args.Key == "beta" {
		// The public runtime's exactly-once result-forwarding boundary must
		// suppress this duplicate identity and report the unresolved beta call.
		resultID = fmt.Sprintf("call-%03d-alpha", args.Turn)
	}
	return messages.ToolCallResponse{ToolCallID: resultID, Name: name, Content: content}, nil
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

func runtimeSnapshotFrom(value memStats) runtimeSnapshot {
	return runtimeSnapshot{HeapLiveBytes: value.heapLive, TotalAllocBytes: value.total, HeapObjects: value.objects, Goroutines: value.goroutine}
}

func runtimeDeltaFrom(before, after runtimeSnapshot) runtimeDelta {
	return runtimeDelta{HeapLiveBytes: int64(after.HeapLiveBytes) - int64(before.HeapLiveBytes), TotalAllocBytes: int64(after.TotalAllocBytes) - int64(before.TotalAllocBytes), HeapObjects: int64(after.HeapObjects) - int64(before.HeapObjects), Goroutines: after.Goroutines - before.Goroutines}
}

func measurementPolicyValue() measurementPolicy {
	return measurementPolicy{
		GC:       "no forced GC; runtime.ReadMemStats sampled before and after the public run",
		Observer: "event collector, recording usage reporter and two runtime.ReadMemStats samples; observer overhead is not subtracted",
		Clock:    "request-to-PCM and request-to-terminal use injected deterministic event timestamps; process elapsed uses monotonic wall time",
	}
}

func emptyStateRecord() stateRecord {
	return stateRecord{Availability: map[string]string{"conversation_items": "not_started", "tool_calls": "not_started", "tool_results": "not_started", "recording_items": "not_started"}, Scope: "publicly observed events; private retained state is unavailable"}
}

func c23RecordingLimits() recording.ResourceLimits {
	return recording.ResourceLimits{
		TranscriptBytes: maxRecordingTranscriptBytes, TranscriptItems: maxRecordingTranscriptItems,
		AudioBytes: maxRecordingAudioBytes, AudioItems: maxRecordingAudioItems,
		SidecarBytes: maxRecordingSidecarBytes, SidecarItems: maxRecordingSidecarItems,
		MetadataBytes: maxRecordingMetadataBytes, MetadataItems: maxRecordingMetadataItems,
		TerminalBytes: maxRecordingTerminalBytes, TerminalItems: maxRecordingTerminalItems,
		ProviderBytes: maxRecordingProviderBytes, ProviderItems: maxRecordingProviderItems,
	}
}

func recordingLimitsMap(limits recording.ResourceLimits) map[string]int64 {
	return map[string]int64{
		"transcript_bytes": limits.TranscriptBytes, "transcript_items": limits.TranscriptItems,
		"audio_bytes": limits.AudioBytes, "audio_items": limits.AudioItems,
		"sidecar_bytes": limits.SidecarBytes, "sidecar_items": limits.SidecarItems,
		"metadata_bytes": limits.MetadataBytes, "metadata_items": limits.MetadataItems,
		"terminal_bytes": limits.TerminalBytes, "terminal_items": limits.TerminalItems,
		"provider_bytes": limits.ProviderBytes, "provider_items": limits.ProviderItems,
	}
}

func recordingUsageMap(usage recording.ResourceUsage) map[string]int64 {
	return map[string]int64{
		"queue_bytes": usage.QueueBytes, "queue_items": usage.QueueItems, "peak_queue_bytes": usage.PeakQueueBytes, "peak_queue_items": usage.PeakQueueItems,
		"accepted_items": usage.AcceptedItems, "processed_items": usage.ProcessedItems, "accepted_messages": usage.AcceptedMessages, "accepted_audio": usage.AcceptedAudio, "accepted_events": usage.AcceptedEvents,
		"transcript_bytes": usage.TranscriptBytes, "transcript_items": usage.TranscriptItems, "audio_bytes": usage.AudioBytes, "audio_items": usage.AudioItems,
		"sidecar_bytes": usage.SidecarBytes, "sidecar_items": usage.SidecarItems, "metadata_bytes": usage.MetadataBytes, "metadata_items": usage.MetadataItems,
		"terminal_bytes": usage.TerminalBytes, "terminal_items": usage.TerminalItems, "summary_bytes": usage.SummaryBytes, "summary_items": usage.SummaryItems, "peak_summary_bytes": usage.PeakSummaryBytes, "peak_summary_items": usage.PeakSummaryItems,
		"provider_queue_bytes": usage.ProviderQueueBytes, "provider_queue_items": usage.ProviderQueueItems, "peak_provider_queue_bytes": usage.PeakProviderQueueBytes, "peak_provider_queue_items": usage.PeakProviderQueueItems,
		"provider_accepted_items": usage.ProviderAcceptedItems, "provider_bytes": usage.ProviderBytes, "provider_items": usage.ProviderItems, "peak_provider_bytes": usage.PeakProviderBytes, "peak_provider_items": usage.PeakProviderItems,
	}
}

func recordingUsageFor(recorder session.LiveRecorder, provider recording.ResourceUsageReporter, enabled bool, limits recording.ResourceLimits, drops uint64) recordingUsageRecord {
	result := recordingUsageRecord{Enabled: enabled, Available: false, Limits: recordingLimitsMap(limits), Drops: map[string]uint64{"live_event_overflow": drops}}
	if provider != nil {
		result.ProviderCapture = true
		result.ProviderCaptureUsage = recordingUsageMap(provider.ResourceUsage())
	}
	if !enabled {
		return result
	}
	reporter, ok := recorder.(recording.ResourceUsageReporter)
	if !ok {
		return result
	}
	result.Available = true
	result.Usage = recordingUsageMap(reporter.ResourceUsage())
	return result
}

func runBaseline(artifactRoot string) (*report, error) {
	if artifactRoot != "" {
		if err := os.MkdirAll(artifactRoot, 0o755); err != nil {
			return nil, err
		}
	}
	start := time.Now()
	before := readMemStats()
	after := readMemStats()
	baselineSnapshot := runtimeSnapshotFrom(before)
	afterSnapshot := runtimeSnapshotFrom(after)
	result := &report{Schema: "c23.v1", Scenario: "baseline", SourceRevision: sourceRevision(), FixtureSHA256: loadedFixtureDigest, ConsumerSurface: "public-runtime-observer", TraceComplete: true, CleanShutdown: true, RecordingUsage: recordingUsageFor(nil, nil, false, c23RecordingLimits(), 0)}
	result.Runtime = runtimeRecord{ElapsedMS: time.Since(start).Milliseconds(), HeapLiveBytes: after.heapLive, HeapAllocatedBytes: after.total - before.total, HeapObjects: after.objects, Goroutines: after.goroutine, RSSAvailability: "unavailable_in_public_consumer", CPUAvailability: "unavailable_in_public_consumer", Baseline: baselineSnapshot, After: afterSnapshot, Delta: runtimeDeltaFrom(baselineSnapshot, afterSnapshot), State: emptyStateRecord(), Measurement: measurementPolicyValue()}
	result.Runtime.State.Availability["conversation_items"] = "baseline_empty_observation"
	result.Runtime.State.Availability["tool_calls"] = "baseline_empty_observation"
	result.Runtime.State.Availability["tool_results"] = "baseline_empty_observation"
	result.Runtime.State.Availability["recording_items"] = "not_enabled"
	return result, nil
}

func runToolMatrix(turns int, recordingEnabled bool, artifactRoot string, toolControl string) (*report, error) {
	if turns <= 0 || turns > 256 {
		return &report{Schema: "c23.v1", Scenario: "tool-matrix", Turns: turns, Recording: recordingEnabled, ToolControl: toolControl, SourceRevision: sourceRevision(), FixtureSHA256: loadedFixtureDigest, ConsumerSurface: "public-live-service+public-recording-wire"}, fmt.Errorf("turn count %d outside 1..256", turns)
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
	limits := c23RecordingLimits()
	reportValue := &report{Schema: "c23.v1", Scenario: "tool-matrix", Turns: turns, Recording: recordingEnabled, ToolControl: toolControl, SourceRevision: sourceRevision(), FixtureSHA256: loadedFixtureDigest, ConsumerSurface: "public-live-service+public-recording-wire", TraceComplete: true, RecordingUsage: recordingUsageFor(nil, nil, recordingEnabled, limits, 0)}
	base := clock.NewDeterministic(time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC), time.Millisecond)
	advance := func() { base.Advance() }
	var pace func()
	if recordingEnabled {
		// Keep the bounded recording worker ahead of the deterministic fixture
		// without changing its messages, ordering, or semantic expectations.
		pace = func() { time.Sleep(recordingFixturePace) }
	}
	provider := newFixtureSession("matrix", turns, advance, pace, toolControl)
	toolExecutor := &fixtureToolExecutor{control: toolControl}
	collector := newEventCollector()
	providerPath := filepath.Join(artifactRoot, "provider.session.json")
	providerCaptureService := recordingwire.NewProviderCaptureService(base)
	providerCapture, err := providerCaptureService.OpenProviderCapture(recording.ProviderCaptureOptions{Destination: providerPath, Limits: limits})
	if err != nil {
		return reportValue, err
	}
	providerUsage, _ := providerCapture.(recording.ResourceUsageReporter)
	defer func() { _ = providerCapture.Abort() }()
	var capturedProvider *providerCaptureSession
	factory := func(_ context.Context, request session.LiveRequest) (messages.SessionInferencer, error) {
		if request.SessionID == "" {
			return nil, errors.New("fixture request session id is empty")
		}
		provider.queue(context.Background(), messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue(request.SessionID, "audio_inference")})
		capturedProvider = newProviderCaptureSession(provider, providerCapture, base, request.SessionID)
		return fixtureInferencer{provider: capturedProvider, flush: func() error { return capturedProvider.FlushCapture(providerPath) }}, nil
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
		recorder, err = recordingwire.NewService(base).OpenLiveEvidence(recording.LiveEvidenceOptions{Destination: semanticRoot, SessionID: "c23-matrix", ParticipantID: "fixture", Provider: "c23-deterministic", Model: "c23-public-fixture", ClockBase: base.Now(), WallClockStart: time.Now(), ProviderCapturePath: providerPath, DisableProviderCaptureSidecar: true, Limits: limits})
		if err != nil {
			return reportValue, err
		}
	}
	runTimeout := 55 * time.Second
	maxDuration := runTimeout
	if toolControl != "" {
		runTimeout = 2 * time.Second
		// Let the public live runner report its unresolved tool-result cause
		// when a malformed result is suppressed before provider admission. The
		// outer context remains the bounded child deadline for this control.
		maxDuration = 0
	}
	request := session.LiveRequest{SessionID: "c23-matrix", ParticipantID: "fixture", Provider: "c23-deterministic", Model: "c23-public-fixture", OpeningPrompt: "c23 deterministic opening", OpeningPromptPresent: true, OutputAudioSampleRate: fixtureSampleRate(), OutputAudioContinuous: true, ToolNames: []string{"lookup_alpha", "lookup_beta"}, FinishAfterResponse: true, ExpectedResponses: turns, MaxDuration: maxDuration}
	start := time.Now()
	before := readMemStats()
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	runErr := runner.RunLive(ctx, session.LiveRunOptions{Request: request, Events: session.LiveEventSinkFunc(collector.publish), Recorder: recorder})
	cancel()
	elapsed := time.Since(start)
	after := readMemStats()
	baselineSnapshot := runtimeSnapshotFrom(before)
	afterSnapshot := runtimeSnapshotFrom(after)
	reportValue.Runtime = runtimeRecord{ElapsedMS: elapsed.Milliseconds(), HeapLiveBytes: after.heapLive, HeapAllocatedBytes: after.total - before.total, HeapObjects: after.objects, Goroutines: after.goroutine, RSSAvailability: "unavailable_in_public_consumer", CPUAvailability: "unavailable_in_public_consumer", Baseline: baselineSnapshot, After: afterSnapshot, Delta: runtimeDeltaFrom(baselineSnapshot, afterSnapshot), Measurement: measurementPolicyValue()}
	if capturedProvider != nil {
		if err := capturedProvider.FlushCapture(providerPath); err != nil && runErr == nil {
			runErr = fmt.Errorf("flush provider capture: %w", err)
		}
	}
	reportValue.Artifacts.ProviderCapture = providerPath
	if failure := provider.queueErrorValue(); failure != nil {
		runErr = errors.Join(runErr, failure)
	}
	events, responses, calls, _, trace, pcm, latency, terminal, terminalSeen, traceComplete := collector.snapshot()
	results := provider.snapshotToolResults()
	reportValue.Events, reportValue.Responses, reportValue.ToolCalls, reportValue.ToolResults, reportValue.Trace, reportValue.PCM, reportValue.Latency, reportValue.Terminal, reportValue.TraceComplete = events, responses, calls, results, trace, pcm, latency, terminal, traceComplete
	reportValue.RecordingUsage = recordingUsageFor(recorder, providerUsage, recordingEnabled, limits, events.OverflowDrops)
	reportValue.Overlap = provider.snapshotOverlap()
	reportValue.Control = runtimeControlObservation(toolControl, runErr)
	reportValue.Overlap.CrossRoutingVerified = toolResultsMatchCalls(calls, results)
	reportValue.Overlap.ExactlyOnceToolResultsVerified = len(results) == len(uniqueToolResultIDs(results))
	state := emptyStateRecord()
	state.ConversationItemsObserved = len(responses)
	state.ToolCallsObserved = len(calls)
	state.ToolResultsObserved = len(results)
	state.Availability["conversation_items"] = "available_public_event_count"
	state.Availability["tool_calls"] = "available_public_event_count"
	state.Availability["tool_results"] = "available_public_event_count"
	if recordingEnabled && reportValue.RecordingUsage.Available {
		if accepted, ok := reportValue.RecordingUsage.Usage["accepted_items"]; ok {
			state.RecordingItemsObserved = &accepted
			state.Availability["recording_items"] = "available_public_recording_usage"
		}
	} else {
		state.Availability["recording_items"] = "not_enabled"
	}
	reportValue.Runtime.State = state
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
	if !terminalSeen || !completionTerminal(result.Terminal, string(messages.TerminalReasonProviderAuthoredCompletion)) {
		return fmt.Errorf("live terminal evidence is not an exact provider completion: %+v", result.Terminal)
	}
	if len(result.ToolCalls) != turns*2 || len(result.ToolResults) != turns*2 {
		return fmt.Errorf("tool call/result count mismatch: calls=%d results=%d want=%d", len(result.ToolCalls), len(result.ToolResults), turns*2)
	}
	for turn := 0; turn < turns; turn++ {
		for offset, suffix := range []string{"alpha", "beta"} {
			index := turn*2 + offset
			callID := fmt.Sprintf("call-%03d-%s", turn, suffix)
			name := "lookup_" + suffix
			call := result.ToolCalls[index]
			if call.ID != callID || call.Name != name || call.Turn != turn {
				return fmt.Errorf("provider tool call order/correlation mismatch at %d: %+v", index, call)
			}
			item := result.ToolResults[index]
			if item.ID != callID || item.Name != name || item.Turn != turn || item.Content != fmt.Sprintf("result:%s:%03d", name, turn) {
				return fmt.Errorf("tool result order/correlation mismatch at %d: %+v", index, item)
			}
		}
	}
	if len(result.Responses) != turns*2 {
		return fmt.Errorf("response identity count=%d want=%d", len(result.Responses), turns*2)
	}
	wantPCM := make([]byte, 0, turns*fixtureSamplesPerTurn()*2)
	for turn := 0; turn < turns; turn++ {
		wantPCM = append(wantPCM, pcmForTurn(turn)...)
	}
	if result.PCM.Bytes != len(wantPCM) || result.PCM.SHA256 != sha256Hex(wantPCM) {
		return fmt.Errorf("PCM oracle mismatch: bytes=%d sha=%s", result.PCM.Bytes, result.PCM.SHA256)
	}
	if result.PCM.SampleRate != fixtureSampleRate() || result.PCM.FrameSamples != fixtureSamplesPerTurn() || result.PCM.Channels != 1 || result.PCM.BitDepth != 16 {
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
	if len(result.Trace) == 0 || len(result.Trace) > maxTraceEvents {
		return errors.New("ordered live trace is missing or outside bounds")
	}
	for index, item := range result.Trace {
		if item.Sequence != index {
			return fmt.Errorf("ordered live trace sequence %d at index %d", item.Sequence, index)
		}
	}
	wantCalls := make([]string, 0, turns*2)
	for turn := 0; turn < turns; turn++ {
		wantCalls = append(wantCalls, fmt.Sprintf("call-%03d-alpha", turn), fmt.Sprintf("call-%03d-beta", turn))
	}
	gotCalls := make([]string, 0, len(wantCalls))
	for _, item := range result.Trace {
		if item.Kind == string(messages.StreamTypeToolCallEnd) && item.ToolCallID != "" {
			gotCalls = append(gotCalls, item.ToolCallID)
		}
	}
	if !slicesEqual(gotCalls, wantCalls) {
		return fmt.Errorf("ordered provider tool-call trace mismatch: got=%v want=%v", gotCalls, wantCalls)
	}
	return nil
}

func completionTerminal(value terminalRecord, classification string) bool {
	return value.Kind == string(session.LiveEventTerminal) &&
		value.Reason == string(messages.TerminalReasonProviderAuthoredCompletion) &&
		value.Classification == classification &&
		value.Provenance == string(messages.TerminalProvenanceProvider) &&
		value.OutputState == string(messages.TerminalOutputComplete)
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func toolResultsMatchCalls(calls, results []toolRecord) bool {
	if len(calls) != len(results) {
		return false
	}
	for index, call := range calls {
		result := results[index]
		if result.ID != call.ID || result.Name != call.Name || result.Turn != call.Turn {
			return false
		}
	}
	return true
}

func uniqueToolResultIDs(results []toolRecord) map[string]struct{} {
	unique := make(map[string]struct{}, len(results))
	for _, result := range results {
		unique[result.ID] = struct{}{}
	}
	return unique
}

func runInterruption(artifactRoot string) (result *report, err error) {
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
	result = &report{Schema: "c23.v1", Scenario: "interruption", SourceRevision: sourceRevision(), FixtureSHA256: loadedFixtureDigest, ConsumerSurface: "public-live-service", TraceComplete: true, RecordingUsage: recordingUsageFor(nil, nil, false, c23RecordingLimits(), 0)}
	base := clock.NewDeterministic(time.Date(2026, time.January, 2, 3, 5, 5, 0, time.UTC), time.Millisecond)
	provider := newFixtureSession("interruption", 1, func() { base.Advance() }, nil, "")
	collector := newEventCollector()
	startedAt := time.Now()
	before := readMemStats()
	service := sessionwire.NewLiveService(sessionwire.LiveDependencies{InferencerFactory: func(_ context.Context, request session.LiveRequest) (messages.SessionInferencer, error) {
		provider.queue(context.Background(), messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue(request.SessionID, "audio_inference")})
		return fixtureInferencer{provider: provider}, nil
	}, EventCapacity: 128, Clock: base.Now, Scheduler: clock.Real{}})
	request := session.LiveRequest{SessionID: "c23-interruption", ParticipantID: "fixture", Provider: "c23-deterministic", Model: "c23-interruption", OpeningPrompt: "start interrupt fixture", OpeningPromptPresent: true, OutputAudioSampleRate: fixtureSampleRate(), OutputAudioContinuous: true}
	handle, openErr := service.OpenLive(context.Background(), request)
	if openErr != nil {
		return result, openErr
	}
	result.Lifecycle.Opened = true
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if startErr := handle.Start(ctx); startErr != nil {
		result.Lifecycle.CloseCalled = true
		if closeErr := handle.Close(); closeErr != nil {
			result.Lifecycle.CloseError = closeErr.Error()
		}
		result.Lifecycle.WaitCalled = true
		if waitErr := handle.Wait(); waitErr != nil {
			result.Lifecycle.WaitError = waitErr.Error()
		}
		return result, startErr
	}
	result.Lifecycle.Started = true
	interrupt := &interruptRecord{}
	healthyDone := false
	cancellationEndSeen := false
	finalize := func(waitErr error) {
		for event := range handle.Events() {
			_ = collector.publish(context.Background(), event)
		}
		events, responses, calls, results, trace, pcm, latency, terminal, terminalSeen, traceComplete := collector.snapshot()
		result.Events, result.Responses, result.ToolCalls, result.ToolResults, result.Trace, result.PCM, result.Latency, result.Terminal, result.TraceComplete = events, responses, calls, results, trace, pcm, latency, terminal, traceComplete
		result.CleanShutdown = waitErr == nil && terminalSeen && providerClosed(provider)
		result.Interruption = interrupt
		interrupt.CleanTerminal = result.CleanShutdown
		healthyTail := collector.responseAudio(interrupt.HealthyResponseID)
		interrupt.HealthyTailBytes = len(healthyTail)
		interrupt.HealthyTailSHA256 = sha256Hex(healthyTail)
		interrupt.HealthyTailNonEmpty = len(healthyTail) > 0
		postCancelPublic, _, postTerminalAudio := postCancelAudio(collector, interrupt.CancelBoundarySequence)
		interrupt.PostCancelAudioObserved, interrupt.PostCancelAudioBytes = provider.snapshotInterruptionDelayedAudio()
		interrupt.PostCancelAudioPubliclyEmitted = postCancelPublic
		interrupt.ForbiddenPostCancelAudio = postTerminalAudio
		interrupt.CancelledOutputRejected = bytes.Equal(collector.responseAudio(interrupt.CancelResponseID), []byte{1, 2, 3, 4})
		after := readMemStats()
		baselineSnapshot := runtimeSnapshotFrom(before)
		afterSnapshot := runtimeSnapshotFrom(after)
		result.Runtime = runtimeRecord{ElapsedMS: time.Since(startedAt).Milliseconds(), HeapLiveBytes: after.heapLive, HeapAllocatedBytes: after.total - before.total, HeapObjects: after.objects, Goroutines: after.goroutine, RSSAvailability: "unavailable_in_public_consumer", CPUAvailability: "unavailable_in_public_consumer", Baseline: baselineSnapshot, After: afterSnapshot, Delta: runtimeDeltaFrom(baselineSnapshot, afterSnapshot), State: emptyStateRecord(), Measurement: measurementPolicyValue()}
		result.Runtime.State.ConversationItemsObserved = len(responses)
		result.Runtime.State.ToolCallsObserved = len(calls)
		result.Runtime.State.ToolResultsObserved = len(results)
		result.Runtime.State.Availability["conversation_items"] = "available_public_event_count"
		result.Runtime.State.Availability["tool_calls"] = "available_public_event_count"
		result.Runtime.State.Availability["tool_results"] = "available_public_event_count"
		result.Runtime.State.Availability["recording_items"] = "not_enabled"
	}
	defer func() {
		result.Lifecycle.CloseCalled = true
		closeErr := handle.Close()
		if closeErr != nil {
			result.Lifecycle.CloseError = closeErr.Error()
			if err == nil {
				err = fmt.Errorf("close interruption handle: %w", closeErr)
			}
		}
		result.Lifecycle.WaitCalled = true
		waitErr := handle.Wait()
		if waitErr != nil {
			result.Lifecycle.WaitError = waitErr.Error()
			if err == nil {
				err = waitErr
			}
		}
		finalize(waitErr)
		if err == nil && (!interrupt.CancelSent || interrupt.CancelBoundarySequence <= 0 || !interrupt.CancellationTerminalObserved || !interrupt.PostCancelAudioObserved || interrupt.PostCancelAudioPubliclyEmitted || !interrupt.CancelledOutputRejected || interrupt.ForbiddenPostCancelAudio || interrupt.HealthyResponseID == "" || !interrupt.HealthyTailNonEmpty || !result.CleanShutdown || !completionTerminal(result.Terminal, "fixture")) {
			err = errors.New("interruption recovery proof is incomplete")
		}
	}()
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
				interrupt.CancelBoundarySequence = collector.traceCount()
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
	result.Lifecycle.WaitCalled = true
	if waitErr != nil {
		return result, waitErr
	}
	return result, nil
}

func postCancelAudio(c *eventCollector, boundary int) (observed bool, bytes int, afterTerminal bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	terminalSequence := -1
	for sequence, item := range c.trace {
		if item.ResponseID == "interrupt-resp-1" && item.Kind == string(messages.StreamTypeMessageEnd) {
			terminalSequence = sequence
			break
		}
	}
	for sequence, item := range c.trace {
		if sequence <= boundary || item.ResponseID != "interrupt-resp-1" || item.Kind != string(messages.StreamTypeAudioDelta) {
			continue
		}
		observed = true
		bytes += item.PCMBytes
		if terminalSequence >= 0 && sequence > terminalSequence {
			afterTerminal = true
		}
	}
	return observed, bytes, afterTerminal
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
	pcm := make([]byte, fixtureSamplesPerTurn()*2)
	for index := 0; index < fixtureSamplesPerTurn(); index++ {
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
	scenario := flag.String("scenario", "tool-matrix", "baseline, tool-matrix, tool-control or interruption")
	fixturePath := flag.String("fixture", "", "JSON fixture path")
	turns := flag.Int("turns", 16, "number of deterministic tool turns")
	recordingEnabled := flag.Bool("recording", false, "enable public semantic recording")
	toolControl := flag.String("control", "", "negative tool-result control: missing-result or duplicate-result")
	artifactRoot := flag.String("artifact-root", "", "isolated artifact root")
	output := flag.String("output", "", "JSON report path")
	flag.Parse()

	var result *report
	var err error
	if err = loadFixture(*fixturePath); err != nil {
		result = &report{Schema: "c23.v1", Scenario: *scenario, SourceRevision: sourceRevision(), ConsumerSurface: "public-runtime-observer", Error: err.Error()}
		if writeErr := writeReport(*output, result); writeErr != nil {
			fmt.Fprintln(os.Stderr, writeErr)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	switch *scenario {
	case "baseline":
		result, err = runBaseline(*artifactRoot)
	case "tool-matrix":
		result, err = runToolMatrix(*turns, *recordingEnabled, *artifactRoot, "")
	case "tool-control":
		if *toolControl != "missing-result" && *toolControl != "duplicate-result" {
			result = &report{Schema: "c23.v1", Scenario: *scenario, ToolControl: *toolControl, SourceRevision: sourceRevision(), FixtureSHA256: loadedFixtureDigest, ConsumerSurface: "public-live-service"}
			err = fmt.Errorf("unsupported tool control %q", *toolControl)
			break
		}
		result, err = runToolMatrix(*turns, false, *artifactRoot, *toolControl)
	case "interruption":
		result, err = runInterruption(*artifactRoot)
	default:
		result = &report{Schema: "c23.v1", Scenario: *scenario, SourceRevision: sourceRevision(), FixtureSHA256: loadedFixtureDigest, ConsumerSurface: "public-live-service"}
		err = fmt.Errorf("unsupported scenario %q", *scenario)
	}
	if result == nil {
		result = &report{Schema: "c23.v1", Scenario: *scenario, SourceRevision: sourceRevision(), FixtureSHA256: loadedFixtureDigest}
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
