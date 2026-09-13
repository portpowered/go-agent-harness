package browserscenario

import (
	"encoding/json"
	"errors"
	"sync"
)

// BrowserConversationRun is a serialized observation collector. All mutation
// is guarded by one mutex, and Finalize publishes one immutable snapshot.
type BrowserConversationRun struct {
	mu              sync.Mutex
	scenario        BrowserConversationScenario
	steps           map[string]BrowserConversationStep
	nextSeq         uint64
	result          BrowserConversationResult
	finalized       bool
	hasCancellation bool
	hasLifecycle    bool
	hasMechanical   bool
	hasValidator    bool
	hasRecovery     bool
	hasCorrections  bool
}

// NewRun validates the full scenario before creating the collector. No
// fixture, provider, process, or audio boundary is touched.
func (s BrowserConversationScenario) NewRun() (*BrowserConversationRun, error) {
	validated, err := s.Admit()
	if err != nil {
		return nil, err
	}
	return newBrowserConversationRun(validated), nil
}

func newBrowserConversationRun(scenario BrowserConversationScenario) *BrowserConversationRun {
	steps := make(map[string]BrowserConversationStep, len(scenario.Steps))
	for _, step := range scenario.Steps {
		steps[step.ID] = step
	}
	return &BrowserConversationRun{
		scenario: scenario,
		steps:    steps,
		nextSeq:  1,
		result: BrowserConversationResult{
			ScenarioID: scenario.ID, ScenarioName: scenario.Name,
			Lifecycle: BrowserConversationLifecycleEvidence{Outcome: BrowserConversationLifecycleNotStarted},
			Validator: BrowserConversationValidatorVerdict{
				Version: BrowserConversationValidatorVersion,
				Status:  BrowserConversationValidatorNotRun,
			},
		},
	}
}

// Scenario returns a defensive copy of the admitted scenario.
func (r *BrowserConversationRun) Scenario() BrowserConversationScenario {
	if r == nil {
		return BrowserConversationScenario{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneBrowserConversationScenario(r.scenario)
}

// ObserveCustomerTurn appends one observed customer turn and binds its
// expected utterance from the validated scenario step.
func (r *BrowserConversationRun) ObserveCustomerTurn(stepID, observed string) error {
	if r == nil {
		return errors.New("browser conversation run is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	step, err := r.stepForObservationLocked(stepID, observed)
	if err != nil {
		return err
	}
	r.result.Turns = append(r.result.Turns, BrowserConversationTurn{
		Sequence: r.takeSequenceLocked(), StepID: step.ID,
		Direction: BrowserConversationCustomerTurn, ExpectedText: step.Utterance,
		ObservedText: observed, Complete: true,
	})
	return nil
}

// ObserveAssistantTurn appends one observed assistant transcript turn.
func (r *BrowserConversationRun) ObserveAssistantTurn(stepID, observed string) error {
	if r == nil {
		return errors.New("browser conversation run is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	step, err := r.stepForObservationLocked(stepID, observed)
	if err != nil {
		return err
	}
	r.result.Turns = append(r.result.Turns, BrowserConversationTurn{
		Sequence: r.takeSequenceLocked(), StepID: step.ID,
		Direction: BrowserConversationAssistantTurn, ObservedText: observed,
		Complete: true,
	})
	return nil
}

// ObserveTurn appends a preassembled turn, useful when the shared transcript
// collector already knows its completion state.
func (r *BrowserConversationRun) ObserveTurn(turn BrowserConversationTurn) error {
	if r == nil {
		return errors.New("browser conversation run is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureMutableLocked(); err != nil {
		return err
	}
	step, err := r.stepForObservationLocked(turn.StepID, turn.ObservedText)
	if err != nil {
		return err
	}
	if turn.Direction != BrowserConversationCustomerTurn && turn.Direction != BrowserConversationAssistantTurn {
		return browserConversationObservationError("turn.direction", "must identify customer or assistant")
	}
	turn.Sequence = r.takeSequenceLocked()
	turn.StepID = step.ID
	if turn.Direction == BrowserConversationCustomerTurn && turn.ExpectedText == "" {
		turn.ExpectedText = step.Utterance
	}
	r.result.Turns = append(r.result.Turns, cloneBrowserConversationTurn(turn))
	return nil
}

// ObserveBrokerCall appends one broker observation without validating or
// rewriting InputJSON. Terminal state is copied exactly as observed.
func (r *BrowserConversationRun) ObserveBrokerCall(call BrowserConversationBrokerCall) error {
	if r == nil {
		return errors.New("browser conversation run is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureMutableLocked(); err != nil {
		return err
	}
	if call.Operation == "" {
		return browserConversationObservationError("broker_call.operation", "is required")
	}
	if !browserConversationBrokerOperationValid(call.Operation) {
		return browserConversationObservationError("broker_call.operation", "is unsupported")
	}
	if call.StepID != "" {
		if _, ok := r.steps[call.StepID]; !ok {
			return browserConversationObservationError("broker_call.step_id", "references unknown step %q", call.StepID)
		}
	}
	call.Sequence = r.takeSequenceLocked()
	call.InputJSON = string([]byte(call.InputJSON))
	call.Output = append(json.RawMessage(nil), call.Output...)
	r.result.BrokerCalls = append(r.result.BrokerCalls, cloneBrowserConversationBrokerCall(call))
	return nil
}
