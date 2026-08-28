package integration

// s2s v4d-tool-timeout vertical: hermetic integration proof that a
// never-returning tool call is bounded by an explicit ToolExecutionTimeout,
// that the deadline expiry surfaces as an observable correlated outcome on the
// session stream, and that the session degrades gracefully and keeps serving.
//
// Proven here through the real production session runtime — services.RunSession
// driving the actual duplex loop construction seam
// (services duplexSessionLoopOptions → newSessionToolExecutorWithTimeout →
// go-agent-loop) — with:
//   - an injected executor whose named call blocks forever and deliberately
//     ignores its context, so only the session tool adapter's deadline can end
//     the call;
//   - a short explicit ToolExecutionTimeout override far below the 60s
//     production default;
//   - a scripted session inferencer that gates every later provider turn on
//     the timeout outcome actually crossing the drained session stream, so
//     ordering and graceful degradation are proven causally, not hopefully;
//   - the canonical structured diagnostics proving post-timeout traffic crossed
//     before clean termination;
//   - two negative controls failing non-vacuously with explicit "missing
//     expected ..." messages: one without the override (default 60s bound
//     cannot produce the outcome) and one with the timeout outcome suppressed
//     by a fast executor.
//
// Why a scripted session inferencer instead of a raw WebSocket replay capture:
// the duplex session runner terminates synchronously on the provider close
// delta and routes tool batches asynchronously, so a canned capture cannot
// create the causal gate between deadline expiry and session teardown without
// racing it. The injected SessionInferencer is the established hermetic seam
// for exactly this (same pattern as the in-package session_tools tests), and
// everything under test — the composed executor wiring, the adapter deadline,
// the correlated failure content, and the session lifecycle — is production
// code reached through the exported RunSession entry point.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const (
	v4dTimeoutToolCallID = "call_v4d_timeout_001"
	v4dTimeoutToolName   = "get_weather"
	v4dQuickToolCallID   = "call_v4d_quick_002"
	v4dQuickToolName     = "get_time"

	// v4dQuickResultPayload is the deterministic success payload returned for
	// every non-hanging invocation.
	v4dQuickResultPayload = `{"utc":"2026-08-26T00:00:00Z"}`

	// v4dRecoveryTranscript and v4dContinuationText are the scripted
	// post-timeout assistant turns; their bytes are asserted through the
	// structured diagnostics sink alongside the rendered text deltas.
	v4dRecoveryTranscript = "Recovered from the delayed weather lookup."
	v4dContinuationText   = "Session continues to serve."

	// v4dTimeoutFailureText is the correlated adapter failure content for a
	// deadline expiry (services.sessionToolContextFailure).
	v4dTimeoutFailureText = "tool execution timed out"

	// v4dToolExecutionTimeout is the explicit short adapter override under
	// test: far above loop tick granularity, far below the 60s production
	// default, and the only mechanism that can end the hanging call.
	v4dToolExecutionTimeout = 25 * time.Millisecond

	// v4dLowerTimingSlack absorbs timer scheduling granularity on the lower
	// timing bound.
	v4dLowerTimingSlack = 2 * time.Millisecond

	// v4dOutcomeLatencyBound caps how long after start the timeout outcome may
	// cross while remaining attributable to the explicit override.
	v4dOutcomeLatencyBound = 1500 * time.Millisecond

	// v4dRunDeadguard is the hard test-level bound for one full run.
	v4dRunDeadguard = 5 * time.Second

	// v4dControlContextBound bounds every run so even a regression to a
	// missing adapter cannot wedge the suite past the go-test timeout.
	v4dControlContextBound = 15 * time.Second

	// v4dGateBound bounds each causal wait inside the scripted inferencer.
	v4dGateBound = 5 * time.Second
)

// v4DHangingExecutor models the pathological dependency: the named call blocks
// forever and ignores context cancellation, so no cooperative exit can end it
// — only the session tool adapter deadline can. Every other invocation answers
// instantly, proving the executor path keeps serving after a timeout.
type v4DHangingExecutor struct {
	mu    sync.Mutex
	calls []messages.ToolCall
}

func newV4DHangingExecutor() *v4DHangingExecutor { return &v4DHangingExecutor{} }

func (e *v4DHangingExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.mu.Lock()
	e.calls = append(e.calls, call)
	e.mu.Unlock()
	if call.ID == v4dTimeoutToolCallID {
		// Never return; deliberately ignore ctx to defeat cooperative cancel.
		select {}
	}
	return messages.ToolCallResponse{Content: v4dQuickResultPayload}, nil
}

func (e *v4DHangingExecutor) recordedCalls() []messages.ToolCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]messages.ToolCall(nil), e.calls...)
}

// v4DFastExecutor answers every call instantly, suppressing any timeout
// outcome; it drives the suppressed-outcome negative control.
type v4DFastExecutor struct{}

func (v4DFastExecutor) Execute(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
	return messages.ToolCallResponse{Content: v4dQuickResultPayload}, nil
}

// v4DCallRecorder lets the runner collect invocations from executors that
// record them.
type v4DCallRecorder interface {
	recordedCalls() []messages.ToolCall
}

// v4DObservableWriter captures the rendered session stream and timestamps the
// first chunk carrying the timeout outcome, giving a direct latency measure
// for the bounded completion. Observed chunks are mirrored onto a channel so
// the scripted inferencer can gate later turns causally.
type v4DObservableWriter struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	started  time.Time
	outcome  time.Time
	observed chan string
}

func newV4DObservableWriter() *v4DObservableWriter {
	return &v4DObservableWriter{
		started:  time.Now(),
		observed: make(chan string, 512),
	}
}

func (w *v4DObservableWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.buf.Write(p)
	isOutcome := w.outcome.IsZero() && bytes.Contains(p, []byte(v4dTimeoutFailureText))
	if isOutcome {
		w.outcome = time.Now()
	}
	w.mu.Unlock()
	if n > 0 {
		select {
		case w.observed <- string(p):
		default:
		}
	}
	return n, err
}

func (w *v4DObservableWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// waitForOutput blocks until want appears in the rendered stream or the bound
// elapses.
func (w *v4DObservableWriter) waitForOutput(want string, bound time.Duration) bool {
	deadline := time.After(bound)
	for {
		select {
		case chunk := <-w.observed:
			if strings.Contains(chunk, want) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func (w *v4DObservableWriter) waitForAnyOutput(wants []string, bound time.Duration) bool {
	deadline := time.After(bound)
	for {
		select {
		case chunk := <-w.observed:
			for _, want := range wants {
				if strings.Contains(chunk, want) {
					return true
				}
			}
		case <-deadline:
			return false
		}
	}
}

// outcomeLatency reports when the timeout outcome first crossed, and whether
// it was observed at all.
func (w *v4DObservableWriter) outcomeLatency() (time.Duration, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.outcome.IsZero() {
		return 0, false
	}
	return w.outcome.Sub(w.started), true
}

// v4DInMemorySession is a minimal messages.Session whose Receive buffer feeds
// the loop; Done closes with the session so the runner bridge can wind down.
type v4DInMemorySession struct {
	recv      *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	closeOnce sync.Once
}

var _ messages.Session = (*v4DInMemorySession)(nil)

func (s *v4DInMemorySession) Send(context.Context, messages.StreamMessage) bool { return true }

func (s *v4DInMemorySession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.recv }

func (s *v4DInMemorySession) Done() <-chan struct{} { return s.done }

func (s *v4DInMemorySession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

// v4DScriptedInferencer replays the provider side of the scenario with causal
// gates: the follow-up turns are released only after the earlier outcomes are
// observed in the drained session stream, and the session closes only after
// everything else crossed.
type v4DScriptedInferencer struct {
	out  *v4DObservableWriter
	done chan struct{}
	once sync.Once
}

var _ messages.SessionInferencer = (*v4DScriptedInferencer)(nil)

func newV4DScriptedInferencer(out *v4DObservableWriter) *v4DScriptedInferencer {
	return &v4DScriptedInferencer{out: out, done: make(chan struct{})}
}

func (i *v4DScriptedInferencer) Done() <-chan struct{} { return i.done }

func (i *v4DScriptedInferencer) finish() { i.once.Do(func() { close(i.done) }) }

func (i *v4DScriptedInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session := &v4DInMemorySession{recv: messages.NewTypedBuffer[messages.StreamMessage](64), done: make(chan struct{})}
	go func() {
		defer i.finish()
		write := func(msgs ...messages.StreamMessage) bool {
			for _, msg := range msgs {
				if !session.recv.Write(ctx, msg) {
					return false
				}
			}
			return true
		}
		if !write(
			messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("sess_v4d_timeout", "scripted")},
		) {
			return
		}
		// Turn envelope opens; the hanging tool call lands inside it. MESSAGE.END
		// assembles the assistant message and dispatches the tool batch.
		if !write(
			messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
			messages.StreamMessage{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, ActorProvidedIndex: 0, Value: messages.NewToolCallStartValue(v4dTimeoutToolCallID, v4dTimeoutToolName)},
			messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ActorProvidedIndex: 0, Value: messages.NewToolCallEndValue(v4dTimeoutToolCallID, v4dTimeoutToolName, `{"city":"Paris"}`)},
			messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
		) {
			return
		}
		// CAUSAL GATE: continue only once the first tool outcome actually crossed
		// the rendered session stream. The fast-executor negative control uses
		// the successful payload here; the shared validator then rejects its
		// transcript for lacking the required timeout outcome.
		if !i.out.waitForAnyOutput([]string{v4dTimeoutFailureText, v4dQuickResultPayload}, v4dGateBound) {
			return
		}
		// Post-timeout recovery turn plus a second, fast-succeeding tool call.
		if !write(
			messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()},
			messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(v4dRecoveryTranscript)},
			messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()},
			messages.StreamMessage{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, ActorProvidedIndex: 1, Value: messages.NewToolCallStartValue(v4dQuickToolCallID, v4dQuickToolName)},
			messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ActorProvidedIndex: 1, Value: messages.NewToolCallEndValue(v4dQuickToolCallID, v4dQuickToolName, `{}`)},
			messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
		) {
			return
		}
		// CAUSAL GATE: the fast result must cross before the session winds down.
		if !i.out.waitForOutput(v4dQuickResultPayload, v4dGateBound) {
			return
		}
		if !write(
			messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()},
			messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(v4dContinuationText)},
			messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()},
			messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
			messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("sess_v4d_timeout", "fixture_complete")},
		) {
			return
		}
	}()
	return session, nil
}

// v4DDiagnosticSink collects the canonical structured session diagnostic
// records emitted by the services observer seam.
type v4DDiagnosticSink struct {
	mu      sync.Mutex
	records []services.SessionDiagnosticRecord
}

func (s *v4DDiagnosticSink) RecordSessionDiagnostic(record services.SessionDiagnosticRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
}

func (s *v4DDiagnosticSink) recordsWith(event string) []services.SessionDiagnosticRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	var matched []services.SessionDiagnosticRecord
	for _, record := range s.records {
		if record.Event == event {
			matched = append(matched, record)
		}
	}
	return matched
}

// filepathV4DScratch satisfies the SessionRunOptions capture-path validation
// for the injected-inferencer replay branch; the file itself is never read by
// the runtime in this mode.
func filepathV4DScratch(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v4d-scratch.session.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write scratch replay path: %v", err)
	}
	return path
}

// runV4DSession drives services.RunSession — the real production session
// runtime — with an injected tool executor, an explicit adapter deadline
// override, and the scripted session inferencer.
func runV4DSession(t *testing.T, executor messages.ToolExecutor, timeout, runBound time.Duration) (*v4DObservableWriter, []messages.ToolCall, *v4DDiagnosticSink, error) {
	t.Helper()

	out := newV4DObservableWriter()
	sink := &v4DDiagnosticSink{}
	inferencer := newV4DScriptedInferencer(out)
	ctx, cancel := context.WithTimeout(context.Background(), runBound)
	defer cancel()
	err := services.RunSession(ctx, out, services.SessionRunOptions{
		ReplayPath:           filepathV4DScratch(t),
		SessionInferencer:    inferencer,
		WaitForClose:         true,
		ConfigDir:            t.TempDir(),
		ToolExecutor:         executor,
		ToolExecutionTimeout: timeout,
		Diagnostics:          sink,
	})

	var calls []messages.ToolCall
	if recorder, ok := executor.(v4DCallRecorder); ok {
		calls = recorder.recordedCalls()
	}
	return out, calls, sink, err
}

// v4DObservation carries everything the shared vertical validator judges.
type v4DObservation struct {
	output          string
	outputLatency   time.Duration // valid only when outcomeObserved
	outcomeObserved bool
	calls           []messages.ToolCall
	diagnostics     *v4DDiagnosticSink
}

// validateV4DBoundedTimeoutOutcome is the shared vertical assertion used by
// the positive path and both negative controls. Every problem is reported as
// an explicit "missing expected ..." expectation so a suppressed or vacuous
// run can never pass silently.
func validateV4DBoundedTimeoutOutcome(obs v4DObservation) error {
	var problems []string
	expect := func(ok bool, want string) {
		if !ok {
			problems = append(problems, want)
		}
	}

	hasCall := func(id, name string) bool {
		for _, call := range obs.calls {
			if call.ID == id && call.Name == name {
				return true
			}
		}
		return false
	}

	expect(hasCall(v4dTimeoutToolCallID, v4dTimeoutToolName),
		fmt.Sprintf("missing expected invocation of hanging tool %q (%s)", v4dTimeoutToolName, v4dTimeoutToolCallID))
	expect(hasCall(v4dQuickToolCallID, v4dQuickToolName),
		fmt.Sprintf("missing expected subsequent fast invocation %q (%s)", v4dQuickToolName, v4dQuickToolCallID))

	expect(obs.outcomeObserved, fmt.Sprintf("missing expected %q outcome on the session stream", v4dTimeoutFailureText))
	expect(strings.Contains(obs.output, v4dTimeoutFailureText),
		fmt.Sprintf("missing expected %q content in rendered output", v4dTimeoutFailureText))
	expect(strings.Contains(obs.output, v4dTimeoutToolName),
		fmt.Sprintf("missing expected timeout outcome naming tool %q", v4dTimeoutToolName))
	if obs.outcomeObserved {
		expect(obs.outputLatency >= v4dToolExecutionTimeout-v4dLowerTimingSlack,
			fmt.Sprintf("missing expected wait consistent with the configured %s bound: outcome crossed after %s", v4dToolExecutionTimeout, obs.outputLatency))
		expect(obs.outputLatency <= v4dOutcomeLatencyBound,
			fmt.Sprintf("missing expected bound-driven completion within %s: outcome crossed after %s", v4dOutcomeLatencyBound, obs.outputLatency))
	}

	expect(strings.Contains(obs.output, v4dQuickResultPayload),
		fmt.Sprintf("missing expected successful post-timeout tool result payload %q", v4dQuickResultPayload))
	expect(strings.Contains(obs.output, v4dRecoveryTranscript),
		fmt.Sprintf("missing expected post-timeout assistant turn %q", v4dRecoveryTranscript))
	expect(strings.Contains(obs.output, v4dContinuationText),
		fmt.Sprintf("missing expected continuation assistant turn %q", v4dContinuationText))
	expect(strings.Contains(obs.output, "[session closed: fixture_complete]"),
		"missing expected clean session termination marker")

	// Canonical structured diagnostics: the terminal metrics matrix must
	// account for the scripted assistant text plus both tool results, proving
	// everything crossed before termination.
	metrics := obs.diagnostics.recordsWith(services.SessionDiagnosticEventMetrics)
	expect(len(metrics) >= 1, "missing expected session_metrics diagnostic record")
	minCrossedBytes := len(v4dRecoveryTranscript) + len(v4dContinuationText) + len(v4dQuickResultPayload)
	textBytesAccounted := false
	for _, record := range metrics {
		if raw, err := strconv.ParseUint(record.Fields["output_text_bytes"], 10, 64); err == nil && raw >= uint64(minCrossedBytes) {
			textBytesAccounted = true
			break
		}
	}
	expect(textBytesAccounted,
		fmt.Sprintf("missing expected post-timeout traffic crossing the loop: session_metrics output_text_bytes must be >= %d", minCrossedBytes))

	if len(problems) > 0 {
		return fmt.Errorf("missing expected v4d bounded-timeout outcome: %s", strings.Join(problems, "; "))
	}
	return nil
}

// TestS2SV4DNeverReturningToolCallBoundedByExplicitTimeout is the full
// positive path: the never-returning tool call completes within the explicit
// override, the correlated timeout outcome names the tool on the stream, and
// the session degrades gracefully into later turns and a succeeding call.
func TestS2SV4DNeverReturningToolCallBoundedByExplicitTimeout(t *testing.T) {
	executor := newV4DHangingExecutor()
	out, calls, sink, runErr := runV4DSession(t, executor, v4dToolExecutionTimeout, v4dRunDeadguard)
	if runErr != nil {
		t.Fatalf("bounded session run must terminate cleanly: %v\noutput:\n%s", runErr, out.String())
	}
	latency, observed := out.outcomeLatency()
	err := validateV4DBoundedTimeoutOutcome(v4DObservation{
		output:          out.String(),
		outputLatency:   latency,
		outcomeObserved: observed,
		calls:           calls,
		diagnostics:     sink,
	})
	if err != nil {
		t.Fatalf("%v\noutput:\n%s", err, out.String())
	}
}

// TestS2SV4DBoundedOutcomeRequiresExplicitOverride is the default-bound
// negative control: with zero override the production default 60s bound cannot
// produce the outcome inside the causal-gate window, so the bounded timeout
// outcome must be absent while the hanging call itself was still exercised. If
// the outcome ever appears here, the positive proof above is vacuous.
func TestS2SV4DBoundedOutcomeRequiresExplicitOverride(t *testing.T) {
	executor := newV4DHangingExecutor()
	out, calls, _, _ := runV4DSession(t, executor, 0, v4dControlContextBound)
	if !hasV4DCall(calls, v4dTimeoutToolCallID, v4dTimeoutToolName) {
		t.Fatalf("missing expected executor invocation under the default bound: control never exercised the hanging call\noutput:\n%s", out.String())
	}
	if strings.Contains(out.String(), v4dTimeoutFailureText) {
		t.Fatalf("missing expected negative-control failure: the default-bound run produced the bounded timeout outcome\noutput:\n%s", out.String())
	}
}

// TestS2SV4DSuppressedTimeoutOutcomeFailsNonVacuously suppresses the timeout
// outcome with a fast executor and requires the shared vertical validator to
// reject the resulting transcript with an explicit "missing expected" message.
func TestS2SV4DSuppressedTimeoutOutcomeFailsNonVacuously(t *testing.T) {
	out, calls, sink, runErr := runV4DSession(t, v4DFastExecutor{}, v4dToolExecutionTimeout, v4dControlContextBound)
	if runErr != nil {
		t.Fatalf("suppressed-outcome control run must stay hermetic-clean: %v\noutput:\n%s", runErr, out.String())
	}
	if strings.Contains(out.String(), v4dTimeoutFailureText) {
		t.Fatalf("missing expected suppression: fast executor unexpectedly produced a timeout outcome\noutput:\n%s", out.String())
	}
	latency, observed := out.outcomeLatency()
	err := validateV4DBoundedTimeoutOutcome(v4DObservation{
		output:          out.String(),
		outputLatency:   latency,
		outcomeObserved: observed,
		calls:           calls,
		diagnostics:     sink,
	})
	if err == nil {
		t.Fatalf("missing expected validation failure: suppressed-outcome transcript passed the bounded-timeout validator\noutput:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "missing expected") {
		t.Fatalf("validation failure must name the missing expectations explicitly, got: %v", err)
	}
}

func hasV4DCall(calls []messages.ToolCall, id, name string) bool {
	for _, call := range calls {
		if call.ID == id && call.Name == name {
			return true
		}
	}
	return false
}
