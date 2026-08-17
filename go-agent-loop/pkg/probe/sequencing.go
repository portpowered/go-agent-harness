package probe

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/test/functional/timeharness"
)

type Participant string

const (
	ScenarioDriver Participant = "driver"
	Client         Participant = "client"
	Agent          Participant = "agent"
)

var (
	ErrOrderingViolation       = errors.New("probe ordering violation")
	ErrWallDurationExpectation = errors.New("probe expectation requires logical ticks")
)

type SequencedEvent struct {
	Tick        LogicalTime `json:"tick"`
	Participant Participant `json:"participant"`
	Source      string      `json:"source"`
	Identity    string      `json:"identity"`
}

type SequenceStep struct {
	Tick LogicalTime `json:"tick"`
	Step Step        `json:"step"`
}

type CausalExpectation struct {
	Stimulus string
	Response string
}

type TickExpectation struct {
	ID           string
	At           LogicalTime
	EventID      string
	WallDuration time.Duration
}

type SequencePlan struct {
	Steps        []SequenceStep
	Expectations []TickExpectation
	Causality    []CausalExpectation
}

type ParticipantHandler func(*TickContext, []SequenceStep) error
type ParticipantHandlers struct {
	Driver ParticipantHandler
	Client ParticipantHandler
	Agent  ParticipantHandler
}

type TickContext struct {
	sequencer   *Sequencer
	participant Participant
	tick        LogicalTime
}

func (c *TickContext) Tick() LogicalTime { return c.tick }
func (c *TickContext) Emit(identity string) {
	c.sequencer.eventsMu.Lock()
	c.sequencer.events = append(c.sequencer.events, SequencedEvent{
		Tick: c.tick, Participant: c.participant, Source: string(c.participant), Identity: identity,
	})
	c.sequencer.eventsMu.Unlock()
}

type OrderingViolationError struct {
	Stimulus SequencedEvent
	Response SequencedEvent
}

func (e *OrderingViolationError) Error() string {
	return fmt.Sprintf("%s: response %q observed at logical tick %d before stimulus %q at logical tick %d",
		ErrOrderingViolation, e.Response.Identity, e.Response.Tick, e.Stimulus.Identity, e.Stimulus.Tick)
}
func (e *OrderingViolationError) Unwrap() error { return ErrOrderingViolation }

type WallDurationExpectationError struct {
	ID       string
	Duration time.Duration
}

func (e *WallDurationExpectationError) Error() string {
	return fmt.Sprintf("%s %q: duration %s is not a logical tick contract", ErrWallDurationExpectation, e.ID, e.Duration)
}
func (e *WallDurationExpectationError) Unwrap() error { return ErrWallDurationExpectation }

type ExpectationOutcome struct {
	ID     string
	Tick   LogicalTime
	Passed bool
}
type SequenceResult struct {
	Events       []SequencedEvent
	Expectations []ExpectationOutcome
}

type worker struct {
	name    Participant
	barrier *timeharness.Participant
	work    chan tickWork
}
type tickWork struct {
	tick  LogicalTime
	steps []SequenceStep
	done  chan error
}
type Sequencer struct {
	plan         SequencePlan
	harness      *timeharness.Scenario
	participants []worker
	eventsMu     sync.Mutex
	events       []SequencedEvent
}

func NewSequencer(plan SequencePlan) (*Sequencer, error) {
	plan, err := normalizePlan(plan)
	if err != nil {
		return nil, err
	}
	harness := timeharness.New(time.Time{}, 0)
	sequencer := &Sequencer{plan: plan, harness: harness}
	for _, name := range []Participant{ScenarioDriver, Client, Agent} {
		participant, err := harness.Register(string(name))
		if err != nil {
			return nil, fmt.Errorf("register %s: %w", name, err)
		}
		sequencer.participants = append(sequencer.participants, worker{name: name, barrier: participant, work: make(chan tickWork, 1)})
	}
	return sequencer, nil
}

func (s *Sequencer) Run(handlers ParticipantHandlers) (SequenceResult, error) {
	var groups sync.WaitGroup
	for i := range s.participants {
		current := &s.participants[i]
		fn := handlers.Driver
		if current.name == Client {
			fn = handlers.Client
		} else if current.name == Agent {
			fn = handlers.Agent
		}
		groups.Add(1)
		current.barrier.Run(func() {
			defer groups.Done()
			defer current.barrier.Complete()
			for task := range current.work {
				if _, err := current.barrier.Observe(uint64(task.tick)); err != nil {
					task.done <- err
				} else if fn == nil {
					task.done <- nil
				} else {
					task.done <- fn(&TickContext{sequencer: s, participant: current.name, tick: task.tick}, task.steps)
				}
			}
		})
	}
	defer func() {
		s.harness.Close()
		for i := range s.participants {
			close(s.participants[i].work)
		}
		groups.Wait()
	}()

	result := SequenceResult{Expectations: make([]ExpectationOutcome, len(s.plan.Expectations))}
	var expectationErr error
	for _, tick := range targets(s.plan) {
		if err := s.runTick(tick, stepsAt(s.plan.Steps, tick)); err != nil {
			return result, err
		}
		events := s.snapshot()
		for i, expectation := range s.plan.Expectations {
			if expectation.At != tick {
				continue
			}
			passed := hasEventAt(events, expectation.EventID, expectation.At)
			result.Expectations[i] = ExpectationOutcome{ID: expectation.ID, Tick: expectation.At, Passed: passed}
			if !passed && expectationErr == nil {
				expectationErr = fmt.Errorf("probe expectation %q failed at logical tick %d for event %q", expectation.ID, expectation.At, expectation.EventID)
			}
		}
	}
	result.Events = s.snapshot()
	if err := causality(s.plan.Causality, result.Events); err != nil {
		return result, err
	}
	return result, expectationErr
}

func (s *Sequencer) runTick(tick LogicalTime, steps []SequenceStep) error {
	task := tickWork{tick: tick, steps: steps, done: make(chan error, len(s.participants))}
	for i := range s.participants {
		s.participants[i].work <- task
	}
	if _, err := s.harness.AdvanceTo(uint64(tick)); err != nil {
		for range s.participants {
			<-task.done
		}
		return fmt.Errorf("probe sequencer at logical tick %d: %w", tick, err)
	}
	for range s.participants {
		if err := <-task.done; err != nil {
			return err
		}
	}
	return nil
}

func (s *Sequencer) snapshot() []SequencedEvent {
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	result := append([]SequencedEvent(nil), s.events...)
	sort.Slice(result, func(i, j int) bool { return eventLess(result[i], result[j]) })
	return result
}

func hasEventAt(events []SequencedEvent, identity string, tick LogicalTime) bool {
	for _, event := range events {
		if event.Identity == identity && event.Tick == tick {
			return true
		}
	}
	return false
}

func causality(expectations []CausalExpectation, events []SequencedEvent) error {
	for _, relation := range expectations {
		var stimulus, response SequencedEvent
		var stimulusOK, responseOK bool
		for _, event := range events {
			if !stimulusOK && event.Identity == relation.Stimulus {
				stimulus, stimulusOK = event, true
			}
			if !responseOK && event.Identity == relation.Response {
				response, responseOK = event, true
			}
		}
		if stimulusOK && responseOK && response.Tick < stimulus.Tick {
			return &OrderingViolationError{Stimulus: stimulus, Response: response}
		}
	}
	return nil
}

func eventLess(left, right SequencedEvent) bool {
	if left.Tick != right.Tick {
		return left.Tick < right.Tick
	}
	if participantRank(left.Participant) != participantRank(right.Participant) {
		return participantRank(left.Participant) < participantRank(right.Participant)
	}
	return left.Identity < right.Identity
}
func participantRank(participant Participant) int {
	switch participant {
	case ScenarioDriver:
		return 0
	case Client:
		return 1
	case Agent:
		return 2
	default:
		return 3
	}
}

func normalizePlan(plan SequencePlan) (SequencePlan, error) {
	if len(plan.Steps) == 0 && len(plan.Expectations) == 0 {
		return SequencePlan{}, errors.New("invalid probe sequencing plan: plan is empty")
	}
	plan.Steps = append([]SequenceStep(nil), plan.Steps...)
	plan.Expectations = append([]TickExpectation(nil), plan.Expectations...)
	for _, step := range plan.Steps {
		if step.Tick <= 0 {
			return SequencePlan{}, fmt.Errorf("invalid probe sequencing plan: step needs a positive tick")
		}
	}
	for _, expectation := range plan.Expectations {
		if expectation.At <= 0 || expectation.EventID == "" {
			return SequencePlan{}, fmt.Errorf("invalid probe sequencing plan: expectation needs a positive tick and event")
		}
		if expectation.WallDuration != 0 {
			return SequencePlan{}, &WallDurationExpectationError{ID: expectation.ID, Duration: expectation.WallDuration}
		}
	}
	return plan, nil
}
func targets(plan SequencePlan) []LogicalTime {
	seen := map[LogicalTime]bool{}
	for _, step := range plan.Steps {
		seen[step.Tick] = true
	}
	for _, expectation := range plan.Expectations {
		seen[expectation.At] = true
	}
	result := make([]LogicalTime, 0, len(seen))
	for tick := range seen {
		result = append(result, tick)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
func stepsAt(steps []SequenceStep, tick LogicalTime) []SequenceStep {
	result := make([]SequenceStep, 0, len(steps))
	for _, step := range steps {
		if step.Tick == tick {
			result = append(result, step)
		}
	}
	return result
}
