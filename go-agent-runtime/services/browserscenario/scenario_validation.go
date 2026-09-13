package browserscenario

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Validate checks every admission-time contract without invoking a hook or
// opening a resource. Errors name the exact invalid field or step.
func (s BrowserConversationScenario) Validate() error {
	if err := validateScenarioHeader(s); err != nil {
		return err
	}
	if err := s.Fixture.validate(); err != nil {
		return err
	}
	if err := validateScenarioSteps(s); err != nil {
		return err
	}
	return validatePostSessionTab(s.Fixture, s.PostSession)
}

func validateScenarioHeader(scenario BrowserConversationScenario) error {
	if scenario.Version != BrowserConversationScenarioVersion {
		return browserScenarioError("version", "must be %q", BrowserConversationScenarioVersion)
	}
	if err := validateScenarioIdentifier("id", scenario.ID); err != nil {
		return err
	}
	if err := validateScenarioIdentifier("name", scenario.Name); err != nil {
		return err
	}
	if err := validateScenarioText("name", scenario.Name); err != nil {
		return err
	}
	if scenario.RunTimeout <= 0 {
		return browserScenarioError("run_timeout", "must be positive")
	}
	return nil
}

func validateScenarioSteps(scenario BrowserConversationScenario) error {
	if len(scenario.Steps) == 0 {
		return browserScenarioError("steps", "must contain at least one step")
	}
	seenSteps := make(map[string]struct{}, len(scenario.Steps))
	stateTransitions := 0
	for index, step := range scenario.Steps {
		path := fmt.Sprintf("steps[%d]", index)
		if _, exists := seenSteps[step.ID]; exists {
			return browserScenarioError(path+".id", "duplicates step %q", step.ID)
		}
		seenSteps[step.ID] = struct{}{}
		transitions, err := validateScenarioStep(scenario, index, path, step)
		if err != nil {
			return err
		}
		stateTransitions += transitions
	}
	if stateTransitions == 0 {
		return browserScenarioError("steps", "at least one expected_state transition is required")
	}
	return nil
}

func validateScenarioStep(scenario BrowserConversationScenario, index int, path string, step BrowserConversationStep) (int, error) {
	if err := validateScenarioIdentifier(path+".id", step.ID); err != nil {
		return 0, err
	}
	if err := validateScenarioText(path+".utterance", step.Utterance); err != nil {
		return 0, err
	}
	if err := validateScenarioPageID(scenario.Fixture, path+".page_id", step.PageID); err != nil {
		return 0, err
	}
	if step.Deadline <= 0 {
		return 0, browserScenarioError(path+".deadline", "must be positive")
	}
	if step.Deadline > scenario.RunTimeout {
		return 0, browserScenarioError(path+".deadline", "must not exceed run_timeout")
	}
	transitions := 0
	if step.ExpectedState != nil {
		if err := validateStateTransition(scenario.Fixture, path+".expected_state", *step.ExpectedState); err != nil {
			return 0, err
		}
		transitions++
	}
	if step.ExpectedState != nil && step.Correction != nil {
		return 0, browserScenarioError(path+".correction", "must not be combined with expected_state")
	}
	if err := validateNavigation(scenario.Fixture, path+".navigation", step.Navigation); err != nil {
		return 0, err
	}
	if err := validateCorrection(scenario, index, path+".correction", step.Correction); err != nil {
		return 0, err
	}
	if step.Correction != nil {
		transitions++
	}
	if err := validateInterrupt(path+".interrupt", step.Interrupt); err != nil {
		return 0, err
	}
	if err := validateCancel(path+".cancel", step.Cancel); err != nil {
		return 0, err
	}
	return transitions, nil
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
	return validateScenarioPageID(f, "fixture.initial_page", f.InitialPage)
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
	rest, err := validateScenarioURLAuthority(path, raw)
	if err != nil {
		return err
	}
	return validateScenarioURLQuery(path, rest)
}

func validateScenarioURLAuthority(path, raw string) (string, error) {
	schemeEnd := strings.Index(raw, "://")
	if schemeEnd <= 0 || !validScenarioScheme(raw[:schemeEnd]) {
		return "", browserScenarioError(path, "must be an absolute URL")
	}
	rest := raw[schemeEnd+3:]
	authorityEnd := len(rest)
	if index := strings.IndexAny(rest, "/?#"); index >= 0 {
		authorityEnd = index
	}
	authority := rest[:authorityEnd]
	if authority == "" || strings.Contains(authority, "@") {
		return "", browserScenarioError(path, "must not contain URL user info")
	}
	if strings.HasPrefix(authority, ":") || strings.HasSuffix(authority, ":") {
		return "", browserScenarioError(path, "must be an absolute URL")
	}
	return rest, nil
}

func validateScenarioURLQuery(path, rest string) error {
	queryStart := strings.IndexByte(rest, '?')
	if queryStart < 0 {
		return nil
	}
	query := rest[queryStart+1:]
	if fragment := strings.IndexByte(query, '#'); fragment >= 0 {
		query = query[:fragment]
	}
	for _, item := range strings.Split(query, "&") {
		key := item
		if equals := strings.IndexByte(key, '='); equals >= 0 {
			key = key[:equals]
		}
		lower := strings.ToLower(key)
		if strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || strings.Contains(lower, "api_key") {
			return browserScenarioError(path, "must not contain credential query parameters")
		}
	}
	return nil
}

func validScenarioScheme(value string) bool {
	for index, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(index == 0 || (char < '0' || char > '9') && char != '+' && char != '-' && char != '.') {
			return false
		}
	}
	return value != ""
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
	targetStep := earlierCorrectionTarget(scenario, index, correction.TargetStepID)
	if targetStep == nil {
		return browserScenarioError(path+".target_step_id", "must reference an earlier step")
	}
	return validateCorrectionTarget(scenario, path, correction, targetStep)
}

func earlierCorrectionTarget(scenario BrowserConversationScenario, index int, targetID string) *BrowserConversationStep {
	for earlier := 0; earlier < index; earlier++ {
		if scenario.Steps[earlier].ID == targetID {
			return &scenario.Steps[earlier]
		}
	}
	return nil
}

func validateCorrectionTarget(scenario BrowserConversationScenario, path string, correction *BrowserConversationCorrection, targetStep *BrowserConversationStep) error {
	if err := validateStateTransition(scenario.Fixture, path+".expected_state", correction.ExpectedState); err != nil {
		return err
	}
	targetTransition := browserConversationExpectedState(targetStep)
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
		return browserScenarioError(path, "must contain exactly one JSON value")
	}
	if _, ok := value.(map[string]any); !ok {
		return browserScenarioError(path, "must be a JSON object")
	}
	return nil
}

// ValidateJSONObject applies the strict object check used by scenario and observation admission.
func (BrowserConversationScenario) ValidateJSONObject(path string, raw json.RawMessage) error {
	return validateJSONObject(path, raw)
}
