package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const roomLifecycleScenarioTimeout = 2 * time.Second

// roomLifecycleEvent is deliberately smaller than a provider event. The
// diagnosis only needs the identity, phase, and deterministic sequence to
// classify ownership; it must not retain credentials, payloads, or goroutine
// scheduling details.
type roomLifecycleEvent struct {
	Sequence      int
	ParticipantID string
	Phase         string
}

type roomLifecycleConnection struct {
	AttemptCount int
	OutcomeCount int
	Succeeded    bool
	Failed       bool
	Failure      string
}

type roomLifecycleSession struct {
	Created    bool
	CloseCalls int
	Closed     bool
}

type roomLifecycleObservation struct {
	Participants []string
	Connections  map[string]roomLifecycleConnection
	Sessions     map[string]roomLifecycleSession
	Terminals    []RoomParticipantResult
	Outstanding  []string
	Returned     bool
	Events       []roomLifecycleEvent
}

// validate is the diagnosis oracle's complete identity-aware contract. It is
// intentionally independent of callback arrival order and rejects a clean
// process result when any owned lifecycle fact is missing.
func (o roomLifecycleObservation) validate() error {
	want := make(map[string]struct{}, len(o.Participants))
	for _, id := range o.Participants {
		if _, exists := want[id]; exists {
			return fmt.Errorf("duplicate participant identity %q", id)
		}
		want[id] = struct{}{}
	}
	for _, id := range o.Participants {
		connection, exists := o.Connections[id]
		if !exists || connection.AttemptCount != 1 || connection.OutcomeCount != 1 {
			return fmt.Errorf("participant %q lacks exactly one connection outcome", id)
		}
		if !connection.Succeeded && !connection.Failed {
			return fmt.Errorf("participant %q connection outcome has no disposition", id)
		}
		session, exists := o.Sessions[id]
		if connection.Succeeded {
			if !exists || !session.Created {
				return fmt.Errorf("participant %q succeeded without a created session", id)
			}
			if session.CloseCalls != 1 || !session.Closed {
				return fmt.Errorf("participant %q created session close count = %d closed=%t, want exactly once", id, session.CloseCalls, session.Closed)
			}
		} else if exists && session.Created {
			return fmt.Errorf("participant %q has a created session for a failed connection", id)
		}
	}
	terminalCounts := make(map[string]int, len(o.Terminals))
	for _, terminal := range o.Terminals {
		if _, exists := want[terminal.ParticipantID]; !exists {
			return fmt.Errorf("terminal callback has unknown participant %q", terminal.ParticipantID)
		}
		if terminal.Reason == "" || terminal.TerminationReason == "" {
			return fmt.Errorf("participant %q terminal callback has no causal reason", terminal.ParticipantID)
		}
		terminalCounts[terminal.ParticipantID]++
	}
	for _, id := range o.Participants {
		if terminalCounts[id] != 1 {
			return fmt.Errorf("participant %q terminal callback count = %d, want exactly one", id, terminalCounts[id])
		}
	}
	if len(o.Outstanding) != 0 {
		return fmt.Errorf("owned lifecycle work remains: %s", strings.Join(o.Outstanding, ", "))
	}
	if !o.Returned {
		return errors.New("room returned without publishing a terminal observation")
	}
	return nil
}

func (o roomLifecycleObservation) eventBefore(participantID, firstPhase, secondParticipantID, secondPhase string) bool {
	first, second := 0, 0
	for _, event := range o.Events {
		if event.ParticipantID == participantID && event.Phase == firstPhase && first == 0 {
			first = event.Sequence
		}
		if event.ParticipantID == secondParticipantID && event.Phase == secondPhase && second == 0 {
			second = event.Sequence
		}
	}
	return first != 0 && second != 0 && first < second
}

type roomLifecycleLedger struct {
	mu sync.Mutex
	roomLifecycleObservation
	sequence    int
	outstanding map[string]struct{}
}

func newRoomLifecycleLedger(participants []string) *roomLifecycleLedger {
	return &roomLifecycleLedger{
		roomLifecycleObservation: roomLifecycleObservation{
			Participants: append([]string(nil), participants...),
			Connections:  make(map[string]roomLifecycleConnection, len(participants)),
			Sessions:     make(map[string]roomLifecycleSession, len(participants)),
		},
		outstanding: make(map[string]struct{}),
	}
}

func (l *roomLifecycleLedger) eventLocked(participantID, phase string) {
	l.sequence++
	l.Events = append(l.Events, roomLifecycleEvent{
		Sequence: l.sequence, ParticipantID: participantID, Phase: phase,
	})
}

func (l *roomLifecycleLedger) markOutstandingLocked(participantID, phase string) {
	l.outstanding[participantID+":"+phase] = struct{}{}
	l.eventLocked(participantID, phase+".pending")
}

func (l *roomLifecycleLedger) clearOutstandingLocked(participantID, phase string) {
	delete(l.outstanding, participantID+":"+phase)
}

func (l *roomLifecycleLedger) connectStarted(participantID string) {
	l.mu.Lock()
	connection := l.Connections[participantID]
	connection.AttemptCount++
	l.Connections[participantID] = connection
	l.markOutstandingLocked(participantID, "connect")
	l.mu.Unlock()
}

func (l *roomLifecycleLedger) connectContextCancelled(participantID string) {
	l.mu.Lock()
	l.eventLocked(participantID, "connect.context_cancelled")
	l.mu.Unlock()
}

func (l *roomLifecycleLedger) connectOutcome(participantID string, session bool, err error) {
	l.mu.Lock()
	connection := l.Connections[participantID]
	connection.OutcomeCount++
	connection.Succeeded = err == nil
	connection.Failed = err != nil
	if err != nil {
		connection.Failure = err.Error()
	}
	l.Connections[participantID] = connection
	l.clearOutstandingLocked(participantID, "connect")
	l.eventLocked(participantID, "connect.outcome")
	if session {
		l.Sessions[participantID] = roomLifecycleSession{Created: true}
		l.markOutstandingLocked(participantID, "session")
		l.eventLocked(participantID, "session.created")
	}
	l.mu.Unlock()
}

func (l *roomLifecycleLedger) sessionClosed(participantID string) {
	l.mu.Lock()
	session := l.Sessions[participantID]
	session.CloseCalls++
	session.Closed = true
	l.Sessions[participantID] = session
	l.clearOutstandingLocked(participantID, "session")
	l.eventLocked(participantID, "session.closed")
	l.mu.Unlock()
}

func (l *roomLifecycleLedger) participantTerminated(result RoomParticipantResult) {
	l.mu.Lock()
	l.Terminals = append(l.Terminals, result)
	l.clearOutstandingLocked(result.ParticipantID, "observer")
	l.eventLocked(result.ParticipantID, "participant.terminal")
	l.mu.Unlock()
}

func (l *roomLifecycleLedger) sessionOpened(participantID string) {
	l.mu.Lock()
	l.eventLocked(participantID, "session.opened")
	l.mu.Unlock()
}

func (l *roomLifecycleLedger) markReturned() {
	l.mu.Lock()
	l.Returned = true
	l.eventLocked("room", "return")
	l.mu.Unlock()
}

func (l *roomLifecycleLedger) snapshot() roomLifecycleObservation {
	l.mu.Lock()
	defer l.mu.Unlock()
	observation := roomLifecycleObservation{
		Participants: append([]string(nil), l.Participants...),
		Connections:  make(map[string]roomLifecycleConnection, len(l.Connections)),
		Sessions:     make(map[string]roomLifecycleSession, len(l.Sessions)),
		Terminals:    append([]RoomParticipantResult(nil), l.Terminals...),
		Returned:     l.Returned,
		Events:       append([]roomLifecycleEvent(nil), l.Events...),
	}
	for id, connection := range l.Connections {
		observation.Connections[id] = connection
	}
	for id, session := range l.Sessions {
		observation.Sessions[id] = session
	}
	for key := range l.outstanding {
		observation.Outstanding = append(observation.Outstanding, key)
	}
	return observation
}

type roomLifecycleConnectDecision struct {
	err error
}

type roomLifecycleInferencer struct {
	id           string
	ledger       *roomLifecycleLedger
	decisions    chan roomLifecycleConnectDecision
	outcomes     chan error
	contextReady chan struct{}

	mu      sync.Mutex
	session *roomLifecycleSessionRuntime
}

func newRoomLifecycleInferencer(id string, ledger *roomLifecycleLedger) *roomLifecycleInferencer {
	return &roomLifecycleInferencer{
		id: id, ledger: ledger,
		decisions: make(chan roomLifecycleConnectDecision, 1),
		outcomes:  make(chan error, 1), contextReady: make(chan struct{}, 1),
	}
}

func (i *roomLifecycleInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	i.ledger.connectStarted(i.id)
	i.contextReady <- struct{}{}
	var decision roomLifecycleConnectDecision
	select {
	case <-ctx.Done():
		i.ledger.connectContextCancelled(i.id)
		err := ctx.Err()
		i.ledger.connectOutcome(i.id, false, err)
		i.outcomes <- err
		return nil, err
	case decision = <-i.decisions:
	}
	if decision.err != nil {
		i.ledger.connectOutcome(i.id, false, decision.err)
		i.outcomes <- decision.err
		return nil, decision.err
	}
	session := newRoomLifecycleSessionRuntime(i.id, i.ledger)
	i.mu.Lock()
	i.session = session
	i.mu.Unlock()
	i.ledger.connectOutcome(i.id, true, nil)
	i.outcomes <- nil
	session.publish(roomTestSessionOpen(i.id))
	return session, nil
}

var _ messages.SessionInferencer = (*roomLifecycleInferencer)(nil)

func (i *roomLifecycleInferencer) waitContextReady(t *testing.T) {
	t.Helper()
	select {
	case <-i.contextReady:
	case <-time.After(roomLifecycleScenarioTimeout):
		t.Fatalf("participant %q did not reach connect phase", i.id)
	}
}

func (i *roomLifecycleInferencer) sessionSnapshot() *roomLifecycleSessionRuntime {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.session
}

type roomLifecycleSessionRuntime struct {
	id      string
	ledger  *roomLifecycleLedger
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	closed  int
	err     error
}

func newRoomLifecycleSessionRuntime(id string, ledger *roomLifecycleLedger) *roomLifecycleSessionRuntime {
	return &roomLifecycleSessionRuntime{
		id: id, ledger: ledger,
		receive: messages.NewTypedBuffer[messages.StreamMessage](32),
		done:    make(chan struct{}),
	}
}

func (s *roomLifecycleSessionRuntime) Send(ctx context.Context, _ messages.StreamMessage) bool {
	select {
	case <-ctx.Done():
		return false
	case <-s.done:
		return false
	default:
		return true
	}
}

func (s *roomLifecycleSessionRuntime) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *roomLifecycleSessionRuntime) Done() <-chan struct{} {
	return s.done
}

func (s *roomLifecycleSessionRuntime) TerminalError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *roomLifecycleSessionRuntime) Close() error {
	s.mu.Lock()
	s.closed++
	s.mu.Unlock()
	s.ledger.sessionClosed(s.id)
	s.end()
	return nil
}

func (s *roomLifecycleSessionRuntime) publish(events ...messages.StreamMessage) {
	for _, event := range events {
		if !s.receive.Write(context.Background(), event) {
			panic(fmt.Sprintf("room lifecycle session %q could not publish %s", s.id, event.Type))
		}
	}
}

func (s *roomLifecycleSessionRuntime) end() {
	s.once.Do(func() { close(s.done) })
}

func (s *roomLifecycleSessionRuntime) fail(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
	s.end()
}

type roomLifecycleRun struct {
	ids         []string
	ledger      *roomLifecycleLedger
	inferencers map[string]*roomLifecycleInferencer
	opened      map[string]chan struct{}
	terminal    map[string]chan RoomParticipantResult
	results     chan roomTestRunOutcome
	cancel      context.CancelFunc
	ctx         context.Context
}

func newRoomLifecycleRun(t *testing.T, ids []string) *roomLifecycleRun {
	t.Helper()
	ledger := newRoomLifecycleLedger(ids)
	inferencers := make(map[string]*roomLifecycleInferencer, len(ids))
	for _, id := range ids {
		inferencers[id] = newRoomLifecycleInferencer(id, ledger)
	}
	base, _ := newRoomTestRunOptions(ids, nil)
	base.Manifest.Room.MaxDuration = 10 * time.Second
	base.SessionFactory = func(participant room.Participant, _ SessionRunOptions) (messages.SessionInferencer, error) {
		inferencer, ok := inferencers[participant.ID]
		if !ok {
			return nil, fmt.Errorf("missing lifecycle inferencer for %s", participant.ID)
		}
		return inferencer, nil
	}
	opened := make(map[string]chan struct{}, len(ids))
	terminal := make(map[string]chan RoomParticipantResult, len(ids))
	for _, id := range ids {
		opened[id] = make(chan struct{}, 1)
		terminal[id] = make(chan RoomParticipantResult, 1)
	}
	base.onParticipantSessionOpen = func(id string) {
		ledger.sessionOpened(id)
		if signal, ok := opened[id]; ok {
			select {
			case signal <- struct{}{}:
			default:
			}
		}
	}
	base.OnParticipantTerminated = func(result RoomParticipantResult) {
		ledger.participantTerminated(result)
		if signal, ok := terminal[result.ParticipantID]; ok {
			select {
			case signal <- result:
			default:
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), roomLifecycleScenarioTimeout)
	run := &roomLifecycleRun{
		ids: ids, ledger: ledger, inferencers: inferencers,
		opened: opened, terminal: terminal,
		results: make(chan roomTestRunOutcome, 1), cancel: cancel, ctx: ctx,
	}
	go func() {
		result, err := RunRoomWithResult(ctx, io.Discard, base)
		ledger.markReturned()
		run.results <- roomTestRunOutcome{result: result, err: err}
	}()
	return run
}

func (r *roomLifecycleRun) waitSessionOpen(t *testing.T, id string) {
	t.Helper()
	signal, ok := r.opened[id]
	if !ok {
		t.Fatalf("participant %q has no session-open signal", id)
	}
	select {
	case <-signal:
	case <-time.After(roomLifecycleScenarioTimeout):
		t.Fatalf("participant %q SESSION.OPEN observation unresolved", id)
	}
}

func (r *roomLifecycleRun) waitConnectStarts(t *testing.T) {
	t.Helper()
	for _, id := range r.ids {
		r.inferencers[id].waitContextReady(t)
	}
}

func (r *roomLifecycleRun) decide(t *testing.T, id string, decision roomLifecycleConnectDecision) {
	t.Helper()
	select {
	case r.inferencers[id].decisions <- decision:
	case <-r.ctx.Done():
		t.Fatalf("participant %q connect decision unresolved: %v", id, r.ctx.Err())
	}
}

func (r *roomLifecycleRun) waitOutcome(t *testing.T, id string) error {
	t.Helper()
	select {
	case err := <-r.inferencers[id].outcomes:
		return err
	case <-time.After(roomLifecycleScenarioTimeout):
		t.Fatalf("participant %q connect outcome unresolved", id)
		return nil
	}
}

func (r *roomLifecycleRun) waitTerminal(t *testing.T, wantID string) RoomParticipantResult {
	t.Helper()
	signal, ok := r.terminal[wantID]
	if !ok {
		t.Fatalf("participant %q has no terminal signal", wantID)
	}
	select {
	case result := <-signal:
		if result.ParticipantID != wantID {
			t.Fatalf("terminal callback for %q carried participant %q", wantID, result.ParticipantID)
		}
		observation := r.ledger.snapshot()
		targetSequence := 0
		for _, event := range observation.Events {
			if event.ParticipantID == wantID && event.Phase == "participant.terminal" {
				targetSequence = event.Sequence
				break
			}
		}
		for _, event := range observation.Events {
			if event.Phase == "participant.terminal" && event.ParticipantID != wantID && event.Sequence < targetSequence {
				t.Fatalf("unexpected sibling %q terminated before target %q: %+v", event.ParticipantID, wantID, observation.Terminals)
			}
		}
		return result
	case <-time.After(roomLifecycleScenarioTimeout):
		t.Fatalf("participant %q terminal callback unresolved", wantID)
		return RoomParticipantResult{}
	}
}

func (r *roomLifecycleRun) waitResult(t *testing.T) roomTestRunOutcome {
	t.Helper()
	select {
	case outcome := <-r.results:
		return outcome
	case <-time.After(roomLifecycleScenarioTimeout):
		t.Fatalf("room result unresolved; expected lifecycle return")
		return roomTestRunOutcome{}
	}
}

func (r *roomLifecycleRun) session(t *testing.T, id string) *roomLifecycleSessionRuntime {
	t.Helper()
	session := r.inferencers[id].sessionSnapshot()
	if session == nil {
		t.Fatalf("participant %q session was not created", id)
	}
	return session
}

func (r *roomLifecycleRun) stop() {
	if r.cancel != nil {
		r.cancel()
	}
}

func (r *roomLifecycleRun) admitAll(t *testing.T) {
	t.Helper()
	r.waitConnectStarts(t)
	for _, id := range r.ids {
		r.decide(t, id, roomLifecycleConnectDecision{})
		if err := r.waitOutcome(t, id); err != nil {
			t.Fatalf("participant %q connection: %v", id, err)
		}
	}
	for _, id := range r.ids {
		r.waitSessionOpen(t, id)
	}
}

func TestRunRoomLifecycleDiagnosis_ForcedOrderings(t *testing.T) {
	ids := []string{"target", "sibling", "observer"}

	t.Run("fast connection failure before sibling completion", func(t *testing.T) {
		run := newRoomLifecycleRun(t, ids)
		defer run.stop()
		run.waitConnectStarts(t)
		run.decide(t, "target", roomLifecycleConnectDecision{err: errors.New("forced dial failure")})
		if err := run.waitOutcome(t, "target"); err == nil {
			t.Fatal("forced target failure returned nil error")
		}
		for _, id := range []string{"sibling", "observer"} {
			run.decide(t, id, roomLifecycleConnectDecision{})
			if err := run.waitOutcome(t, id); err != nil {
				t.Fatalf("viable sibling %q connection error = %v, want successful admission outcome", id, err)
			}
		}
		outcome := run.waitResult(t)
		if outcome.result.Reason != RoomTerminationFailed {
			t.Fatalf("room reason = %q, want failed", outcome.result.Reason)
		}
		assertAtomicStartupFailureResult(t, outcome.result, "target", []string{"sibling", "observer"})
		observation := run.ledger.snapshot()
		if err := observation.validate(); err != nil {
			t.Fatalf("identity-aware observation: %v\n%+v", err, observation)
		}
		// A fast failure must be recorded before the still-gated sibling is
		// admitted. The sibling is then allowed to publish its own success
		// outcome before rollback begins.
		if !observation.eventBefore("target", "connect.outcome", "sibling", "connect.outcome") {
			t.Fatalf("forced failure was not observed before sibling outcome: %+v", observation.Events)
		}
	})

	t.Run("sibling connection success before failure", func(t *testing.T) {
		run := newRoomLifecycleRun(t, ids)
		defer run.stop()
		run.waitConnectStarts(t)
		run.decide(t, "sibling", roomLifecycleConnectDecision{})
		if err := run.waitOutcome(t, "sibling"); err != nil {
			t.Fatalf("sibling connection: %v", err)
		}
		run.decide(t, "target", roomLifecycleConnectDecision{err: errors.New("forced late failure")})
		if err := run.waitOutcome(t, "target"); err == nil {
			t.Fatal("forced target failure returned nil error")
		}
		run.decide(t, "observer", roomLifecycleConnectDecision{})
		if err := run.waitOutcome(t, "observer"); err != nil {
			t.Fatalf("viable observer connection error = %v, want successful admission outcome", err)
		}
		outcome := run.waitResult(t)
		if outcome.result.Reason != RoomTerminationFailed {
			t.Fatalf("room reason = %q, want failed", outcome.result.Reason)
		}
		assertAtomicStartupFailureResult(t, outcome.result, "target", []string{"sibling", "observer"})
		observation := run.ledger.snapshot()
		if err := observation.validate(); err != nil {
			t.Fatalf("identity-aware observation: %v\n%+v", err, observation)
		}
		if !observation.eventBefore("sibling", "connect.outcome", "target", "connect.outcome") {
			t.Fatalf("sibling success was not observed before target failure: %+v", observation.Events)
		}
	})

	t.Run("target transport end before sibling terminal completion", func(t *testing.T) {
		run := newRoomLifecycleRun(t, ids)
		defer run.stop()
		run.waitConnectStarts(t)
		for _, id := range ids {
			run.decide(t, id, roomLifecycleConnectDecision{})
			if err := run.waitOutcome(t, id); err != nil {
				t.Fatalf("participant %q connection: %v", id, err)
			}
		}
		for _, id := range ids {
			run.waitSessionOpen(t, id)
		}
		run.session(t, "target").end()
		terminal := run.waitTerminal(t, "target")
		if terminal.Reason != ParticipantTerminationDisconnected {
			t.Fatalf("target terminal = %+v, want disconnected", terminal)
		}
		run.stop()
		outcome := run.waitResult(t)
		if outcome.err != nil {
			t.Fatalf("room stop after target transport end: %v", outcome.err)
		}
		if err := run.ledger.snapshot().validate(); err != nil {
			t.Fatalf("identity-aware observation: %v", err)
		}
	})

	t.Run("sibling activity concurrent with target transport end", func(t *testing.T) {
		run := newRoomLifecycleRun(t, ids)
		defer run.stop()
		run.waitConnectStarts(t)
		for _, id := range ids {
			run.decide(t, id, roomLifecycleConnectDecision{})
			if err := run.waitOutcome(t, id); err != nil {
				t.Fatalf("participant %q connection: %v", id, err)
			}
		}
		for _, id := range ids {
			run.waitSessionOpen(t, id)
		}
		sibling := run.session(t, "sibling")
		target := run.session(t, "target")
		// Both events are released by this one test-controlled boundary. The
		// observer must correlate the terminal callback to target regardless of
		// which receive loop consumes its pending activity first.
		sibling.publish(roomTestMessageEnd())
		target.end()
		terminal := run.waitTerminal(t, "target")
		if terminal.Reason != ParticipantTerminationDisconnected {
			t.Fatalf("target terminal = %+v, want disconnected", terminal)
		}
		run.stop()
		outcome := run.waitResult(t)
		if outcome.err != nil {
			t.Fatalf("room stop after concurrent activity: %v", outcome.err)
		}
		observation := run.ledger.snapshot()
		if err := observation.validate(); err != nil {
			t.Fatalf("identity-aware observation: %v", err)
		}
		if len(observation.Terminals) != len(ids) {
			t.Fatalf("terminal callbacks = %d, want %d", len(observation.Terminals), len(ids))
		}
	})

	t.Run("transport end before explicit session close and coordinator cancellation", func(t *testing.T) {
		run := newRoomLifecycleRun(t, ids)
		defer run.stop()
		run.waitConnectStarts(t)
		for _, id := range ids {
			run.decide(t, id, roomLifecycleConnectDecision{})
			if err := run.waitOutcome(t, id); err != nil {
				t.Fatalf("participant %q connection: %v", id, err)
			}
		}
		for _, id := range ids {
			run.waitSessionOpen(t, id)
		}
		target := run.session(t, "target")
		// The transport end is forced first; explicit room cancellation follows
		// only after the identity-correlated target callback is visible.
		target.end()
		terminal := run.waitTerminal(t, "target")
		if terminal.Reason != ParticipantTerminationDisconnected {
			t.Fatalf("target terminal = %+v, want disconnected", terminal)
		}
		run.stop()
		outcome := run.waitResult(t)
		if outcome.err != nil {
			t.Fatalf("room cancellation after transport end: %v", outcome.err)
		}
		if err := run.ledger.snapshot().validate(); err != nil {
			t.Fatalf("identity-aware observation: %v", err)
		}
	})
}

func TestRunRoomLifecycleTerminalCausePrecedence(t *testing.T) {
	ids := []string{"target", "sibling", "observer"}

	t.Run("genuine participant error survives coordinator cancellation", func(t *testing.T) {
		run := newRoomLifecycleRun(t, ids)
		defer run.stop()
		run.admitAll(t)
		target := run.session(t, "target")
		target.fail(errors.New("forced participant failure"))
		terminal := run.waitTerminal(t, "target")
		if terminal.Reason != ParticipantTerminationError || !strings.Contains(terminal.Error, "forced participant failure") {
			t.Fatalf("genuine participant failure terminal = %+v, want error with causal detail", terminal)
		}
		run.stop()
		outcome := run.waitResult(t)
		if outcome.err != nil {
			t.Fatalf("room cancellation after participant failure: %v", outcome.err)
		}
		if err := run.ledger.snapshot().validate(); err != nil {
			t.Fatalf("identity-aware observation: %v", err)
		}
	})

	t.Run("typed provider close survives coordinator cancellation", func(t *testing.T) {
		run := newRoomLifecycleRun(t, ids)
		defer run.stop()
		run.admitAll(t)
		target := run.session(t, "target")
		target.publish(messages.StreamMessage{
			Type: messages.StreamTypeSessionClose,
			Value: messages.NewSessionCloseValueWithTerminal(
				"target",
				"provider_closed",
				"transport",
				messages.TerminalReasonProviderClose,
				messages.TerminalProvenanceProvider,
				messages.TerminalOutputNone,
			),
		})
		terminal := run.waitTerminal(t, "target")
		if terminal.Reason != ParticipantTerminationDisconnected {
			t.Fatalf("typed provider close terminal = %+v, want disconnected", terminal)
		}
		run.stop()
		outcome := run.waitResult(t)
		if outcome.err != nil {
			t.Fatalf("room cancellation after typed provider close: %v", outcome.err)
		}
		observation := run.ledger.snapshot()
		if err := observation.validate(); err != nil {
			t.Fatalf("identity-aware observation: %v", err)
		}
		if session := observation.Sessions["target"]; session.CloseCalls != 1 || !session.Closed {
			t.Fatalf("typed provider session ownership = %+v, want exactly one close", session)
		}
	})

	t.Run("typed session close survives later transport end", func(t *testing.T) {
		run := newRoomLifecycleRun(t, ids)
		defer run.stop()
		run.admitAll(t)
		target := run.session(t, "target")
		target.publish(messages.StreamMessage{
			Type:  messages.StreamTypeSessionClose,
			Value: messages.NewSessionCloseValue("target", "client_close"),
		})
		terminal := run.waitTerminal(t, "target")
		if terminal.Reason != ParticipantTerminationEnded {
			t.Fatalf("typed session close terminal = %+v, want ended", terminal)
		}
		// The runner closes the session after consuming the typed close. A
		// repeated transport end must not replace that observed session cause.
		target.end()
		run.stop()
		outcome := run.waitResult(t)
		if outcome.err != nil {
			t.Fatalf("room cancellation after typed session close: %v", outcome.err)
		}
		if err := run.ledger.snapshot().validate(); err != nil {
			t.Fatalf("identity-aware observation: %v", err)
		}
	})

	t.Run("transport end wins over a later typed close", func(t *testing.T) {
		run := newRoomLifecycleRun(t, ids)
		defer run.stop()
		run.admitAll(t)
		target := run.session(t, "target")
		target.end()
		terminal := run.waitTerminal(t, "target")
		target.publish(messages.StreamMessage{
			Type:  messages.StreamTypeSessionClose,
			Value: messages.NewSessionCloseValue("target", "client_close"),
		})
		if terminal.Reason != ParticipantTerminationDisconnected {
			t.Fatalf("transport-first terminal = %+v, want disconnected", terminal)
		}
		run.stop()
		outcome := run.waitResult(t)
		if outcome.err != nil {
			t.Fatalf("room cancellation after transport-first close: %v", outcome.err)
		}
		if err := run.ledger.snapshot().validate(); err != nil {
			t.Fatalf("identity-aware observation: %v", err)
		}
	})
}

func assertAtomicStartupFailureResult(t *testing.T, result RoomResult, failedID string, viableIDs []string) {
	t.Helper()
	if len(result.Participants) != len(viableIDs)+1 {
		t.Fatalf("startup failure participants = %d, want %d: %+v", len(result.Participants), len(viableIDs)+1, result.Participants)
	}
	failed := result.Participants[failedID]
	if failed.ParticipantID != failedID || failed.ID != failedID || failed.Connected {
		t.Fatalf("failed participant result = %+v, want identity %q and no connection", failed, failedID)
	}
	if failed.Reason != ParticipantTerminationError || failed.Error == "" {
		t.Fatalf("failed participant result = %+v, want causal error disposition", failed)
	}
	for _, id := range viableIDs {
		participant := result.Participants[id]
		if participant.ParticipantID != id || participant.ID != id || !participant.Connected {
			t.Fatalf("viable participant %q result = %+v, want connected identity", id, participant)
		}
		if participant.Error != "" {
			t.Fatalf("viable participant %q inherited startup error: %+v", id, participant)
		}
	}
}

func TestRunRoomLifecycleDiagnosis_NegativeControls(t *testing.T) {
	valid := roomLifecycleObservation{
		Participants: []string{"a", "b"},
		Connections: map[string]roomLifecycleConnection{
			"a": {AttemptCount: 1, OutcomeCount: 1, Succeeded: true},
			"b": {AttemptCount: 1, OutcomeCount: 1, Failed: true, Failure: "context canceled"},
		},
		Sessions: map[string]roomLifecycleSession{
			"a": {Created: true, CloseCalls: 1, Closed: true},
		},
		Terminals: []RoomParticipantResult{
			{ID: "a", ParticipantID: "a", Reason: ParticipantTerminationEnded, TerminationReason: ParticipantTerminationEnded},
			{ID: "b", ParticipantID: "b", Reason: ParticipantTerminationError, TerminationReason: ParticipantTerminationError},
		},
		Returned: true,
	}
	cases := []struct {
		name   string
		mutate func(*roomLifecycleObservation)
		want   string
	}{
		{
			name: "first-callback-only oracle",
			mutate: func(observation *roomLifecycleObservation) {
				observation.Terminals = observation.Terminals[:1]
			},
			want: "terminal callback count",
		},
		{
			name: "missing participant outcome",
			mutate: func(observation *roomLifecycleObservation) {
				delete(observation.Connections, "b")
			},
			want: "connection outcome",
		},
		{
			name: "unclosed created session",
			mutate: func(observation *roomLifecycleObservation) {
				observation.Sessions["a"] = roomLifecycleSession{Created: true}
			},
			want: "close count",
		},
		{
			name: "duplicate callback",
			mutate: func(observation *roomLifecycleObservation) {
				observation.Terminals = append(observation.Terminals, observation.Terminals[0])
			},
			want: "terminal callback count",
		},
		{
			name: "duplicate close",
			mutate: func(observation *roomLifecycleObservation) {
				observation.Sessions["a"] = roomLifecycleSession{Created: true, CloseCalls: 2, Closed: true}
			},
			want: "close count",
		},
		{
			name: "wrong identity",
			mutate: func(observation *roomLifecycleObservation) {
				observation.Terminals[0].ParticipantID = "wrong"
			},
			want: "unknown participant",
		},
		{
			name: "wrong reason",
			mutate: func(observation *roomLifecycleObservation) {
				observation.Terminals[0].Reason = ""
			},
			want: "no causal reason",
		},
		{
			name: "unresolved work at return",
			mutate: func(observation *roomLifecycleObservation) {
				observation.Outstanding = []string{"a:participant.loop"}
			},
			want: "owned lifecycle work",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			observation := cloneRoomLifecycleObservation(valid)
			testCase.mutate(&observation)
			if err := observation.validate(); err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("negative control error = %v, want substring %q", err, testCase.want)
			}
		})
	}
}

func cloneRoomLifecycleObservation(observation roomLifecycleObservation) roomLifecycleObservation {
	clone := roomLifecycleObservation{
		Participants: append([]string(nil), observation.Participants...),
		Connections:  make(map[string]roomLifecycleConnection, len(observation.Connections)),
		Sessions:     make(map[string]roomLifecycleSession, len(observation.Sessions)),
		Terminals:    append([]RoomParticipantResult(nil), observation.Terminals...),
		Outstanding:  append([]string(nil), observation.Outstanding...),
		Returned:     observation.Returned,
		Events:       append([]roomLifecycleEvent(nil), observation.Events...),
	}
	for id, connection := range observation.Connections {
		clone.Connections[id] = connection
	}
	for id, session := range observation.Sessions {
		clone.Sessions[id] = session
	}
	return clone
}
