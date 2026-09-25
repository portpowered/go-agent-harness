package objective

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

// StaleToolRefLabel is the stable label for a rejected stale tool reference.
const StaleToolRefLabel = string(webmcp.ErrorStaleToolRef)

func checkInvocationCount(evidence BrowserEvidence, _ json.RawMessage, expectation probe.ScenarioV2Expectation) Check {
	var count int64
	lastPosition := 0
	for _, invocation := range evidence.invocations {
		if invocation.Name != expectation.Name {
			continue
		}
		count++
		lastPosition = max(lastPosition, invocationPosition(invocation))
	}
	check := newBrowserCheck()
	check.Expected = formatInt(expectation.Equals)
	check.Actual = formatInt(count)
	check.EventPosition = lastPosition
	check.Passed = count == expectation.Equals
	return check
}

func checkToolInput(evidence BrowserEvidence, _ json.RawMessage, expectation probe.ScenarioV2Expectation) Check {
	check := newBrowserCheck()
	check.Expected = SafeJSON(json.RawMessage(expectation.InputJSON))
	invocation, found := latestInvocation(evidence, expectation.Name)
	if !found {
		check.Actual = Missing
		return check
	}
	check.Actual = SafeJSON(invocation.Input)
	check.EventPosition = invocationPosition(invocation)
	check.ToolRef = invocation.ToolRef
	check.Passed = SemanticJSONEqual(invocation.Input, json.RawMessage(expectation.InputJSON))
	return check
}

func checkToolResultPath(evidence BrowserEvidence, _ json.RawMessage, expectation probe.ScenarioV2Expectation) Check {
	check := newBrowserCheck()
	check.Expected = SafeJSON(expectation.Value)
	check.Path = expectation.Path
	invocation, found := latestInvocation(evidence, expectation.Name)
	if !found {
		check.Actual = Missing
		return check
	}
	value, err := JSONPathValue(invocation.Output, expectation.Path)
	check.EventPosition = invocationPosition(invocation)
	check.ToolRef = invocation.ToolRef
	if err != nil {
		check.Actual = Missing
		check.ErrorCode = jsonPathNotFoundCode
		return check
	}
	check.Actual = SafeJSON(value)
	check.Passed = SemanticJSONEqual(value, expectation.Value)
	return check
}

func checkToolStatus(evidence BrowserEvidence, _ json.RawMessage, expectation probe.ScenarioV2Expectation) Check {
	check := newBrowserCheck()
	check.Expected = expectation.Status
	invocation, found := latestInvocation(evidence, expectation.Name)
	if !found {
		check.Actual = Missing
		return check
	}
	check.Actual = FirstNonEmpty(invocation.State, Missing)
	check.EventPosition = invocationPosition(invocation)
	check.ToolRef = invocation.ToolRef
	check.Passed = strings.EqualFold(invocation.State, expectation.Status)
	return check
}

func checkNoPendingInvocations(evidence BrowserEvidence, _ json.RawMessage, _ probe.ScenarioV2Expectation) Check {
	pending := 0
	lastPosition := 0
	for _, invocation := range evidence.invocations {
		if !invocation.Terminal {
			pending++
			lastPosition = max(lastPosition, invocationPosition(invocation))
		}
	}
	check := newBrowserCheck()
	check.Expected = "0"
	check.Actual = strconv.Itoa(pending)
	check.EventPosition = lastPosition
	check.Passed = pending == 0
	return check
}

func checkApproval(evidence BrowserEvidence, _ json.RawMessage, expectation probe.ScenarioV2Expectation) Check {
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
		position = max(position, invocation.ApprovalPos)
	}
	if expectation.ToolRef == "" && evidence.approvalRequested {
		requested = true
	}
	check := newBrowserCheck()
	check.EventPosition = position
	if expectation.Type == probe.ScenarioV2ExpectationApprovalNotRequested {
		check.Expected = "not-requested"
		check.Passed = !requested
	} else {
		check.Expected = "requested"
		check.Passed = requested
	}
	check.Actual = Presence(requested)
	return check
}

func checkStaleToolRejected(evidence BrowserEvidence, _ json.RawMessage, expectation probe.ScenarioV2Expectation) Check {
	check := newBrowserCheck()
	check.Expected = StaleToolRefLabel
	check.Actual = None
	check.EventPosition = evidence.stalePos
	for _, invocation := range evidence.invocations {
		if invocation.ErrorCode != StaleToolRefLabel {
			continue
		}
		if expectation.ToolRef != "" && invocation.ToolRef != expectation.ToolRef {
			continue
		}
		check.Actual = StaleToolRefLabel
		check.ToolRef = invocation.ToolRef
		check.EventPosition = invocationPosition(invocation)
		check.Passed = true
		return check
	}
	if evidence.stale && (expectation.ToolRef == "" || evidence.staleToolRef == "" || evidence.staleToolRef == expectation.ToolRef) {
		check.Actual = StaleToolRefLabel
		check.ToolRef = evidence.staleToolRef
		check.Passed = true
	}
	return check
}

func selectedTarget(evidence BrowserEvidence) (persistedTarget, bool) {
	if evidence.selectedTarget == "" {
		return persistedTarget{}, false
	}
	for key, target := range evidence.targets {
		if strings.HasSuffix(key, "\x00"+evidence.selectedTarget) {
			return target, true
		}
	}
	return persistedTarget{}, false
}

func latestInvocation(evidence BrowserEvidence, name string) (*persistedInvocation, bool) {
	for index := len(evidence.invocations) - 1; index >= 0; index-- {
		if evidence.invocations[index].Name == name {
			return evidence.invocations[index], true
		}
	}
	return nil, false
}

func invocationPosition(invocation *persistedInvocation) int {
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

func catalogExpectation(expectation probe.ScenarioV2Expectation) string {
	if expectation.Type == probe.ScenarioV2ExpectationToolCatalogNotContains {
		return "tool " + expectation.Name + " absent"
	}
	return "tool " + expectation.Name + " present"
}
