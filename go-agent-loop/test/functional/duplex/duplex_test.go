package duplex

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/parity"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-loop/test/functional/sessions"
	"github.com/portpowered/go-agent-harness/go-agent-loop/test/functional/timeharness"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const (
	directionAToB = "A-to-B"
	directionBToA = "B-to-A"

	duplexTickDuration = time.Millisecond
	duplexRepetitions  = 100
)

// canonicalDuplexTrace is intentionally literal. The trace has one retained
// event per tick so the observed order cannot be hidden by sorting or by
// comparing only aggregate counts. The speech windows overlap on ticks 3-6.
var canonicalDuplexTrace = []duplexEvent{
	{Tick: 1, Direction: directionAToB, Kind: "speech.start", Payload: "a-open"},
	{Tick: 2, Direction: directionAToB, Kind: "speech.frame", Payload: "a-frame-1"},
	{Tick: 3, Direction: directionBToA, Kind: "speech.start", Payload: "b-open"},
	{Tick: 4, Direction: directionAToB, Kind: "speech.frame", Payload: "a-overlap"},
	{Tick: 5, Direction: directionBToA, Kind: "speech.frame", Payload: "b-overlap"},
	{Tick: 6, Direction: directionAToB, Kind: "speech.stop", Payload: "a-close"},
	{Tick: 7, Direction: directionBToA, Kind: "speech.frame", Payload: "b-frame-2"},
	{Tick: 8, Direction: directionBToA, Kind: "speech.stop", Payload: "b-close"},
}

type duplexEvent struct {
	Tick      uint64
	Direction string
	Kind      string
	Payload   string
}

type duplexRun struct {
	trace  []duplexEvent
	client []transcript.Record
	agent  []transcript.Record
}

// sessionCapture records every crossing through the existing session
// transcript recorder and also provides an event-driven observation channel.
// The channel is only a liveness seam; the transcript itself remains the
// authoritative artifact used by parity normalization.
type sessionCapture struct {
	collector *sessions.SessionTranscript
	records   chan transcript.Record
}

func newSessionCapture() *sessionCapture {
	return &sessionCapture{
		collector: sessions.NewSessionTranscript(),
		records:   make(chan transcript.Record, 64),
	}
}

func (c *sessionCapture) Write(record transcript.Record) error {
	if err := c.collector.Write(record); err != nil {
		return err
	}
	c.records <- record
	return nil
}

type observedCrossing struct {
	tick            uint64
	payload         []byte
	clientDirection transcript.Direction
	agentDirection  transcript.Direction
}

func (c *sessionCapture) awaitCrossing(payload []byte, clientDirection, agentDirection transcript.Direction) (observedCrossing, error) {
	matched := make([]transcript.Record, 0, 2)
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()

	for len(matched) < 2 {
		select {
		case record := <-c.records:
			if bytes.Equal(record.Payload, payload) {
				matched = append(matched, record)
			}
		case <-timer.C:
			return observedCrossing{}, fmt.Errorf("timed out waiting for two captured records with payload %q", payload)
		}
	}

	var clientRecord, agentRecord *transcript.Record
	for index := range matched {
		record := matched[index]
		switch record.Peer {
		case transcript.PeerClient:
			if clientRecord != nil {
				return observedCrossing{}, fmt.Errorf("captured duplicate client record for payload %q", payload)
			}
			clientRecord = &record
		case transcript.PeerAgent:
			if agentRecord != nil {
				return observedCrossing{}, fmt.Errorf("captured duplicate agent record for payload %q", payload)
			}
			agentRecord = &record
		default:
			return observedCrossing{}, fmt.Errorf("captured unknown peer %q for payload %q", record.Peer, payload)
		}
	}
	if clientRecord == nil || agentRecord == nil {
		return observedCrossing{}, fmt.Errorf("captured payload %q without one client and one agent record", payload)
	}
	if clientRecord.Direction != clientDirection || agentRecord.Direction != agentDirection {
		return observedCrossing{}, fmt.Errorf(
			"captured payload %q directions client=%q agent=%q, want client=%q agent=%q",
			payload, clientRecord.Direction, agentRecord.Direction, clientDirection, agentDirection,
		)
	}
	if clientRecord.Tick != agentRecord.Tick || clientRecord.Timestamp != agentRecord.Timestamp {
		return observedCrossing{}, fmt.Errorf("captured payload %q has mismatched logical metadata: client=%+v agent=%+v", payload, clientRecord, agentRecord)
	}
	if clientRecord.Stream != transcript.StreamRTCAudio || agentRecord.Stream != transcript.StreamRTCAudio {
		return observedCrossing{}, fmt.Errorf("captured payload %q streams client=%q agent=%q, want %q", payload, clientRecord.Stream, agentRecord.Stream, transcript.StreamRTCAudio)
	}

	return observedCrossing{
		tick:            clientRecord.Tick,
		payload:         append([]byte(nil), clientRecord.Payload...),
		clientDirection: clientRecord.Direction,
		agentDirection:  agentRecord.Direction,
	}, nil
}

func (c *sessionCapture) awaitRecordCount(count int) error {
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for index := 0; index < count; index++ {
		select {
		case <-c.records:
		case <-timer.C:
			return fmt.Errorf("timed out waiting for %d initial captured records", count)
		}
	}
	return nil
}

type traceRecorder struct {
	mu     sync.Mutex
	events []duplexEvent
}

func (r *traceRecorder) append(event duplexEvent) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *traceRecorder) snapshot() []duplexEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]duplexEvent(nil), r.events...)
}

type tickCompletion struct {
	direction string
	tick      uint64
}

type directionPath struct {
	name            string
	clock           *clock.Deterministic
	participant     *timeharness.Participant
	events          map[uint64]duplexEvent
	clientDirection transcript.Direction
	agentDirection  transcript.Direction
	sendAudio       func([]byte)
	capture         *sessionCapture
}

func (p directionPath) execute(event duplexEvent, expectedTick uint64) (duplexEvent, error) {
	p.sendAudio([]byte(event.Payload))
	crossing, err := p.capture.awaitCrossing([]byte(event.Payload), p.clientDirection, p.agentDirection)
	if err != nil {
		return duplexEvent{}, fmt.Errorf("%s tick %d %s: %w", p.name, expectedTick, event.Kind, err)
	}
	if crossing.tick != expectedTick || p.clock.Tick() != expectedTick {
		return duplexEvent{}, fmt.Errorf("%s event %s observed at capture tick %d/current tick %d, want %d", p.name, event.Kind, crossing.tick, p.clock.Tick(), expectedTick)
	}

	direction := directionAToB
	if crossing.clientDirection == transcript.DirectionIn {
		direction = directionBToA
	}
	return duplexEvent{
		Tick:      crossing.tick,
		Direction: direction,
		Kind:      event.Kind,
		Payload:   string(crossing.payload),
	}, nil
}

func runDirection(path directionPath, trace *traceRecorder, completions chan<- tickCompletion, workerErrors chan<- error, workers *sync.WaitGroup) {
	path.participant.Run(func() {
		defer workers.Done()
		defer path.participant.Complete()

		for tick := uint64(1); tick <= uint64(len(canonicalDuplexTrace)); tick++ {
			observation, err := path.participant.Observe(tick)
			if err != nil {
				workerErrors <- fmt.Errorf("%s failed to observe tick %d: %w", path.name, tick, err)
				return
			}
			if observation.Tick != tick || path.clock.Tick() != tick {
				workerErrors <- fmt.Errorf("%s observed tick=%d/current tick=%d, want %d", path.name, observation.Tick, path.clock.Tick(), tick)
				return
			}

			if event, ok := path.events[tick]; ok {
				observed, err := path.execute(event, tick)
				if err != nil {
					workerErrors <- err
					return
				}
				trace.append(observed)
			}
			completions <- tickCompletion{direction: path.name, tick: tick}
		}
	})
}

func awaitTickCompletions(completions <-chan tickCompletion, workerErrors <-chan error, tick uint64) error {
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for completed := 0; completed < 2; {
		select {
		case err := <-workerErrors:
			return err
		case completion := <-completions:
			if completion.tick != tick {
				return fmt.Errorf("%s completion reported tick %d while driving tick %d", completion.direction, completion.tick, tick)
			}
			completed++
		case <-timer.C:
			return fmt.Errorf("timed out waiting for both direction workers to finish tick %d", tick)
		}
	}
	return nil
}

func TestDuplexFunctionalOverlappingSpeechIsExactAndParityStable(t *testing.T) {
	var firstTrace []duplexEvent
	for run := 1; run <= duplexRepetitions; run++ {
		result := runDuplexScenario(t, run)

		if !reflect.DeepEqual(result.trace, canonicalDuplexTrace) {
			t.Fatalf("run %d observed trace differs from the literal canonical trace:\n got: %#v\nwant: %#v", run, result.trace, canonicalDuplexTrace)
		}
		if run == 1 {
			firstTrace = append([]duplexEvent(nil), result.trace...)
		} else if !reflect.DeepEqual(result.trace, firstTrace) {
			t.Fatalf("run %d observed trace differs from run 1:\n got: %#v\nrun1: %#v", run, result.trace, firstTrace)
		}

		assertDuplexCountsAndOverlap(t, run, result.trace)
		assertDirectionalParity(t, run, result)
	}
}

func runDuplexScenario(t *testing.T, run int) duplexRun {
	t.Helper()

	functionalTime := timeharness.New(time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC), duplexTickDuration)
	defer functionalTime.Close()
	sharedClock := functionalTime.Clock()

	capture := newSessionCapture()
	inferencer := sessions.NewMockSessionInferencer()
	session := sessions.NewSessionScenarioWithConfig(t, inferencer, sessions.NewMockToolExecutor(), sessions.SessionScenarioOptions{
		Clock:   sharedClock,
		Capture: capture,
	})
	if sessionClock, ok := session.Clock().(*clock.Deterministic); !ok || sessionClock != sharedClock {
		t.Fatalf("run %d session clock identity changed: got %T/%p, want shared deterministic clock %p", run, session.Clock(), sessionClock, sharedClock)
	}

	session.Start()
	stopped := false
	defer func() {
		if !stopped {
			_ = session.Stop(3 * time.Second)
		}
	}()
	if err := capture.awaitRecordCount(2); err != nil {
		t.Fatalf("run %d session startup capture: %v", run, err)
	}

	aToB, err := functionalTime.Register(directionAToB)
	if err != nil {
		t.Fatalf("run %d register %s: %v", run, directionAToB, err)
	}
	bToA, err := functionalTime.Register(directionBToA)
	if err != nil {
		t.Fatalf("run %d register %s: %v", run, directionBToA, err)
	}

	paths := []directionPath{
		{
			name:            directionAToB,
			clock:           sharedClock,
			participant:     aToB,
			events:          eventsForDirection(directionAToB),
			clientDirection: transcript.DirectionOut,
			agentDirection:  transcript.DirectionIn,
			sendAudio:       session.SendAudioInput,
			capture:         capture,
		},
		{
			name:            directionBToA,
			clock:           sharedClock,
			participant:     bToA,
			events:          eventsForDirection(directionBToA),
			clientDirection: transcript.DirectionIn,
			agentDirection:  transcript.DirectionOut,
			sendAudio: func(payload []byte) {
				inferencer.AddServerEvent(messages.StreamMessage{
					Type:  messages.StreamTypeAudioDelta,
					Role:  messages.RoleAssistant,
					Value: messages.NewAudioDeltaValue(payload),
				})
			},
			capture: capture,
		},
	}
	for _, path := range paths {
		if path.clock != sharedClock {
			t.Fatalf("run %d %s received a different deterministic clock: got %p, want %p", run, path.name, path.clock, sharedClock)
		}
	}

	trace := &traceRecorder{}
	completions := make(chan tickCompletion, 2)
	workerErrors := make(chan error, 2)
	var workers sync.WaitGroup
	workers.Add(len(paths))
	for _, path := range paths {
		runDirection(path, trace, completions, workerErrors, &workers)
	}

	for tick := uint64(1); tick <= uint64(len(canonicalDuplexTrace)); tick++ {
		if _, err := functionalTime.AdvanceTo(tick); err != nil {
			t.Fatalf("run %d advance to logical tick %d: %v", run, tick, err)
		}
		if err := awaitTickCompletions(completions, workerErrors, tick); err != nil {
			t.Fatalf("run %d logical tick %d: %v", run, tick, err)
		}
	}
	workers.Wait()

	select {
	case err := <-workerErrors:
		t.Fatalf("run %d direction worker: %v", run, err)
	default:
	}

	if err := session.Stop(3 * time.Second); err != nil {
		t.Fatalf("run %d stop session: %v", run, err)
	}
	stopped = true

	return duplexRun{
		trace:  trace.snapshot(),
		client: capture.collector.ClientRecords(),
		agent:  capture.collector.AgentRecords(),
	}
}

func eventsForDirection(direction string) map[uint64]duplexEvent {
	events := make(map[uint64]duplexEvent)
	for _, event := range canonicalDuplexTrace {
		if event.Direction == direction {
			events[event.Tick] = event
		}
	}
	return events
}

func assertDuplexCountsAndOverlap(t *testing.T, run int, trace []duplexEvent) {
	t.Helper()
	counts := map[string]int{directionAToB: 0, directionBToA: 0}
	payloadCounts := map[string]int{directionAToB: 0, directionBToA: 0}
	starts := map[string]uint64{}
	stops := map[string]uint64{}

	for index, event := range trace {
		if event.Direction != directionAToB && event.Direction != directionBToA {
			t.Fatalf("run %d trace[%d] has unknown direction %q", run, index, event.Direction)
		}
		if event.Payload == "" {
			t.Fatalf("run %d trace[%d] has empty payload identity", run, index)
		}
		counts[event.Direction]++
		payloadCounts[event.Direction]++
		switch event.Kind {
		case "speech.start":
			starts[event.Direction] = event.Tick
		case "speech.stop":
			stops[event.Direction] = event.Tick
		}
	}

	for _, direction := range []string{directionAToB, directionBToA} {
		if counts[direction] != 4 || payloadCounts[direction] != 4 {
			t.Fatalf("run %d %s counts: events=%d payloads=%d, want four non-zero events and payloads", run, direction, counts[direction], payloadCounts[direction])
		}
		if starts[direction] == 0 || stops[direction] == 0 || starts[direction] >= stops[direction] {
			t.Fatalf("run %d %s speech window: start=%d stop=%d", run, direction, starts[direction], stops[direction])
		}
	}

	startA, startB := starts[directionAToB], starts[directionBToA]
	stopA, stopB := stops[directionAToB], stops[directionBToA]
	maxStart := startA
	if startB > maxStart {
		maxStart = startB
	}
	minStop := stopA
	if stopB < minStop {
		minStop = stopB
	}
	if maxStart >= minStop {
		t.Fatalf("run %d speech windows do not overlap: max(startA=%d,startB=%d)=%d, min(stopA=%d,stopB=%d)=%d", run, startA, startB, maxStart, stopA, stopB, minStop)
	}
}

func assertDirectionalParity(t *testing.T, run int, result duplexRun) {
	t.Helper()
	assertParityForDirection(t, run, directionAToB, transcript.DirectionOut, transcript.DirectionIn, result.client, result.agent)
	assertParityForDirection(t, run, directionBToA, transcript.DirectionIn, transcript.DirectionOut, result.client, result.agent)
}

func assertParityForDirection(t *testing.T, run int, direction string, clientDirection, agentDirection transcript.Direction, clientRecords, agentRecords []transcript.Record) {
	t.Helper()
	client := audioRecords(clientRecords, transcript.PeerClient, clientDirection)
	agent := audioRecords(agentRecords, transcript.PeerAgent, agentDirection)
	if len(client) != 4 || len(agent) != 4 {
		t.Fatalf("run %d %s captured audio records: client=%d agent=%d, want four per side", run, direction, len(client), len(agent))
	}

	clientProjection := normalizeClient(t, run, direction, client)
	agentProjection := normalizeAgent(t, run, direction, agent)
	if differences := parity.Compare(clientProjection, agentProjection); len(differences) != 0 {
		t.Fatalf("run %d %s client/agent parity differences: %#v", run, direction, differences)
	}
}

func audioRecords(records []transcript.Record, peer transcript.Peer, direction transcript.Direction) []transcript.Record {
	filtered := make([]transcript.Record, 0, len(records))
	for _, record := range records {
		if record.Peer == peer && record.Direction == direction && record.Stream == transcript.StreamRTCAudio {
			filtered = append(filtered, record)
		}
	}
	return filtered
}

func normalizeClient(t *testing.T, run int, direction string, records []transcript.Record) parity.Projection {
	t.Helper()
	projection, err := parity.NormalizeClient(records)
	if err != nil {
		failNormalization(t, run, direction, "client", err)
	}
	return projection
}

func normalizeAgent(t *testing.T, run int, direction string, records []transcript.Record) parity.Projection {
	t.Helper()
	projection, err := parity.NormalizeAgent(fmt.Sprintf("agent %s", direction), records)
	if err != nil {
		failNormalization(t, run, direction, "agent", err)
	}
	return projection
}

func failNormalization(t *testing.T, run int, direction, side string, err error) {
	t.Helper()
	var typed *parity.NormalizationError
	if !errors.As(err, &typed) {
		t.Fatalf("run %d %s %s normalization error lost typed identity: %v", run, direction, side, err)
	}
	t.Fatalf("run %d %s %s normalization failed (interface=%q field=%q reason=%q): %v", run, direction, side, typed.Interface, typed.Field, typed.Reason, err)
}
