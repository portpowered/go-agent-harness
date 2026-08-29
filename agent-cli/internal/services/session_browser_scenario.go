package services

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

const (
	// BrowserConversationScenarioVersion is the version of the typed browser
	// conversation contract. It is independent from the browser event stream
	// and fixture-script versions.
	BrowserConversationScenarioVersion = "webmcp.conversational-scenario.v1"
	// BrowserConversationValidatorVersion identifies the rubric expected in a
	// browser conversation result. The validator implementation is supplied by
	// the caller in a later orchestration layer.
	BrowserConversationValidatorVersion = "webmcp.conversational-validator.v1"
)

var (
	// ErrInvalidBrowserConversationScenario identifies a scenario that cannot
	// be admitted to any browser, provider, process, or audio boundary.
	ErrInvalidBrowserConversationScenario = errors.New("invalid WebMCP browser conversation scenario")
	// ErrBrowserConversationRunFinalized identifies an observation sent after
	// the run has published its immutable result.
	ErrBrowserConversationRunFinalized = errors.New("WebMCP browser conversation run is finalized")
	// ErrBrowserConversationDuplicateObservation identifies a lifecycle or
	// validator fact that was supplied more than once.
	ErrBrowserConversationDuplicateObservation = errors.New("duplicate WebMCP browser conversation observation")
	// ErrInvalidBrowserConversationResult identifies malformed joined evidence.
	ErrInvalidBrowserConversationResult = errors.New("invalid WebMCP browser conversation result")
)

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
// scenario is allowed to address. The fixture server and browser remain
// orchestration concerns; this scope is only a typed allow-list.
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

// BrowserScenarioFixture, BrowserScenarioPage, and the following aliases
// retain the contract's discoverable names for callers that prefer the
// shorter scenario vocabulary.
type BrowserScenarioFixture = BrowserConversationFixture
type BrowserScenarioPage = BrowserConversationPage
type BrowserScenarioStateTransition = BrowserStateTransition
type BrowserScenarioNavigation = BrowserCustomerNavigation
type BrowserScenarioCorrection = BrowserConversationCorrection
type BrowserScenarioInterrupt = BrowserConversationInterrupt
type BrowserScenarioCancelRequest = BrowserConversationCancelRequest
type BrowserScenarioTabStateRequired = BrowserConversationTabStateRequired

// BrowserConversationStep is one ordered customer utterance and its browser
// expectations. A step may additionally describe customer navigation,
// correction, interruption, or explicit cancellation.
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

// BrowserStateTransition is the independent page-state assertion associated
// with a browser mutation. Before and After remain raw JSON so page-owned
// values, including large integers, are not converted through float64.
type BrowserStateTransition struct {
	PageID string          `json:"page_id"`
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
}

// BrowserCustomerNavigation describes a customer-owned page switch. The
// runner must observe this event before using any capability from the new
// page; no tool reference is accepted in this declaration.
type BrowserCustomerNavigation struct {
	FromPageID string `json:"from_page_id,omitempty"`
	ToPageID   string `json:"to_page_id"`
	URL        string `json:"url"`
}

// BrowserConversationCorrection describes a later intent that supersedes an
// earlier step and has its own independent expected state transition.
type BrowserConversationCorrection struct {
	TargetStepID  string                 `json:"target_step_id"`
	ExpectedState BrowserStateTransition `json:"expected_state"`
}

// BrowserConversationInterruptTrigger identifies an event-driven interruption
// condition. Fixed sleeps are deliberately not part of this contract.
type BrowserConversationInterruptTrigger string

const (
	// BrowserInterruptOnInFlightInvocation waits for an observed invocation
	// that has not reached a terminal state.
	BrowserInterruptOnInFlightInvocation BrowserConversationInterruptTrigger = "in_flight_invocation"
)

// BrowserConversationInterrupt requests overlapping customer audio after a
// semantic in-flight browser event has been observed.
type BrowserConversationInterrupt struct {
	Trigger  BrowserConversationInterruptTrigger `json:"trigger"`
	ToolName string                              `json:"tool_name,omitempty"`
}

// BrowserConversationCancelRequest is an explicit customer stop request.
// The step utterance is the spoken request; Reason is bounded evidence text.
type BrowserConversationCancelRequest struct {
	Reason string `json:"reason"`
}

// BrowserConversationTabStateRequired declares the post-session independent
// probe expected for the externally owned fixture tab.
type BrowserConversationTabStateRequired struct {
	PageID            string `json:"page_id"`
	MustRemainAlive   bool   `json:"must_remain_alive"`
	MustBeResponsive  bool   `json:"must_be_responsive"`
	MustAllowMutation bool   `json:"must_allow_mutation"`
}

// BrowserConversationValidator is the validator-agent seam. It receives the
// finalized, sanitized result and returns a structured verdict; it cannot
// mutate the run after finalization.
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

// NewBrowserConversationScenario validates and defensively copies a scenario.
// It performs no external work, making it safe to call before constructing a
// fixture, dialing a provider, launching a subprocess, or opening audio.
func NewBrowserConversationScenario(scenario BrowserConversationScenario) (BrowserConversationScenario, error) {
	if err := scenario.Validate(); err != nil {
		return BrowserConversationScenario{}, err
	}
	return cloneBrowserConversationScenario(scenario), nil
}

// NewBrowserScenario is a descriptive constructor alias.
func NewBrowserScenario(scenario BrowserScenario) (BrowserScenario, error) {
	return NewBrowserConversationScenario(scenario)
}

// NewWebMCPConversationScenario is a descriptive constructor alias.
func NewWebMCPConversationScenario(scenario WebMCPConversationScenario) (WebMCPConversationScenario, error) {
	return NewBrowserConversationScenario(scenario)
}

// Validate checks every admission-time contract without invoking a hook or
// opening a resource. Errors name the exact invalid field or step.
func (s BrowserConversationScenario) Validate() error {
	if s.Version != BrowserConversationScenarioVersion {
		return browserScenarioError("version", "must be %q", BrowserConversationScenarioVersion)
	}
	if err := validateScenarioIdentifier("id", s.ID); err != nil {
		return err
	}
	if err := validateScenarioIdentifier("name", s.Name); err != nil {
		return err
	}
	if err := validateScenarioText("name", s.Name); err != nil {
		return err
	}
	if s.RunTimeout <= 0 {
		return browserScenarioError("run_timeout", "must be positive")
	}
	if err := s.Fixture.validate(); err != nil {
		return err
	}
	if len(s.Steps) == 0 {
		return browserScenarioError("steps", "must contain at least one step")
	}
	seenSteps := make(map[string]struct{}, len(s.Steps))
	stateTransitions := 0
	for index, step := range s.Steps {
		path := fmt.Sprintf("steps[%d]", index)
		if err := validateScenarioIdentifier(path+".id", step.ID); err != nil {
			return err
		}
		if _, exists := seenSteps[step.ID]; exists {
			return browserScenarioError(path+".id", "duplicates step %q", step.ID)
		}
		seenSteps[step.ID] = struct{}{}
		if err := validateScenarioText(path+".utterance", step.Utterance); err != nil {
			return err
		}
		if err := validateScenarioPageID(s.Fixture, path+".page_id", step.PageID); err != nil {
			return err
		}
		if step.Deadline <= 0 {
			return browserScenarioError(path+".deadline", "must be positive")
		}
		if step.Deadline > s.RunTimeout {
			return browserScenarioError(path+".deadline", "must not exceed run_timeout")
		}
		if step.ExpectedState != nil {
			stateTransitions++
			if err := validateStateTransition(s.Fixture, path+".expected_state", *step.ExpectedState); err != nil {
				return err
			}
		}
		if step.ExpectedState != nil && step.Correction != nil {
			return browserScenarioError(path+".correction", "must not be combined with expected_state")
		}
		if err := validateNavigation(s.Fixture, path+".navigation", step.Navigation); err != nil {
			return err
		}
		if err := validateCorrection(s, index, path+".correction", step.Correction); err != nil {
			return err
		}
		if step.Correction != nil {
			stateTransitions++
		}
		if err := validateInterrupt(path+".interrupt", step.Interrupt); err != nil {
			return err
		}
		if err := validateCancel(path+".cancel", step.Cancel); err != nil {
			return err
		}
	}
	if stateTransitions == 0 {
		return browserScenarioError("steps", "at least one expected_state transition is required")
	}
	return validatePostSessionTab(s.Fixture, s.PostSession)
}

// ScheduleAudioInputs translates one finite PCM payload per customer step to
// the existing session scheduler contract. It does not start a session or
// copy the duplex loop; the normal SessionAudioInput/ScheduledAudioInput path
// remains responsible for delivery and turn-boundary signaling.
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
		inputs[index] = ScheduledAudioInput{
			AfterCompletedTurns: index,
			PCM:                 append([]byte(nil), pcm...),
			EndOfTurn:           true,
		}
	}
	return inputs, nil
}

func (f BrowserConversationFixture) validate() error {
	if err := validateScenarioIdentifier("fixture.id", f.ID); err != nil {
		return err
	}
	if len(f.Pages) == 0 {
		return browserScenarioError("fixture.pages", "must contain at least one page")
	}
	seen := make(map[string]struct{}, len(f.Pages))
	for index, page := range f.Pages {
		path := fmt.Sprintf("fixture.pages[%d]", index)
		if err := validateScenarioIdentifier(path+".id", page.ID); err != nil {
			return err
		}
		if _, exists := seen[page.ID]; exists {
			return browserScenarioError(path+".id", "duplicates page %q", page.ID)
		}
		seen[page.ID] = struct{}{}
		if err := validateScenarioURL(path+".url", page.URL); err != nil {
			return err
		}
	}
	if err := validateScenarioPageID(f, "fixture.initial_page", f.InitialPage); err != nil {
		return err
	}
	return nil
}

func validateScenarioIdentifier(path, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return browserScenarioError(path, "is required")
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return browserScenarioError(path, "contains control characters")
		}
	}
	return validateScenarioText(path, value)
}

func validateScenarioText(path, value string) error {
	if strings.TrimSpace(value) == "" {
		return browserScenarioError(path, "must not be empty")
	}
	if scenarioContainsCredential(value) {
		return browserScenarioError(path, "contains credential-like data")
	}
	return nil
}

func scenarioContainsCredential(value string) bool {
	lower := strings.ToLower(value)
	markers := []string{
		"authorization:", "bearer ", "api_key=", "api-key=", "access_token=", "refresh_token=",
		"client_secret=", "-----begin ", "sk-",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func validateScenarioURL(path, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	if err := validateScenarioText(path, raw); err != nil {
		return err
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return browserScenarioError(path, "must be an absolute URL")
	}
	if parsed.User != nil {
		return browserScenarioError(path, "must not contain URL user info")
	}
	for key := range parsed.Query() {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || strings.Contains(lower, "api_key") {
			return browserScenarioError(path, "must not contain credential query parameters")
		}
	}
	return nil
}

func validateScenarioPageID(fixture BrowserConversationFixture, path, pageID string) error {
	if strings.TrimSpace(pageID) == "" {
		return browserScenarioError(path, "is required")
	}
	for _, page := range fixture.Pages {
		if page.ID == pageID {
			return nil
		}
	}
	return browserScenarioError(path, "references unknown fixture page %q", pageID)
}

func validateStateTransition(fixture BrowserConversationFixture, path string, transition BrowserStateTransition) error {
	if err := validateScenarioPageID(fixture, path+".page_id", transition.PageID); err != nil {
		return err
	}
	if err := validateJSONObject(path+".before", transition.Before); err != nil {
		return err
	}
	return validateJSONObject(path+".after", transition.After)
}

func validateNavigation(fixture BrowserConversationFixture, path string, navigation *BrowserCustomerNavigation) error {
	if navigation == nil {
		return nil
	}
	if err := validateScenarioPageID(fixture, path+".to_page_id", navigation.ToPageID); err != nil {
		return err
	}
	if navigation.FromPageID != "" {
		if err := validateScenarioPageID(fixture, path+".from_page_id", navigation.FromPageID); err != nil {
			return err
		}
	}
	if err := validateScenarioURL(path+".url", navigation.URL); err != nil {
		return err
	}
	if strings.TrimSpace(navigation.URL) == "" {
		return browserScenarioError(path+".url", "is required for customer-owned navigation")
	}
	return nil
}

func validateCorrection(scenario BrowserConversationScenario, index int, path string, correction *BrowserConversationCorrection) error {
	if correction == nil {
		return nil
	}
	if strings.TrimSpace(correction.TargetStepID) == "" {
		return browserScenarioError(path+".target_step_id", "is required")
	}
	for earlier := 0; earlier < index; earlier++ {
		if scenario.Steps[earlier].ID == correction.TargetStepID {
			if err := validateStateTransition(scenario.Fixture, path+".expected_state", correction.ExpectedState); err != nil {
				return err
			}
			targetTransition := browserConversationExpectedState(&scenario.Steps[earlier])
			if targetTransition == nil {
				return browserScenarioError(path+".target_step_id", "must reference an earlier step with an expected_state transition")
			}
			if targetTransition.PageID != correction.ExpectedState.PageID {
				return browserScenarioError(path+".expected_state.page_id", "must match target step %q page %q", correction.TargetStepID, targetTransition.PageID)
			}
			if browserConversationJSONEqual(targetTransition.Before, targetTransition.After) {
				return browserScenarioError(path+".target_step_id", "must reference a state-changing transition")
			}
			if !browserConversationJSONEqual(targetTransition.After, correction.ExpectedState.Before) {
				return browserScenarioError(path+".expected_state.before", "must match target step %q after state", correction.TargetStepID)
			}
			if browserConversationJSONEqual(correction.ExpectedState.Before, correction.ExpectedState.After) {
				return browserScenarioError(path+".expected_state", "must describe a state-changing correction")
			}
			return nil
		}
	}
	return browserScenarioError(path+".target_step_id", "must reference an earlier step")
}

func validateInterrupt(path string, interrupt *BrowserConversationInterrupt) error {
	if interrupt == nil {
		return nil
	}
	if interrupt.Trigger != BrowserInterruptOnInFlightInvocation {
		return browserScenarioError(path+".trigger", "must be %q", BrowserInterruptOnInFlightInvocation)
	}
	if interrupt.ToolName != "" {
		return validateScenarioText(path+".tool_name", interrupt.ToolName)
	}
	return nil
}

func validateCancel(path string, cancel *BrowserConversationCancelRequest) error {
	if cancel == nil {
		return nil
	}
	return validateScenarioText(path+".reason", cancel.Reason)
}

func validatePostSessionTab(fixture BrowserConversationFixture, required BrowserConversationTabStateRequired) error {
	if err := validateScenarioPageID(fixture, "post_session.page_id", required.PageID); err != nil {
		return err
	}
	if !required.MustRemainAlive {
		return browserScenarioError("post_session.must_remain_alive", "must be true for an externally owned tab")
	}
	if !required.MustBeResponsive {
		return browserScenarioError("post_session.must_be_responsive", "must be true")
	}
	if !required.MustAllowMutation {
		return browserScenarioError("post_session.must_allow_mutation", "must be true for the independent tab probe")
	}
	return nil
}

func validateJSONObject(path string, raw json.RawMessage) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return browserScenarioError(path, "must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return browserScenarioError(path, "must be valid JSON: %v", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return browserScenarioError(path, "must contain exactly one JSON value")
		}
		return browserScenarioError(path, "must contain exactly one JSON value")
	}
	if _, ok := value.(map[string]any); !ok {
		return browserScenarioError(path, "must be a JSON object")
	}
	return nil
}

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
// the scenario contract's fields. It rejects invalid scenarios before any
// artifact can contain a partially admitted declaration.
func (s BrowserConversationScenario) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	steps := make([]browserConversationStepJSON, len(s.Steps))
	for index, step := range s.Steps {
		steps[index] = browserConversationStepJSON{
			ID:            step.ID,
			Utterance:     step.Utterance,
			PageID:        step.PageID,
			ExpectedState: cloneStateTransitionPointer(step.ExpectedState),
			Navigation:    cloneNavigationPointer(step.Navigation),
			Correction:    cloneCorrectionPointer(step.Correction),
			Interrupt:     cloneInterruptPointer(step.Interrupt),
			Cancel:        cloneCancelPointer(step.Cancel),
			Deadline:      step.Deadline.String(),
		}
	}
	return json.Marshal(browserConversationScenarioJSON{
		Version: s.Version, ID: s.ID, Name: s.Name, Fixture: s.Fixture, Steps: steps,
		RunTimeout: s.RunTimeout.String(), PostSession: s.PostSession,
	})
}

// UnmarshalJSON accepts only the versioned scenario fields and readable
// duration strings. Unknown fields, including credential-shaped fields, are
// rejected before the scenario can be handed to a runner.
func (s *BrowserConversationScenario) UnmarshalJSON(data []byte) error {
	if s == nil {
		return errors.New("cannot unmarshal browser conversation scenario into nil receiver")
	}
	var wire browserConversationScenarioJSON
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return browserScenarioError("scenario", "invalid JSON: %v", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return browserScenarioError("scenario", "must contain exactly one JSON object")
	}
	runTimeout, err := parseScenarioDuration("run_timeout", wire.RunTimeout)
	if err != nil {
		return err
	}
	steps := make([]BrowserConversationStep, len(wire.Steps))
	for index, step := range wire.Steps {
		deadline, parseErr := parseScenarioDuration(fmt.Sprintf("steps[%d].deadline", index), step.Deadline)
		if parseErr != nil {
			return parseErr
		}
		steps[index] = BrowserConversationStep{
			ID: step.ID, Utterance: step.Utterance, PageID: step.PageID,
			ExpectedState: cloneStateTransitionPointer(step.ExpectedState),
			Navigation:    cloneNavigationPointer(step.Navigation),
			Correction:    cloneCorrectionPointer(step.Correction),
			Interrupt:     cloneInterruptPointer(step.Interrupt),
			Cancel:        cloneCancelPointer(step.Cancel), Deadline: deadline,
		}
	}
	parsed := BrowserConversationScenario{
		Version: wire.Version, ID: wire.ID, Name: wire.Name, Fixture: wire.Fixture,
		Steps: steps, RunTimeout: runTimeout, PostSession: wire.PostSession,
	}
	if err := parsed.Validate(); err != nil {
		return err
	}
	*s = cloneBrowserConversationScenario(parsed)
	return nil
}

func parseScenarioDuration(path, raw string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(raw)
	if err != nil {
		return 0, browserScenarioError(path, "must be a duration string: %v", err)
	}
	return duration, nil
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

// BrowserConversationScenarioForSession is the narrow extension seam consumed
// by a shared session runner. It intentionally exposes scheduled utterances
// as intent only; audio conversion and scheduling remain owned by the normal
// SessionAudioInput/ScheduledAudioInput path.
type BrowserConversationScenarioForSession interface {
	BrowserConversationScenario() BrowserConversationScenario
}

// BrowserConversationScenarioValue adapts a validated scenario to the shared
// session extension seam without adding a browser-specific audio loop.
type BrowserConversationScenarioValue struct {
	scenario BrowserConversationScenario
}

// NewBrowserConversationScenarioValue validates and wraps a scenario.
func NewBrowserConversationScenarioValue(scenario BrowserConversationScenario) (BrowserConversationScenarioValue, error) {
	validated, err := NewBrowserConversationScenario(scenario)
	if err != nil {
		return BrowserConversationScenarioValue{}, err
	}
	return BrowserConversationScenarioValue{scenario: validated}, nil
}

// BrowserConversationScenario returns a defensive scenario copy.
func (v BrowserConversationScenarioValue) BrowserConversationScenario() BrowserConversationScenario {
	return cloneBrowserConversationScenario(v.scenario)
}
