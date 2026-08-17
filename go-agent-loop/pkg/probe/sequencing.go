package probe

import (
	"errors"
	"fmt"
	"sort"
	"strings"
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
	ErrInvalidSequencingPlan   = errors.New("invalid probe sequencing plan")
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
	ID   string      `json:"id"`
	Tick LogicalTime `json:"tick"`
	Step Step        `json:"step"`
}

type CausalExpectation struct {
	ID       string
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
func (c *TickContext) Emit(identity string) error {
	identity = strings.TrimSpace(identity)
	if c == nil || c.sequencer == nil || identity == "" {
		return fmt.Errorf("%w: event identity and context are required", ErrInvalidSequencingPlan)
	}
	c.sequencer.eventsMu.Lock()
	c.sequencer.events = append(c.sequencer.events, SequencedEvent{
		Tick: c.tick, Participant: c.participant, Source: string(c.participant), Identity: identity,
	})
	c.sequencer.eventsMu.Unlock()
	return nil
}

type OrderingViolationError struct {
	Relation string
	Stimulus SequencedEvent
	Response SequencedEvent
}

func (e *OrderingViolationError) Error() string {
	return fmt.Sprintf("%s %q: response %q observed at logical tick %d before stimulus %q at logical tick %d",
		ErrOrderingViolation, e.Relation, e.Response.Identity, e.Response.Tick, e.Stimulus.Identity, e.Stimulus.Tick)
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
	Err    error
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
			return nil, fmt.Errorf("%w: register %s: %v", ErrInvalidSequencingPlan, name, err)
		}
		sequencer.participants = append(sequencer.participants, worker{name: name, barrier: participant, work: make(chan tickWork, 1)})
	}
	return sequencer, nil
}

func (s *Sequencer) Participants() []Participant {
	result := make([]Participant, len(s.participants))
	for i, participant := range s.participants {
		result[i] = participant.name
	}
	return result
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
	for _, tick := range targets(s.plan) {
		if err := s.runTick(tick, stepsAt(s.plan.Steps, tick)); err != nil {
			return result, err
		}
		events := s.snapshot()
		for i, expectation := range s.plan.Expectations {
			if expectation.At != tick {
				continue
			}
			result.Expectations[i] = evaluate(expectation, events)
			if !result.Expectations[i].Passed {
				return result, result.Expectations[i].Err
			}
		}
	}
	result.Events = s.snapshot()
	if err := causality(s.plan.Causality, result.Events); err != nil {
		return result, err
	}
	return result, nil
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

func evaluate(expectation TickExpectation, events []SequencedEvent) ExpectationOutcome {
	outcome := ExpectationOutcome{ID: expectation.ID, Tick: expectation.At}
	for _, event := range events {
		if event.Identity == expectation.EventID && event.Tick == expectation.At {
			outcome.Passed = true
			break
		}
	}
	if !outcome.Passed {
		outcome.Err = fmt.Errorf("probe expectation %q failed at logical tick %d for event %q", expectation.ID, expectation.At, expectation.EventID)
	}
	return outcome
}

func causality(expectations []CausalExpectation, events []SequencedEvent) error {
	for i, relation := range expectations {
		stimulus, sok := firstEvent(events, relation.Stimulus)
		response, rok := firstEvent(events, relation.Response)
		if !sok || !rok || response.Tick >= stimulus.Tick {
			continue
		}
		if relation.ID == "" {
			relation.ID = fmt.Sprintf("causality-%d", i)
		}
		return &OrderingViolationError{Relation: relation.ID, Stimulus: stimulus, Response: response}
	}
	return nil
}
func firstEvent(events []SequencedEvent, identity string) (SequencedEvent, bool) {
	for _, event := range events {
		if event.Identity == identity {
			return event, true
		}
	}
	return SequencedEvent{}, false
}
func eventLess(left, right SequencedEvent) bool {
	if left.Tick != right.Tick {
		return left.Tick < right.Tick
	}
	return rank(left.Participant) < rank(right.Participant) ||
		(left.Participant == right.Participant && (left.Source < right.Source || (left.Source == right.Source && left.Identity < right.Identity)))
}
func rank(participant Participant) int {
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
		return SequencePlan{}, fmt.Errorf("%w: plan is empty", ErrInvalidSequencingPlan)
	}
	plan.Steps = append([]SequenceStep(nil), plan.Steps...)
	plan.Expectations = append([]TickExpectation(nil), plan.Expectations...)
	plan.Causality = append([]CausalExpectation(nil), plan.Causality...)
	for i := range plan.Steps {
		if plan.Steps[i].ID == "" {
			plan.Steps[i].ID = fmt.Sprintf("step-%d", i)
		}
		if plan.Steps[i].Tick <= 0 {
			return SequencePlan{}, fmt.Errorf("%w: step %q needs a positive tick", ErrInvalidSequencingPlan, plan.Steps[i].ID)
		}
	}
	for i := range plan.Expectations {
		expectation := &plan.Expectations[i]
		if expectation.ID == "" {
			expectation.ID = fmt.Sprintf("expectation-%d", i)
		}
		if expectation.At <= 0 || expectation.EventID == "" {
			return SequencePlan{}, fmt.Errorf("%w: expectation %q needs a positive tick and event", ErrInvalidSequencingPlan, expectation.ID)
		}
		if expectation.WallDuration != 0 {
			return SequencePlan{}, &WallDurationExpectationError{ID: expectation.ID, Duration: expectation.WallDuration}
		}
	}
	for i, relation := range plan.Causality {
		if relation.Stimulus == "" || relation.Response == "" {
			return SequencePlan{}, fmt.Errorf("%w: causality %d needs stimulus and response", ErrInvalidSequencingPlan, i)
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
