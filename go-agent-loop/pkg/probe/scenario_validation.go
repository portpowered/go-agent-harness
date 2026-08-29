package probe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
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
	logical, closeIndex := LogicalTime(0), -1
	for index, step := range s.Steps {
		kind := step.Kind
		if kind == "" {
			kind = step.Type
		}
		if step.Kind != "" && step.Type != "" && step.Kind != step.Type {
			return makeError(CategoryContradictory, fmt.Sprintf("steps[%d].type", index), "type and kind disagree")
		}
		if _, ok := stepKind(string(kind)); !ok {
			return makeError(CategoryUnknownVariant, fmt.Sprintf("steps[%d].type", index), "unknown step variant %q", kind)
		}
		if err := validateStep(step, kind, index); err != nil {
			return err
		}
		switch kind {
		case StepSendAudio:
			id, _ := stepCorpusID(step, fmt.Sprintf("steps[%d]", index))
			if lookup == nil {
				return makeError(CategoryInvalidField, fmt.Sprintf("steps[%d].corpus_id", index), "audio corpus lookup is required")
			}
			if !lookupCorpus(lookup, id) {
				e := makeError(CategoryUnknownCorpus, fmt.Sprintf("steps[%d].corpus_id", index), "unknown audio corpus ID %q", id)
				e.CorpusID, e.StepIndex = id, index
				return e
			}
		case StepAdvanceTo:
			at := step.At
			if at == 0 {
				at = step.Time
			}
			if at <= logical {
				return makeError(CategoryContradictory, fmt.Sprintf("steps[%d].at", index), "logical time must progress beyond %d", logical)
			}
			logical = at
		case StepWait:
			if logical > LogicalTime(math.MaxInt64)-step.Duration {
				return makeError(CategoryInvalidField, fmt.Sprintf("steps[%d].duration", index), "logical time overflows")
			}
			logical += step.Duration
		case StepClose:
			if closeIndex >= 0 {
				return makeError(CategoryContradictory, fmt.Sprintf("steps[%d]", index), "scenario may contain only one close")
			}
			closeIndex = index
		}
	}
	if closeIndex < 0 {
		return makeError(CategoryContradictory, "steps", "scenario must end with close")
	}
	if closeIndex != len(s.Steps)-1 {
		return makeError(CategoryContradictory, fmt.Sprintf("steps[%d]", closeIndex), "close must be the terminal step")
	}
	for index, expectation := range expectations {
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
		if err := satisfiable(expectation, index, expectations, s.Steps, closeIndex, logical); err != nil {
			return err
		}
	}
	return nil
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

func rejectStepFields(location string, kind StepKind, fields ...struct {
	name      string
	populated bool
}) error {
	for _, field := range fields {
		if field.populated {
			return unexpectedStepField(location, field.name, kind)
		}
	}
	return nil
}

func validateStep(step Step, kind StepKind, index int) error {
	location := fmt.Sprintf("steps[%d]", index)
	switch kind {
	case StepSendText:
		if strings.TrimSpace(step.Text) == "" {
			return makeError(CategoryMissingField, location+".text", "required field is missing")
		}
		if err := rejectStepFields(location, kind,
			struct {
				name      string
				populated bool
			}{"corpus_id", stepHasCorpus(step)},
			struct {
				name      string
				populated bool
			}{"tool_call_id", step.ToolCallID != ""},
			struct {
				name      string
				populated bool
			}{"tool_name", step.ToolName != ""},
			struct {
				name      string
				populated bool
			}{"result", len(step.ToolResult) != 0 || len(step.Result) != 0},
			struct {
				name      string
				populated bool
			}{"at", step.At != 0 || step.Time != 0},
			struct {
				name      string
				populated bool
			}{"duration", step.Duration != 0},
		); err != nil {
			return err
		}
	case StepSendAudio:
		id, err := stepCorpusID(step, location)
		if err != nil {
			return err
		}
		if strings.TrimSpace(id) == "" {
			return makeError(CategoryMissingField, location+".corpus_id", "required field is missing")
		}
		if err := rejectStepFields(location, kind,
			struct {
				name      string
				populated bool
			}{"tool_call_id", step.ToolCallID != ""},
			struct {
				name      string
				populated bool
			}{"tool_name", step.ToolName != ""},
			struct {
				name      string
				populated bool
			}{"result", len(step.ToolResult) != 0 || len(step.Result) != 0},
			struct {
				name      string
				populated bool
			}{"at", step.At != 0 || step.Time != 0},
			struct {
				name      string
				populated bool
			}{"duration", step.Duration != 0},
		); err != nil {
			return err
		}
	case StepSendToolResult:
		if strings.TrimSpace(step.ToolCallID) == "" {
			return makeError(CategoryMissingField, location+".tool_call_id", "required field is missing")
		}
		if lenResult, err := stepResult(step, location); err != nil {
			return err
		} else if len(lenResult) == 0 {
			return makeError(CategoryMissingField, location+".result", "required field is missing")
		}
		if err := rejectStepFields(location, kind,
			struct {
				name      string
				populated bool
			}{"text", step.Text != ""},
			struct {
				name      string
				populated bool
			}{"corpus_id", stepHasCorpus(step)},
			struct {
				name      string
				populated bool
			}{"at", step.At != 0 || step.Time != 0},
			struct {
				name      string
				populated bool
			}{"duration", step.Duration != 0},
		); err != nil {
			return err
		}
	case StepAdvanceTo:
		at, err := stepLogicalTime(step, location)
		if err != nil {
			return err
		}
		if at <= 0 {
			return makeError(CategoryInvalidField, location+".at", "must be positive")
		}
		if err := rejectStepFields(location, kind,
			struct {
				name      string
				populated bool
			}{"text", step.Text != ""},
			struct {
				name      string
				populated bool
			}{"corpus_id", stepHasCorpus(step)},
			struct {
				name      string
				populated bool
			}{"tool_call_id", step.ToolCallID != ""},
			struct {
				name      string
				populated bool
			}{"tool_name", step.ToolName != ""},
			struct {
				name      string
				populated bool
			}{"result", len(step.ToolResult) != 0 || len(step.Result) != 0},
			struct {
				name      string
				populated bool
			}{"duration", step.Duration != 0},
		); err != nil {
			return err
		}
	case StepWait:
		if step.Duration <= 0 {
			return makeError(CategoryInvalidField, location+".duration", "must be positive")
		}
		if err := rejectStepFields(location, kind,
			struct {
				name      string
				populated bool
			}{"text", step.Text != ""},
			struct {
				name      string
				populated bool
			}{"corpus_id", stepHasCorpus(step)},
			struct {
				name      string
				populated bool
			}{"tool_call_id", step.ToolCallID != ""},
			struct {
				name      string
				populated bool
			}{"tool_name", step.ToolName != ""},
			struct {
				name      string
				populated bool
			}{"result", len(step.ToolResult) != 0 || len(step.Result) != 0},
			struct {
				name      string
				populated bool
			}{"at", step.At != 0 || step.Time != 0},
		); err != nil {
			return err
		}
	case StepClose:
		if err := rejectStepFields(location, kind,
			struct {
				name      string
				populated bool
			}{"text", step.Text != ""},
			struct {
				name      string
				populated bool
			}{"corpus_id", stepHasCorpus(step)},
			struct {
				name      string
				populated bool
			}{"tool_call_id", step.ToolCallID != ""},
			struct {
				name      string
				populated bool
			}{"tool_name", step.ToolName != ""},
			struct {
				name      string
				populated bool
			}{"result", len(step.ToolResult) != 0 || len(step.Result) != 0},
			struct {
				name      string
				populated bool
			}{"at", step.At != 0 || step.Time != 0},
			struct {
				name      string
				populated bool
			}{"duration", step.Duration != 0},
		); err != nil {
			return err
		}
	}
	return nil
}

func unexpectedStepField(location string, field string, kind StepKind) error {
	return makeError(CategoryInvalidField, location+"."+field, "unexpected payload for %s", kind)
}
func satisfiable(value ExpectedBehavior, index int, all []ExpectedBehavior, steps []Step, closeIndex int, finalTime LogicalTime) error {
	location := fmt.Sprintf("expectations[%d]", index)
	if value.HasStep {
		if value.StepIndex < 0 || value.StepIndex >= len(steps) {
			return makeError(CategoryUnsatisfiable, location+".step", "referenced step %d does not exist", value.StepIndex)
		}
		if value.StepIndex >= closeIndex && (value.Kind != ExpectClose || value.StepIndex != closeIndex) {
			return makeError(CategoryUnsatisfiable, location+".step", "expectation refers to a terminal step incompatibly")
		}
	}
	if value.HasAfter && (value.AfterStep >= len(steps) || value.AfterStep >= closeIndex) {
		return makeError(CategoryUnsatisfiable, location+".after", "after step %d cannot occur before terminal close", value.AfterStep)
	}
	if value.HasBefore && (value.BeforeStep >= len(steps) || value.BeforeStep > closeIndex) {
		return makeError(CategoryUnsatisfiable, location+".before", "before step %d is outside the scenario", value.BeforeStep)
	}
	if index > 0 && value.HasStep && all[index-1].HasStep && value.StepIndex < all[index-1].StepIndex {
		return makeError(CategoryContradictory, location+".step", "expectations must preserve step order")
	}
	switch value.Kind {
	case ExpectToolResult:
		for _, step := range steps {
			kind := step.Kind
			if kind == "" {
				kind = step.Type
			}
			if kind == StepSendToolResult && step.ToolCallID == value.ToolCallID {
				return nil
			}
		}
		return makeError(CategoryUnsatisfiable, location+".tool_call_id", "no declared tool result can satisfy this expectation")
	case ExpectAudio:
		for _, step := range steps {
			kind := step.Kind
			if kind == "" {
				kind = step.Type
			}
			id := step.CorpusID
			if id == "" {
				id = step.Corpus.CorpusID
			}
			if kind == StepSendAudio && id == value.CorpusID {
				return nil
			}
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

func lookupCorpus(lookup CorpusLookup, id string) bool {
	return lookup != nil && lookup.Has(id)
}
