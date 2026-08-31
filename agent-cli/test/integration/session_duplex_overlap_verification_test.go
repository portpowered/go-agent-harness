package integration

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
	"os"
	"runtime"
	"testing"
	"time"
)

func v8RuntimeObservation(observations []services.SessionRuntimeObservation, kind services.SessionRuntimeObservationKind) (services.SessionRuntimeObservation, error) {
	var found services.SessionRuntimeObservation
	count := 0
	for _, observation := range observations {
		if observation.Kind != kind {
			continue
		}
		found = observation
		count++
	}
	if count != 1 {
		return services.SessionRuntimeObservation{}, fmt.Errorf("runtime observation %q count = %d, want exactly one", kind, count)
	}
	return found, nil
}

func appendArtifactPaths(artifacts map[string]string, viewName, jsonPath, wavPath string) map[string]string {
	if artifacts == nil {
		artifacts = make(map[string]string)
	}
	artifacts[viewName+".json"] = jsonPath
	artifacts[viewName+".wav"] = wavPath
	return artifacts
}

func writeV8ViewArtifacts(t *testing.T, view *v8RecordingView, terminal v8TerminalFact, jsonPath, wavPath string) {
	t.Helper()
	artifact := v8ViewArtifact{
		Harness:    view.Harness,
		Role:       view.Role,
		SampleRate: audio.SampleRate,
		Records:    view.snapshot(),
		Terminal:   terminal,
	}
	data, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		t.Fatalf("marshal v8 %s/%s recording artifact: %v", view.Harness, view.Role, err)
	}
	if err := os.WriteFile(jsonPath, data, 0o600); err != nil {
		t.Fatalf("write v8 %s/%s recording artifact: %v", view.Harness, view.Role, err)
	}
	payload := []byte{}
	for _, record := range artifact.Records {
		payload = append(payload, record.Payload...)
	}
	if len(payload) == 0 {
		t.Fatalf("v8 %s/%s recording has no PCM payload", view.Harness, view.Role)
	}
	samples := make([]int16, len(payload)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(payload[i*2:]))
	}
	var wav bytes.Buffer
	if err := wavio.Write(&wav, audio.SampleRate, samples); err != nil {
		t.Fatalf("encode v8 %s/%s WAV artifact: %v", view.Harness, view.Role, err)
	}
	if err := os.WriteFile(wavPath, wav.Bytes(), 0o600); err != nil {
		t.Fatalf("write v8 %s/%s WAV artifact: %v", view.Harness, view.Role, err)
	}
}

func verifyV8Run(run v8DuplexRun, expected map[string][]byte) error {
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
	if len(run.crossings) != 2 {
		return fmt.Errorf("expected two retained PCM crossings, observed %d", len(run.crossings))
	}
	wantDirections := []string{"A-to-B", "B-to-A"}
	for i, crossing := range run.crossings {
		if crossing.Sequence != i+1 || crossing.Direction != wantDirections[i] {
			return fmt.Errorf("crossing order mismatch at index %d: got sequence=%d direction=%s", i, crossing.Sequence, crossing.Direction)
		}
		if crossing.Tick != v8OverlapTick {
			return fmt.Errorf("%s crossing recorded at logical tick %d, want %d", crossing.Direction, crossing.Tick, v8OverlapTick)
		}
		wantTime := run.base.Add(time.Duration(crossing.Tick) * v8TickDuration)
		if !crossing.Timestamp.Equal(wantTime) {
			return fmt.Errorf("%s tick %d timestamp=%s, want deterministic timestamp %s", crossing.Direction, crossing.Tick, crossing.Timestamp.Format(time.RFC3339Nano), wantTime.Format(time.RFC3339Nano))
		}
		want := expected[crossing.Direction]
		if !bytes.Equal(crossing.Emitted, want) {
			return v8PCMFailure(crossing, want, crossing.Emitted, "CLI output")
		}
		_, deliveredRMS := v8PCMStats(crossing.Delivered)
		if !bytes.Equal(crossing.Delivered, want) || deliveredRMS <= v8VADThreshold {
			return v8PCMFailure(crossing, want, crossing.Delivered, "peer input")
		}

		sender, receiver := "A", "B"
		if crossing.Direction == "B-to-A" {
			sender, receiver = "B", "A"
		}
		outputObservation, err := v8RuntimeObservation(run.harnesses[sender].Runtime, services.SessionRuntimeObservationAudioOutput)
		if err != nil {
			return fmt.Errorf("harness %s output runtime observation: %w", sender, err)
		}
		if outputObservation.Tick != crossing.Tick || !outputObservation.Timestamp.Equal(crossing.Timestamp) {
			return fmt.Errorf("%s runtime output timing differs from crossing: runtime tick=%d timestamp=%s, crossing tick=%d timestamp=%s", crossing.Direction, outputObservation.Tick, outputObservation.Timestamp.Format(time.RFC3339Nano), crossing.Tick, crossing.Timestamp.Format(time.RFC3339Nano))
		}
		if !bytes.Equal(outputObservation.Payload, crossing.Emitted) {
			return v8PCMFailure(crossing, crossing.Emitted, outputObservation.Payload, "runtime output")
		}
		inputObservation, err := v8RuntimeObservation(run.harnesses[receiver].Runtime, services.SessionRuntimeObservationAudioInput)
		if err != nil {
			return fmt.Errorf("harness %s input runtime observation: %w", receiver, err)
		}
		if inputObservation.Tick != crossing.Tick || !inputObservation.Timestamp.Equal(crossing.Timestamp) {
			return fmt.Errorf("%s runtime input timing differs from crossing: runtime tick=%d timestamp=%s, crossing tick=%d timestamp=%s", crossing.Direction, inputObservation.Tick, inputObservation.Timestamp.Format(time.RFC3339Nano), crossing.Tick, crossing.Timestamp.Format(time.RFC3339Nano))
		}
		if !bytes.Equal(inputObservation.Payload, crossing.Delivered) {
			return v8PCMFailure(crossing, crossing.Delivered, inputObservation.Payload, "runtime input")
		}
	}

	if run.crossings[0].Tick != run.crossings[1].Tick {
		return fmt.Errorf("directional speech windows do not overlap: A-to-B tick %d, B-to-A tick %d", run.crossings[0].Tick, run.crossings[1].Tick)
	}
	parityPairs := [][2]string{{"A/client", "B/agent"}, {"B/client", "A/agent"}}
	for _, pair := range parityPairs {
		left := run.views[pair[0]].snapshot()
		right := run.views[pair[1]].snapshot()
		if len(left) != 1 || len(right) != 1 {
			return fmt.Errorf("recording parity %s vs %s: got %d and %d records, want one each", pair[0], pair[1], len(left), len(right))
		}
		if err := compareV8ViewRecords(pair[0], left[0], pair[1], right[0]); err != nil {
			return err
		}
	}

	for name, terminal := range run.terminal {
		if !terminal.Clean || !terminal.InputEOF || !terminal.OutputFrame {
			return fmt.Errorf("harness %s terminal facts are not clean: %+v", name, terminal)
		}
		if terminal.Turns > run.turnsBound || terminal.FinalTick > v8OverlapTickLimit {
			return fmt.Errorf("harness %s exceeded turn/tick bounds: %+v", name, terminal)
		}
		turnObservation, err := v8RuntimeObservation(run.harnesses[name].Runtime, services.SessionRuntimeObservationTurnCompleted)
		if err != nil {
			return fmt.Errorf("harness %s turn runtime observation: %w", name, err)
		}
		if turnObservation.TurnsCompleted != terminal.Turns {
			return fmt.Errorf("harness %s completed-turn observation = %d, terminal observation = %d", name, turnObservation.TurnsCompleted, terminal.Turns)
		}
		if terminal.Turns == 0 {
			return fmt.Errorf("harness %s terminal observation reported no completed turns", name)
		}
		terminalObservation, err := v8RuntimeObservation(run.harnesses[name].Runtime, services.SessionRuntimeObservationTerminal)
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
	}
	aTerminal, aTerminalOK := run.terminal["A"]
	bTerminal, bTerminalOK := run.terminal["B"]
	if !aTerminalOK || !bTerminalOK {
		return fmt.Errorf("terminal facts missing for A or B")
	}
	if aTerminal != bTerminal {
		return fmt.Errorf("terminal parity A vs B differs: A=%+v B=%+v", aTerminal, bTerminal)
	}
	for name, result := range run.harnesses {
		if result.Err != nil {
			return fmt.Errorf("harness %s CLI failed after %s: %w", name, result.Elapsed, result.Err)
		}
		if result.Elapsed > v8CommandMaxDuration+500*time.Millisecond {
			return fmt.Errorf("harness %s exceeded command bound: %s", name, result.Elapsed)
		}
	}
	return verifyV8Artifacts(run)
}

func v8RuntimeObservations(observations []services.SessionRuntimeObservation, kind services.SessionRuntimeObservationKind) []services.SessionRuntimeObservation {
	matched := make([]services.SessionRuntimeObservation, 0)
	for _, observation := range observations {
		if observation.Kind != kind {
			continue
		}
		observation.Payload = append([]byte(nil), observation.Payload...)
		matched = append(matched, observation)
	}
	return matched
}

func v8StreamTextMarkers(records []v8StreamRecord) []string {
	markers := make([]string, 0)
	for _, record := range records {
		if record.Type == string(messages.StreamTypeTextDelta) && record.Text != "" {
			markers = append(markers, record.Text)
		}
	}
	return markers
}

func verifyV8TranscriptMarkers(harness string, records []v8StreamRecord) error {
	wantMarkers := make([]string, v8MultiTurnCount)
	for index := range wantMarkers {
		wantMarkers[index] = fmt.Sprintf("%s transcript turn %d", harness, index+1)
	}
	gotMarkers := v8StreamTextMarkers(records)
	if len(gotMarkers) != len(wantMarkers) {
		return fmt.Errorf("multi-turn harness %s transcript ledger has %d markers, want %d: expected=%v observed=%v", harness, len(gotMarkers), len(wantMarkers), wantMarkers, gotMarkers)
	}
	for index, expected := range wantMarkers {
		if gotMarkers[index] != expected {
			turnKey := v8MultiTurnKey("A-to-B", index+1)
			if harness == "B" {
				turnKey = v8MultiTurnKey("B-to-A", index+1)
			}
			return fmt.Errorf("multi-turn harness %s turn %d (%s) transcript marker mismatch: expected=%q observed=%q", harness, index+1, turnKey, expected, gotMarkers[index])
		}
	}
	return nil
}

func v8InputDirection(harness string) string {
	if harness == "A" {
		return "B-to-A"
	}
	return "A-to-B"
}

func v8InputCrossingIndex(harness string, turn int) int {
	index := (turn - 1) * 2
	if harness == "A" {
		index++
	}
	return index
}

func v8InputCommitFailure(harness string, crossing v8Crossing, expected, observed []byte) error {
	wantHash, wantRMS := v8PCMStats(expected)
	gotHash, gotRMS := v8PCMStats(observed)
	return fmt.Errorf("multi-turn harness %s %s %s turn %d input commit PCM mismatch: expected hash=%s RMS=%.1f (> %.1f); observed hash=%s RMS=%.1f", harness, crossing.Direction, crossing.TurnKey, crossing.Turn, wantHash, wantRMS, v8VADThreshold, gotHash, gotRMS)
}

func verifyV8InputCommitLedger(harness string, result v8HarnessResult, crossings []v8Crossing, completions []services.SessionRuntimeObservation, expected [][]byte, base time.Time) error {
	direction := v8InputDirection(harness)
	commits := v8RuntimeObservations(result.Runtime, services.SessionRuntimeObservationInputCommit)
	markers := v8StreamTextMarkers(result.Stream)
	observedOrdinals := make([]int, 0, len(commits))
	for turnIndex, observation := range commits {
		turn := turnIndex + 1
		observedOrdinals = append(observedOrdinals, observation.InputCommit)
		if observation.InputCommit != turn {
			return fmt.Errorf("multi-turn harness %s direction %s %s turn %d input commit ordinal mismatch: expected=%d observed=%d", harness, direction, v8MultiTurnKey(direction, turn), turn, turn, observation.InputCommit)
		}
	}
	if len(commits) != v8MultiTurnCount {
		missingTurn := 0
		seen := make(map[int]struct{}, len(commits))
		for _, ordinal := range observedOrdinals {
			seen[ordinal] = struct{}{}
		}
		for turn := 1; turn <= v8MultiTurnCount; turn++ {
			if _, ok := seen[turn]; !ok {
				missingTurn = turn
				break
			}
		}
		if missingTurn == 0 {
			return fmt.Errorf("multi-turn harness %s input commit ledger has %d commits, want %d; duplicate or unexpected commit ordinals=%v", harness, len(commits), v8MultiTurnCount, observedOrdinals)
		}
		return fmt.Errorf("multi-turn harness %s input commit ledger has %d commits, want %d; missing stable %s turn %d; observed ordinals=%v", harness, len(commits), v8MultiTurnCount, v8MultiTurnKey(direction, missingTurn), missingTurn, observedOrdinals)
	}
	if len(markers) != v8MultiTurnCount {
		return fmt.Errorf("multi-turn harness %s input commit ledger cannot bind transcript markers: expected %d, observed %d", harness, v8MultiTurnCount, len(markers))
	}
	for turnIndex, observation := range commits {
		turn := turnIndex + 1
		crossing := crossings[v8InputCrossingIndex(harness, turn)]
		turnKey := v8MultiTurnKey(direction, turn)
		completion := completions[turnIndex]
		wantTimestamp := base.Add(time.Duration(observation.Tick) * v8TickDuration)
		if !observation.Timestamp.Equal(wantTimestamp) {
			return fmt.Errorf("multi-turn harness %s %s turn %d input commit timestamp=%s is not deterministic for tick %d", harness, turnKey, turn, observation.Timestamp.Format(time.RFC3339Nano), observation.Tick)
		}
		if observation.Tick < crossing.Tick || (observation.Tick == crossing.Tick && observation.Timestamp.Before(crossing.Timestamp)) {
			return fmt.Errorf("multi-turn harness %s %s turn %d input commit precedes its audio crossing: commit tick=%d timestamp=%s; crossing tick=%d timestamp=%s", harness, turnKey, turn, observation.Tick, observation.Timestamp.Format(time.RFC3339Nano), crossing.Tick, crossing.Timestamp.Format(time.RFC3339Nano))
		}
		if completion.TurnsCompleted != turn || completion.Tick < observation.Tick || (completion.Tick == observation.Tick && completion.Timestamp.Before(observation.Timestamp)) {
			return fmt.Errorf("multi-turn harness %s %s turn %d input commit is not bound to completed turn: commit tick=%d timestamp=%s; completion turns=%d tick=%d timestamp=%s", harness, turnKey, turn, observation.Tick, observation.Timestamp.Format(time.RFC3339Nano), completion.TurnsCompleted, completion.Tick, completion.Timestamp.Format(time.RFC3339Nano))
		}
		if !bytes.Equal(observation.Payload, expected[turnIndex]) || !bytes.Equal(observation.Payload, crossing.Delivered) {
			return v8InputCommitFailure(harness, crossing, expected[turnIndex], observation.Payload)
		}
		_, rms := v8PCMStats(observation.Payload)
		if rms <= v8VADThreshold {
			return v8InputCommitFailure(harness, crossing, expected[turnIndex], observation.Payload)
		}
		expectedMarker := fmt.Sprintf("%s transcript turn %d", harness, turn)
		if markers[turnIndex] != expectedMarker {
			return fmt.Errorf("multi-turn harness %s %s turn %d input commit transcript attribution mismatch: expected=%q observed=%q", harness, turnKey, turn, expectedMarker, markers[turnIndex])
		}
	}
	return nil
}

func verifyV8ViewLedger(viewName string, view *v8RecordingView, crossings []v8Crossing, direction string, expected [][]byte) error {
	if view == nil {
		return fmt.Errorf("multi-turn recording view %s is missing", viewName)
	}
	records := view.snapshot()
	if len(records) != v8MultiTurnCount {
		return fmt.Errorf("multi-turn recording view %s has %d records, want %d", viewName, len(records), v8MultiTurnCount)
	}
	for turnIndex, payload := range expected {
		crossingIndex := turnIndex * 2
		if direction == "B-to-A" {
			crossingIndex++
		}
		if crossingIndex >= len(crossings) {
			return fmt.Errorf("multi-turn recording view %s turn %d has no crossing ledger entry", viewName, turnIndex+1)
		}
		crossing := crossings[crossingIndex]
		record := records[turnIndex]
		if record.Order != crossing.Sequence || record.Direction != crossing.Direction || record.Turn != crossing.Turn || record.TurnKey != crossing.TurnKey || record.Tick != crossing.Tick || !record.Timestamp.Equal(crossing.Timestamp) {
			return fmt.Errorf("multi-turn recording view %s turn %d identity/timing mismatch: expected order=%d direction=%s key=%s tick=%d timestamp=%s; observed order=%d direction=%s key=%s tick=%d timestamp=%s", viewName, turnIndex+1, crossing.Sequence, crossing.Direction, crossing.TurnKey, crossing.Tick, crossing.Timestamp.Format(time.RFC3339Nano), record.Order, record.Direction, record.TurnKey, record.Tick, record.Timestamp.Format(time.RFC3339Nano))
		}
		wantHash, wantRMS := v8PCMStats(payload)
		gotHash, gotRMS := v8PCMStats(record.Payload)
		if !bytes.Equal(record.Payload, payload) || record.SHA256 != wantHash || record.RMS != wantRMS {
			return fmt.Errorf("multi-turn recording view %s %s turn %d PCM identity mismatch: expected hash=%s RMS=%.1f; observed hash=%s RMS=%.1f", viewName, crossing.TurnKey, crossing.Turn, wantHash, wantRMS, gotHash, gotRMS)
		}
	}
	return nil
}

func verifyV8MultiTurnRun(run v8DuplexRun, aToB, bToA [][]byte) error {
	if len(aToB) != v8MultiTurnCount || len(bToA) != v8MultiTurnCount {
		return fmt.Errorf("multi-turn verifier expected %d scripted frames per direction, got A-to-B=%d B-to-A=%d", v8MultiTurnCount, len(aToB), len(bToA))
	}
	if len(run.harnesses) != 2 {
		return fmt.Errorf("multi-turn verifier expected two CLI harnesses, observed %d", len(run.harnesses))
	}
	if len(run.crossings) != len(v8MultiTurnSchedule()) {
		return fmt.Errorf("multi-turn verifier expected %d scheduled crossings, observed %d", len(v8MultiTurnSchedule()), len(run.crossings))
	}
	schedule := v8MultiTurnSchedule()
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
	for direction, frames := range map[string][][]byte{"A-to-B": aToB, "B-to-A": bToA} {
		seen := make(map[string]int, len(frames))
		for turn, frame := range frames {
			hash := v8PCMHash(frame)
			if previous, ok := seen[hash]; ok && bytes.Equal(frames[previous], frame) {
				return fmt.Errorf("multi-turn %s scripted PCM identity is duplicated between turns %d and %d (hash=%s)", direction, previous+1, turn+1, hash)
			}
			seen[hash] = turn
		}
	}
	for index, entry := range schedule {
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
		want := aToB[entry.Turn-1]
		if entry.Direction == "B-to-A" {
			want = bToA[entry.Turn-1]
		}
		if !bytes.Equal(crossing.Emitted, want) || !bytes.Equal(crossing.Delivered, want) {
			return v8PCMFailure(crossing, want, crossing.Delivered, "multi-turn bridge delivery")
		}
		_, rms := v8PCMStats(crossing.Delivered)
		if rms <= v8VADThreshold {
			return v8PCMFailure(crossing, want, crossing.Delivered, "multi-turn bridge delivery")
		}
	}

	for _, name := range []string{"A", "B"} {
		result := run.harnesses[name]
		if result.Err != nil {
			return fmt.Errorf("multi-turn harness %s CLI failed after %s: %w", name, result.Elapsed, result.Err)
		}
		if result.Instruction != map[string]string{"A": v8HarnessAInstruction, "B": v8HarnessBInstruction}[name] {
			return fmt.Errorf("multi-turn harness %s instruction = %q, want its distinct scripted instruction", name, result.Instruction)
		}
		if result.Elapsed > v8CommandMaxDuration+500*time.Millisecond {
			return fmt.Errorf("multi-turn harness %s exceeded command bound: %s", name, result.Elapsed)
		}
		outputObservations := v8RuntimeObservations(result.Runtime, services.SessionRuntimeObservationAudioOutput)
		inputObservations := v8RuntimeObservations(result.Runtime, services.SessionRuntimeObservationAudioInput)
		turnObservations := v8RuntimeObservations(result.Runtime, services.SessionRuntimeObservationTurnCompleted)
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
		inputExpected := aToB
		if name == "A" {
			inputExpected = bToA
		}
		if err := verifyV8InputCommitLedger(name, result, run.crossings, turnObservations, inputExpected, run.base); err != nil {
			return err
		}
		for index, observation := range outputObservations {
			entryIndex := index * 2
			if name == "B" {
				entryIndex++
			}
			crossing := run.crossings[entryIndex]
			if observation.Tick != crossing.Tick || !observation.Timestamp.Equal(crossing.Timestamp) || !bytes.Equal(observation.Payload, crossing.Emitted) {
				return fmt.Errorf("multi-turn harness %s output observation %d does not match %s turn %d timing or PCM", name, index+1, crossing.TurnKey, crossing.Turn)
			}
		}
		for index, observation := range inputObservations {
			entryIndex := index * 2
			if name == "A" {
				entryIndex++
			}
			crossing := run.crossings[entryIndex]
			if observation.Tick != crossing.Tick || !observation.Timestamp.Equal(crossing.Timestamp) || !bytes.Equal(observation.Payload, crossing.Delivered) {
				return fmt.Errorf("multi-turn harness %s input observation %d does not match %s turn %d timing or PCM", name, index+1, crossing.TurnKey, crossing.Turn)
			}
		}
		terminal, ok := run.terminal[name]
		if !ok || !terminal.Clean || !terminal.InputEOF || !terminal.OutputFrame || terminal.Turns != v8MultiTurnCount || terminal.FinalTick != v8MultiTurnFinalTick {
			return fmt.Errorf("multi-turn harness %s terminal facts are not clean or complete: %+v", name, terminal)
		}
		wantTerminalTime := run.base.Add(time.Duration(terminal.FinalTick) * v8TickDuration)
		if !terminal.FinalTimestamp.Equal(wantTerminalTime) {
			return fmt.Errorf("multi-turn harness %s terminal timestamp=%s, want %s", name, terminal.FinalTimestamp.Format(time.RFC3339Nano), wantTerminalTime.Format(time.RFC3339Nano))
		}
	}

	for _, expectation := range []struct {
		name      string
		direction string
		expected  [][]byte
	}{
		{name: "A/client", direction: "A-to-B", expected: aToB},
		{name: "B/agent", direction: "A-to-B", expected: aToB},
		{name: "B/client", direction: "B-to-A", expected: bToA},
		{name: "A/agent", direction: "B-to-A", expected: bToA},
	} {
		if err := verifyV8ViewLedger(expectation.name, run.views[expectation.name], run.crossings, expectation.direction, expectation.expected); err != nil {
			return err
		}
	}

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
	if run.finalTick != v8MultiTurnFinalTick {
		return fmt.Errorf("multi-turn final logical tick = %d, want %d", run.finalTick, v8MultiTurnFinalTick)
	}
	return verifyV8Artifacts(run)
}

func verifyV8Artifacts(run v8DuplexRun) error {
	viewNames := []string{"A/client", "A/agent", "B/client", "B/agent"}
	if len(run.artifacts) != len(viewNames)*2 {
		return fmt.Errorf("expected JSON and WAV artifacts for four views, observed %d paths", len(run.artifacts))
	}
	for _, viewName := range viewNames {
		jsonPath, jsonOK := run.artifacts[viewName+".json"]
		wavPath, wavOK := run.artifacts[viewName+".wav"]
		if !jsonOK || !wavOK {
			return fmt.Errorf("artifacts missing for %s", viewName)
		}
		data, err := os.ReadFile(jsonPath)
		if err != nil {
			return fmt.Errorf("read %s JSON artifact: %w", viewName, err)
		}
		var artifact v8ViewArtifact
		if err := json.Unmarshal(data, &artifact); err != nil {
			return fmt.Errorf("decode %s JSON artifact: %w", viewName, err)
		}
		view := run.views[viewName]
		if view == nil {
			return fmt.Errorf("recording view %s is missing", viewName)
		}
		if artifact.Harness != view.Harness || artifact.Role != view.Role || artifact.SampleRate != audio.SampleRate {
			return fmt.Errorf("%s artifact metadata is invalid: %+v", viewName, artifact)
		}
		liveRecords := view.snapshot()
		if len(artifact.Records) != len(liveRecords) || len(artifact.Records) == 0 {
			return fmt.Errorf("%s artifact has %d records, live view has %d; want the same non-empty per-turn ledger", viewName, len(artifact.Records), len(liveRecords))
		}
		for index := range artifact.Records {
			if err := compareV8ViewRecords(fmt.Sprintf("%s artifact turn %d", viewName, index+1), artifact.Records[index], fmt.Sprintf("%s live turn %d", viewName, index+1), liveRecords[index]); err != nil {
				return err
			}
		}
		if wantTerminal, ok := run.terminal[view.Harness]; !ok || artifact.Terminal != wantTerminal {
			return fmt.Errorf("%s artifact terminal facts do not match the harness terminal facts", viewName)
		}

		wavData, err := os.ReadFile(wavPath)
		if err != nil {
			return fmt.Errorf("read %s WAV artifact: %w", viewName, err)
		}
		rate, samples, err := wavio.Read(bytes.NewReader(wavData))
		if err != nil {
			return fmt.Errorf("decode %s WAV artifact: %w", viewName, err)
		}
		livePayload := []byte{}
		for _, record := range liveRecords {
			livePayload = append(livePayload, record.Payload...)
		}
		if rate != audio.SampleRate || len(samples) != len(livePayload)/2 {
			return fmt.Errorf("%s WAV artifact shape is rate=%d samples=%d, want rate=%d samples=%d", viewName, rate, len(samples), audio.SampleRate, len(livePayload)/2)
		}
		if !bytes.Equal(v8PCM16Bytes(samples), livePayload) {
			return fmt.Errorf("%s WAV artifact payload differs from the recorded PCM", viewName)
		}
	}
	return nil
}

func v8PCMFailure(crossing v8Crossing, expected, observed []byte, view string) error {
	wantHash, wantRMS := v8PCMStats(expected)
	gotHash, gotRMS := v8PCMStats(observed)
	return fmt.Errorf("%s %s turn %d logical tick %d %s PCM mismatch: expected hash=%s RMS=%.1f (> %.1f); observed hash=%s RMS=%.1f", crossing.Direction, crossing.TurnKey, crossing.Turn, crossing.Tick, view, wantHash, wantRMS, v8VADThreshold, gotHash, gotRMS)
}

func compareV8ViewRecords(leftName string, left v8ViewRecord, rightName string, right v8ViewRecord) error {
	if left.Direction != right.Direction {
		return fmt.Errorf("recording parity %s vs %s direction differs: %s != %s", leftName, rightName, left.Direction, right.Direction)
	}
	if left.TurnKey != right.TurnKey || left.Turn != right.Turn {
		return fmt.Errorf("recording parity %s vs %s turn identity differs: left key=%s turn=%d; right key=%s turn=%d", leftName, rightName, left.TurnKey, left.Turn, right.TurnKey, right.Turn)
	}
	if left.Order != right.Order || left.Tick != right.Tick || !left.Timestamp.Equal(right.Timestamp) {
		return fmt.Errorf("recording parity %s vs %s timing/order differs: left order=%d tick=%d timestamp=%s; right order=%d tick=%d timestamp=%s", leftName, rightName, left.Order, left.Tick, left.Timestamp.Format(time.RFC3339Nano), right.Order, right.Tick, right.Timestamp.Format(time.RFC3339Nano))
	}
	if left.SHA256 != right.SHA256 || !bytes.Equal(left.Payload, right.Payload) || left.RMS != right.RMS {
		return fmt.Errorf("recording parity %s vs %s payload differs: hash %s != %s RMS %.1f != %.1f", leftName, rightName, left.SHA256, right.SHA256, left.RMS, right.RMS)
	}
	return nil
}

func assertV8GoroutinesSettled(t *testing.T, baseline int, operation string) {
	t.Helper()
	settleTimeout := 10 * time.Second
	timer := time.NewTimer(settleTimeout)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if runtime.NumGoroutine() <= baseline+2 {
			return
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatalf("goroutines after %s = %d, baseline = %d; CLI lifecycle did not settle within %s", operation, runtime.NumGoroutine(), baseline, settleTimeout)
		}
	}
}

func mutateV8ViewPayload(run *v8DuplexRun, viewName string, turn int, payload []byte) error {
	if run == nil {
		return fmt.Errorf("cannot mutate a nil multi-turn run")
	}
	view := run.views[viewName]
	if view == nil {
		return fmt.Errorf("multi-turn recording view %s is missing", viewName)
	}
	if turn < 1 || turn > v8MultiTurnCount {
		return fmt.Errorf("multi-turn recording view %s mutation turn %d is outside 1..%d", viewName, turn, v8MultiTurnCount)
	}
	view.mu.Lock()
	defer view.mu.Unlock()
	if turn > len(view.records) {
		return fmt.Errorf("multi-turn recording view %s has %d records; cannot mutate turn %d", viewName, len(view.records), turn)
	}
	view.records[turn-1].Payload = append([]byte(nil), payload...)
	view.records[turn-1].SHA256, view.records[turn-1].RMS = v8PCMStats(payload)
	return nil
}

func mutateV8TranscriptMarker(run *v8DuplexRun, harness string, turn int, replacement string) error {
	if run == nil {
		return fmt.Errorf("cannot mutate a nil multi-turn run")
	}
	if turn < 1 || turn > v8MultiTurnCount {
		return fmt.Errorf("multi-turn harness %s transcript mutation turn %d is outside 1..%d", harness, turn, v8MultiTurnCount)
	}
	result, ok := run.harnesses[harness]
	if !ok {
		return fmt.Errorf("multi-turn harness %s is missing", harness)
	}
	markerIndex := 0
	for index := range result.Stream {
		if result.Stream[index].Type != string(messages.StreamTypeTextDelta) || result.Stream[index].Text == "" {
			continue
		}
		markerIndex++
		if markerIndex == turn {
			result.Stream[index].Text = replacement
			run.harnesses[harness] = result
			return nil
		}
	}
	return fmt.Errorf("multi-turn harness %s has no transcript marker for turn %d", harness, turn)
}

func mutateV8InputCommitPayload(run *v8DuplexRun, harness string, turn int, payload []byte) error {
	if run == nil {
		return fmt.Errorf("cannot mutate a nil multi-turn run")
	}
	if turn < 1 || turn > v8MultiTurnCount {
		return fmt.Errorf("multi-turn harness %s input commit mutation turn %d is outside 1..%d", harness, turn, v8MultiTurnCount)
	}
	result, ok := run.harnesses[harness]
	if !ok {
		return fmt.Errorf("multi-turn harness %s is missing", harness)
	}
	commitOrdinal := 0
	for index := range result.Runtime {
		if result.Runtime[index].Kind != services.SessionRuntimeObservationInputCommit {
			continue
		}
		commitOrdinal++
		if commitOrdinal == turn {
			result.Runtime[index].Payload = append([]byte(nil), payload...)
			run.harnesses[harness] = result
			return nil
		}
	}
	return fmt.Errorf("multi-turn harness %s has no input commit for turn %d", harness, turn)
}

func dropV8InputCommit(run *v8DuplexRun, harness string, turn int) error {
	if run == nil {
		return fmt.Errorf("cannot mutate a nil multi-turn run")
	}
	if turn < 1 || turn > v8MultiTurnCount {
		return fmt.Errorf("multi-turn harness %s input commit drop turn %d is outside 1..%d", harness, turn, v8MultiTurnCount)
	}
	result, ok := run.harnesses[harness]
	if !ok {
		return fmt.Errorf("multi-turn harness %s is missing", harness)
	}
	filtered := make([]services.SessionRuntimeObservation, 0, len(result.Runtime))
	commitOrdinal := 0
	dropped := false
	for _, observation := range result.Runtime {
		if observation.Kind == services.SessionRuntimeObservationInputCommit {
			commitOrdinal++
			if commitOrdinal == turn {
				dropped = true
				continue
			}
		}
		filtered = append(filtered, observation)
	}
	if !dropped {
		return fmt.Errorf("multi-turn harness %s has no input commit for turn %d", harness, turn)
	}
	result.Runtime = filtered
	run.harnesses[harness] = result
	return nil
}
