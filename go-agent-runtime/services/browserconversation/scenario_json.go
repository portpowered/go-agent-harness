package browserconversation

import (
	"encoding/json"
)

func cloneBrowserConversationScenario(scenario BrowserConversationScenario) BrowserConversationScenario {
	clone := scenario
	clone.Fixture.Pages = append([]BrowserConversationPage(nil), scenario.Fixture.Pages...)
	clone.Steps = make([]BrowserConversationStep, len(scenario.Steps))
	for index, step := range scenario.Steps {
		clone.Steps[index] = step
		if step.ExpectedState != nil {
			transition := *step.ExpectedState
			transition.Before = append(json.RawMessage(nil), step.ExpectedState.Before...)
			transition.After = append(json.RawMessage(nil), step.ExpectedState.After...)
			clone.Steps[index].ExpectedState = &transition
		}
		if step.Navigation != nil {
			navigation := *step.Navigation
			clone.Steps[index].Navigation = &navigation
		}
		if step.Correction != nil {
			correction := *step.Correction
			correction.ExpectedState.Before = append(json.RawMessage(nil), step.Correction.ExpectedState.Before...)
			correction.ExpectedState.After = append(json.RawMessage(nil), step.Correction.ExpectedState.After...)
			clone.Steps[index].Correction = &correction
		}
		if step.Interrupt != nil {
			interrupt := *step.Interrupt
			clone.Steps[index].Interrupt = &interrupt
		}
		if step.Cancel != nil {
			cancel := *step.Cancel
			clone.Steps[index].Cancel = &cancel
		}
	}
	return clone
}

// Clone returns a defensive copy of the public scenario value.
func (s BrowserConversationScenario) Clone() BrowserConversationScenario {
	return cloneBrowserConversationScenario(s)
}

type browserConversationScenarioJSON struct {
	Version     string                              `json:"version"`
	ID          string                              `json:"id"`
	Name        string                              `json:"name"`
	Fixture     BrowserConversationFixture          `json:"fixture"`
	Steps       []browserConversationStepJSON       `json:"steps"`
	RunTimeout  string                              `json:"run_timeout"`
	PostSession BrowserConversationTabStateRequired `json:"post_session"`
}

type browserConversationStepJSON struct {
	ID            string                            `json:"id"`
	Utterance     string                            `json:"utterance"`
	PageID        string                            `json:"page_id"`
	ExpectedState *BrowserStateTransition           `json:"expected_state,omitempty"`
	Navigation    *BrowserCustomerNavigation        `json:"navigation,omitempty"`
	Correction    *BrowserConversationCorrection    `json:"correction,omitempty"`
	Interrupt     *BrowserConversationInterrupt     `json:"interrupt,omitempty"`
	Cancel        *BrowserConversationCancelRequest `json:"cancel,omitempty"`
	Deadline      string                            `json:"deadline"`
}

// MarshalJSON emits bounded durations as readable strings and exposes only
// the scenario contract's fields.
func (s BrowserConversationScenario) MarshalJSON() ([]byte, error) {
	steps := make([]browserConversationStepJSON, len(s.Steps))
	for index, step := range s.Steps {
		steps[index] = browserConversationStepJSON{
			ID: step.ID, Utterance: step.Utterance, PageID: step.PageID,
			ExpectedState: cloneStateTransitionPointer(step.ExpectedState),
			Navigation:    cloneNavigationPointer(step.Navigation),
			Correction:    cloneCorrectionPointer(step.Correction),
			Interrupt:     cloneInterruptPointer(step.Interrupt),
			Cancel:        cloneCancelPointer(step.Cancel), Deadline: step.Deadline.String(),
		}
	}
	return json.Marshal(browserConversationScenarioJSON{
		Version: s.Version, ID: s.ID, Name: s.Name, Fixture: s.Fixture, Steps: steps,
		RunTimeout: s.RunTimeout.String(), PostSession: s.PostSession,
	})
}

func cloneStateTransitionPointer(value *BrowserStateTransition) *BrowserStateTransition {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Before = append(json.RawMessage(nil), value.Before...)
	clone.After = append(json.RawMessage(nil), value.After...)
	return &clone
}

func cloneNavigationPointer(value *BrowserCustomerNavigation) *BrowserCustomerNavigation {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneCorrectionPointer(value *BrowserConversationCorrection) *BrowserConversationCorrection {
	if value == nil {
		return nil
	}
	clone := *value
	clone.ExpectedState.Before = append(json.RawMessage(nil), value.ExpectedState.Before...)
	clone.ExpectedState.After = append(json.RawMessage(nil), value.ExpectedState.After...)
	return &clone
}

func cloneInterruptPointer(value *BrowserConversationInterrupt) *BrowserConversationInterrupt {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneCancelPointer(value *BrowserConversationCancelRequest) *BrowserConversationCancelRequest {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
