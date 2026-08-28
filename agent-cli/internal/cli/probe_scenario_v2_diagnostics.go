package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// probeScenarioV2Divergence is the stable, redacted explanation attached to a
// failed typed expectation. Expected and Actual are deliberately structural
// summaries: browser arguments, tool results, endpoint credentials, and raw
// CDP payloads never belong in a run result or objective artifact.
type probeScenarioV2Divergence struct {
	Class             string                          `json:"class"`
	ScenarioID        string                          `json:"scenario_id"`
	ExpectationType   probe.ScenarioV2ExpectationType `json:"expectation_type"`
	ExpectationIndex  int                             `json:"expectation_index"`
	EvidenceArtifact  string                          `json:"evidence_artifact"`
	Path              string                          `json:"path,omitempty"`
	Expected          string                          `json:"expected"`
	Actual            string                          `json:"actual"`
	EventPosition     int                             `json:"event_position,omitempty"`
	OperationPosition int                             `json:"operation_position,omitempty"`
	ErrorCode         string                          `json:"error_code,omitempty"`
	ToolName          string                          `json:"tool_name,omitempty"`
	ToolRef           string                          `json:"tool_ref,omitempty"`
	Generation        int64                           `json:"generation,omitempty"`
}

const probeScenarioV2DivergenceClass = "browser_expectation_divergence"

func (d *probeScenarioV2Divergence) Error() string {
	if d == nil {
		return "browser expectation divergence"
	}
	message := fmt.Sprintf(
		"browser expectation divergence: scenario %s expectation[%d] %s; expected %s, actual %s; evidence %s",
		d.ScenarioID,
		d.ExpectationIndex,
		d.ExpectationType,
		d.Expected,
		d.Actual,
		d.EvidenceArtifact,
	)
	if d.Path != "" {
		message += "; path " + d.Path
	}
	if d.EventPosition > 0 {
		message += fmt.Sprintf("; event position %d", d.EventPosition)
	}
	if d.OperationPosition > 0 {
		message += fmt.Sprintf("; operation position %d", d.OperationPosition)
	}
	if d.ErrorCode != "" {
		message += "; error code " + d.ErrorCode
	}
	return message
}

type probeScenarioV2ObservationCheck struct {
	Passed            bool
	Expected          string
	Actual            string
	EvidenceArtifact  string
	Path              string
	EventPosition     int
	OperationPosition int
	ErrorCode         string
	ToolName          string
	ToolRef           string
	Generation        int64
}

func makeProbeScenarioV2Divergence(
	scenario probe.ScenarioV2,
	index int,
	expectation probe.ScenarioV2Expectation,
	check probeScenarioV2ObservationCheck,
) *probeScenarioV2Divergence {
	artifact := check.EvidenceArtifact
	if artifact == "" {
		artifact = probeScenarioV2ExpectationArtifact(expectation.Type)
	}
	return &probeScenarioV2Divergence{
		Class:             probeScenarioV2DivergenceClass,
		ScenarioID:        scenario.ID,
		ExpectationType:   expectation.Type,
		ExpectationIndex:  index,
		EvidenceArtifact:  artifact,
		Path:              firstNonEmptyProbeScenarioV2String(check.Path, expectation.Path, expectation.JSONPath),
		Expected:          firstNonEmptyProbeScenarioV2String(check.Expected, "<unavailable>"),
		Actual:            firstNonEmptyProbeScenarioV2String(check.Actual, "<unavailable>"),
		EventPosition:     check.EventPosition,
		OperationPosition: check.OperationPosition,
		ErrorCode:         firstNonEmptyProbeScenarioV2String(check.ErrorCode),
		ToolName:          firstNonEmptyProbeScenarioV2String(check.ToolName, expectation.Name),
		ToolRef:           firstNonEmptyProbeScenarioV2String(check.ToolRef, expectation.ToolRef),
		Generation:        check.Generation,
	}
}

func firstNonEmptyProbeScenarioV2String(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func probeScenarioV2ExpectationArtifact(kind probe.ScenarioV2ExpectationType) string {
	switch kind {
	case probe.ScenarioV2ExpectationTranscriptContains,
		probe.ScenarioV2ExpectationAssistantAudioStarted,
		probe.ScenarioV2ExpectationAssistantAudioStopped:
		return probeScenarioV2ProviderArtifactPath
	case probe.ScenarioV2ExpectationPageStateEquals:
		return probeScenarioV2PageStateArtifactPath
	default:
		return transcript.BrowserArtifactDefaultPath
	}
}

func safeProbeScenarioV2JSON(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "<missing>"
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "<invalid-json>"
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return "<invalid-json>"
	}
	switch typed := value.(type) {
	case map[string]any:
		return fmt.Sprintf("object(fields=%d)", len(typed))
	case []any:
		return fmt.Sprintf("array(items=%d)", len(typed))
	case string:
		return "string"
	case json.Number:
		return "number"
	case bool:
		return "boolean"
	case nil:
		return "null"
	default:
		return "json"
	}
}

func safeProbeScenarioV2Text(present bool) string {
	if present {
		return "text-present"
	}
	return "text-absent"
}

func safeProbeScenarioV2Error(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, webmcp.ErrStaleToolRef):
		return string(webmcp.ErrorStaleToolRef)
	case errors.Is(err, testkit.ErrFixtureOperationMismatch):
		return "fixture_operation_mismatch"
	case errors.Is(err, testkit.ErrFixtureIncomplete):
		return "fixture_incomplete"
	case errors.Is(err, testkit.ErrFixturePendingInvocations):
		return "fixture_pending_invocations"
	case errors.Is(err, testkit.ErrFixtureClosed):
		return "fixture_closed"
	case errors.Is(err, testkit.ErrFixtureCanceled):
		return "fixture_canceled"
	case errors.Is(err, testkit.ErrInvalidBrowserScript):
		return "invalid_browser_script"
	case errors.Is(err, webmcp.ErrBrowserNotFound):
		return "no_browser"
	case errors.Is(err, webmcp.ErrTargetNotFound):
		return "no_page"
	case errors.Is(err, webmcp.ErrInvalidToolInput):
		return string(webmcp.ErrorInvalidToolInput)
	case errors.Is(err, webmcp.ErrInvocationNotFound):
		return "no_invocation"
	default:
		var classified *webmcp.ClassifiedError
		if errors.As(err, &classified) && classified != nil && classified.Code != "" {
			return string(classified.Code)
		}
		return "observation_error"
	}
}

func probeScenarioV2ExpectationCheckFromError(
	expectation probe.ScenarioV2Expectation,
	expected, actual string,
	err error,
) probeScenarioV2ObservationCheck {
	return probeScenarioV2ObservationCheck{
		Passed:    false,
		Expected:  expected,
		Actual:    actual,
		Path:      firstNonEmptyProbeScenarioV2String(expectation.Path, expectation.JSONPath),
		ErrorCode: safeProbeScenarioV2Error(err),
	}
}

func probeScenarioV2DivergenceForExpectation(
	scenario probe.ScenarioV2,
	index int,
	expectation probe.ScenarioV2Expectation,
	expected, actual string,
	err error,
	evidence *probeScenarioV2PersistedBrowserEvidence,
) *probeScenarioV2Divergence {
	check := probeScenarioV2ExpectationCheckFromError(expectation, expected, actual, err)
	check.EvidenceArtifact = probeScenarioV2ExpectationArtifact(expectation.Type)
	if evidence != nil && isProbeScenarioV2BrowserObjective(expectation.Type) && expectation.Type != probe.ScenarioV2ExpectationPageStateEquals {
		observed := probeScenarioV2BrowserObjectiveCheck(*evidence, nil, expectation)
		if observed.EvidenceArtifact == "" {
			observed.EvidenceArtifact = check.EvidenceArtifact
		}
		if observed.Expected == "" {
			observed.Expected = expected
		}
		if observed.Actual == "" {
			observed.Actual = actual
		}
		if observed.ErrorCode == "" {
			observed.ErrorCode = check.ErrorCode
		}
		check = observed
	}
	return makeProbeScenarioV2Divergence(scenario, index, expectation, check)
}

func probeScenarioV2DivergenceEqual(left, right *probeScenarioV2Divergence) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func probeScenarioV2BrowserObjectiveCheck(
	evidence probeScenarioV2PersistedBrowserEvidence,
	pageState json.RawMessage,
	expectation probe.ScenarioV2Expectation,
) probeScenarioV2ObservationCheck {
	check := probeScenarioV2ObservationCheck{EvidenceArtifact: transcript.BrowserArtifactDefaultPath}
	countCheck := func(actual int64, present bool) probeScenarioV2ObservationCheck {
		check.Expected = fmt.Sprintf("%d", expectation.Equals)
		if !present {
			check.Actual = "<missing>"
			return check
		}
		check.Actual = fmt.Sprintf("%d", actual)
		check.Passed = actual == expectation.Equals
		return check
	}

	switch expectation.Type {
	case probe.ScenarioV2ExpectationBrowserCountEquals:
		check.EventPosition = evidence.browserCountPos
		return countCheck(evidence.browserCount, evidence.browserCountSet)
	case probe.ScenarioV2ExpectationEligibleTabCountEquals:
		var count int64
		lastPosition := 0
		for _, target := range evidence.targets {
			if target.Eligible {
				count++
			}
			if target.Position > lastPosition {
				lastPosition = target.Position
			}
		}
		check.EventPosition = lastPosition
		return countCheck(count, len(evidence.targets) > 0 || expectation.Equals == 0)
	case probe.ScenarioV2ExpectationSelectedTabEquals:
		check.Expected = expectation.TargetID
		check.Actual = firstNonEmptyProbeScenarioV2String(evidence.selectedTarget, "<missing>")
		check.EventPosition = evidence.selectedTargetPos
		check.Passed = evidence.selectedTarget == expectation.TargetID
		return check
	case probe.ScenarioV2ExpectationSelectedOriginEquals:
		check.Expected = safeEvidenceURL(expectation.Origin)
		target, ok := probeScenarioV2SelectedTarget(evidence)
		if ok {
			check.Actual = firstNonEmptyProbeScenarioV2String(safeEvidenceURL(target.Origin), "<missing>")
			check.EventPosition = target.Position
			check.Passed = target.Origin == expectation.Origin
		} else {
			check.Actual = "<missing>"
		}
		return check
	case probe.ScenarioV2ExpectationCatalogGenerationEquals:
		check.Expected = fmt.Sprintf("%d", expectation.Generation)
		check.EventPosition = evidence.catalogPos
		check.Generation = evidence.catalogGeneration
		if !evidence.catalogSet {
			check.Actual = "<missing>"
			return check
		}
		check.Actual = fmt.Sprintf("%d", evidence.catalogGeneration)
		check.Passed = evidence.catalogGeneration == expectation.Generation
		return check
	case probe.ScenarioV2ExpectationToolCatalogContains, probe.ScenarioV2ExpectationToolCatalogNotContains:
		_, found := evidence.catalog[expectation.Name]
		check.Expected = probeScenarioV2CatalogExpectation(expectation)
		check.Actual = probeScenarioV2Presence(found)
		if tool, ok := evidence.catalog[expectation.Name]; ok {
			check.EventPosition = tool.Position
		}
		check.Passed = found == (expectation.Type == probe.ScenarioV2ExpectationToolCatalogContains)
		return check
	case probe.ScenarioV2ExpectationToolSchemaEquals:
		check.Expected = safeProbeScenarioV2JSON(expectation.Schema)
		tool, found := evidence.catalog[expectation.Name]
		if !found {
			check.Actual = "<missing>"
			return check
		}
		check.Actual = safeProbeScenarioV2JSON(tool.InputSchema)
		check.EventPosition = tool.Position
		check.Passed = semanticJSONEqual(tool.InputSchema, expectation.Schema)
		return check
	case probe.ScenarioV2ExpectationToolInvocationCount:
		var count int64
		lastPosition := 0
		for _, invocation := range evidence.invocations {
			if invocation.Name != expectation.Name {
				continue
			}
			count++
			if position := probeScenarioV2InvocationPosition(invocation); position > lastPosition {
				lastPosition = position
			}
		}
		check.Expected = fmt.Sprintf("%d", expectation.Equals)
		check.Actual = fmt.Sprintf("%d", count)
		check.EventPosition = lastPosition
		check.Passed = count == expectation.Equals
		return check
	case probe.ScenarioV2ExpectationToolInputJSONEquals:
		check.Expected = safeProbeScenarioV2JSON(json.RawMessage(expectation.InputJSON))
		invocation, found := probeScenarioV2LatestInvocation(evidence, expectation.Name)
		if !found {
			check.Actual = "<missing>"
			return check
		}
		check.Actual = safeProbeScenarioV2JSON(invocation.Input)
		check.EventPosition = probeScenarioV2InvocationPosition(invocation)
		check.ToolRef = invocation.ToolRef
		check.Passed = semanticJSONEqual(invocation.Input, json.RawMessage(expectation.InputJSON))
		return check
	case probe.ScenarioV2ExpectationToolResultJSONPathEquals:
		check.Expected = safeProbeScenarioV2JSON(expectation.Value)
		check.Path = expectation.Path
		invocation, found := probeScenarioV2LatestInvocation(evidence, expectation.Name)
		if !found {
			check.Actual = "<missing>"
			return check
		}
		value, err := jsonPathValue(invocation.Output, expectation.Path)
		check.EventPosition = probeScenarioV2InvocationPosition(invocation)
		check.ToolRef = invocation.ToolRef
		if err != nil {
			check.Actual = "<missing>"
			check.ErrorCode = "jsonpath_not_found"
			return check
		}
		check.Actual = safeProbeScenarioV2JSON(value)
		check.Passed = semanticJSONEqual(value, expectation.Value)
		return check
	case probe.ScenarioV2ExpectationToolStatusEquals:
		check.Expected = expectation.Status
		invocation, found := probeScenarioV2LatestInvocation(evidence, expectation.Name)
		if !found {
			check.Actual = "<missing>"
			return check
		}
		check.Actual = firstNonEmptyProbeScenarioV2String(invocation.State, "<missing>")
		check.EventPosition = probeScenarioV2InvocationPosition(invocation)
		check.ToolRef = invocation.ToolRef
		check.Passed = strings.EqualFold(invocation.State, expectation.Status)
		return check
	case probe.ScenarioV2ExpectationChromeOperationOrder:
		return probeScenarioV2OrderedObservationCheck(evidence.operations, expectation.Operations)
	case probe.ScenarioV2ExpectationNoUnexpectedChromeOperations:
		return probeScenarioV2AllowedObservationCheck(evidence.operations, expectation.Operations)
	case probe.ScenarioV2ExpectationGeneratedCDPMethodOrder:
		return probeScenarioV2OrderedObservationCheck(evidence.methods, expectation.Methods)
	case probe.ScenarioV2ExpectationNoUnexpectedGeneratedCDPMethods:
		return probeScenarioV2AllowedObservationCheck(evidence.methods, expectation.Methods)
	case probe.ScenarioV2ExpectationNoPendingInvocations:
		pending := 0
		lastPosition := 0
		for _, invocation := range evidence.invocations {
			if !invocation.Terminal {
				pending++
				if position := probeScenarioV2InvocationPosition(invocation); position > lastPosition {
					lastPosition = position
				}
			}
		}
		check.Expected = "0"
		check.Actual = fmt.Sprintf("%d", pending)
		check.EventPosition = lastPosition
		check.Passed = pending == 0
		return check
	case probe.ScenarioV2ExpectationPageStateEquals:
		check.EvidenceArtifact = probeScenarioV2PageStateArtifactPath
		check.Path = expectation.Path
		check.Expected = safeProbeScenarioV2JSON(expectation.Value)
		value, err := jsonPathValue(pageState, expectation.Path)
		if err != nil {
			check.Actual = "<missing>"
			check.ErrorCode = "jsonpath_not_found"
			return check
		}
		check.Actual = safeProbeScenarioV2JSON(value)
		check.Passed = semanticJSONEqual(value, expectation.Value)
		return check
	case probe.ScenarioV2ExpectationResponseCanceled:
		check.Expected = "canceled"
		check.Actual = probeScenarioV2Presence(evidence.canceled)
		check.EventPosition = evidence.canceledPos
		check.Passed = evidence.canceled
		return check
	case probe.ScenarioV2ExpectationApprovalRequested, probe.ScenarioV2ExpectationApprovalNotRequested:
		requested := false
		position := evidence.approvalPos
		for _, invocation := range evidence.invocations {
			if expectation.ToolRef != "" && invocation.ToolRef != expectation.ToolRef {
				continue
			}
			if !invocation.Approval {
				continue
			}
			requested = true
			if invocation.ApprovalPos > position {
				position = invocation.ApprovalPos
			}
		}
		if expectation.ToolRef == "" && evidence.approvalRequested {
			requested = true
		}
		check.EventPosition = position
		if expectation.Type == probe.ScenarioV2ExpectationApprovalNotRequested {
			check.Expected = "not-requested"
			check.Passed = !requested
		} else {
			check.Expected = "requested"
			check.Passed = requested
		}
		check.Actual = probeScenarioV2Presence(requested)
		return check
	case probe.ScenarioV2ExpectationStaleToolRejected:
		check.Expected = "stale_tool_ref"
		check.Actual = "<none>"
		check.EventPosition = evidence.stalePos
		for _, invocation := range evidence.invocations {
			if invocation.ErrorCode != string(webmcp.ErrorStaleToolRef) {
				continue
			}
			if expectation.ToolRef != "" && invocation.ToolRef != expectation.ToolRef {
				continue
			}
			check.Actual = "stale_tool_ref"
			check.ToolRef = invocation.ToolRef
			check.EventPosition = probeScenarioV2InvocationPosition(invocation)
			check.Passed = true
			return check
		}
		if evidence.stale && (expectation.ToolRef == "" || evidence.staleToolRef == "" || evidence.staleToolRef == expectation.ToolRef) {
			check.Actual = "stale_tool_ref"
			check.ToolRef = evidence.staleToolRef
			check.Passed = true
		}
		return check
	case probe.ScenarioV2ExpectationBrowserConnectionClosed:
		check.Expected = "closed"
		check.Actual = probeScenarioV2Presence(evidence.closed)
		check.EventPosition = evidence.closedPos
		check.Passed = evidence.closed
		return check
	default:
		check.Expected = "supported browser objective"
		check.Actual = "unsupported"
		check.ErrorCode = "unsupported_expectation"
		return check
	}
}

func isProbeScenarioV2ProviderObjective(kind probe.ScenarioV2ExpectationType) bool {
	switch kind {
	case probe.ScenarioV2ExpectationAssistantAudioStarted,
		probe.ScenarioV2ExpectationAssistantAudioStopped,
		probe.ScenarioV2ExpectationTranscriptContains:
		return true
	default:
		return false
	}
}

func probeScenarioV2ProviderObjectiveCheck(
	capture gatewaytesting.SessionCapture,
	expectation probe.ScenarioV2Expectation,
) probeScenarioV2ObservationCheck {
	check := probeScenarioV2ObservationCheck{EvidenceArtifact: probeScenarioV2ProviderArtifactPath}
	switch expectation.Type {
	case probe.ScenarioV2ExpectationTranscriptContains:
		present := strings.Contains(replayTranscriptFromCapture(capture), expectation.Text)
		check.Expected = "text-present"
		check.Actual = safeProbeScenarioV2Text(present)
		check.Passed = present
		return check
	case probe.ScenarioV2ExpectationAssistantAudioStarted:
		started, position, _ := probeScenarioV2ProviderAudioBounds(capture)
		check.Expected = "audio-started"
		check.Actual = probeScenarioV2Presence(started)
		check.EventPosition = position
		check.Passed = started
		return check
	case probe.ScenarioV2ExpectationAssistantAudioStopped:
		_, position, stopped := probeScenarioV2ProviderAudioBounds(capture)
		check.Expected = "audio-stopped"
		check.Actual = probeScenarioV2Presence(stopped)
		check.EventPosition = position
		check.Passed = stopped
		return check
	default:
		check.Expected = "provider objective"
		check.Actual = "unsupported"
		check.ErrorCode = "unsupported_expectation"
		return check
	}
}

func probeScenarioV2ProviderAudioBounds(capture gatewaytesting.SessionCapture) (started bool, position int, stopped bool) {
	for index, record := range capture.Records {
		if record.Direction != gatewaytesting.DirectionServerToClient {
			continue
		}
		currentPosition := record.Sequence
		if currentPosition <= 0 {
			currentPosition = index + 1
		}
		switch record.Type {
		case "response.output_audio.delta", "response.audio.delta":
			started = true
			if position == 0 {
				position = currentPosition
			}
		case "response.output_audio.done", "response.audio.done":
			if started {
				stopped = true
				position = currentPosition
			}
		}
	}
	return started, position, stopped
}

func probeScenarioV2SelectedTarget(evidence probeScenarioV2PersistedBrowserEvidence) (probeScenarioV2PersistedTarget, bool) {
	if evidence.selectedTarget == "" {
		return probeScenarioV2PersistedTarget{}, false
	}
	for key, target := range evidence.targets {
		if strings.HasSuffix(key, "\x00"+evidence.selectedTarget) {
			return target, true
		}
	}
	return probeScenarioV2PersistedTarget{}, false
}

func probeScenarioV2LatestInvocation(evidence probeScenarioV2PersistedBrowserEvidence, name string) (*probeScenarioV2PersistedInvocation, bool) {
	for index := len(evidence.invocations) - 1; index >= 0; index-- {
		if evidence.invocations[index].Name == name {
			return evidence.invocations[index], true
		}
	}
	return nil, false
}

func probeScenarioV2InvocationPosition(invocation *probeScenarioV2PersistedInvocation) int {
	if invocation == nil {
		return 0
	}
	if invocation.TerminalPos > 0 {
		return invocation.TerminalPos
	}
	if invocation.DispatchedPos > 0 {
		return invocation.DispatchedPos
	}
	return invocation.CreatedPos
}

func probeScenarioV2CatalogExpectation(expectation probe.ScenarioV2Expectation) string {
	if expectation.Type == probe.ScenarioV2ExpectationToolCatalogNotContains {
		return "tool " + expectation.Name + " absent"
	}
	return "tool " + expectation.Name + " present"
}

func probeScenarioV2Presence(present bool) string {
	if present {
		return "present"
	}
	return "absent"
}

func probeScenarioV2ObservedNames(observed []probeScenarioV2ObservedOperation) []string {
	result := make([]string, 0, len(observed))
	for _, item := range observed {
		result = append(result, item.Name)
	}
	return result
}

func probeScenarioV2StringList(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	return "[" + strings.Join(values, ", ") + "]"
}

func probeScenarioV2OrderedObservationCheck(observed []probeScenarioV2ObservedOperation, expected []string) probeScenarioV2ObservationCheck {
	check := probeScenarioV2ObservationCheck{
		Expected:         probeScenarioV2StringList(expected),
		Actual:           probeScenarioV2StringList(probeScenarioV2ObservedNames(observed)),
		EvidenceArtifact: transcript.BrowserArtifactDefaultPath,
	}
	if len(expected) == 0 {
		check.Passed = true
		return check
	}
	observedIndex := 0
	for _, wanted := range expected {
		for observedIndex < len(observed) && observed[observedIndex].Name != wanted {
			observedIndex++
		}
		if observedIndex == len(observed) {
			if len(observed) > 0 {
				last := observed[len(observed)-1]
				check.OperationPosition = last.OperationPosition
				check.EventPosition = last.EventPosition
			}
			return check
		}
		observedIndex++
	}
	check.Passed = true
	return check
}

func probeScenarioV2AllowedObservationCheck(observed []probeScenarioV2ObservedOperation, allowed []string) probeScenarioV2ObservationCheck {
	check := probeScenarioV2ObservationCheck{
		Expected:         probeScenarioV2StringList(allowed),
		Actual:           probeScenarioV2StringList(probeScenarioV2ObservedNames(observed)),
		EvidenceArtifact: transcript.BrowserArtifactDefaultPath,
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, value := range allowed {
		allowedSet[value] = struct{}{}
	}
	for _, item := range observed {
		if _, ok := allowedSet[item.Name]; ok {
			continue
		}
		check.OperationPosition = item.OperationPosition
		check.EventPosition = item.EventPosition
		return check
	}
	check.Passed = true
	return check
}

func (e *probeScenarioV2Executor) persistedBrowserEvidence() *probeScenarioV2PersistedBrowserEvidence {
	if e == nil || len(e.eventOutput.Bytes()) == 0 {
		return nil
	}
	events, err := testkit.ValidateEventStream(e.eventOutput.Bytes())
	if err != nil || len(events) == 0 {
		return nil
	}
	evidence := indexProbeScenarioV2BrowserEvents(events)
	return &evidence
}
