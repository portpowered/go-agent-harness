package sessions

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-loop/test/functional/timeharness"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// This file provides the shared machinery for the multi-session concurrency
// proofs: unique per-session markers, an isolation checker that detects any
// cross-session leakage in captures or deltas, and a tick-driven driver that
// runs N independent agent-loop sessions over replay-backed mock transports
// under one shared clock.Deterministic instance.
//
// Synchronization rules (S15):
//   - All interleaving comes from advancing the timeharness clock in logical
//     ticks. Participants never call time.Sleep and never poll wall clocks;
//     turn completion is polled once per logical tick and is eventual, and a
//     failure-only wall-clock budget in the coordinator bounds pathology.
//   - Blocking work outside tick generations (scenario start, teardown, leak
//     settling) happens on the coordinating goroutine between AdvanceTo calls,
//     where no generation watchdog is armed.

const (
	// concurrentDefaultSessions is the minimum session count the contract requires.
	concurrentDefaultSessions = 8
	// concurrentOpenTick is the tick every session settles SESSION.OPEN on.
	concurrentOpenTick = 2
	// concurrentFirstTurnTick is the send tick of every session's first turn.
	concurrentFirstTurnTick = 3
	// concurrentAudioChunksPerTurn counts the audio frames in one audio turn.
	concurrentAudioChunksPerTurn = 3
	// concurrentTurnStrideTicks is the logical spacing between consecutive
	// scripted turns of one session. Sessions start their k-th turn at
	// concurrentFirstTurnTick + offset + k*stride, offset < breadth, so send
	// ticks of different sessions interleave inside every turn phase. Turn
	// COMPLETION is not bounded by this constant: workers poll once per
	// logical tick until the terminal delta arrives, however many ticks that
	// takes under load, and pathology is bounded by the coordinator's
	// wall-clock budget instead.
	concurrentTurnStrideTicks = 256
	// concurrentStaggerBreadth spreads the send ticks of the eight sessions
	// across this many distinct offsets inside each turn window.
	concurrentStaggerBreadth = 4
	// concurrentEngineTickRate throttles every scenario's engine hot loop.
	// At the default zero the eight free-running engines busy-spin and starve
	// the tool pipeline under the race detector; a small floor keeps their
	// CPU use bounded while staying far below per-tick processing cost.
	concurrentEngineTickRate = 200 * time.Microsecond
	// concurrentToolName is the tool every session invokes during its tool turn.
	concurrentToolName = "session_marker_lookup"
	// consecutiveStableTicks is how many consecutive drain ticks with no new
	// captures mark a run quiescent before teardown snapshots are taken.
	consecutiveStableTicks = 200
	// concurrentMaxDrainTicks bounds the quiescence drain.
	concurrentMaxDrainTicks = 2000
)

// concurrentDefaultTurns is the shared script: a text-led turn, an audio-led
// turn, and a tool-call turn. Every session runs this identical script, so any
// foreign marker in a capture is provably cross-session leakage.
var concurrentDefaultTurns = []concurrentTurnKind{turnText, turnAudio, turnTool}

// concurrentRunBudget bounds one concurrent run in wall-clock time. It is a
// failure-only watchdog in the coordinator, never pacing: all pacing and
// interleaving happens on logical ticks, and the budget only converts a
// wedged pipeline into a test failure instead of a hang.
const concurrentRunBudget = 120 * time.Second

// concurrentSessionToken returns the unique marker token embedded in every
// audio payload, transcript text, and tool-call argument of session k.
func concurrentSessionToken(k int) string {
	return fmt.Sprintf("sess-%02d", k)
}

// concurrentAllTokens returns the marker token of every session index.
func concurrentAllTokens(count int) []string {
	tokens := make([]string, count)
	for k := range tokens {
		tokens[k] = concurrentSessionToken(k)
	}
	return tokens
}

// concurrentAudioFrame builds one audio frame carrying the session token and a
// per-frame sequence number as literal bytes so the isolation checker can scan
// captured payloads without decoding.
func concurrentAudioFrame(token string, seq int) []byte {
	frame := make([]byte, 0, 32)
	frame = append(frame, []byte(token)...)
	frame = append(frame, []byte(fmt.Sprintf("|seq=%d|", seq))...)
	for len(frame) < 32 {
		frame = append(frame, 'a')
	}
	return frame
}

// concurrentAudioSeq extracts the sequence number previously written by
// concurrentAudioFrame.
func concurrentAudioSeq(payload []byte) (int, bool) {
	marker := []byte("|seq=")
	start := bytes.Index(payload, marker)
	if start < 0 {
		return 0, false
	}
	digits := payload[start+len(marker):]
	end := bytes.IndexByte(digits, '|')
	if end <= 0 {
		return 0, false
	}
	var seq int
	if _, err := fmt.Sscanf(string(digits[:end]), "%d", &seq); err != nil {
		return 0, false
	}
	return seq, true
}

// ---------------------------------------------------------------------------
// Global trace
// ---------------------------------------------------------------------------
type concurrentTraceEntry struct {
	Session    int
	Token      string
	Peer       transcript.Peer
	Direction  transcript.Direction
	Stream     transcript.Stream
	Payload    []byte
	Tick       uint64 // logical tick at capture time, from the shared clock
	GlobalSeq  uint64
	CaptureIdx int // index of this record inside the owning session's collector
}

// concurrentTrace records the global, process-wide capture order across all
// sessions. Sequence numbers are assigned under one mutex at capture time, so
// the trace is the authoritative interleaving witness.
type concurrentTrace struct {
	mu      sync.Mutex
	entries []concurrentTraceEntry
	counts  map[string]int // per-session capture counter backing CaptureIdx
}

func newConcurrentTrace() *concurrentTrace {
	return &concurrentTrace{counts: map[string]int{}}
}

func (t *concurrentTrace) append(session int, token string, record transcript.Record) {
	t.mu.Lock()
	defer t.mu.Unlock()
	idx := t.counts[token]
	t.entries = append(t.entries, concurrentTraceEntry{
		Session:    session,
		Token:      token,
		Peer:       record.Peer,
		Direction:  record.Direction,
		Stream:     record.Stream,
		Payload:    append([]byte(nil), record.Payload...),
		Tick:       record.Tick,
		GlobalSeq:  uint64(len(t.entries)),
		CaptureIdx: idx,
	})
}

func (t *concurrentTrace) snapshot() []concurrentTraceEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]concurrentTraceEntry, len(t.entries))
	copy(out, t.entries)
	return out
}

// clientEntries returns only client-authored outbound crossings. Their order
// is deterministic because participants write them synchronously at scripted
// ticks inside barriered generations.
func (t *concurrentTrace) clientEntries() []concurrentTraceEntry {
	all := t.snapshot()
	out := make([]concurrentTraceEntry, 0, len(all))
	for _, entry := range all {
		if entry.Peer == transcript.PeerClient && entry.Direction == transcript.DirectionOut {
			out = append(out, entry)
		}
	}
	return out
}

// tracedSink forwards every captured record into a per-session collector while
// appending to the shared global trace. It implements transcript.RecordSink.
type tracedSink struct {
	session   int
	token     string
	collector *SessionTranscript
	trace     *concurrentTrace
}

func (s *tracedSink) Write(record transcript.Record) error {
	s.trace.append(s.session, s.token, record)
	return s.collector.Write(record)
}

// ---------------------------------------------------------------------------
// Isolation checker
// ---------------------------------------------------------------------------

// isolationFinding describes one detected cross-session contamination.
type isolationFinding struct {
	Owner        string // session whose capture was scanned
	ForeignToken string // another session's marker found in the capture
	Where        string // location of the offending record
	Snippet      string // printable snippet of the offending payload
}

func (f isolationFinding) String() string {
	return fmt.Sprintf("session %q capture contains foreign marker %q at %s (payload %q)", f.Owner, f.ForeignToken, f.Where, f.Snippet)
}

// containsSessionMarker reports whether payload contains token as a whole
// marker: an occurrence whose neighboring bytes are not themselves marker
// characters. Plain substring matching misfires at large session counts where
// one token is a prefix of another ("sess-10" inside "sess-100"), which would
// fabricate leakage findings during the ceiling ramp.
func containsSessionMarker(payload []byte, token string) bool {
	if len(token) == 0 {
		return false
	}
	isMarkerByte := func(b byte) bool {
		return b == '-' || b == '_' ||
			(b >= '0' && b <= '9') ||
			(b >= 'A' && b <= 'Z') ||
			(b >= 'a' && b <= 'z')
	}
	for start := 0; start+len(token) <= len(payload); start++ {
		if !bytes.Equal(payload[start:start+len(token)], []byte(token)) {
			continue
		}
		beforeOK := start == 0 || !isMarkerByte(payload[start-1])
		end := start + len(token)
		afterOK := end == len(payload) || !isMarkerByte(payload[end])
		if beforeOK && afterOK {
			return true
		}
	}
	return false
}

// checkRecordsIsolation scans one session's captured records for foreign
// session tokens.
func checkRecordsIsolation(ownerToken string, foreignTokens []string, records []transcript.Record) []isolationFinding {
	findings := []isolationFinding{}
	for idx, record := range records {
		for _, foreign := range foreignTokens {
			if foreign != ownerToken && containsSessionMarker(record.Payload, foreign) {
				findings = append(findings, isolationFinding{
					Owner:        ownerToken,
					ForeignToken: foreign,
					Where:        fmt.Sprintf("record[%d] peer=%s dir=%s stream=%s", idx, record.Peer, record.Direction, record.Stream),
					Snippet:      printableSnippet(record.Payload),
				})
			}
		}
	}
	return findings
}

// checkDeltasIsolation scans one session's collected delta stream. Values are
// reduced to comparable bytes through the same projection the capture sink
// uses, plus the marshaled message for structured values such as tool calls.
func checkDeltasIsolation(ownerToken string, foreignTokens []string, deltas []messages.StreamMessage) []isolationFinding {
	findings := []isolationFinding{}
	for idx, delta := range deltas {
		payload := streamPayload(delta)
		if len(payload) == 0 || bytes.Equal(payload, []byte(delta.Type)) {
			payload = marshalPayload(delta, []byte(delta.Type))
		}
		for _, foreign := range foreignTokens {
			if foreign != ownerToken && containsSessionMarker(payload, foreign) {
				findings = append(findings, isolationFinding{
					Owner:        ownerToken,
					ForeignToken: foreign,
					Where:        fmt.Sprintf("delta[%d] type=%s role=%s", idx, delta.Type, delta.Role),
					Snippet:      printableSnippet(payload),
				})
			}
		}
	}
	return findings
}

// checkSessionIsolation runs both projections for one session and fails t with
// named findings when any foreign marker appears.
func checkSessionIsolation(t *testing.T, ownerToken string, tokens []string, records []transcript.Record, deltas []messages.StreamMessage) {
	t.Helper()
	foreign := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if token != ownerToken {
			foreign = append(foreign, token)
		}
	}
	findings := checkRecordsIsolation(ownerToken, foreign, records)
	findings = append(findings, checkDeltasIsolation(ownerToken, foreign, deltas)...)
	if len(findings) != 0 {
		rendered := make([]string, 0, len(findings))
		for _, finding := range findings {
			rendered = append(rendered, finding.String())
		}
		t.Fatalf("cross-session leakage detected in session %q:\n%s", ownerToken, strings.Join(rendered, "\n"))
	}
}

func printableSnippet(payload []byte) string {
	const maxSnippet = 96
	end := len(payload)
	if end > maxSnippet {
		end = maxSnippet
	}
	snippet := bytes.Map(func(r rune) rune {
		if r >= 32 && r < 127 {
			return r
		}
		return '.'
	}, payload[:end])
	return string(snippet)
}

// ---------------------------------------------------------------------------
// Scripted turns
// ---------------------------------------------------------------------------

// concurrentTurnKind names one scripted turn type.
type concurrentTurnKind int

const (
	turnText concurrentTurnKind = iota
	turnAudio
	turnTool
)

func (k concurrentTurnKind) String() string {
	switch k {
	case turnText:
		return "text"
	case turnAudio:
		return "audio"
	case turnTool:
		return "tool"
	default:
		return fmt.Sprintf("unknown(%d)", int(k))
	}
}

// queueServerEvents enqueues the scripted provider response for one turn of
// the named session. Events are consumed by the mock session transport in FIFO
// order, mirroring a replay source feeding a live provider connection.
func queueServerEvents(inf *MockSessionInferencer, token string, kind concurrentTurnKind) {
	switch kind {
	case turnText:
		inf.AddServerEventSequence([]messages.StreamMessage{
			{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()},
			{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(fmt.Sprintf("ack %s text turn", token))},
			{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
		})
	case turnAudio:
		events := make([]messages.StreamMessage, 0, concurrentAudioChunksPerTurn+3)
		for seq := 1; seq <= concurrentAudioChunksPerTurn; seq++ {
			events = append(events, messages.StreamMessage{
				Type:  messages.StreamTypeAudioDelta,
				Role:  messages.RoleAssistant,
				Value: messages.NewAudioDeltaValue(concurrentAudioFrame(token, seq)),
			})
		}
		events = append(events,
			messages.StreamMessage{
				Type:  messages.StreamTypeTranscriptDelta,
				Role:  messages.RoleAssistant,
				Value: messages.NewTranscriptDeltaValue(fmt.Sprintf("%s heard you", token)),
			},
			messages.StreamMessage{
				Type:  messages.StreamTypeMessageEnd,
				Role:  messages.RoleAssistant,
				Value: messages.NewMessageEndValue(messages.TokenUsage{}),
			},
		)
		inf.AddServerEventSequence(events)
	case turnTool:
		callID := fmt.Sprintf("call-%s-1", token)
		inf.AddServerEventSequence([]messages.StreamMessage{
			{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, Value: messages.NewToolCallStartValue(callID, concurrentToolName)},
			{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, Value: messages.NewToolCallEndValue(callID, concurrentToolName, fmt.Sprintf(`{"session":"%s","query":"marker"}`, token))},
			{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
		})
	}
}

// sendClientInputs performs the client-side inputs for one turn. Client
// crossings are recorded synchronously, so they land on the current tick.
func sendClientInputs(result *concurrentSessionResult, kind concurrentTurnKind) {
	token := result.Token
	switch kind {
	case turnText:
		result.Scenario.SendText(fmt.Sprintf("%s requests a text answer", token))
	case turnAudio:
		for seq := 1; seq <= concurrentAudioChunksPerTurn; seq++ {
			result.Scenario.SendAudioInput(concurrentAudioFrame(token, seq))
		}
	case turnTool:
		result.Scenario.SendText(fmt.Sprintf("%s asks to run %s", token, concurrentToolName))
	}
}

// messageEndProgress counts assistant MESSAGE.END deltas observed so far.
// A scripted turn is complete when its model response reaches MESSAGE.END;
// executed-tool-result delivery past that point is asynchronous engine
// plumbing owned outside these proofs, so tool execution is asserted at the
// executor boundary instead (see the capacity test).
func messageEndProgress(result *concurrentSessionResult) int {
	return result.Scenario.DeltaProgress(func(delta messages.StreamMessage) bool {
		return delta.Type == messages.StreamTypeMessageEnd && delta.Role == messages.RoleAssistant
	})
}

// turnProgress is the monotone completion counter polled for every turn
// kind. It must be allocation-free: workers poll it once per logical tick.
func turnProgress(result *concurrentSessionResult, kind concurrentTurnKind) int {
	return messageEndProgress(result)
}

// ---------------------------------------------------------------------------
// Driver
// ---------------------------------------------------------------------------

// concurrentSessionResult is the observed end state of one session.
type concurrentSessionResult struct {
	ID         int
	Token      string
	Scenario   *SessionScenario
	Inferencer *MockSessionInferencer
	Tool       *MockToolExecutor
	Collector  *SessionTranscript

	// turnCompletedAt records the logical tick on which each scripted turn
	// reached MESSAGE.END. Written only by the owning worker before the
	// coordinator's workers.Wait, so no extra synchronization is required.
	turnCompletedAt []uint64
}

// snapshot freezes the post-run observations of one session.
func (r *concurrentSessionResult) snapshot(cancelled bool) concurrentSessionState {
	return concurrentSessionState{
		ID:              r.ID,
		Token:           r.Token,
		Deltas:          r.Scenario.Deltas(),
		Records:         r.Collector.Records(),
		MessageEndCount: messageEndProgress(r),
		ToolCalls:       r.Tool.Calls(),
		Cancelled:       cancelled,
		TurnCompletedAt: append([]uint64(nil), r.turnCompletedAt...),
	}
}

// concurrentSessionState is the frozen observation of one session.
type concurrentSessionState struct {
	ID              int
	Token           string
	Deltas          []messages.StreamMessage
	Records         []transcript.Record
	MessageEndCount int
	ToolCalls       []messages.ToolCall
	Cancelled       bool
	// TurnCompletedAt holds the logical tick on which each scripted turn of
	// this session reached MESSAGE.END, in script order.
	TurnCompletedAt []uint64
}

// concurrentRun is the full outcome of one driven concurrent run.
type concurrentRun struct {
	States    []concurrentSessionState // one per surviving session, indexed like the roster
	Cancelled *concurrentSessionState  // set when a session was cancelled mid-run
	Trace     *concurrentTrace
	FinalTick uint64
	Clock     *clock.Deterministic
}

// concurrentDriverOptions configures the shared driver.
type concurrentDriverOptions struct {
	SessionCount int
	Turns        []concurrentTurnKind
	CancelID     int // session cancelled after completing CancelAfterTurns turns (-1 disables)
	CancelAfter  int // how many turns the cancelled session completes first
}

// runConcurrentSessions drives sessionCount independent agent-loop sessions
// through their scripts over one shared deterministic clock. Interleaving is
// produced solely by logical ticks: every send tick is executed inside its
// barriered generation. Turn completion is eventual — workers poll once per
// logical tick until the terminal delta arrives, however long the engine
// pipeline takes under load — and concurrentRunBudget converts a wedged
// pipeline into a failure instead of a hang.
func runConcurrentSessions(t *testing.T, options concurrentDriverOptions) *concurrentRun {
	t.Helper()

	functionalTime := timeharness.New(time.Date(2026, time.August, 23, 9, 0, 0, 0, time.UTC), time.Millisecond)
	defer functionalTime.Close()
	sharedClock := functionalTime.Clock()
	trace := newConcurrentTrace()
	results := make([]*concurrentSessionResult, options.SessionCount)
	for id := range results {
		token := concurrentSessionToken(id)
		sink := &tracedSink{session: id, token: token, collector: NewSessionTranscript(), trace: trace}
		inf := NewMockSessionInferencer()
		tool := NewMockToolExecutor().AddResult(concurrentToolName, fmt.Sprintf("result-for-%s", token))
		scenario := NewSessionScenarioWithConfig(t, inf, tool, SessionScenarioOptions{
			Clock:   sharedClock,
			Capture: sink,
		}, agentloop.WithTickRate(concurrentEngineTickRate))
		if clock, ok := scenario.Clock().(*clock.Deterministic); !ok || clock != sharedClock {
			t.Fatalf("session %d received a different clock: got %T/%p, want shared %p", id, scenario.Clock(), clock, sharedClock)
		}
		results[id] = &concurrentSessionResult{
			ID:         id,
			Token:      token,
			Scenario:   scenario,
			Inferencer: inf,
			Tool:       tool,
			Collector:  sink.collector,
		}
		scenario.Start()
	}

	cancelled := -1
	if options.CancelID >= 0 {
		cancelled = options.CancelID
	}
	if options.CancelID >= 0 && options.CancelAfter >= len(options.Turns) {
		t.Fatalf("CancelAfter=%d must leave unexecuted turns so cancellation stays observable", options.CancelAfter)
	}
	victimDone := make(chan *concurrentSessionResult, 1)

	workerErrors := make(chan error, options.SessionCount+1)
	var workers sync.WaitGroup
	workers.Add(options.SessionCount)
	live := int64(options.SessionCount)
	for id, result := range results {
		result := result
		done := func() {}
		report := func(err error) { workerErrors <- err }
		if id == cancelled {
			result := result
			done = func() { victimDone <- result }
		}
		turns := options.Turns
		if id == cancelled {
			turns = options.Turns[:options.CancelAfter]
		}
		participant, err := functionalTime.Register(result.Token)
		if err != nil {
			t.Fatalf("register %s: %v", result.Token, err)
		}
		participant.Run(func() {
			defer workers.Done()
			defer atomic.AddInt64(&live, -1)
			runSessionScript(participant, result, turns, done, report)
		})
	}

	stopVictim := false
	tick := uint64(concurrentOpenTick)
	deadline := time.Now().Add(concurrentRunBudget)
	for atomic.LoadInt64(&live) > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("concurrent run did not finish within %v (stuck sessions likely)", concurrentRunBudget)
		}
		if _, err := functionalTime.AdvanceTo(tick); err != nil {
			t.Fatalf("advance to logical tick %d: %v", tick, err)
		}
		tick++
		select {
		case victim := <-victimDone:
			if stopVictim {
				continue
			}
			stopVictim = true
			if err := victim.Scenario.Stop(10 * time.Second); err != nil {
				t.Fatalf("cancel session %s: %v", victim.Token, err)
			}
		default:
		}
		if err := drainWorkerError(workerErrors); err != nil {
			t.Fatalf("logical tick %d: %v", tick-1, err)
		}
	}
	workers.Wait()
	if err := drainWorkerError(workerErrors); err != nil {
		t.Fatalf("session worker: %v", err)
	}

	// Drain to quiescence on logical ticks. After the last MESSAGE.END the
	// engine still delivers executed-tool results and lifecycle records
	// through asynchronous pipeline stages; stopping a scenario before that
	// tail lands would truncate its captures by a scheduling-dependent
	// amount and make reference comparisons flaky. Advancing ticks until
	// every capture has been stable for consecutiveStableTicks keeps the
	// teardown point deterministic without any wall-clock polling.
	stable := 0
	lastCounts := make([]int, len(results))
	for i := range results {
		lastCounts[i] = -1
	}
	for drained := 0; drained < concurrentMaxDrainTicks && stable < consecutiveStableTicks; drained++ {
		if _, err := functionalTime.AdvanceTo(tick); err != nil {
			t.Fatalf("drain advance to logical tick %d: %v", tick, err)
		}
		tick++
		moved := false
		for i := range results {
			count := len(results[i].Collector.Records())
			if count != lastCounts[i] {
				lastCounts[i] = count
				moved = true
			}
		}
		if moved {
			stable = 0
		} else {
			stable++
		}
	}
	if stable < consecutiveStableTicks {
		t.Fatalf("captures never stabilized after %d drain ticks", concurrentMaxDrainTicks)
	}

	states := make([]concurrentSessionState, 0, len(results))
	var cancelledState *concurrentSessionState
	for id, result := range results {
		if id == cancelled && stopVictim {
			state := result.snapshot(true)
			cancelledState = &state
			continue
		}
		if err := result.Scenario.Stop(10 * time.Second); err != nil {
			t.Fatalf("stop session %s: %v", result.Token, err)
		}
		states = append(states, result.snapshot(false))
	}

	return &concurrentRun{
		States:    states,
		Cancelled: cancelledState,
		Trace:     trace,
		FinalTick: tick - 1,
		Clock:     sharedClock,
	}
}

func drainWorkerError(workerErrors <-chan error) error {
	select {
	case err := <-workerErrors:
		return err
	default:
		return nil
	}
}

// sessionScriptStep is one actionable point in a session's tick-by-tick walk.
type sessionScriptStep struct {
	turnIndex int
	kind      concurrentTurnKind
	sendTick  uint64
}

// sessionScriptPlan lays out when each turn of a session sends its inputs.
// Send ticks are staggered per session so client crossings of different
// sessions interleave inside the global trace.
func sessionScriptPlan(result *concurrentSessionResult, turns []concurrentTurnKind) []sessionScriptStep {
	steps := make([]sessionScriptStep, len(turns))
	offset := uint64(result.ID % concurrentStaggerBreadth)
	for index, kind := range turns {
		steps[index] = sessionScriptStep{
			turnIndex: index,
			kind:      kind,
			sendTick:  concurrentFirstTurnTick + offset + uint64(index)*concurrentTurnStrideTicks,
		}
	}
	return steps
}

// runSessionScript walks ONE participant through the logical ticks of the
// run until its script is done. The timeharness contract requires every live
// participant to Observe each tick so barriered generations complete; a
// worker therefore keeps observing until its script finishes and then calls
// Complete, which the harness credits against any generation it was waiting
// on. Turn completion is eventual: after its send tick the worker polls the
// turn-completion condition once per logical tick for as long as it takes —
// the engine pipeline's wall cost under load is not expressible in ticks —
// and stuck pipelines surface through drainWorkerError or the coordinator's
// run budget instead of a per-turn bound.
//
// Scripted turns start only after the session observed its own SESSION.OPEN,
// so provider startup cannot race the first turn's send. On send ticks the
// participant queues the scripted provider response and performs its client
// inputs synchronously. No wall clock, no sleeps. Failures flow through
// report exactly once so the coordinator fails the test from its own
// goroutine; done runs exactly once after the full scripted prefix succeeds.
func runSessionScript(participant *timeharness.Participant, result *concurrentSessionResult, turns []concurrentTurnKind, done func(), report func(error)) {
	token := result.Token
	plan := sessionScriptPlan(result, turns)

	var reportedErr error
	reportOnce := func(err error) {
		if reportedErr == nil {
			reportedErr = err
			report(err)
		}
	}

	nextStep := 0
	observedBefore := 0
	awaitingCompletion := false
	tick := uint64(concurrentOpenTick)
	for {
		if _, err := participant.Observe(tick); err != nil {
			reportOnce(fmt.Errorf("session %s: %w", token, err))
			return
		}
		tick++
		if reportedErr != nil {
			continue
		}
		if !awaitingCompletion && nextStep >= len(plan) {
			done()
			participant.Complete()
			return
		}
		step := plan[nextStep]
		if !awaitingCompletion {
			if tick-1 < step.sendTick || !sessionOpen(result) {
				continue
			}
			queueServerEvents(result.Inferencer, token, step.kind)
			sendClientInputs(result, step.kind)
			observedBefore = turnProgress(result, step.kind)
			awaitingCompletion = true
			continue
		}
		if turnProgress(result, step.kind) > observedBefore {
			result.turnCompletedAt = append(result.turnCompletedAt, tick-1)
			awaitingCompletion = false
			nextStep++
			continue
		}
	}
}

// sessionOpen reports whether this session's delta stream has observed
// SESSION.OPEN yet.
func sessionOpen(result *concurrentSessionResult) bool {
	return result.Scenario.DeltaProgress(func(delta messages.StreamMessage) bool {
		return delta.Type == messages.StreamTypeSessionOpen
	}) > 0
}
