package webmcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
)

func (b *StatefulBroker) observeOwnedBrowserInvocationLocked(selected *brokerSession, event BrowserEvent, invocation *brokerInvocation) {
	if invocation == nil || invocation.terminalized || invocation.invokedObserved {
		return
	}
	if invocation.selected != selected {
		return
	}
	if reason := invocationEventFreshnessReason(invocation, event); reason != "" {
		b.takeEarlyTerminalLocked(event.InvocationID, 0)
		b.finishInvocationLocked(invocation, freshnessFailureResult(invocation, "invocation_provenance", reason, false))
		return
	}
	if reason := invocationCatalogFreshnessReasonLocked(b, invocation); reason != "" {
		b.takeEarlyTerminalLocked(event.InvocationID, 0)
		b.finishInvocationLocked(invocation, freshnessFailureResult(invocation, "catalog_provenance", reason, false))
		return
	}
	invocation.invokedObserved = true
	invocation.invokedSequence = event.Sequence
	if terminal, ok := b.takeEarlyTerminalLocked(event.InvocationID, 0); ok {
		if reason := terminalObservationFreshnessReason(invocation, terminal); reason != "" {
			b.finishInvocationLocked(invocation, freshnessFailureResult(invocation, "terminal_provenance", reason, true))
			return
		}
		b.applyTerminalObservationLocked(invocation, terminal)
	}
}

func invocationEventFreshnessReason(invocation *brokerInvocation, event BrowserEvent) string {
	if invocation == nil {
		return "invocation_missing"
	}
	descriptor := invocation.invocation.Tool
	if event.BrowserID == "" {
		return "invocation_browser_identity_missing"
	}
	if event.BrowserID != descriptor.BrowserID {
		return "invocation_browser_identity_mismatch"
	}
	if event.TargetID == "" {
		return "invocation_target_identity_missing"
	}
	if event.TargetID != descriptor.TargetID {
		return "invocation_target_identity_mismatch"
	}
	if event.Generation == 0 {
		return "invocation_generation_missing"
	}
	if event.Generation != descriptor.Generation {
		return "invocation_generation_mismatch"
	}
	if event.FrameID == "" {
		return "invocation_frame_missing"
	}
	if event.FrameID != descriptor.FrameID {
		return "invocation_frame_mismatch"
	}
	if event.ToolName == "" {
		return "invocation_tool_name_missing"
	}
	if event.ToolName != descriptor.Name {
		return "invocation_tool_name_mismatch"
	}
	if len(event.Input) == 0 {
		return "invocation_input_missing"
	}
	if !sameJSONValue(event.Input, invocation.invocation.Arguments) {
		return "invocation_input_mismatch"
	}
	if event.Sequence == 0 {
		return "invocation_sequence_missing"
	}
	return ""
}

func freshnessFailureResult(invocation *brokerInvocation, phase, reason string, terminalObserved bool) InvokeResult {
	if invocation == nil {
		return InvokeResult{State: InvocationError, ErrorCode: string(ErrorInvocationFailed)}
	}
	descriptor := invocation.invocation.Tool
	recovery := "Refresh the current page tool catalog and retry with a newly correlated invocation."
	if invocation.invocation.Operation != OperationReadOnly {
		recovery = "Do not retry this mutation; reconcile the target state before deciding whether to issue it again."
	}
	return invocationFailureResult(invocation, InvocationError, ErrorInvocationFailed, map[string]any{
		"invocation_id":         string(invocation.invocation.ID),
		"browser_invocation_id": string(invocation.browserID),
		"browser_id":            string(descriptor.BrowserID),
		"target_id":             string(descriptor.TargetID),
		"generation":            descriptor.Generation,
		"tool_ref":              string(descriptor.Ref),
		"phase":                 "result_freshness",
		"freshness_phase":       phase,
		"reason_code":           reason,
		"terminal_observed":     terminalObserved,
		"side_effect_unknown":   true,
		"safe_retryable":        invocation.invocation.Operation == OperationReadOnly,
		"recovery":              recovery,
	})
}

func terminalObservationFreshnessReason(invocation *brokerInvocation, observation terminalObservation) string {
	if invocation == nil {
		return "invocation_missing"
	}
	descriptor := invocation.invocation.Tool
	if observation.browserID == "" {
		return "terminal_browser_identity_missing"
	}
	if observation.browserID != descriptor.BrowserID {
		return "terminal_browser_identity_mismatch"
	}
	if observation.targetID == "" {
		return "terminal_target_identity_missing"
	}
	if observation.targetID != descriptor.TargetID {
		return "terminal_target_identity_mismatch"
	}
	if observation.generation == 0 {
		return "terminal_generation_missing"
	}
	if observation.generation != descriptor.Generation {
		return "terminal_generation_mismatch"
	}
	if invocation.invokedSequence == 0 {
		return "invocation_sequence_missing"
	}
	if observation.sequence == 0 {
		return "terminal_sequence_missing"
	}
	if observation.sequence <= invocation.invokedSequence {
		return "terminal_before_invocation"
	}
	return ""
}

func invocationCatalogFreshnessReasonLocked(b *StatefulBroker, invocation *brokerInvocation) string {
	if b == nil || invocation == nil || invocation.selected == nil {
		return "selected_session_missing"
	}
	selected := invocation.selected
	if selected.context.Key.BrowserID != invocation.invocation.Tool.BrowserID || selected.context.Key.TargetID != invocation.invocation.Tool.TargetID {
		return "selected_target_mismatch"
	}
	if selected.context.Generation != invocation.invocation.Tool.Generation {
		return "catalog_generation_mismatch"
	}
	record, ok := b.refs[invocation.invocation.Tool.Ref]
	if !ok || !refCurrentLocked(selected, record) {
		return "tool_ref_not_current"
	}
	return ""
}

func sameJSONValue(left, right json.RawMessage) bool {
	leftValue, leftErr := jsonValueWithNumbers(left)
	rightValue, rightErr := jsonValueWithNumbers(right)
	return leftErr == nil && rightErr == nil && reflect.DeepEqual(leftValue, rightValue)
}

func jsonValueWithNumbers(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("webmcp: JSON value contains multiple values")
		}
		return nil, err
	}
	return value, nil
}
