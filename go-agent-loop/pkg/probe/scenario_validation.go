package probe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
)

func (s Scenario) validate(lookup CorpusLookup) error {
	if strings.TrimSpace(s.ID) == "" && strings.TrimSpace(s.Name) == "" {
		return makeError(CategoryMissingField, "scenario.id", "required field is missing")
	}
	if len(s.Steps) == 0 {
		return makeError(CategoryEmpty, "steps", "scenario must contain at least one step")
	}
	expectations := s.expectedValues()
	if len(expectations) == 0 {
		return makeError(CategoryEmpty, "expectations", "at least one expected behavior is required")
	}
	timeline := scenarioTimeline{closeIndex: -1}
	for index, step := range s.Steps {
		if err := timeline.validateStep(step, index, lookup); err != nil {
			return err
		}
	}
	if timeline.closeIndex < 0 {
		return makeError(CategoryContradictory, "steps", "scenario must end with close")
	}
	if timeline.closeIndex != len(s.Steps)-1 {
		return makeError(CategoryContradictory, fmt.Sprintf("steps[%d]", timeline.closeIndex), "close must be the terminal step")
	}
	for index, expectation := range expectations {
		if err := validateScenarioExpectation(expectation, index, expectations, s.Steps, timeline); err != nil {
			return err
		}
	}
	return nil
}

// scenarioTimeline tracks the logical clock and the close position while
// steps are validated in declaration order.
type scenarioTimeline struct {
	logical    LogicalTime
	closeIndex int
}

// resolveStepKind returns the step variant after reconciling its type and
// kind aliases.
func resolveStepKind(step Step, index int) (StepKind, error) {
	kind := step.Kind
	if kind == "" {
		kind = step.Type
	}
	if step.Kind != "" && step.Type != "" && step.Kind != step.Type {
		return "", makeError(CategoryContradictory, fmt.Sprintf("steps[%d].type", index), "type and kind disagree")
	}
	if _, ok := stepKind(string(kind)); !ok {
		return "", makeError(CategoryUnknownVariant, fmt.Sprintf("steps[%d].type", index), "unknown step variant %q", kind)
	}
	return kind, nil
}

func (t *scenarioTimeline) validateStep(step Step, index int, lookup CorpusLookup) error {
	kind, err := resolveStepKind(step, index)
	if err != nil {
		return err
	}
	if err := validateStep(step, kind, index); err != nil {
		return err
	}
	switch kind {
	case StepSendAudio:
		return validateStepCorpus(step, index, lookup)
	case StepAdvanceTo:
		at := step.At
		if at == 0 {
			at = step.Time
		}
		if at <= t.logical {
			return makeError(CategoryContradictory, fmt.Sprintf("steps[%d].at", index), "logical time must progress beyond %d", t.logical)
		}
		t.logical = at
	case StepWait:
		if t.logical > LogicalTime(math.MaxInt64)-step.Duration {
			return makeError(CategoryInvalidField, fmt.Sprintf("steps[%d].duration", index), "logical time overflows")
		}
		t.logical += step.Duration
	case StepClose:
		if t.closeIndex >= 0 {
			return makeError(CategoryContradictory, fmt.Sprintf("steps[%d]", index), "scenario may contain only one close")
		}
		t.closeIndex = index
	case StepSendText, StepSendToolResult:
		// These steps do not advance the logical timeline.
	}
	return nil
}

// validateStepCorpus requires a lookup that knows the step's audio corpus.
// validateStep has already rejected malformed corpus aliases.
func validateStepCorpus(step Step, index int, lookup CorpusLookup) error {
	id, err := stepCorpusID(step, fmt.Sprintf("steps[%d]", index))
	if err != nil {
		return err
	}
	if lookup == nil {
		return makeError(CategoryInvalidField, fmt.Sprintf("steps[%d].corpus_id", index), "audio corpus lookup is required")
	}
	if !lookupCorpus(lookup, id) {
		e := makeError(CategoryUnknownCorpus, fmt.Sprintf("steps[%d].corpus_id", index), "unknown audio corpus ID %q", id)
		e.CorpusID, e.StepIndex = id, index
		return e
	}
	return nil
}

func validateScenarioExpectation(expectation ExpectedBehavior, index int, all []ExpectedBehavior, steps []Step, timeline scenarioTimeline) error {
	if expectation.Kind == "" {
		expectation.Kind = expectation.Type
	}
	if expectation.Type != "" && expectation.Kind != "" && expectation.Type != expectation.Kind {
		return makeError(CategoryContradictory, fmt.Sprintf("expectations[%d].type", index), "type and kind disagree")
	}
	if !expectation.HasAt && expectation.At == 0 && expectation.Time != 0 {
		expectation.At, expectation.HasAt = expectation.Time, true
	}
	location := fmt.Sprintf("expectations[%d]", index)
	if err := validateExpectationFields(expectation, location); err != nil {
		return err
	}
	return satisfiable(expectation, index, all, steps, timeline.closeIndex, timeline.logical)
}

func stepCorpusID(step Step, location string) (string, error) {
	id := ""
	for _, candidate := range []string{step.CorpusID, step.Corpus.CorpusID, step.Corpus.ID} {
		if candidate == "" {
			continue
		}
		if strings.TrimSpace(candidate) == "" {
			return "", makeError(CategoryInvalidField, location+".corpus_id", "must not be empty")
		}
		if id != "" && id != candidate {
			return "", makeError(CategoryInvalidField, location+".corpus_id", "corpus ID aliases disagree")
		}
		id = candidate
	}
	return id, nil
}

func stepHasCorpus(step Step) bool {
	return step.CorpusID != "" || step.Corpus.CorpusID != "" || step.Corpus.ID != ""
}

func stepResult(step Step, location string) (json.RawMessage, error) {
	if len(step.ToolResult) != 0 && len(step.Result) != 0 && !bytes.Equal(step.ToolResult, step.Result) {
		return nil, makeError(CategoryInvalidField, location+".result", "result aliases disagree")
	}
	if len(step.ToolResult) != 0 {
		return step.ToolResult, nil
	}
	return step.Result, nil
}

func stepLogicalTime(step Step, location string) (LogicalTime, error) {
	if step.At != 0 && step.Time != 0 && step.At != step.Time {
		return 0, makeError(CategoryInvalidField, location+".at", "at and time aliases disagree")
	}
	if step.At != 0 {
		return step.At, nil
	}
	return step.Time, nil
}

type stepPayloadField struct {
	name      string
	populated bool
}

// stepPayloadFields lists every variant payload field in the order in which
// unexpected fields are reported.
func stepPayloadFields(step Step) []stepPayloadField {
	return []stepPayloadField{
		{"text", step.Text != ""},
		{"corpus_id", stepHasCorpus(step)},
		{"tool_call_id", step.ToolCallID != ""},
		{"tool_name", step.ToolName != ""},
		{"result", len(step.ToolResult) != 0 || len(step.Result) != 0},
		{"at", step.At != 0 || step.Time != 0},
		{"duration", step.Duration != 0},
	}
}

// rejectStepFields reports the first populated payload field that the step
// variant does not own.
func rejectStepFields(step Step, kind StepKind, location string, allowed ...string) error {
	for _, field := range stepPayloadFields(step) {
		if field.populated && !slices.Contains(allowed, field.name) {
			return unexpectedStepField(location, field.name, kind)
		}
	}
	return nil
}

func validateStep(step Step, kind StepKind, index int) error {
	location := fmt.Sprintf("steps[%d]", index)
	var allowed []string
	switch kind {
	case StepSendText:
		if strings.TrimSpace(step.Text) == "" {
			return makeError(CategoryMissingField, location+".text", "required field is missing")
		}
		allowed = []string{"text"}
	case StepSendAudio:
		if err := validateSendAudioStep(step, location); err != nil {
			return err
		}
		allowed = []string{"text", "corpus_id"}
	case StepSendToolResult:
		if err := validateSendToolResultStep(step, location); err != nil {
			return err
		}
		allowed = []string{"tool_call_id", "tool_name", "result"}
	case StepAdvanceTo:
		if err := validateAdvanceToStep(step, location); err != nil {
			return err
		}
		allowed = []string{"at"}
	case StepWait:
		if step.Duration <= 0 {
			return makeError(CategoryInvalidField, location+".duration", "must be positive")
		}
		allowed = []string{"duration"}
	case StepClose:
		// A close step owns no payload fields.
	default:
		return nil
	}
	return rejectStepFields(step, kind, location, allowed...)
}

func validateSendAudioStep(step Step, location string) error {
	id, err := stepCorpusID(step, location)
	if err != nil {
		return err
	}
	if strings.TrimSpace(id) == "" {
		return makeError(CategoryMissingField, location+".corpus_id", "required field is missing")
	}
	return nil
}

func validateSendToolResultStep(step Step, location string) error {
	if strings.TrimSpace(step.ToolCallID) == "" {
		return makeError(CategoryMissingField, location+".tool_call_id", "required field is missing")
	}
	result, err := stepResult(step, location)
	if err != nil {
		return err
	}
	if len(result) == 0 {
		return makeError(CategoryMissingField, location+".result", "required field is missing")
	}
	return nil
}

func validateAdvanceToStep(step Step, location string) error {
	at, err := stepLogicalTime(step, location)
	if err != nil {
		return err
	}
	if at <= 0 {
		return makeError(CategoryInvalidField, location+".at", "must be positive")
	}
	return nil
}

func unexpectedStepField(location string, field string, kind StepKind) error {
	return makeError(CategoryInvalidField, location+"."+field, "unexpected payload for %s", kind)
}
func satisfiable(value ExpectedBehavior, index int, all []ExpectedBehavior, steps []Step, closeIndex int, finalTime LogicalTime) error {
	location := fmt.Sprintf("expectations[%d]", index)
	if err := validateExpectationStepReferences(value, index, all, len(steps), closeIndex); err != nil {
		return err
	}
	switch value.Kind {
	case ExpectToolResult:
		if declaresToolResult(steps, value.ToolCallID) {
			return nil
		}
		return makeError(CategoryUnsatisfiable, location+".tool_call_id", "no declared tool result can satisfy this expectation")
	case ExpectAudio:
		if declaresAudioCorpus(steps, value.CorpusID) {
			return nil
		}
		return makeError(CategoryUnsatisfiable, location+".corpus_id", "no declared audio input uses corpus ID %q", value.CorpusID)
	case ExpectClose:
		if index != len(all)-1 {
			return makeError(CategoryContradictory, location, "close expectation must be last")
		}
	case ExpectTime:
		if value.At > finalTime {
			return makeError(CategoryUnsatisfiable, location+".at", "logical time %d is unreachable; scenario ends at %d", value.At, finalTime)
		}
	}
	return nil
}

// validateExpectationStepReferences checks step, after, and before anchors
// against the declared steps and the preceding expectation's order.
func validateExpectationStepReferences(value ExpectedBehavior, index int, all []ExpectedBehavior, stepCount, closeIndex int) error {
	location := fmt.Sprintf("expectations[%d]", index)
	if err := validateExpectationStepIndex(value, location, stepCount, closeIndex); err != nil {
		return err
	}
	if value.HasAfter && (value.AfterStep >= stepCount || value.AfterStep >= closeIndex) {
		return makeError(CategoryUnsatisfiable, location+".after", "after step %d cannot occur before terminal close", value.AfterStep)
	}
	if value.HasBefore && (value.BeforeStep >= stepCount || value.BeforeStep > closeIndex) {
		return makeError(CategoryUnsatisfiable, location+".before", "before step %d is outside the scenario", value.BeforeStep)
	}
	if index > 0 && value.HasStep && all[index-1].HasStep && value.StepIndex < all[index-1].StepIndex {
		return makeError(CategoryContradictory, location+".step", "expectations must preserve step order")
	}
	return nil
}

func validateExpectationStepIndex(value ExpectedBehavior, location string, stepCount, closeIndex int) error {
	if !value.HasStep {
		return nil
	}
	if value.StepIndex < 0 || value.StepIndex >= stepCount {
		return makeError(CategoryUnsatisfiable, location+".step", "referenced step %d does not exist", value.StepIndex)
	}
	if value.StepIndex >= closeIndex && (value.Kind != ExpectClose || value.StepIndex != closeIndex) {
		return makeError(CategoryUnsatisfiable, location+".step", "expectation refers to a terminal step incompatibly")
	}
	return nil
}

func declaredStepKind(step Step) StepKind {
	if step.Kind == "" {
		return step.Type
	}
	return step.Kind
}

func declaresToolResult(steps []Step, toolCallID string) bool {
	for _, step := range steps {
		if declaredStepKind(step) == StepSendToolResult && step.ToolCallID == toolCallID {
			return true
		}
	}
	return false
}

func declaresAudioCorpus(steps []Step, corpusID string) bool {
	for _, step := range steps {
		id := step.CorpusID
		if id == "" {
			id = step.Corpus.CorpusID
		}
		if declaredStepKind(step) == StepSendAudio && id == corpusID {
			return true
		}
	}
	return false
}

func lookupCorpus(lookup CorpusLookup, id string) bool {
	return lookup != nil && lookup.Has(id)
}
