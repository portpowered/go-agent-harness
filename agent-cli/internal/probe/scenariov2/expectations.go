package scenariov2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/scenariov2/internal/objective"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

const (
	errorPlaceholder = "<error>"
	canceledLabel    = "canceled"
)

// outcome is one live expectation evaluation.
type outcome struct {
	passed   bool
	expected string
	actual   string
	err      error
}

type expectationEvaluator func(*executor, context.Context, probe.ScenarioV2Expectation) outcome

func expectationEvaluators() map[probe.ScenarioV2ExpectationType]expectationEvaluator {
	return map[probe.ScenarioV2ExpectationType]expectationEvaluator{
		probe.ScenarioV2ExpectationBrowserCountEquals:              (*executor).evalBrowserCount,
		probe.ScenarioV2ExpectationEligibleTabCountEquals:          (*executor).evalEligibleTabCount,
		probe.ScenarioV2ExpectationSelectedTabEquals:               (*executor).evalSelectedTab,
		probe.ScenarioV2ExpectationSelectedOriginEquals:            (*executor).evalSelectedOrigin,
		probe.ScenarioV2ExpectationCatalogGenerationEquals:         (*executor).evalCatalogGeneration,
		probe.ScenarioV2ExpectationToolCatalogContains:             (*executor).evalCatalogMembership,
		probe.ScenarioV2ExpectationToolCatalogNotContains:          (*executor).evalCatalogMembership,
		probe.ScenarioV2ExpectationToolSchemaEquals:                (*executor).evalToolSchema,
		probe.ScenarioV2ExpectationToolInvocationCount:             (*executor).evalInvocationCount,
		probe.ScenarioV2ExpectationToolInputJSONEquals:             (*executor).evalToolInput,
		probe.ScenarioV2ExpectationToolResultJSONPathEquals:        (*executor).evalToolResultPath,
		probe.ScenarioV2ExpectationToolStatusEquals:                (*executor).evalToolStatus,
		probe.ScenarioV2ExpectationChromeOperationOrder:            (*executor).evalObservedOrder,
		probe.ScenarioV2ExpectationNoUnexpectedChromeOperations:    (*executor).evalObservedOrder,
		probe.ScenarioV2ExpectationGeneratedCDPMethodOrder:         (*executor).evalObservedOrder,
		probe.ScenarioV2ExpectationNoUnexpectedGeneratedCDPMethods: (*executor).evalObservedOrder,
		probe.ScenarioV2ExpectationNoPendingInvocations:            (*executor).evalNoPendingInvocations,
		probe.ScenarioV2ExpectationPageStateEquals:                 (*executor).evalPageState,
		probe.ScenarioV2ExpectationTranscriptContains:              (*executor).evalTranscript,
		probe.ScenarioV2ExpectationResponseCanceled:                (*executor).evalResponseCanceled,
		probe.ScenarioV2ExpectationStaleToolRejected:               (*executor).evalStaleToolRejected,
		probe.ScenarioV2ExpectationBrowserConnectionClosed:         (*executor).evalConnectionClosed,
		probe.ScenarioV2ExpectationApprovalRequested:               (*executor).evalApproval,
		probe.ScenarioV2ExpectationApprovalNotRequested:            (*executor).evalApproval,
		probe.ScenarioV2ExpectationAssistantAudioStarted:           (*executor).evalProviderAudio,
		probe.ScenarioV2ExpectationAssistantAudioStopped:           (*executor).evalProviderAudio,
	}
}

func (e *executor) populateResult(ctx context.Context, result Result) Result {
	if e == nil {
		return result
	}
	result.StepCount = len(e.scenario.Steps)
	result.Steps = e.scenario.Steps
	result.Expectations = e.scenario.Expectations
	result.Ticks = probe.LogicalTime(e.clock.MonotonicMillis())
	result.Frames = len(e.invocations)
	if e.provider != nil {
		result.Ticks = e.provider.ObservedTick
		result.Frames = e.provider.FrameCount
		result.TerminalReason = e.provider.TerminalReason
		result.TerminalProvenance = e.provider.TerminalProvenance
		result.OutputState = e.provider.OutputState
		result.InputDropCount = e.provider.InputDrops
		result.OutputDropCount = e.provider.OutputDrops
	}
	var divergence *Divergence
	result.ExpectationResults, divergence = e.evaluateExpectations(ctx)
	if divergence != nil {
		result.Divergence = divergence
		if result.Error == "" {
			result.Error = divergence.Error()
		}
	}
	result.Pass = result.Error == ""
	for _, expectation := range result.ExpectationResults {
		if !expectation.Passed {
			result.Pass = false
			break
		}
	}
	return result
}

func (e *executor) evaluateExpectations(ctx context.Context) ([]ExpectationResult, *Divergence) {
	results := make([]ExpectationResult, 0, len(e.scenario.Expectations))
	var firstDivergence *Divergence
	evidence := e.persistedBrowserEvidence()
	for index, expectation := range e.scenario.Expectations {
		evaluated := e.evaluateExpectation(ctx, expectation)
		result := ExpectationResult{Index: index, Type: expectation.Type, Passed: evaluated.passed, Expected: evaluated.expected, Actual: evaluated.actual}
		switch {
		case evaluated.passed:
			result.Error = objective.SafeError(evaluated.err)
		case firstDivergence == nil:
			firstDivergence = objective.DivergenceForExpectation(e.scenario, index, expectation, evaluated.expected, evaluated.actual, evaluated.err, evidence)
			result.Error = firstDivergence.Error()
		case evaluated.err != nil:
			result.Error = objective.SafeError(evaluated.err)
		default:
			result.Error = "expectation not satisfied"
		}
		results = append(results, result)
	}
	return results, firstDivergence
}

func (e *executor) evaluateExpectation(ctx context.Context, expectation probe.ScenarioV2Expectation) outcome {
	evaluate, ok := expectationEvaluators()[expectation.Type]
	if !ok {
		return outcome{expected: string(expectation.Type), actual: objective.Unsupported, err: fmt.Errorf("unsupported probe.scenario.v2 expectation %q", expectation.Type)}
	}
	return evaluate(e, ctx, expectation)
}

func countOutcome(expectation probe.ScenarioV2Expectation, actual int64) outcome {
	return outcome{passed: actual == expectation.Equals, expected: strconv.FormatInt(expectation.Equals, 10), actual: strconv.FormatInt(actual, 10)}
}

func (e *executor) evalBrowserCount(_ context.Context, expectation probe.ScenarioV2Expectation) outcome {
	return countOutcome(expectation, int64(len(e.discovered)))
}

func (e *executor) evalEligibleTabCount(ctx context.Context, expectation probe.ScenarioV2Expectation) outcome {
	var eligible int64
	if e.broker == nil {
		return countOutcome(expectation, eligible)
	}
	for _, candidate := range e.discovered {
		targets, err := e.broker.ListTargets(context.WithoutCancel(ctx), webmcp.BrowserSelector{BrowserID: candidate.ID})
		if err != nil {
			return outcome{expected: strconv.FormatInt(expectation.Equals, 10), actual: errorPlaceholder, err: err}
		}
		for _, target := range targets {
			if target.Eligible {
				eligible++
			}
		}
	}
	return countOutcome(expectation, eligible)
}

func (e *executor) evalSelectedTab(_ context.Context, expectation probe.ScenarioV2Expectation) outcome {
	return outcome{passed: e.selected.Key.TargetID == webmcp.TargetID(expectation.TargetID), expected: expectation.TargetID, actual: string(e.selected.Key.TargetID)}
}

func (e *executor) evalSelectedOrigin(_ context.Context, expectation probe.ScenarioV2Expectation) outcome {
	return outcome{passed: e.selected.Origin == expectation.Origin, expected: objective.SafeURL(expectation.Origin), actual: objective.SafeURL(e.selected.Origin)}
}

func (e *executor) evalCatalogGeneration(_ context.Context, expectation probe.ScenarioV2Expectation) outcome {
	generation := e.selected.Generation
	if e.hasCatalog {
		generation = e.catalog.Generation
	}
	return outcome{passed: int64(generation) == expectation.Generation, expected: strconv.FormatInt(expectation.Generation, 10), actual: strconv.FormatUint(generation, 10)}
}

func (e *executor) evalObservedOrder(_ context.Context, expectation probe.ScenarioV2Expectation) outcome {
	evidence := e.persistedBrowserEvidence()
	if evidence == nil {
		if expectation.Operations != nil {
			return outcome{expected: objective.StringList(expectation.Operations), actual: objective.Missing}
		}
		return outcome{expected: objective.StringList(expectation.Methods), actual: objective.Missing}
	}
	check := objective.BrowserCheck(*evidence, nil, expectation)
	return outcome{passed: check.Passed, expected: check.Expected, actual: check.Actual}
}

func (e *executor) evalNoPendingInvocations(context.Context, probe.ScenarioV2Expectation) outcome {
	pending := 0
	if e.runtime != nil {
		pending += len(e.runtime.PendingInvocationIDs())
	}
	if e.broker != nil {
		pending += len(e.broker.PendingInvocations())
	}
	return outcome{passed: pending == 0, expected: "0", actual: strconv.Itoa(pending)}
}

func (e *executor) evalPageState(_ context.Context, expectation probe.ScenarioV2Expectation) outcome {
	state, available := e.currentPageState()
	if !available && e.mode == BrowserExecutorReal {
		return outcome{expected: objective.SafeJSON(expectation.Value), actual: objective.Unavailable, err: newBrowserExecutorError(e.mode, phasePageState, webmcp.ErrorUnsupportedWebMCP, errors.New("real browser runtime does not provide an independent page-state oracle"))}
	}
	actual, err := objective.JSONPathValue(state, expectation.Path)
	if err != nil {
		return outcome{expected: objective.SafeJSON(expectation.Value), actual: objective.Missing, err: err}
	}
	return outcome{passed: objective.SemanticJSONEqual(actual, expectation.Value), expected: objective.SafeJSON(expectation.Value), actual: objective.SafeJSON(actual)}
}

// currentPageState returns the captured page-state oracle value, or JSON null
// with available=false when no oracle observed the page.
func (e *executor) currentPageState() (json.RawMessage, bool) {
	if e.pageStateSet {
		return e.pageState, true
	}
	if e.runtime != nil && len(e.runtime.PageState()) > 0 {
		return e.runtime.PageState(), true
	}
	return json.RawMessage(`null`), false
}

func (e *executor) evalTranscript(_ context.Context, expectation probe.ScenarioV2Expectation) outcome {
	actual := ""
	if e.provider != nil {
		actual = e.provider.Transcript
	}
	present := strings.Contains(actual, expectation.Text)
	return outcome{passed: present, expected: objective.TextPresent, actual: objective.SafeText(present)}
}

func (e *executor) evalResponseCanceled(context.Context, probe.ScenarioV2Expectation) outcome {
	for _, current := range e.invocations {
		if current.Result.State == webmcp.InvocationCanceled || strings.EqualFold(string(current.Result.State), canceledLabel) {
			return outcome{passed: true, expected: canceledLabel, actual: string(current.Result.State)}
		}
	}
	if e.provider != nil && e.provider.HasResponseCancel {
		return outcome{passed: true, expected: canceledLabel, actual: "provider_response_cancel"}
	}
	return outcome{expected: canceledLabel, actual: objective.None}
}

func (e *executor) evalConnectionClosed(context.Context, probe.ScenarioV2Expectation) outcome {
	return outcome{passed: e.closed, expected: "closed", actual: strconv.FormatBool(e.closed)}
}

func (e *executor) evalApproval(_ context.Context, expectation probe.ScenarioV2Expectation) outcome {
	evidence := e.persistedBrowserEvidence()
	if evidence == nil {
		return outcome{expected: "approval evidence", actual: objective.Missing}
	}
	check := objective.BrowserCheck(*evidence, nil, expectation)
	return outcome{passed: check.Passed, expected: check.Expected, actual: check.Actual}
}

func (e *executor) evalProviderAudio(_ context.Context, expectation probe.ScenarioV2Expectation) outcome {
	check := objective.ProviderCheck(*e.providerReport, expectation)
	return outcome{passed: check.Passed, expected: check.Expected, actual: check.Actual}
}
