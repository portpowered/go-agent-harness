package sessions

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-loop/test/functional/internal/sessionmock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type MockSession = sessionmock.Session
type MockSessionInferencer = sessionmock.Inferencer

var NewMockSessionInferencer = sessionmock.NewInferencer

// SessionTranscript is a scenario-local, concurrency-safe transcript
// collector. It implements transcript.RecordSink so callers can provide it to
// the scenario and later inspect a stable copy of the records.
type SessionTranscript struct {
	mu      sync.RWMutex
	records []transcript.Record
}

// NewSessionTranscript creates an empty both-side transcript collector.
func NewSessionTranscript() *SessionTranscript { return &SessionTranscript{} }

// NewSessionCapture is a descriptive alias for NewSessionTranscript.
func NewSessionCapture() *SessionTranscript { return NewSessionTranscript() }

// TranscriptCapture and SessionCapture are compatibility aliases for callers
// that name the capability rather than the storage implementation.
type TranscriptCapture = SessionTranscript
type SessionCapture = SessionTranscript

// Write appends an owned copy of record. Capture failures must never alter the
// live session path, so the in-memory collector always accepts the record.
func (c *SessionTranscript) Write(record transcript.Record) error {
	if c == nil {
		return nil
	}
	record.Payload = append([]byte(nil), record.Payload...)
	c.mu.Lock()
	c.records = append(c.records, record)
	c.mu.Unlock()
	return nil
}

// Records returns records in the order the crossings were observed.
func (c *SessionTranscript) Records() []transcript.Record {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cloneTranscriptRecords(c.records)
}

// Snapshot is an alias for Records.
func (c *SessionTranscript) Snapshot() []transcript.Record { return c.Records() }

// ClientRecords returns only client-authored records while preserving order.
func (c *SessionTranscript) ClientRecords() []transcript.Record {
	return filterTranscriptRecords(c.Records(), transcript.PeerClient)
}

// AgentRecords returns only agent-authored records while preserving order.
func (c *SessionTranscript) AgentRecords() []transcript.Record {
	return filterTranscriptRecords(c.Records(), transcript.PeerAgent)
}

// SessionScenarioOptions configures optional harness-only behavior. A zero
// value preserves the existing session fixture behavior.
type SessionScenarioOptions struct {
	Clock   clock.Source
	Capture transcript.RecordSink
}

// SessionScenarioConfig is a descriptive alias for SessionScenarioOptions.
type SessionScenarioConfig = SessionScenarioOptions

// SessionScenarioOption configures a SessionScenario without changing the
// agentloop.Option contract used by existing callers.
type SessionScenarioOption func(*SessionScenarioOptions)

// WithClock injects the clock used for transcript metadata. Nil is resolved
// to clock.Real by the scenario constructor.
func WithClock(source clock.Source) SessionScenarioOption {
	return func(options *SessionScenarioOptions) { options.Clock = source }
}

// WithSessionClock is an explicit alias for WithClock.
func WithSessionClock(source clock.Source) SessionScenarioOption { return WithClock(source) }

// WithCapture enables both-side capture. With no sink it creates a collector
// that can be read through SessionScenario.CapturedRecords.
func WithCapture(sinks ...transcript.RecordSink) SessionScenarioOption {
	return func(options *SessionScenarioOptions) {
		if len(sinks) == 0 {
			options.Capture = NewSessionTranscript()
			return
		}
		options.Capture = sinks[0]
	}
}

// WithSessionCapture is an explicit alias for WithCapture.
func WithSessionCapture(sinks ...transcript.RecordSink) SessionScenarioOption {
	return WithCapture(sinks...)
}

// WithTranscriptCapture is an explicit alias for WithCapture.
func WithTranscriptCapture(sink transcript.RecordSink) SessionScenarioOption {
	return WithCapture(sink)
}

// ---------------------------------------------------------------------------
// SessionScenario
// ---------------------------------------------------------------------------

// SessionScenario manages a session-mode AgenticLoop lifecycle for testing.
// Create with NewSessionScenario, start with Start, inject events, then stop.
type SessionScenario struct {
	t    *testing.T
	Loop *agentloop.AgentLoop
	Inf  *MockSessionInferencer
	Tool *MockToolExecutor

	clock      clock.Source
	capture    *sessionCapture
	Transcript *SessionTranscript

	cancel   context.CancelFunc
	errCh    chan error
	deltasMu sync.Mutex
	deltas   []messages.StreamMessage
}

// NewSessionScenario creates a SessionScenario with the given mock inferencer
// and tool executor. Additional agentloop.Option values (e.g. WithTools) can
// be passed, preserving the typed variadic contract used by existing callers.
// Use NewSessionScenarioWithConfig when deterministic clocks or capture are
// needed alongside those loop options.
func NewSessionScenario(t *testing.T, inf *MockSessionInferencer, tool *MockToolExecutor, opts ...agentloop.Option) *SessionScenario {
	t.Helper()
	return newSessionScenario(t, inf, tool, SessionScenarioOptions{}, opts...)
}

// NewSessionScenarioWithConfig is the typed constructor for callers that keep
// agentloop options in a typed slice.
func NewSessionScenarioWithConfig(t *testing.T, inf *MockSessionInferencer, tool *MockToolExecutor, options SessionScenarioOptions, opts ...agentloop.Option) *SessionScenario {
	t.Helper()
	return newSessionScenario(t, inf, tool, options, opts...)
}

// NewSessionScenarioWithOptions is a naming alias for
// NewSessionScenarioWithConfig.
func NewSessionScenarioWithOptions(t *testing.T, inf *MockSessionInferencer, tool *MockToolExecutor, options SessionScenarioOptions, opts ...agentloop.Option) *SessionScenario {
	return NewSessionScenarioWithConfig(t, inf, tool, options, opts...)
}

func newSessionScenario(t *testing.T, inf *MockSessionInferencer, tool *MockToolExecutor, options SessionScenarioOptions, opts ...agentloop.Option) *SessionScenario {
	allOpts := []agentloop.Option{
		agentloop.WithSessionInferencer(inf),
		agentloop.WithToolExecutor(tool),
		agentloop.WithMode(engine.DuplexSession),
	}
	allOpts = append(allOpts, opts...)

	loop, err := agentloop.New(allOpts...)
	if err != nil {
		t.Fatalf("NewSessionScenario: failed to create loop: %v", err)
	}

	resolvedClock := clock.Ensure(options.Clock)
	scenario := &SessionScenario{
		t:     t,
		Loop:  loop,
		Inf:   inf,
		Tool:  tool,
		clock: resolvedClock,
	}
	if options.Capture != nil {
		scenario.capture = newSessionCapture(resolvedClock, options.Capture)
		if collector, ok := options.Capture.(*SessionTranscript); ok {
			scenario.Transcript = collector
		}
	}
	return scenario
}

// Start begins the session. Call this before sending events.
func (s *SessionScenario) Start() {
	s.t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.errCh = make(chan error, 1)

	// Collect deltas from the loop's consumer-facing Deltas() buffer in background.
	go func() {
		for {
			delta, ok := s.Loop.Deltas().ReadBlockingContext(ctx)
			if !ok {
				return
			}
			if s.capture != nil {
				s.capture.agentToClient(delta)
			}
			s.deltasMu.Lock()
			s.deltas = append(s.deltas, delta)
			s.deltasMu.Unlock()
		}
	}()

	// Start the loop.
	go func() {
		s.errCh <- s.Loop.Run(ctx)
	}()

	// Brief wait for engine to initialize.
	time.Sleep(50 * time.Millisecond)
}

// SendControlPlane sends a control plane message to the session (e.g. session_close, stop, ping).
func (s *SessionScenario) SendControlPlane(cpType messages.ControlPlaneMessageType) {
	msg := messages.Message{
		Role: messages.RoleUser,
		ContentParts: []messages.ContentPart{
			messages.ControlPlanePart{ControlPlaneMessageType: cpType},
		},
	}
	if err := s.Loop.Send(context.Background(), []messages.Message{msg}); err != nil {
		s.t.Fatalf("SessionScenario.SendControlPlane: %v", err)
	}
	if s.capture != nil {
		s.capture.clientToAgent(transcript.StreamWS, messagePayload(msg))
	}
}

// SendAudioInput sends raw PCM audio to the session loop for user audio forwarding
// and barge-in. Panics if the loop is not in session mode.
func (s *SessionScenario) SendAudioInput(pcm []byte) {
	s.t.Helper()
	if err := s.Loop.SendAudioInput(context.Background(), pcm); err != nil {
		s.t.Fatalf("SessionScenario.SendAudioInput: %v", err)
	}
	if s.capture != nil {
		s.capture.clientToAgent(transcript.StreamRTCAudio, append([]byte(nil), pcm...))
	}
}

// SendText sends a text message to the session.
func (s *SessionScenario) SendText(text string) {
	msg := messages.NewTextMessage(messages.RoleUser, text)
	if err := s.Loop.Send(context.Background(), []messages.Message{msg}); err != nil {
		s.t.Fatalf("SessionScenario.SendText: %v", err)
	}
	if s.capture != nil {
		s.capture.clientToAgent(transcript.StreamWS, messagePayload(msg))
	}
}

// Stop triggers a graceful session close. It sends session_close, waits
// briefly for the events to propagate, then cancels the context and
// closes the mock inferencer.
func (s *SessionScenario) Stop(timeout time.Duration) error {
	s.SendControlPlane(messages.ControlPlaneMessageTypeSessionClose)

	// Brief wait for the control plane message to be processed by the engine.
	time.Sleep(200 * time.Millisecond)

	// Close the mock session (unblocks runSession if it's blocked on session.Done()).
	s.Inf.Close()

	// Cancel context to stop the engine hot loop.
	s.cancel()

	select {
	case err := <-s.errCh:
		// context.Canceled is expected — the loop was cancelled by us.
		if err == context.Canceled {
			return nil
		}
		return err
	case <-time.After(timeout):
		return context.DeadlineExceeded
	}
}

// Deltas returns a copy of all collected delta events.
func (s *SessionScenario) Deltas() []messages.StreamMessage {
	s.deltasMu.Lock()
	defer s.deltasMu.Unlock()
	out := make([]messages.StreamMessage, len(s.deltas))
	copy(out, s.deltas)
	return out
}

// DeltaProgress returns the number of collected deltas matching match,
// without copying the stream. Concurrency tests poll it many times per
// logical tick, so it must not allocate.
func (s *SessionScenario) DeltaProgress(match func(messages.StreamMessage) bool) int {
	if s == nil {
		return 0
	}
	s.deltasMu.Lock()
	defer s.deltasMu.Unlock()
	count := 0
	for _, delta := range s.deltas {
		if match(delta) {
			count++
		}
	}
	return count
}

// Clock returns the scenario's resolved clock. It is clock.Real when no
// source was supplied and never advances an injected deterministic clock.
func (s *SessionScenario) Clock() clock.Source { return s.clock }

// CapturedRecords returns a stable copy of the configured transcript records.
func (s *SessionScenario) CapturedRecords() []transcript.Record {
	if s == nil || s.Transcript == nil {
		return nil
	}
	return s.Transcript.Records()
}

// ClientRecords returns the client-authored records from the configured
// collector.
func (s *SessionScenario) ClientRecords() []transcript.Record {
	if s == nil || s.Transcript == nil {
		return nil
	}
	return s.Transcript.ClientRecords()
}

// AgentRecords returns the agent-authored records from the configured
// collector.
func (s *SessionScenario) AgentRecords() []transcript.Record {
	if s == nil || s.Transcript == nil {
		return nil
	}
	return s.Transcript.AgentRecords()
}

// WaitForEvent blocks until a delta event with the given type appears or times out.
func (s *SessionScenario) WaitForEvent(eventType messages.StreamMessageType, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		s.deltasMu.Lock()
		for _, d := range s.deltas {
			if d.Type == eventType {
				s.deltasMu.Unlock()
				return true
			}
		}
		s.deltasMu.Unlock()

		select {
		case <-deadline:
			return false
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// ---------------------------------------------------------------------------
// Session assertion helpers
// ---------------------------------------------------------------------------

// AssertSessionDeltaContains checks that the delta slice contains at least one
// event matching every entry in required (subsequence matching).
func AssertSessionDeltaContains(t *testing.T, deltas []messages.StreamMessage, required []ExpectedDelta) {
	t.Helper()
	AssertDeltaContains(t, deltas, required)
}

// AssertSessionLifecycle verifies that SESSION.OPEN is the first event and
// SESSION.CLOSE + LOOP.END are the final events in correct order.
func AssertSessionLifecycle(t *testing.T, deltas []messages.StreamMessage) {
	t.Helper()
	if len(deltas) < 3 {
		t.Fatalf("expected at least 3 delta events (SESSION.OPEN, SESSION.CLOSE, LOOP.END), got %d", len(deltas))
	}

	if deltas[0].Type != messages.StreamTypeSessionOpen {
		t.Errorf("first event should be SESSION.OPEN, got %q", deltas[0].Type)
	}

	n := len(deltas)
	if deltas[n-1].Type != messages.StreamTypeLoopEnd {
		t.Errorf("last event should be LOOP.END, got %q", deltas[n-1].Type)
	}
	if deltas[n-2].Type != messages.StreamTypeSessionClose {
		t.Errorf("second-to-last event should be SESSION.CLOSE, got %q", deltas[n-2].Type)
	}
}

type sessionCapture struct {
	crossingMu sync.Mutex
	source     clock.Source
	sink       transcript.RecordSink
	sequence   atomic.Uint64
}

type sessionSnapshot struct {
	tick      uint64
	timestamp time.Time
}

func (s sessionSnapshot) Now() time.Time { return s.timestamp }
func (s sessionSnapshot) Tick() uint64   { return s.tick }

func newSessionCapture(source clock.Source, sink transcript.RecordSink) *sessionCapture {
	return &sessionCapture{source: clock.Ensure(source), sink: sink}
}

func (s *sessionCapture) snapshot() sessionSnapshot {
	tick := s.sequence.Add(1)
	if source, ok := s.source.(interface{ Tick() uint64 }); ok {
		tick = source.Tick()
	}
	return sessionSnapshot{tick: tick, timestamp: s.source.Now()}
}

func (s *sessionCapture) clientToAgent(stream transcript.Stream, payload []byte) {
	if s == nil || s.sink == nil || len(payload) == 0 {
		return
	}
	s.crossingMu.Lock()
	defer s.crossingMu.Unlock()
	snapshot := s.snapshot()
	client := transcript.NewClientCapture(&sessionClientStreamSink{sink: s.sink, stream: stream}, func() (uint64, time.Time) {
		return snapshot.tick, snapshot.timestamp
	})
	wire := &sessionCaptureWebSocket{}
	if err := client.WrapWebSocket(wire).WriteMessage(1, payload); err != nil {
		return
	}
	agent := transcript.NewAgentCapture(s.sink, snapshot)
	_, _ = agent.Inbound(stream, payload, func(data []byte) (int, error) {
		return len(data), nil
	})
}

func (s *sessionCapture) agentToClient(delta messages.StreamMessage) {
	if s == nil || s.sink == nil {
		return
	}
	payload := streamPayload(delta)
	if len(payload) == 0 {
		return
	}
	s.crossingMu.Lock()
	defer s.crossingMu.Unlock()
	snapshot := s.snapshot()
	agent := transcript.NewAgentCapture(s.sink, snapshot)
	_, _ = agent.Outbound(streamForDelta(delta), payload, func(data []byte) (int, error) {
		return len(data), nil
	})
	wire := &sessionCaptureWebSocket{readPayload: append([]byte(nil), payload...)}
	client := transcript.NewClientCapture(&sessionClientStreamSink{sink: s.sink, stream: streamForDelta(delta)}, func() (uint64, time.Time) {
		return snapshot.tick, snapshot.timestamp
	})
	_, _, _ = client.WrapWebSocket(wire).ReadMessage()
}

type sessionCaptureWebSocket struct {
	readPayload []byte
}

// ClientCapture's WebSocket adapter correctly identifies the client peer and
// direction but defaults its transport to StreamWS. The session harness knows
// whether the crossing is text/data or audio, so this narrow sink adapter keeps
// that typed stream identity while leaving record ownership and capture failure
// behavior to the transcript package.
type sessionClientStreamSink struct {
	sink   transcript.RecordSink
	stream transcript.Stream
}

func (s *sessionClientStreamSink) Write(record transcript.Record) error {
	record.Stream = s.stream
	return s.sink.Write(record)
}

func (*sessionCaptureWebSocket) WriteMessage(_ int, _ []byte) error { return nil }

func (w *sessionCaptureWebSocket) ReadMessage() (int, []byte, error) {
	return 1, append([]byte(nil), w.readPayload...), nil
}

func messagePayload(message messages.Message) []byte {
	if text := message.TextContent(); text != "" {
		return []byte(text)
	}
	for _, part := range message.ContentParts {
		switch value := part.(type) {
		case messages.ControlPlanePart:
			if value.ControlPlaneMessageType != "" {
				return []byte(value.ControlPlaneMessageType)
			}
		case messages.AudioPart:
			if len(value.Bytes) > 0 {
				return append([]byte(nil), value.Bytes...)
			}
		case messages.ImagePart:
			if len(value.Bytes) > 0 {
				return append([]byte(nil), value.Bytes...)
			}
		case messages.VideoPart:
			if len(value.Bytes) > 0 {
				return append([]byte(nil), value.Bytes...)
			}
		case messages.FilePart:
			if len(value.Bytes) > 0 {
				return append([]byte(nil), value.Bytes...)
			}
		}
	}
	return marshalPayload(message, []byte("message"))
}

func streamPayload(delta messages.StreamMessage) []byte {
	switch value := delta.Value.(type) {
	case *messages.TextDeltaValue:
		if value != nil && value.Content != "" {
			return []byte(value.Content)
		}
	case *messages.ReasoningDeltaValue:
		if value != nil && value.Content != "" {
			return []byte(value.Content)
		}
	case *messages.AudioDeltaValue:
		if value != nil && len(value.Content) > 0 {
			return append([]byte(nil), value.Content...)
		}
	case *messages.TranscriptDeltaValue:
		if value != nil && value.Text != "" {
			return []byte(value.Text)
		}
	case *messages.TranscriptEndValue:
		if value != nil && value.FullText != "" {
			return []byte(value.FullText)
		}
	}
	return marshalPayload(delta, []byte(delta.Type))
}

func marshalPayload(value any, fallback []byte) []byte {
	encoded, err := json.Marshal(value)
	if err == nil && len(encoded) > 0 {
		return encoded
	}
	return append([]byte(nil), fallback...)
}

func streamForDelta(delta messages.StreamMessage) transcript.Stream {
	switch delta.Type {
	case messages.StreamTypeAudioStart, messages.StreamTypeAudioDelta, messages.StreamTypeAudioEnd,
		messages.StreamTypeVADSpeechStarted, messages.StreamTypeVADSpeechStopped:
		return transcript.StreamRTCAudio
	default:
		return transcript.StreamWS
	}
}

func cloneTranscriptRecords(records []transcript.Record) []transcript.Record {
	cloned := make([]transcript.Record, len(records))
	for index, record := range records {
		record.Payload = append([]byte(nil), record.Payload...)
		cloned[index] = record
	}
	return cloned
}

func filterTranscriptRecords(records []transcript.Record, peer transcript.Peer) []transcript.Record {
	filtered := make([]transcript.Record, 0, len(records))
	for _, record := range records {
		if record.Peer == peer {
			filtered = append(filtered, record)
		}
	}
	return filtered
}
