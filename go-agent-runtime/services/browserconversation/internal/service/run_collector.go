package service

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
)

// browserConversationRun is a serialized observation collector. All mutation
// is guarded by one mutex, and Finalize publishes one immutable snapshot.
type browserConversationRun struct {
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

func newBrowserConversationRun(scenario BrowserConversationScenario) *browserConversationRun {
	steps := make(map[string]BrowserConversationStep, len(scenario.Steps))
	for _, step := range scenario.Steps {
		steps[step.ID] = step
	}
	return &browserConversationRun{
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
func (r *browserConversationRun) Scenario() BrowserConversationScenario {
	if r == nil {
		return BrowserConversationScenario{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.scenario.Clone()
}

// ObserveCustomerTurn appends one observed customer turn and binds its
// expected utterance from the validated scenario step.
func (r *browserConversationRun) ObserveCustomerTurn(stepID, observed string) error {
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
func (r *browserConversationRun) ObserveAssistantTurn(stepID, observed string) error {
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
func (r *browserConversationRun) ObserveTurn(turn BrowserConversationTurn) error {
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
func (r *browserConversationRun) ObserveBrokerCall(call BrowserConversationBrokerCall) error {
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

// Snapshot returns a defensive result snapshot. It does not finalize the run.
func (r *browserConversationRun) Snapshot() BrowserConversationResult {
	if r == nil {
		return BrowserConversationResult{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneBrowserConversationResult(r.result)
}

// Finalize publishes exactly one immutable result. Repeated calls return the
// same snapshot and do not permit late observations to alter it.
func (r *browserConversationRun) Finalize() (BrowserConversationResult, error) {
	if r == nil {
		return BrowserConversationResult{}, errors.New("browser conversation run is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.finalized {
		r.finalized = true
		r.result.Finalized = true
		r.result = cloneBrowserConversationResult(r.result)
	}
	return cloneBrowserConversationResult(r.result), nil
}

func (r *browserConversationRun) stepForObservationLocked(stepID, observed string) (BrowserConversationStep, error) {
	if err := r.ensureMutableLocked(); err != nil {
		return BrowserConversationStep{}, err
	}
	if strings.TrimSpace(stepID) == "" {
		return BrowserConversationStep{}, browserConversationObservationError("step_id", "is required")
	}
	step, ok := r.steps[stepID]
	if !ok {
		return BrowserConversationStep{}, browserConversationObservationError("step_id", "references unknown step %q", stepID)
	}
	if strings.TrimSpace(observed) == "" {
		return BrowserConversationStep{}, browserConversationObservationError("observed_text", "must not be empty")
	}
	return step, nil
}

func (r *browserConversationRun) ensureMutableLocked() error {
	if r.finalized {
		return ErrBrowserConversationRunFinalized
	}
	return nil
}

func (r *browserConversationRun) takeSequenceLocked() uint64 {
	sequence := r.nextSeq
	r.nextSeq++
	return sequence
}
