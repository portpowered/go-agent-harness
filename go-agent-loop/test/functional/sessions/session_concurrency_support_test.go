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
// proofs: unique per-session markers, a global capture trace, and a
// tick-driven driver that runs N independent agent-loop sessions over
// replay-backed mock transports under one shared clock.Deterministic instance.
// The isolation checker lives beside its proof in session_isolation_test.go.
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
const concurrentRunBudget = 240 * time.Second

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

	// progress is the worker's live script position, published atomically so
	// the coordinator can name stuck sessions when the run budget expires.
	progress scriptProgress
}

// scriptProgress is the atomically published position of one script walker.
type scriptProgress struct {
	nextStep atomic.Int64 // index of the scripted turn being sent or awaited
	awaiting atomic.Bool  // inputs sent; waiting for the turn's MESSAGE.END
	baseline atomic.Int64 // completion counter sampled before the turn's inputs
}

// describeScriptProgress renders every unfinished session's script position
// for the run-budget failure message.
func describeScriptProgress(results []*concurrentSessionResult, turns []concurrentTurnKind) string {
	lines := []string{}
	for _, result := range results {
		next := int(result.progress.nextStep.Load())
		if next >= len(turns) {
			continue
		}
		lines = append(lines, fmt.Sprintf("session %s: turn %d/%d (%s) awaiting=%t baseline=%d message_end=%d deltas=%d",
			result.Token, next+1, len(turns), turns[next], result.progress.awaiting.Load(),
			result.progress.baseline.Load(), messageEndProgress(result), len(result.Scenario.Deltas())))
	}
	return strings.Join(lines, "\n")
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
	driver := &concurrentDriver{
		t: t, options: options, functionalTime: functionalTime, sharedClock: functionalTime.Clock(), trace: newConcurrentTrace(),
		victimDone: make(chan *concurrentSessionResult, 1), workerErrors: make(chan error, options.SessionCount+1), tick: uint64(concurrentOpenTick),
	}
	driver.startSessions()
	if options.CancelID >= 0 && options.CancelAfter >= len(options.Turns) {
		t.Fatalf("CancelAfter=%d must leave unexecuted turns so cancellation stays observable", options.CancelAfter)
	}
	driver.launchWorkers()
	driver.advanceUntilScriptsFinish()
	driver.drainToQuiescence()
	return driver.finish()
}

// concurrentDriver holds one run's state; a negative CancelID disables cancellation.
type concurrentDriver struct {
	t              *testing.T
	options        concurrentDriverOptions
	functionalTime *timeharness.Scenario
	sharedClock    *clock.Deterministic
	trace          *concurrentTrace
	results        []*concurrentSessionResult
	victimDone     chan *concurrentSessionResult
	stopVictim     bool
	workerErrors   chan error
	workers        sync.WaitGroup
	live           int64
	tick           uint64
}

func (d *concurrentDriver) startSessions() {
	d.results = make([]*concurrentSessionResult, d.options.SessionCount)
	for id := range d.results {
		token := concurrentSessionToken(id)
		sink := &tracedSink{session: id, token: token, collector: NewSessionTranscript(), trace: d.trace}
		inf := NewMockSessionInferencer()
		tool := NewMockToolExecutor().AddResult(concurrentToolName, fmt.Sprintf("result-for-%s", token))
		scenario := NewSessionScenarioWithConfig(d.t, inf, tool, SessionScenarioOptions{Clock: d.sharedClock, Capture: sink}, agentloop.WithTickRate(concurrentEngineTickRate))
		if clock, ok := scenario.Clock().(*clock.Deterministic); !ok || clock != d.sharedClock {
			d.t.Fatalf("session %d received a different clock: got %T/%p, want shared %p", id, scenario.Clock(), clock, d.sharedClock)
		}
		d.results[id] = &concurrentSessionResult{ID: id, Token: token, Scenario: scenario, Inferencer: inf, Tool: tool, Collector: sink.collector}
		scenario.Start()
	}
}

// launchWorkers registers one participant per session and runs its script;
// the cancelled session runs only its first CancelAfter turns.
func (d *concurrentDriver) launchWorkers() {
	d.workers.Add(d.options.SessionCount)
	d.live = int64(d.options.SessionCount)
	for id, result := range d.results {
		done := func() {}
		report := func(err error) { d.workerErrors <- err }
		turns := d.options.Turns
		if id == d.options.CancelID {
			done = func() { d.victimDone <- result }
			turns = d.options.Turns[:d.options.CancelAfter]
		}
		participant, err := d.functionalTime.Register(result.Token)
		if err != nil {
			d.t.Fatalf("register %s: %v", result.Token, err)
		}
		participant.Run(func() {
			defer d.workers.Done()
			defer atomic.AddInt64(&d.live, -1)
			runSessionScript(participant, result, turns, done, report)
		})
	}
}

// advanceUntilScriptsFinish advances logical ticks until every worker is done,
// stopping the cancelled session once it completes its partial script.
func (d *concurrentDriver) advanceUntilScriptsFinish() {
	deadline := time.Now().Add(concurrentRunBudget)
	for atomic.LoadInt64(&d.live) > 0 {
		if time.Now().After(deadline) {
			d.t.Fatalf("concurrent run did not finish within %v at logical tick %d; unfinished sessions:\n%s",
				concurrentRunBudget, d.tick, describeScriptProgress(d.results, d.options.Turns))
		}
		if _, err := d.functionalTime.AdvanceTo(d.tick); err != nil {
			d.t.Fatalf("advance to logical tick %d: %v", d.tick, err)
		}
		d.tick++
		select {
		case victim := <-d.victimDone:
			if d.stopVictim {
				continue
			}
			d.stopVictim = true
			if err := victim.Scenario.Stop(10 * time.Second); err != nil {
				d.t.Fatalf("cancel session %s: %v", victim.Token, err)
			}
		default:
		}
		if err := drainWorkerError(d.workerErrors); err != nil {
			d.t.Fatalf("logical tick %d: %v", d.tick-1, err)
		}
	}
	d.workers.Wait()
	if err := drainWorkerError(d.workerErrors); err != nil {
		d.t.Fatalf("session worker: %v", err)
	}
}

// drainToQuiescence drains on logical ticks. After the last MESSAGE.END the
// engine still delivers tool results and lifecycle records asynchronously;
// stopping before that tail lands would truncate captures by a
// scheduling-dependent amount. Advancing until every capture is stable for
// consecutiveStableTicks keeps teardown deterministic without wall-clock polling.
func (d *concurrentDriver) drainToQuiescence() {
	stable := 0
	lastCounts := make([]int, len(d.results))
	for i := range d.results {
		lastCounts[i] = -1
	}
	for drained := 0; drained < concurrentMaxDrainTicks && stable < consecutiveStableTicks; drained++ {
		if _, err := d.functionalTime.AdvanceTo(d.tick); err != nil {
			d.t.Fatalf("drain advance to logical tick %d: %v", d.tick, err)
		}
		d.tick++
		stable++
		for i := range d.results {
			if count := len(d.results[i].Collector.Records()); count != lastCounts[i] {
				lastCounts[i] = count
				stable = 0
			}
		}
	}
	if stable < consecutiveStableTicks {
		d.t.Fatalf("captures never stabilized after %d drain ticks", concurrentMaxDrainTicks)
	}
}

// finish stops every surviving session and snapshots all captures.
func (d *concurrentDriver) finish() *concurrentRun {
	states := make([]concurrentSessionState, 0, len(d.results))
	var cancelledState *concurrentSessionState
	for id, result := range d.results {
		if id == d.options.CancelID && d.stopVictim {
			state := result.snapshot(true)
			cancelledState = &state
			continue
		}
		if err := result.Scenario.Stop(10 * time.Second); err != nil {
			d.t.Fatalf("stop session %s: %v", result.Token, err)
		}
		states = append(states, result.snapshot(false))
	}
	return &concurrentRun{States: states, Cancelled: cancelledState, Trace: d.trace, FinalTick: d.tick - 1, Clock: d.sharedClock}
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
	ops := sessionScriptOps{
		token: result.Token,
		open:  func() bool { return sessionOpen(result) },
		send: func(kind concurrentTurnKind) {
			queueServerEvents(result.Inferencer, result.Token, kind)
			sendClientInputs(result, kind)
		},
		completions: func() int { return messageEndProgress(result) },
		completed:   func(tick uint64) { result.turnCompletedAt = append(result.turnCompletedAt, tick) },
		progress:    &result.progress,
	}
	walkSessionScript(participant, ops, sessionScriptPlan(result, turns), done, report)
}

// sessionScriptOps is the session surface one script walker drives. It is
// separated from the live scenario so the walker's completion detection can
// be proven against adversarial interleavings deterministically.
type sessionScriptOps struct {
	token string
	// open reports whether the session observed SESSION.OPEN.
	open func() bool
	// send queues the scripted provider response and performs the client
	// inputs of one turn. The engine consumes both concurrently, so the turn
	// may complete before send even returns.
	send func(kind concurrentTurnKind)
	// completions is the monotone turn-completion counter (assistant
	// MESSAGE.END deltas). It must be allocation-free: it is polled once per
	// logical tick.
	completions func() int
	// completed records the logical tick on which a turn completed.
	completed func(tick uint64)
	// progress publishes the walker position for stuck-run diagnostics.
	progress *scriptProgress
}

// walkSessionScript is the tick-driven state machine behind runSessionScript.
//
// The completion baseline is sampled BEFORE send: the scripted provider
// response is injected straight into the provider receive buffer, so the
// engine can emit the turn's MESSAGE.END while send is still performing the
// client inputs. Sampling after send would fold that MESSAGE.END into the
// baseline and the walker would wait forever for a completion it already
// consumed, observing logical ticks until the coordinator's run budget fires.
func walkSessionScript(participant *timeharness.Participant, ops sessionScriptOps, plan []sessionScriptStep, done func(), report func(error)) {
	var reportedErr error
	reportOnce := func(err error) {
		if reportedErr == nil {
			reportedErr = err
			report(err)
		}
	}

	nextStep := 0
	baseline := 0
	awaitingCompletion := false
	tick := uint64(concurrentOpenTick)
	for {
		if _, err := participant.Observe(tick); err != nil {
			reportOnce(fmt.Errorf("session %s: %w", ops.token, err))
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
			if tick-1 < step.sendTick || !ops.open() {
				continue
			}
			baseline = ops.completions()
			ops.progress.baseline.Store(int64(baseline))
			ops.progress.awaiting.Store(true)
			ops.send(step.kind)
			awaitingCompletion = true
			continue
		}
		if ops.completions() > baseline {
			ops.completed(tick - 1)
			awaitingCompletion = false
			nextStep++
			ops.progress.awaiting.Store(false)
			ops.progress.nextStep.Store(int64(nextStep))
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
