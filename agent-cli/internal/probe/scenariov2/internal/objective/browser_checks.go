package objective

import (
	"encoding/json"
	"strconv"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

type browserCheck func(BrowserEvidence, json.RawMessage, probe.ScenarioV2Expectation) Check

// browserChecks maps every browser objective to its evidence check. The key
// set is also the definition of a browser objective.
func browserChecks() map[probe.ScenarioV2ExpectationType]browserCheck {
	return map[probe.ScenarioV2ExpectationType]browserCheck{
		probe.ScenarioV2ExpectationBrowserCountEquals:              checkBrowserCount,
		probe.ScenarioV2ExpectationEligibleTabCountEquals:          checkEligibleTabCount,
		probe.ScenarioV2ExpectationSelectedTabEquals:               checkSelectedTab,
		probe.ScenarioV2ExpectationSelectedOriginEquals:            checkSelectedOrigin,
		probe.ScenarioV2ExpectationCatalogGenerationEquals:         checkCatalogGeneration,
		probe.ScenarioV2ExpectationToolCatalogContains:             checkCatalogMembership,
		probe.ScenarioV2ExpectationToolCatalogNotContains:          checkCatalogMembership,
		probe.ScenarioV2ExpectationToolSchemaEquals:                checkToolSchema,
		probe.ScenarioV2ExpectationToolInvocationCount:             checkInvocationCount,
		probe.ScenarioV2ExpectationToolInputJSONEquals:             checkToolInput,
		probe.ScenarioV2ExpectationToolResultJSONPathEquals:        checkToolResultPath,
		probe.ScenarioV2ExpectationToolStatusEquals:                checkToolStatus,
		probe.ScenarioV2ExpectationChromeOperationOrder:            checkOperationOrder,
		probe.ScenarioV2ExpectationNoUnexpectedChromeOperations:    checkOperationsAllowed,
		probe.ScenarioV2ExpectationGeneratedCDPMethodOrder:         checkMethodOrder,
		probe.ScenarioV2ExpectationNoUnexpectedGeneratedCDPMethods: checkMethodsAllowed,
		probe.ScenarioV2ExpectationNoPendingInvocations:            checkNoPendingInvocations,
		probe.ScenarioV2ExpectationPageStateEquals:                 checkPageState,
		probe.ScenarioV2ExpectationResponseCanceled:                checkResponseCanceled,
		probe.ScenarioV2ExpectationApprovalRequested:               checkApproval,
		probe.ScenarioV2ExpectationApprovalNotRequested:            checkApproval,
		probe.ScenarioV2ExpectationStaleToolRejected:               checkStaleToolRejected,
		probe.ScenarioV2ExpectationBrowserConnectionClosed:         checkConnectionClosed,
	}
}

// IsBrowserObjective reports whether kind is verified from browser evidence.
func IsBrowserObjective(kind probe.ScenarioV2ExpectationType) bool {
	_, ok := browserChecks()[kind]
	return ok
}

// BrowserCheck evaluates one browser objective against persisted evidence.
func BrowserCheck(evidence BrowserEvidence, pageState json.RawMessage, expectation probe.ScenarioV2Expectation) Check {
	check, ok := browserChecks()[expectation.Type]
	if !ok {
		return Check{
			EvidenceArtifact: transcript.BrowserArtifactDefaultPath,
			Expected:         "supported browser objective",
			Actual:           Unsupported,
			ErrorCode:        unsupportedExpectationCode,
		}
	}
	return check(evidence, pageState, expectation)
}

func newBrowserCheck() Check {
	return Check{EvidenceArtifact: transcript.BrowserArtifactDefaultPath}
}

func formatInt(value int64) string {
	return strconv.FormatInt(value, 10)
}

func countCheck(check Check, expectation probe.ScenarioV2Expectation, actual int64, present bool) Check {
	check.Expected = formatInt(expectation.Equals)
	if !present {
		check.Actual = Missing
		return check
	}
	check.Actual = formatInt(actual)
	check.Passed = actual == expectation.Equals
	return check
}

func checkBrowserCount(evidence BrowserEvidence, _ json.RawMessage, expectation probe.ScenarioV2Expectation) Check {
	check := newBrowserCheck()
	check.EventPosition = evidence.browserCountPos
	return countCheck(check, expectation, evidence.browserCount, evidence.browserCountSet)
}

func checkEligibleTabCount(evidence BrowserEvidence, _ json.RawMessage, expectation probe.ScenarioV2Expectation) Check {
	var count int64
	lastPosition := 0
	for _, target := range evidence.targets {
		if target.Eligible {
			count++
		}
		lastPosition = max(lastPosition, target.Position)
	}
	check := newBrowserCheck()
	check.EventPosition = lastPosition
	return countCheck(check, expectation, count, len(evidence.targets) > 0 || expectation.Equals == 0)
}

func checkSelectedTab(evidence BrowserEvidence, _ json.RawMessage, expectation probe.ScenarioV2Expectation) Check {
	check := newBrowserCheck()
	check.Expected = expectation.TargetID
	check.Actual = FirstNonEmpty(evidence.selectedTarget, Missing)
	check.EventPosition = evidence.selectedTargetPos
	check.Passed = evidence.selectedTarget == expectation.TargetID
	return check
}

func checkSelectedOrigin(evidence BrowserEvidence, _ json.RawMessage, expectation probe.ScenarioV2Expectation) Check {
	check := newBrowserCheck()
	check.Expected = SafeURL(expectation.Origin)
	target, ok := selectedTarget(evidence)
	if !ok {
		check.Actual = Missing
		return check
	}
	check.Actual = FirstNonEmpty(SafeURL(target.Origin), Missing)
	check.EventPosition = target.Position
	check.Passed = target.Origin == expectation.Origin
	return check
}

func checkCatalogGeneration(evidence BrowserEvidence, _ json.RawMessage, expectation probe.ScenarioV2Expectation) Check {
	check := newBrowserCheck()
	check.Expected = formatInt(expectation.Generation)
	check.EventPosition = evidence.catalogPos
	check.Generation = evidence.catalogGeneration
	if !evidence.catalogSet {
		check.Actual = Missing
		return check
	}
	check.Actual = formatInt(evidence.catalogGeneration)
	check.Passed = evidence.catalogGeneration == expectation.Generation
	return check
}

func checkCatalogMembership(evidence BrowserEvidence, _ json.RawMessage, expectation probe.ScenarioV2Expectation) Check {
	check := newBrowserCheck()
	tool, found := evidence.catalog[expectation.Name]
	check.Expected = catalogExpectation(expectation)
	check.Actual = Presence(found)
	if found {
		check.EventPosition = tool.Position
	}
	check.Passed = found == (expectation.Type == probe.ScenarioV2ExpectationToolCatalogContains)
	return check
}

func checkToolSchema(evidence BrowserEvidence, _ json.RawMessage, expectation probe.ScenarioV2Expectation) Check {
	check := newBrowserCheck()
	check.Expected = SafeJSON(expectation.Schema)
	tool, found := evidence.catalog[expectation.Name]
	if !found {
		check.Actual = Missing
		return check
	}
	check.Actual = SafeJSON(tool.InputSchema)
	check.EventPosition = tool.Position
	check.Passed = SemanticJSONEqual(tool.InputSchema, expectation.Schema)
	return check
}

func checkOperationOrder(evidence BrowserEvidence, _ json.RawMessage, expectation probe.ScenarioV2Expectation) Check {
	return OrderedCheck(evidence.operations, expectation.Operations)
}

func checkOperationsAllowed(evidence BrowserEvidence, _ json.RawMessage, expectation probe.ScenarioV2Expectation) Check {
	return AllowedCheck(evidence.operations, expectation.Operations)
}

func checkMethodOrder(evidence BrowserEvidence, _ json.RawMessage, expectation probe.ScenarioV2Expectation) Check {
	return OrderedCheck(evidence.methods, expectation.Methods)
}

func checkMethodsAllowed(evidence BrowserEvidence, _ json.RawMessage, expectation probe.ScenarioV2Expectation) Check {
	return AllowedCheck(evidence.methods, expectation.Methods)
}

func checkPageState(_ BrowserEvidence, pageState json.RawMessage, expectation probe.ScenarioV2Expectation) Check {
	check := Check{EvidenceArtifact: PageStateArtifactPath}
	check.Path = expectation.Path
	check.Expected = SafeJSON(expectation.Value)
	value, err := JSONPathValue(pageState, expectation.Path)
	if err != nil {
		check.Actual = Missing
		check.ErrorCode = jsonPathNotFoundCode
		return check
	}
	check.Actual = SafeJSON(value)
	check.Passed = SemanticJSONEqual(value, expectation.Value)
	return check
}

func checkResponseCanceled(evidence BrowserEvidence, _ json.RawMessage, _ probe.ScenarioV2Expectation) Check {
	check := newBrowserCheck()
	check.Expected = "canceled"
	check.Actual = Presence(evidence.canceled)
	check.EventPosition = evidence.canceledPos
	check.Passed = evidence.canceled
	return check
}

func checkConnectionClosed(evidence BrowserEvidence, _ json.RawMessage, _ probe.ScenarioV2Expectation) Check {
	check := newBrowserCheck()
	check.Expected = "closed"
	check.Actual = Presence(evidence.closed)
	check.EventPosition = evidence.closedPos
	check.Passed = evidence.closed
	return check
}
