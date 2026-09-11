package agentruntime

// This file is a deliberately small compatibility boundary for the read-only
// session runner. Browser scenario policy lives in the public runtime service;
// these value conversions keep the legacy runner source unchanged while it is
// retired in a later execution slice.
// Deprecated: use go-agent-runtime/services/browserscenario through its
// injected service contract instead.

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	runtimeBrowser "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserscenario"
	browserScenarioWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserscenario/wire"
)

const (
	BrowserConversationScenarioVersion  = runtimeBrowser.BrowserConversationScenarioVersion
	BrowserConversationValidatorVersion = runtimeBrowser.BrowserConversationValidatorVersion
)

const (
	ErrInvalidBrowserConversationScenario      = runtimeBrowser.ErrInvalidBrowserConversationScenario
	ErrBrowserConversationRunFinalized         = runtimeBrowser.ErrBrowserConversationRunFinalized
	ErrBrowserConversationDuplicateObservation = runtimeBrowser.ErrBrowserConversationDuplicateObservation
	ErrInvalidBrowserConversationResult        = runtimeBrowser.ErrInvalidBrowserConversationResult
)

type BrowserConversationScenarioError struct {
	Path   string
	Reason string
}

func (e *BrowserConversationScenarioError) Error() string {
	if e == nil {
		return ErrInvalidBrowserConversationScenario.Error()
	}
	message := ErrInvalidBrowserConversationScenario.Error()
	if e.Path != "" {
		message += " at " + e.Path
	}
	if e.Reason != "" {
		message += ": " + e.Reason
	}
	return message
}

func (e *BrowserConversationScenarioError) Unwrap() error {
	return ErrInvalidBrowserConversationScenario
}

func validateJSONObject(path string, raw json.RawMessage) error {
	return legacyBrowserScenarioError((runtimeBrowser.BrowserConversationScenario{}).ValidateJSONObject(path, raw))
}

type BrowserConversationScenario struct {
	Version     string                              `json:"version"`
	ID          string                              `json:"id"`
	Name        string                              `json:"name"`
	Fixture     BrowserConversationFixture          `json:"fixture"`
	Steps       []BrowserConversationStep           `json:"steps"`
	RunTimeout  time.Duration                       `json:"-"`
	PostSession BrowserConversationTabStateRequired `json:"post_session"`
}

type BrowserScenario = BrowserConversationScenario
type WebMCPScenario = BrowserConversationScenario
type WebMCPConversationScenario = BrowserConversationScenario

type BrowserConversationFixture struct {
	ID          string                    `json:"id"`
	Pages       []BrowserConversationPage `json:"pages"`
	InitialPage string                    `json:"initial_page"`
}

type BrowserConversationPage struct {
	ID  string `json:"id"`
	URL string `json:"url,omitempty"`
}

type BrowserScenarioFixture = BrowserConversationFixture
type BrowserScenarioPage = BrowserConversationPage
type BrowserScenarioStateTransition = BrowserStateTransition
type BrowserScenarioNavigation = BrowserCustomerNavigation
type BrowserScenarioCorrection = BrowserConversationCorrection
type BrowserScenarioInterrupt = BrowserConversationInterrupt
type BrowserScenarioCancelRequest = BrowserConversationCancelRequest
type BrowserScenarioTabStateRequired = BrowserConversationTabStateRequired

type BrowserConversationStep struct {
	ID            string                            `json:"id"`
	Utterance     string                            `json:"utterance"`
	PageID        string                            `json:"page_id"`
	ExpectedState *BrowserStateTransition           `json:"expected_state,omitempty"`
	Navigation    *BrowserCustomerNavigation        `json:"navigation,omitempty"`
	Correction    *BrowserConversationCorrection    `json:"correction,omitempty"`
	Interrupt     *BrowserConversationInterrupt     `json:"interrupt,omitempty"`
	Cancel        *BrowserConversationCancelRequest `json:"cancel,omitempty"`
	Deadline      time.Duration                     `json:"-"`
}

type BrowserScenarioStep = BrowserConversationStep

type BrowserStateTransition struct {
	PageID string          `json:"page_id"`
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
}

type BrowserCustomerNavigation struct {
	FromPageID string `json:"from_page_id,omitempty"`
	ToPageID   string `json:"to_page_id"`
	URL        string `json:"url"`
}

type BrowserConversationCorrection struct {
	TargetStepID  string                 `json:"target_step_id"`
	ExpectedState BrowserStateTransition `json:"expected_state"`
}

type BrowserConversationInterruptTrigger string

const BrowserInterruptOnInFlightInvocation BrowserConversationInterruptTrigger = "in_flight_invocation"

type BrowserConversationInterrupt struct {
	Trigger  BrowserConversationInterruptTrigger `json:"trigger"`
	ToolName string                              `json:"tool_name,omitempty"`
}

type BrowserConversationCancelRequest struct {
	Reason string `json:"reason"`
}

type BrowserConversationTabStateRequired struct {
	PageID            string `json:"page_id"`
	MustRemainAlive   bool   `json:"must_remain_alive"`
	MustBeResponsive  bool   `json:"must_be_responsive"`
	MustAllowMutation bool   `json:"must_allow_mutation"`
}

type BrowserConversationValidator interface {
	ValidateBrowserConversation(BrowserConversationResult) (BrowserConversationValidatorVerdict, error)
}

type BrowserConversationValidatorFunc func(BrowserConversationResult) (BrowserConversationValidatorVerdict, error)

func (f BrowserConversationValidatorFunc) ValidateBrowserConversation(result BrowserConversationResult) (BrowserConversationValidatorVerdict, error) {
	if f == nil {
		return BrowserConversationValidatorVerdict{}, errors.New("browser conversation validator function is nil")
	}
	return f(result)
}

func NewBrowserConversationScenario(scenario BrowserConversationScenario) (BrowserConversationScenario, error) {
	validated, err := browserScenarioWire.NewService().AdmitScenario(legacyScenarioToRuntime(scenario))
	if err != nil {
		return BrowserConversationScenario{}, legacyBrowserScenarioError(err)
	}
	return runtimeScenarioToLegacy(validated), nil
}

func NewBrowserScenario(scenario BrowserScenario) (BrowserScenario, error) {
	return NewBrowserConversationScenario(scenario)
}

func NewWebMCPConversationScenario(scenario WebMCPConversationScenario) (WebMCPConversationScenario, error) {
	return NewBrowserConversationScenario(scenario)
}

func (s BrowserConversationScenario) Validate() error {
	if err := legacyScenarioToRuntime(s).Validate(); err != nil {
		return legacyBrowserScenarioError(err)
	}
	return nil
}

func (s BrowserConversationScenario) ScheduleAudioInputs(audioByStep map[string][]byte) ([]ScheduledAudioInput, error) {
	validated, err := NewBrowserConversationScenario(s)
	if err != nil {
		return nil, err
	}
	runtimeInputs, err := browserScenarioWire.NewService().ScheduleAudioInputs(legacyScenarioToRuntime(validated), audioByStep)
	if err != nil {
		return nil, err
	}
	inputs := make([]ScheduledAudioInput, len(runtimeInputs))
	for index, input := range runtimeInputs {
		inputs[index] = ScheduledAudioInput{AfterCompletedTurns: input.AfterCompletedTurns, PCM: append([]byte(nil), input.PCM...), SourceSampleRate: input.SourceSampleRate, EndOfTurn: input.EndOfTurn}
	}
	return inputs, nil
}

func (s BrowserConversationScenario) MarshalJSON() ([]byte, error) {
	validated, err := NewBrowserConversationScenario(s)
	if err != nil {
		return nil, err
	}
	return json.Marshal(legacyScenarioToRuntime(validated))
}

func (s *BrowserConversationScenario) UnmarshalJSON(data []byte) error {
	if s == nil {
		return errors.New("cannot unmarshal browser conversation scenario into nil receiver")
	}
	var decoded runtimeBrowser.BrowserConversationScenario
	if err := json.Unmarshal(data, &decoded); err != nil {
		return legacyBrowserScenarioError(err)
	}
	*s = runtimeScenarioToLegacy(decoded)
	return nil
}

type BrowserConversationScenarioForSession interface {
	BrowserConversationScenario() BrowserConversationScenario
}

type BrowserConversationScenarioValue struct{ scenario BrowserConversationScenario }

func NewBrowserConversationScenarioValue(scenario BrowserConversationScenario) (BrowserConversationScenarioValue, error) {
	validated, err := NewBrowserConversationScenario(scenario)
	if err != nil {
		return BrowserConversationScenarioValue{}, err
	}
	return BrowserConversationScenarioValue{scenario: validated}, nil
}

func (v BrowserConversationScenarioValue) BrowserConversationScenario() BrowserConversationScenario {
	return cloneBrowserConversationScenario(v.scenario)
}

func cloneBrowserConversationScenario(scenario BrowserConversationScenario) BrowserConversationScenario {
	return runtimeScenarioToLegacy(legacyScenarioToRuntime(scenario))
}

func legacyScenarioToRuntime(scenario BrowserConversationScenario) runtimeBrowser.BrowserConversationScenario {
	converted := runtimeBrowser.BrowserConversationScenario{
		Version: scenario.Version, ID: scenario.ID, Name: scenario.Name, RunTimeout: scenario.RunTimeout,
		PostSession: runtimeBrowser.BrowserConversationTabStateRequired{
			PageID: scenario.PostSession.PageID, MustRemainAlive: scenario.PostSession.MustRemainAlive,
			MustBeResponsive: scenario.PostSession.MustBeResponsive, MustAllowMutation: scenario.PostSession.MustAllowMutation,
		},
		Fixture: runtimeBrowser.BrowserConversationFixture{ID: scenario.Fixture.ID, InitialPage: scenario.Fixture.InitialPage},
	}
	for _, page := range scenario.Fixture.Pages {
		converted.Fixture.Pages = append(converted.Fixture.Pages, runtimeBrowser.BrowserConversationPage{ID: page.ID, URL: page.URL})
	}
	for _, step := range scenario.Steps {
		convertedStep := runtimeBrowser.BrowserConversationStep{ID: step.ID, Utterance: step.Utterance, PageID: step.PageID, Deadline: step.Deadline}
		if step.ExpectedState != nil {
			convertedStep.ExpectedState = &runtimeBrowser.BrowserStateTransition{PageID: step.ExpectedState.PageID, Before: append(json.RawMessage(nil), step.ExpectedState.Before...), After: append(json.RawMessage(nil), step.ExpectedState.After...)}
		}
		if step.Navigation != nil {
			convertedStep.Navigation = &runtimeBrowser.BrowserCustomerNavigation{FromPageID: step.Navigation.FromPageID, ToPageID: step.Navigation.ToPageID, URL: step.Navigation.URL}
		}
		if step.Correction != nil {
			convertedStep.Correction = &runtimeBrowser.BrowserConversationCorrection{TargetStepID: step.Correction.TargetStepID, ExpectedState: runtimeBrowser.BrowserStateTransition{PageID: step.Correction.ExpectedState.PageID, Before: append(json.RawMessage(nil), step.Correction.ExpectedState.Before...), After: append(json.RawMessage(nil), step.Correction.ExpectedState.After...)}}
		}
		if step.Interrupt != nil {
			convertedStep.Interrupt = &runtimeBrowser.BrowserConversationInterrupt{Trigger: runtimeBrowser.BrowserConversationInterruptTrigger(step.Interrupt.Trigger), ToolName: step.Interrupt.ToolName}
		}
		if step.Cancel != nil {
			convertedStep.Cancel = &runtimeBrowser.BrowserConversationCancelRequest{Reason: step.Cancel.Reason}
		}
		converted.Steps = append(converted.Steps, convertedStep)
	}
	return converted
}

func runtimeScenarioToLegacy(scenario runtimeBrowser.BrowserConversationScenario) BrowserConversationScenario {
	converted := BrowserConversationScenario{Version: scenario.Version, ID: scenario.ID, Name: scenario.Name, RunTimeout: scenario.RunTimeout, PostSession: BrowserConversationTabStateRequired{PageID: scenario.PostSession.PageID, MustRemainAlive: scenario.PostSession.MustRemainAlive, MustBeResponsive: scenario.PostSession.MustBeResponsive, MustAllowMutation: scenario.PostSession.MustAllowMutation}, Fixture: BrowserConversationFixture{ID: scenario.Fixture.ID, InitialPage: scenario.Fixture.InitialPage}}
	for _, page := range scenario.Fixture.Pages {
		converted.Fixture.Pages = append(converted.Fixture.Pages, BrowserConversationPage{ID: page.ID, URL: page.URL})
	}
	for _, step := range scenario.Steps {
		convertedStep := BrowserConversationStep{ID: step.ID, Utterance: step.Utterance, PageID: step.PageID, Deadline: step.Deadline}
		if step.ExpectedState != nil {
			convertedStep.ExpectedState = &BrowserStateTransition{PageID: step.ExpectedState.PageID, Before: append(json.RawMessage(nil), step.ExpectedState.Before...), After: append(json.RawMessage(nil), step.ExpectedState.After...)}
		}
		if step.Navigation != nil {
			convertedStep.Navigation = &BrowserCustomerNavigation{FromPageID: step.Navigation.FromPageID, ToPageID: step.Navigation.ToPageID, URL: step.Navigation.URL}
		}
		if step.Correction != nil {
			convertedStep.Correction = &BrowserConversationCorrection{TargetStepID: step.Correction.TargetStepID, ExpectedState: BrowserStateTransition{PageID: step.Correction.ExpectedState.PageID, Before: append(json.RawMessage(nil), step.Correction.ExpectedState.Before...), After: append(json.RawMessage(nil), step.Correction.ExpectedState.After...)}}
		}
		if step.Interrupt != nil {
			convertedStep.Interrupt = &BrowserConversationInterrupt{Trigger: BrowserConversationInterruptTrigger(step.Interrupt.Trigger), ToolName: step.Interrupt.ToolName}
		}
		if step.Cancel != nil {
			convertedStep.Cancel = &BrowserConversationCancelRequest{Reason: step.Cancel.Reason}
		}
		converted.Steps = append(converted.Steps, convertedStep)
	}
	return converted
}

func legacyBrowserScenarioError(err error) error {
	if err == nil {
		return nil
	}
	var source *runtimeBrowser.BrowserConversationScenarioError
	if errors.As(err, &source) {
		return &BrowserConversationScenarioError{Path: source.Path, Reason: source.Reason}
	}
	if errors.Is(err, runtimeBrowser.ErrInvalidBrowserConversationScenario) {
		return fmt.Errorf("%w: %w", ErrInvalidBrowserConversationScenario, err)
	}
	return err
}
