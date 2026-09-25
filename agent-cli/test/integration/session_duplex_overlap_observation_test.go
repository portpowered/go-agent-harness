package integration

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimecontract "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
)

type v8RuntimeObserver struct {
	outputBridge v8RuntimeOutputSink
	inputBridge  v8RuntimeInputSink
	turnTwoReady chan struct{}

	mu           sync.Mutex
	observations []runtimecontract.SessionRuntimeObservation
	turnTwoOnce  sync.Once
}

func (o *v8RuntimeObserver) ObserveSessionRuntime(observation runtimecontract.SessionRuntimeObservation) {
	if o == nil {
		return
	}
	observation.Payload = append([]byte(nil), observation.Payload...)
	o.mu.Lock()
	o.observations = append(o.observations, observation)
	o.mu.Unlock()
	if observation.Kind == runtimecontract.SessionRuntimeObservationTurnCompleted && observation.TurnsCompleted == 2 && o.turnTwoReady != nil {
		o.turnTwoOnce.Do(func() { close(o.turnTwoReady) })
	}
	if observation.Kind == runtimecontract.SessionRuntimeObservationAudioInput && o.inputBridge != nil {
		o.inputBridge.acceptRuntimeInput(observation)
	}
	if observation.Kind == runtimecontract.SessionRuntimeObservationAudioOutput && o.outputBridge != nil {
		o.outputBridge.acceptRuntimeOutput(observation)
	}
}

func (o *v8RuntimeObserver) snapshot() []runtimecontract.SessionRuntimeObservation {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	observations := make([]runtimecontract.SessionRuntimeObservation, len(o.observations))
	for i, observation := range o.observations {
		observations[i] = observation
		observations[i].Payload = append([]byte(nil), observation.Payload...)
	}
	return observations
}

type v8StreamRecorder struct {
	mu      sync.Mutex
	records []v8StreamRecord
}

func (o *v8StreamRecorder) Observe(msg messages.StreamMessage) {
	if o == nil {
		return
	}
	record := v8StreamRecord{Type: string(msg.Type)}
	if value, ok := msg.Value.(*messages.TextDeltaValue); ok && value != nil {
		record.Text = value.Content
	}
	o.mu.Lock()
	o.records = append(o.records, record)
	o.mu.Unlock()
}

func (o *v8StreamRecorder) snapshot() []v8StreamRecord {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]v8StreamRecord(nil), o.records...)
}

type v8PCMWriter struct{ bridge *v8PCMBridge }

func (w v8PCMWriter) Write(data []byte) (int, error) { return w.bridge.write(data) }

type v8PCMReader struct{ bridge *v8PCMBridge }

func (r v8PCMReader) Read(data []byte) (int, error) {
	return r.bridge.read(context.Background(), data)
}

func (r v8PCMReader) ReadContext(ctx context.Context, data []byte) (int, error) {
	return r.bridge.read(ctx, data)
}

type v8ViewRecord struct {
	Order     int       `json:"order"`
	Direction string    `json:"direction"`
	TurnKey   string    `json:"turn_key,omitempty"`
	Turn      int       `json:"turn,omitempty"`
	Tick      uint64    `json:"tick"`
	Timestamp time.Time `json:"timestamp"`
	Payload   []byte    `json:"payload"`
	SHA256    string    `json:"sha256"`
	RMS       float64   `json:"rms"`
}

type v8RecordingView struct {
	Harness string
	Role    string

	mu      sync.Mutex
	records []v8ViewRecord
}

func (v *v8RecordingView) record(crossing v8Crossing, payload []byte) {
	hash, rms := v8PCMStats(payload)
	v.mu.Lock()
	v.records = append(v.records, v8ViewRecord{
		Order:     crossing.Sequence,
		Direction: crossing.Direction,
		TurnKey:   crossing.TurnKey,
		Turn:      crossing.Turn,
		Tick:      crossing.Tick,
		Timestamp: crossing.Timestamp,
		Payload:   append([]byte(nil), payload...),
		SHA256:    hash,
		RMS:       rms,
	})
	v.mu.Unlock()
}

func (v *v8RecordingView) snapshot() []v8ViewRecord {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]v8ViewRecord, len(v.records))
	for i, record := range v.records {
		out[i] = record
		out[i].Payload = append([]byte(nil), record.Payload...)
	}
	return out
}

type v8TerminalFact struct {
	Clean          bool      `json:"clean"`
	Turns          int       `json:"turns"`
	FinalTick      uint64    `json:"final_tick"`
	FinalTimestamp time.Time `json:"final_timestamp"`
	InputEOF       bool      `json:"input_eof"`
	OutputFrame    bool      `json:"output_frame"`
	Error          string    `json:"error,omitempty"`
}

type v8ViewArtifact struct {
	Harness    string         `json:"harness"`
	Role       string         `json:"role"`
	SampleRate int            `json:"sample_rate_hz"`
	Records    []v8ViewRecord `json:"records"`
	Terminal   v8TerminalFact `json:"terminal"`
}

type v8HarnessResult struct {
	Name        string
	Instruction string
	ReplayPath  string
	Err         error
	Elapsed     time.Duration
	Runtime     []runtimecontract.SessionRuntimeObservation
	Stream      []v8StreamRecord
}

type v8StreamRecord struct {
	Type string
	Text string
}

func requireV8MultiTurnBaseline(t *testing.T, run v8DuplexRun, frames [][]byte) {
	t.Helper()
	err := verifyV8MultiTurnRun(run, frames, frames)
	if err == nil {
		return
	}
	states := make([]string, 0, 2)
	for _, name := range []string{"A", "B"} {
		result, ok := run.harnesses[name]
		if !ok {
			states = append(states, fmt.Sprintf("harness %s=<missing>", name))
			continue
		}
		runtimeState := make([]string, 0, len(result.Runtime))
		for _, observation := range result.Runtime {
			runtimeState = append(runtimeState, fmt.Sprintf("%s@%d/t%d/c%d", observation.Kind, observation.Tick, observation.TurnsCompleted, observation.InputCommit))
		}
		states = append(states, fmt.Sprintf("harness %s err=%v elapsed=%s runtime=%v stream=%v terminal=%+v", name, result.Err, result.Elapsed, runtimeState, result.Stream, run.terminal[name]))
	}
	crossingState := make([]string, 0, len(run.crossings))
	for _, crossing := range run.crossings {
		crossingState = append(crossingState, fmt.Sprintf("%d:%s/%s@%d", crossing.Sequence, crossing.Direction, crossing.TurnKey, crossing.Tick))
	}
	t.Fatalf("multi-turn negative-control baseline failed before mutation: %v; %s; %s; crossings=%d/%v final_tick=%d", err, states[0], states[1], len(run.crossings), crossingState, run.finalTick)
}

// verifyV8Harnesses requires both scripted harnesses with their distinct
// instructions.
func verifyV8Harnesses(run v8DuplexRun) error {
	if len(run.harnesses) != 2 {
		return fmt.Errorf("expected two CLI harness results, observed %d", len(run.harnesses))
	}
	aHarness, aOK := run.harnesses["A"]
	bHarness, bOK := run.harnesses["B"]
	if !aOK || !bOK {
		return fmt.Errorf("expected harness results for A and B")
	}
	if aHarness.Instruction == bHarness.Instruction {
		return fmt.Errorf("harness instructions are not distinct: %q", aHarness.Instruction)
	}
	if aHarness.Instruction != v8HarnessAInstruction || bHarness.Instruction != v8HarnessBInstruction {
		return fmt.Errorf("harness instructions do not match the two scripted profiles")
	}
	return nil
}

// verifyV8Crossing checks one overlap-tick crossing's deterministic timing,
// exact PCM on both sides of the bridge, and the runtime observations.
func verifyV8Crossing(run v8DuplexRun, crossing v8Crossing, want []byte) error {
	if crossing.Tick != v8OverlapTick {
		return fmt.Errorf("%s crossing recorded at logical tick %d, want %d", crossing.Direction, crossing.Tick, v8OverlapTick)
	}
	wantTime := run.base.Add(time.Duration(crossing.Tick) * v8TickDuration)
	if !crossing.Timestamp.Equal(wantTime) {
		return fmt.Errorf("%s tick %d timestamp=%s, want deterministic timestamp %s", crossing.Direction, crossing.Tick, crossing.Timestamp.Format(time.RFC3339Nano), wantTime.Format(time.RFC3339Nano))
	}
	if !bytes.Equal(crossing.Emitted, want) {
		return v8PCMFailure(crossing, want, crossing.Emitted, "CLI output")
	}
	_, deliveredRMS := v8PCMStats(crossing.Delivered)
	if !bytes.Equal(crossing.Delivered, want) || deliveredRMS <= v8VADThreshold {
		return v8PCMFailure(crossing, want, crossing.Delivered, "peer input")
	}
	sender, receiver := "A", "B"
	if crossing.Direction == v8DirectionBToA {
		sender, receiver = "B", "A"
	}
	outputObservation, err := v8RuntimeObservation(run.harnesses[sender].Runtime, runtimecontract.SessionRuntimeObservationAudioOutput)
	if err != nil {
		return fmt.Errorf("harness %s output runtime observation: %w", sender, err)
	}
	if outputObservation.Tick != crossing.Tick || !outputObservation.Timestamp.Equal(crossing.Timestamp) {
		return fmt.Errorf("%s runtime output timing differs from crossing: runtime tick=%d timestamp=%s, crossing tick=%d timestamp=%s", crossing.Direction, outputObservation.Tick, outputObservation.Timestamp.Format(time.RFC3339Nano), crossing.Tick, crossing.Timestamp.Format(time.RFC3339Nano))
	}
	if !bytes.Equal(outputObservation.Payload, crossing.Emitted) {
		return v8PCMFailure(crossing, crossing.Emitted, outputObservation.Payload, "runtime output")
	}
	inputObservation, err := v8RuntimeObservation(run.harnesses[receiver].Runtime, runtimecontract.SessionRuntimeObservationAudioInput)
	if err != nil {
		return fmt.Errorf("harness %s input runtime observation: %w", receiver, err)
	}
	if inputObservation.Tick != crossing.Tick || !inputObservation.Timestamp.Equal(crossing.Timestamp) {
		return fmt.Errorf("%s runtime input timing differs from crossing: runtime tick=%d timestamp=%s, crossing tick=%d timestamp=%s", crossing.Direction, inputObservation.Tick, inputObservation.Timestamp.Format(time.RFC3339Nano), crossing.Tick, crossing.Timestamp.Format(time.RFC3339Nano))
	}
	if !bytes.Equal(inputObservation.Payload, crossing.Delivered) {
		return v8PCMFailure(crossing, crossing.Delivered, inputObservation.Payload, "runtime input")
	}
	return nil
}

// verifyV8Terminal checks one harness's terminal facts against its runtime
// observations and the deterministic tick clock.
func verifyV8Terminal(run v8DuplexRun, name string, terminal v8TerminalFact) error {
	if !terminal.Clean || !terminal.InputEOF || !terminal.OutputFrame {
		return fmt.Errorf("harness %s terminal facts are not clean: %+v", name, terminal)
	}
	if terminal.Turns > run.turnsBound || terminal.FinalTick > v8OverlapTickLimit {
		return fmt.Errorf("harness %s exceeded turn/tick bounds: %+v", name, terminal)
	}
	turnObservation, err := v8RuntimeObservation(run.harnesses[name].Runtime, runtimecontract.SessionRuntimeObservationTurnCompleted)
	if err != nil {
		return fmt.Errorf("harness %s turn runtime observation: %w", name, err)
	}
	if turnObservation.TurnsCompleted != terminal.Turns {
		return fmt.Errorf("harness %s completed-turn observation = %d, terminal observation = %d", name, turnObservation.TurnsCompleted, terminal.Turns)
	}
	if terminal.Turns == 0 {
		return fmt.Errorf("harness %s terminal observation reported no completed turns", name)
	}
	terminalObservation, err := v8RuntimeObservation(run.harnesses[name].Runtime, runtimecontract.SessionRuntimeObservationTerminal)
	if err != nil {
		return fmt.Errorf("harness %s terminal runtime observation: %w", name, err)
	}
	if terminalObservation.Tick != terminal.FinalTick || !terminalObservation.Timestamp.Equal(terminal.FinalTimestamp) {
		return fmt.Errorf("harness %s terminal fact differs from runtime observation", name)
	}
	wantTerminalTime := run.base.Add(time.Duration(terminal.FinalTick) * v8TickDuration)
	if !terminal.FinalTimestamp.Equal(wantTerminalTime) {
		return fmt.Errorf("harness %s terminal tick %d timestamp=%s, want deterministic timestamp %s", name, terminal.FinalTick, terminal.FinalTimestamp.Format(time.RFC3339Nano), wantTerminalTime.Format(time.RFC3339Nano))
	}
	if (run.harnesses[name].Err == nil) != terminalObservation.Clean {
		return fmt.Errorf("harness %s runtime clean=%t disagrees with CLI error=%v", name, terminalObservation.Clean, run.harnesses[name].Err)
	}
	return nil
}

// verifyV8MultiTurnScript requires at least two overlap boundaries, the
// sequential turn-3 boundary, and distinct scripted PCM per turn.
func verifyV8MultiTurnScript(schedule []v8MultiTurnScheduleEntry, aToB, bToA [][]byte) error {
	overlapTurns := make(map[int]struct{})
	for _, entry := range schedule {
		if entry.Overlapping {
			overlapTurns[entry.Turn] = struct{}{}
		}
	}
	if len(overlapTurns) < 2 {
		return fmt.Errorf("multi-turn schedule has %d overlap turns, want at least two distinct overlap boundaries", len(overlapTurns))
	}
	if schedule[4].Overlapping || schedule[5].Overlapping || schedule[4].Tick == schedule[5].Tick {
		return fmt.Errorf("multi-turn schedule lacks the required sequential turn-3 boundary: entries=%+v", schedule[4:])
	}
	for direction, frames := range map[string][][]byte{v8DirectionAToB: aToB, v8DirectionBToA: bToA} {
		seen := make(map[string]int, len(frames))
		for turn, frame := range frames {
			hash := v8PCMHash(frame)
			if previous, ok := seen[hash]; ok && bytes.Equal(frames[previous], frame) {
				return fmt.Errorf("multi-turn %s scripted PCM identity is duplicated between turns %d and %d (hash=%s)", direction, previous+1, turn+1, hash)
			}
			seen[hash] = turn
		}
	}
	return nil
}

// verifyV8MultiTurnCrossing checks one scheduled crossing's identity,
// deterministic timing, and audible exact PCM delivery.
func verifyV8MultiTurnCrossing(run v8DuplexRun, index int, entry v8MultiTurnScheduleEntry, want []byte) error {
	crossing := run.crossings[index]
	if crossing.Sequence != index+1 || crossing.Schedule != index || crossing.Direction != entry.Direction || crossing.Turn != entry.Turn || crossing.TurnKey != v8MultiTurnKey(entry.Direction, entry.Turn) {
		return fmt.Errorf("multi-turn crossing %d identity mismatch: got sequence=%d schedule=%d direction=%s turn=%d key=%s; want direction=%s turn=%d key=%s", index+1, crossing.Sequence, crossing.Schedule, crossing.Direction, crossing.Turn, crossing.TurnKey, entry.Direction, entry.Turn, v8MultiTurnKey(entry.Direction, entry.Turn))
	}
	if crossing.Tick != entry.Tick {
		return fmt.Errorf("multi-turn %s turn %d recorded at logical tick %d, want %d", crossing.Direction, crossing.Turn, crossing.Tick, entry.Tick)
	}
	wantTimestamp := run.base.Add(time.Duration(entry.Tick) * v8TickDuration)
	if !crossing.Timestamp.Equal(wantTimestamp) {
		return fmt.Errorf("multi-turn %s turn %d timestamp=%s, want %s", crossing.Direction, crossing.Turn, crossing.Timestamp.Format(time.RFC3339Nano), wantTimestamp.Format(time.RFC3339Nano))
	}
	if !bytes.Equal(crossing.Emitted, want) || !bytes.Equal(crossing.Delivered, want) {
		return v8PCMFailure(crossing, want, crossing.Delivered, "multi-turn bridge delivery")
	}
	if _, rms := v8PCMStats(crossing.Delivered); rms <= v8VADThreshold {
		return v8PCMFailure(crossing, want, crossing.Delivered, "multi-turn bridge delivery")
	}
	return nil
}

// verifyV8MultiTurnHarness checks one harness's CLI result, runtime ledger,
// transcripts, input commits, and terminal facts.
func verifyV8MultiTurnHarness(run v8DuplexRun, name string, inputExpected [][]byte) error {
	result := run.harnesses[name]
	if result.Err != nil {
		return fmt.Errorf("multi-turn harness %s CLI failed after %s: %w", name, result.Elapsed, result.Err)
	}
	if result.Instruction != map[string]string{"A": v8HarnessAInstruction, "B": v8HarnessBInstruction}[name] {
		return fmt.Errorf("multi-turn harness %s instruction = %q, want its distinct scripted instruction", name, result.Instruction)
	}
	if result.Elapsed > v8MultiTurnCommandMaxDuration+500*time.Millisecond {
		return fmt.Errorf("multi-turn harness %s exceeded command bound: %s", name, result.Elapsed)
	}
	outputObservations := v8RuntimeObservations(result.Runtime, runtimecontract.SessionRuntimeObservationAudioOutput)
	inputObservations := v8RuntimeObservations(result.Runtime, runtimecontract.SessionRuntimeObservationAudioInput)
	turnObservations := v8RuntimeObservations(result.Runtime, runtimecontract.SessionRuntimeObservationTurnCompleted)
	if len(outputObservations) != v8MultiTurnCount || len(inputObservations) != v8MultiTurnCount || len(turnObservations) != v8MultiTurnCount {
		return fmt.Errorf("multi-turn harness %s runtime counts output=%d input=%d completed=%d, want %d each", name, len(outputObservations), len(inputObservations), len(turnObservations), v8MultiTurnCount)
	}
	for index, observation := range turnObservations {
		if observation.TurnsCompleted != index+1 {
			return fmt.Errorf("multi-turn harness %s completed-turn observation %d reports %d, want %d", name, index+1, observation.TurnsCompleted, index+1)
		}
	}
	if err := verifyV8TranscriptMarkers(name, result.Stream); err != nil {
		return err
	}
	if err := verifyV8InputCommitLedger(name, result, run.crossings, turnObservations, inputExpected, run.base); err != nil {
		return err
	}
	if err := verifyV8AudioObservations(run, name, outputObservations, inputObservations); err != nil {
		return err
	}
	terminal, ok := run.terminal[name]
	if !ok || !terminal.Clean || !terminal.InputEOF || !terminal.OutputFrame || terminal.Turns != v8MultiTurnCount || terminal.FinalTick != v8MultiTurnFinalTick {
		return fmt.Errorf("multi-turn harness %s terminal facts are not clean or complete: %+v", name, terminal)
	}
	wantTerminalTime := run.base.Add(time.Duration(terminal.FinalTick) * v8TickDuration)
	if !terminal.FinalTimestamp.Equal(wantTerminalTime) {
		return fmt.Errorf("multi-turn harness %s terminal timestamp=%s, want %s", name, terminal.FinalTimestamp.Format(time.RFC3339Nano), wantTerminalTime.Format(time.RFC3339Nano))
	}
	return nil
}

// verifyV8AudioObservations binds each runtime audio observation to its
// scheduled crossing: A emits on even entries and hears odd ones; B the
// reverse.
func verifyV8AudioObservations(run v8DuplexRun, name string, outputs, inputs []runtimecontract.SessionRuntimeObservation) error {
	for index, observation := range outputs {
		entryIndex := index * 2
		if name == "B" {
			entryIndex++
		}
		crossing := run.crossings[entryIndex]
		if observation.Tick != crossing.Tick || !observation.Timestamp.Equal(crossing.Timestamp) || !bytes.Equal(observation.Payload, crossing.Emitted) {
			return fmt.Errorf("multi-turn harness %s output observation %d does not match %s turn %d timing or PCM", name, index+1, crossing.TurnKey, crossing.Turn)
		}
	}
	for index, observation := range inputs {
		entryIndex := index * 2
		if name == "A" {
			entryIndex++
		}
		crossing := run.crossings[entryIndex]
		if observation.Tick != crossing.Tick || !observation.Timestamp.Equal(crossing.Timestamp) || !bytes.Equal(observation.Payload, crossing.Delivered) {
			return fmt.Errorf("multi-turn harness %s input observation %d does not match %s turn %d timing or PCM", name, index+1, crossing.TurnKey, crossing.Turn)
		}
	}
	return nil
}

// verifyV8MultiTurnParity requires each client view to match its peer's
// agent view record for record.
func verifyV8MultiTurnParity(run v8DuplexRun) error {
	for _, pair := range [][2]string{{"A/client", "B/agent"}, {"B/client", "A/agent"}} {
		left := run.views[pair[0]].snapshot()
		right := run.views[pair[1]].snapshot()
		if len(left) != v8MultiTurnCount || len(right) != v8MultiTurnCount {
			return fmt.Errorf("multi-turn recording parity %s vs %s has %d and %d records, want %d each", pair[0], pair[1], len(left), len(right), v8MultiTurnCount)
		}
		for index := range left {
			if err := compareV8ViewRecords(fmt.Sprintf("%s turn %d", pair[0], index+1), left[index], fmt.Sprintf("%s turn %d", pair[1], index+1), right[index]); err != nil {
				return err
			}
		}
	}
	return nil
}
