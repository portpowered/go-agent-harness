package probe

import "strings"

func expectationTimeValue(value ExpectedBehavior, location string) (LogicalTime, bool, error) {
	if value.At != 0 && value.Time != 0 && value.At != value.Time {
		return 0, false, makeError(CategoryInvalidField, location+".at", "at and time aliases disagree")
	}
	if value.HasAt {
		if value.At != 0 {
			return value.At, true, nil
		}
		return value.Time, true, nil
	}
	if value.At != 0 {
		return value.At, true, nil
	}
	if value.Time != 0 {
		return value.Time, true, nil
	}
	return 0, false, nil
}

func typedExpectationFieldsByKind() map[ExpectationKind]map[string]bool {
	return map[ExpectationKind]map[string]bool{
		ExpectText:       {"text": true},
		ExpectTranscript: {"text": true},
		ExpectContains:   {"text": true},
		ExpectAudio:      {"corpus_id": true},
		ExpectToolCall:   {"tool_call_id": true, "tool_name": true},
		ExpectToolResult: {"tool_call_id": true, "result": true}, ExpectToolResultDelivered: {"tool_call_id": true},
		ExpectToolResultDiscarded: {"tool_call_id": true}, ExpectNoOrphanedToolResult: {}, ExpectClose: {},
		ExpectTime:  {"at": true},
		ExpectEvent: {"value": true},

		// Measurable expectation kinds.
		ExpectAudioEnergy:        {},
		ExpectTranscriptContains: {"text": true},
		ExpectToolCalled:         {"tool_name": true},
		ExpectLatencyWithinTicks: {"at": true},
		ExpectTerminalReason:     {"value": true},
		ExpectTerminalProvenance: {"value": true},
		ExpectOutputState:        {"value": true},
		ExpectFrameCount:         {},
		ExpectBufferDisposition:  {"value": true},
		ExpectMetricsReconcile:   {},
		ExpectResponseCancel:     {"value": true, "at": true},

		// Repeated barge-in (v3c) expectation kinds.
		ExpectBargeInCancelOnce:      {},
		ExpectMessageCountsReconcile: {"value": true},
	}
}

func rejectTypedExpectationFields(value ExpectedBehavior, location string, hasAt bool) error {
	allowed := typedExpectationFieldsByKind()[value.Kind]
	fields := []struct {
		name      string
		populated bool
	}{
		{"text", value.Text != ""},
		{"value", value.Value != ""},
		{"corpus_id", value.CorpusID != ""},
		{"tool_call_id", value.ToolCallID != ""},
		{"tool_name", value.ToolName != ""},
		{"result", len(value.Result) != 0},
		{"at", hasAt},
	}
	for _, field := range fields {
		if field.populated && !allowed[field.name] {
			return makeError(CategoryInvalidField, location+"."+field.name, "unexpected payload for %s expectation", value.Kind)
		}
	}
	return nil
}

func validateExpectationFields(value ExpectedBehavior, location string) error {
	at, hasAt, err := expectationTimeValue(value, location)
	if err != nil {
		return err
	}
	if err := validateExpectationAnchors(value, location); err != nil {
		return err
	}
	if value.Count < 0 {
		return makeError(CategoryInvalidField, location+".count", "must not be negative")
	}
	if err := rejectTypedExpectationFields(value, location, hasAt); err != nil {
		return err
	}
	switch value.Kind {
	case ExpectText, ExpectTranscript, ExpectContains:
		if strings.TrimSpace(value.Text) == "" {
			return makeError(CategoryMissingField, location+".text", "expected text is required")
		}
	case ExpectAudio:
		if value.CorpusID == "" {
			return makeError(CategoryMissingField, location+".corpus_id", "expected corpus ID is required")
		}
	case ExpectToolCall:
		if value.ToolName == "" && value.ToolCallID == "" {
			return makeError(CategoryMissingField, location+".tool_name", "tool name or call ID is required")
		}
	case ExpectToolResult:
		if value.ToolCallID == "" {
			return makeError(CategoryMissingField, location+".tool_call_id", "tool call ID is required")
		}
		if len(value.Result) == 0 {
			return makeError(CategoryMissingField, location+".result", "expected result is required")
		}
	case ExpectTime:
		if !hasAt {
			return makeError(CategoryMissingField, location+".at", "expected logical time is required")
		}
		if at <= 0 {
			return makeError(CategoryInvalidField, location+".at", "must be positive")
		}
	case ExpectEvent:
		if value.Value == "" {
			return makeError(CategoryMissingField, location+".event", "event name is required")
		}
	case ExpectAudioEnergy, ExpectTranscriptContains, ExpectToolCalled, ExpectLatencyWithinTicks, ExpectTerminalReason, ExpectTerminalProvenance, ExpectOutputState, ExpectFrameCount, ExpectMetricsReconcile, ExpectToolResultDelivered, ExpectToolResultDiscarded, ExpectNoOrphanedToolResult, ExpectResponseCancel, ExpectBufferDisposition, ExpectBargeInCancelOnce, ExpectMessageCountsReconcile, ExpectClose:
		// These kinds need no handling here.
	}
	return nil
}

// validateExpectationAnchors checks the step, after, and before anchors.
func validateExpectationAnchors(value ExpectedBehavior, location string) error {
	if value.HasStep && value.StepIndex < 0 {
		return makeError(CategoryInvalidField, location+".step", "must not be negative")
	}
	if value.HasAfter && value.AfterStep < 0 {
		return makeError(CategoryInvalidField, location+".after", "must not be negative")
	}
	if value.HasBefore && value.BeforeStep < 0 {
		return makeError(CategoryInvalidField, location+".before", "must not be negative")
	}
	if value.HasAfter && value.HasBefore && value.AfterStep >= value.BeforeStep {
		return makeError(CategoryContradictory, location, "after step must precede before step")
	}
	return nil
}
