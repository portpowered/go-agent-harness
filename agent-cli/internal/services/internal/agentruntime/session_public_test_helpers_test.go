package agentruntime

import (
	"bytes"
	"context"
	"encoding/binary"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
)

// These small fixtures keep unrelated runtime and room tests independent of
// the retired observability implementation. They record only the public
// service contracts exercised by those tests.
type diagnosticRecordSink struct {
	mu      sync.Mutex
	records []SessionDiagnosticRecord
}

func (s *diagnosticRecordSink) RecordSessionDiagnostic(record SessionDiagnosticRecord) {
	s.mu.Lock()
	s.records = append(s.records, record)
	s.mu.Unlock()
}

func (s *diagnosticRecordSink) all() []SessionDiagnosticRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SessionDiagnosticRecord(nil), s.records...)
}

func (s *diagnosticRecordSink) events(event string) []SessionDiagnosticRecord {
	var matched []SessionDiagnosticRecord
	for _, record := range s.all() {
		if record.Event == event {
			matched = append(matched, record)
		}
	}
	return matched
}

type sessionDiagnosticArtifacts struct {
	records  *diagnosticRecordSink
	snapshot metrics.Snapshot
	runErr   error
}

func (a sessionDiagnosticArtifacts) failureRecords() []SessionDiagnosticRecord {
	return a.records.events(SessionDiagnosticEventFailure)
}

func (a sessionDiagnosticArtifacts) turnRecords() []SessionDiagnosticRecord {
	return a.records.events(SessionDiagnosticEventTurn)
}

func (a sessionDiagnosticArtifacts) series(direction metrics.Direction, modality metrics.Modality) metrics.SeriesSnapshot {
	return a.snapshot.SeriesFor(direction, modality)
}

func runSessionWithDiagnostics(t testingT, mutate func(*SessionRunOptions)) sessionDiagnosticArtifacts {
	t.Helper()
	sink := &diagnosticRecordSink{}
	metricSink, err := metrics.NewInMemorySink()
	if err != nil {
		t.Fatalf("metrics.NewInMemorySink: %v", err)
	}
	opts := SessionRunOptions{ModelCatalog: testModelCatalog(), Diagnostics: sink, MetricsRecorder: metricSink}
	if mutate != nil {
		mutate(&opts)
	}
	var out bytes.Buffer
	runErr := RunSession(context.Background(), &out, opts)
	return sessionDiagnosticArtifacts{records: sink, snapshot: metricSink.Snapshot(), runErr: runErr}
}

type testingT interface {
	Helper()
	Fatalf(string, ...any)
}

type recordingSessionRuntimeObserver struct {
	mu           sync.Mutex
	observations []SessionRuntimeObservation
}

func (o *recordingSessionRuntimeObserver) ObserveSessionRuntime(observation SessionRuntimeObservation) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.observations = append(o.observations, observation)
	o.mu.Unlock()
}

func sessionTerminalObservationForCancellation(outputState messages.TerminalOutputState, roomBound bool) sessionTerminalObservation {
	return sessionTerminalObservation{
		TerminalReason:     messages.TerminalReasonCancellation,
		TerminalProvenance: messages.TerminalProvenanceRoom,
		OutputState:        outputState,
		RoomBound:          roomBound,
	}
}

func defaultSessionRuntimeFactory() SessionRuntimeFactory { return newDefaultSessionRuntimeFactory() }

func sessionAudioFrame(seed int16) []int16 {
	frame := make([]int16, 480)
	for index := range frame {
		frame[index] = seed + int16(index)
	}
	return frame
}

func pcm16Bytes(samples []int16) []byte {
	data := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(data[index*2:], uint16(sample))
	}
	return data
}

func equalInt16(got, want []int16) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
