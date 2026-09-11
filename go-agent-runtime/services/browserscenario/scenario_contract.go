package browserscenario

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	// BrowserConversationScenarioVersion is the version of the typed browser
	// conversation contract. It is independent from event-stream versions.
	BrowserConversationScenarioVersion = "webmcp.conversational-scenario.v1"
	// BrowserConversationValidatorVersion identifies the rubric expected in a
	// browser conversation result.
	BrowserConversationValidatorVersion = "webmcp.conversational-validator.v1"
)

// ScheduledAudioInput is the transport-neutral audio scheduling value returned by scenario admission.
type ScheduledAudioInput struct {
	AfterCompletedTurns int
	PCM                 []byte
	SourceSampleRate    int
	EndOfTurn           bool
}

// BrowserConversationScenarioError carries the exact scenario location that
// prevented admission. Its message intentionally contains no credentials or
// page payloads.
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

func browserScenarioError(path, format string, args ...any) error {
	return &BrowserConversationScenarioError{Path: path, Reason: fmt.Sprintf(format, args...)}
}

// BrowserConversationScenario describes one bounded customer conversation.
// It contains intent and independent oracle expectations, never provider
// credentials, authorization data, tool references, or invocation IDs.
type BrowserConversationScenario struct {
	Version     string                              `json:"version"`
	ID          string                              `json:"id"`
	Name        string                              `json:"name"`
	Fixture     BrowserConversationFixture          `json:"fixture"`
	Steps       []BrowserConversationStep           `json:"steps"`
	RunTimeout  time.Duration                       `json:"-"`
	PostSession BrowserConversationTabStateRequired `json:"post_session"`
}

// BrowserScenario, WebMCPScenario, and WebMCPConversationScenario are
// descriptive aliases for callers that use a shorter product name.
type BrowserScenario = BrowserConversationScenario
type WebMCPScenario = BrowserConversationScenario
type WebMCPConversationScenario = BrowserConversationScenario

// BrowserConversationFixture identifies the declarative fixture pages that a
// scenario is allowed to address.
type BrowserConversationFixture struct {
	ID          string                    `json:"id"`
	Pages       []BrowserConversationPage `json:"pages"`
	InitialPage string                    `json:"initial_page"`
}

// BrowserConversationPage is one declarative page in the scenario scope.
type BrowserConversationPage struct {
	ID  string `json:"id"`
	URL string `json:"url,omitempty"`
}

// BrowserScenarioFixture and related aliases retain the shorter scenario vocabulary.
type BrowserScenarioFixture = BrowserConversationFixture
type BrowserScenarioPage = BrowserConversationPage
type BrowserScenarioStateTransition = BrowserStateTransition
type BrowserScenarioNavigation = BrowserCustomerNavigation
type BrowserScenarioCorrection = BrowserConversationCorrection
type BrowserScenarioInterrupt = BrowserConversationInterrupt
type BrowserScenarioCancelRequest = BrowserConversationCancelRequest
type BrowserScenarioTabStateRequired = BrowserConversationTabStateRequired

// BrowserConversationStep is one ordered customer utterance and its browser expectations.
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

// BrowserScenarioStep is a descriptive alias for BrowserConversationStep.
type BrowserScenarioStep = BrowserConversationStep

// BrowserStateTransition is the independent page-state assertion associated with a browser mutation.
type BrowserStateTransition struct {
	PageID string          `json:"page_id"`
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
}

// BrowserCustomerNavigation describes a customer-owned page switch.
type BrowserCustomerNavigation struct {
	FromPageID string `json:"from_page_id,omitempty"`
	ToPageID   string `json:"to_page_id"`
	URL        string `json:"url"`
}

// BrowserConversationCorrection describes a later intent that supersedes an earlier step.
type BrowserConversationCorrection struct {
	TargetStepID  string                 `json:"target_step_id"`
	ExpectedState BrowserStateTransition `json:"expected_state"`
}

// BrowserConversationInterruptTrigger identifies an event-driven interruption condition.
type BrowserConversationInterruptTrigger string

const (
	// BrowserInterruptOnInFlightInvocation waits for an observed invocation that has not reached a terminal state.
	BrowserInterruptOnInFlightInvocation BrowserConversationInterruptTrigger = "in_flight_invocation"
)

// BrowserConversationInterrupt requests overlapping customer audio after a semantic in-flight browser event.
type BrowserConversationInterrupt struct {
	Trigger  BrowserConversationInterruptTrigger `json:"trigger"`
	ToolName string                              `json:"tool_name,omitempty"`
}

// BrowserConversationCancelRequest is an explicit customer stop request.
type BrowserConversationCancelRequest struct {
	Reason string `json:"reason"`
}

// BrowserConversationTabStateRequired declares the post-session independent probe.
type BrowserConversationTabStateRequired struct {
	PageID            string `json:"page_id"`
	MustRemainAlive   bool   `json:"must_remain_alive"`
	MustBeResponsive  bool   `json:"must_be_responsive"`
	MustAllowMutation bool   `json:"must_allow_mutation"`
}

// BrowserConversationValidator is the validator-agent seam.
type BrowserConversationValidator interface {
	ValidateBrowserConversation(BrowserConversationResult) (BrowserConversationValidatorVerdict, error)
}

// BrowserConversationValidatorFunc adapts a function to the validator seam.
type BrowserConversationValidatorFunc func(BrowserConversationResult) (BrowserConversationValidatorVerdict, error)

// ValidateBrowserConversation implements BrowserConversationValidator.
func (f BrowserConversationValidatorFunc) ValidateBrowserConversation(result BrowserConversationResult) (BrowserConversationValidatorVerdict, error) {
	if f == nil {
		return BrowserConversationValidatorVerdict{}, errors.New("browser conversation validator function is nil")
	}
	return f(result)
}

// Admit validates and defensively copies a scenario before any host resource is touched.
func (s BrowserConversationScenario) Admit() (BrowserConversationScenario, error) {
	scenario := s
	if err := scenario.Validate(); err != nil {
		return BrowserConversationScenario{}, err
	}
	return cloneBrowserConversationScenario(scenario), nil
}

// ScheduleAudioInputs translates finite PCM payloads to the shared scheduler contract.
func (s BrowserConversationScenario) ScheduleAudioInputs(audioByStep map[string][]byte) ([]ScheduledAudioInput, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if audioByStep == nil {
		return nil, browserScenarioError("audio", "one PCM payload is required for every step")
	}
	inputs := make([]ScheduledAudioInput, len(s.Steps))
	for index, step := range s.Steps {
		pcm, ok := audioByStep[step.ID]
		if !ok || len(pcm) == 0 {
			return nil, browserScenarioError(fmt.Sprintf("steps[%d].audio", index), "one non-empty PCM payload is required")
		}
		inputs[index] = ScheduledAudioInput{AfterCompletedTurns: index, PCM: append([]byte(nil), pcm...), EndOfTurn: true}
	}
	return inputs, nil
}

// BrowserConversationScenarioForSession is the narrow extension seam consumed by a shared session runner.
type BrowserConversationScenarioForSession interface {
	BrowserConversationScenario() BrowserConversationScenario
}

// BrowserConversationScenarioValue adapts a validated scenario to the shared session extension seam.
type BrowserConversationScenarioValue struct {
	Scenario BrowserConversationScenario
}

// ScenarioValue validates and wraps a scenario for the shared session seam.
func (s BrowserConversationScenario) ScenarioValue() (BrowserConversationScenarioValue, error) {
	validated, err := s.Admit()
	if err != nil {
		return BrowserConversationScenarioValue{}, err
	}
	return BrowserConversationScenarioValue{Scenario: validated}, nil
}

// BrowserConversationScenario returns a defensive scenario copy.
func (v BrowserConversationScenarioValue) BrowserConversationScenario() BrowserConversationScenario {
	return cloneBrowserConversationScenario(v.Scenario)
}
