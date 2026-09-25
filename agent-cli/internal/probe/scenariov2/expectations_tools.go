package scenariov2

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/scenariov2/internal/objective"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

func (e *executor) evalCatalogMembership(ctx context.Context, expectation probe.ScenarioV2Expectation) outcome {
	if !e.hasCatalog && e.broker != nil {
		catalog, err := e.broker.ListTools(context.WithoutCancel(ctx), webmcp.ListToolsOptions{IncludeSchemas: true})
		if err != nil {
			return outcome{expected: expectation.Name, actual: errorPlaceholder, err: err}
		}
		e.catalog = catalog
		e.hasCatalog = true
	}
	found := false
	for _, tool := range e.catalog.Tools {
		if tool.Name == expectation.Name {
			found = true
			break
		}
	}
	result := outcome{passed: found, expected: "catalog contains " + expectation.Name, actual: fmt.Sprintf("present=%t", found)}
	if expectation.Type == probe.ScenarioV2ExpectationToolCatalogNotContains {
		result.passed = !found
		result.expected = "catalog does not contain " + expectation.Name
	}
	return result
}

func (e *executor) evalToolSchema(_ context.Context, expectation probe.ScenarioV2Expectation) outcome {
	expected := objective.SafeJSON(expectation.Schema)
	for _, tool := range e.catalog.Tools {
		if tool.Name == expectation.Name {
			return outcome{passed: objective.SemanticJSONEqual(tool.InputSchema, expectation.Schema), expected: expected, actual: objective.SafeJSON(tool.InputSchema)}
		}
	}
	return outcome{expected: expected, actual: objective.Missing}
}

func (e *executor) evalInvocationCount(_ context.Context, expectation probe.ScenarioV2Expectation) outcome {
	var actual int64
	for _, current := range e.invocations {
		if current.Name == expectation.Name {
			actual++
		}
	}
	return outcome{passed: actual == expectation.Equals, expected: strconv.FormatInt(expectation.Equals, 10), actual: strconv.FormatInt(actual, 10)}
}

// latestInvocation returns the most recent invocation of the named tool.
func (e *executor) latestInvocation(name string) (invocation, bool) {
	for index := len(e.invocations) - 1; index >= 0; index-- {
		if e.invocations[index].Name == name {
			return e.invocations[index], true
		}
	}
	return invocation{}, false
}

func (e *executor) evalToolInput(_ context.Context, expectation probe.ScenarioV2Expectation) outcome {
	want := json.RawMessage(expectation.InputJSON)
	current, found := e.latestInvocation(expectation.Name)
	if !found {
		return outcome{expected: objective.SafeJSON(want), actual: objective.Missing}
	}
	return outcome{passed: objective.SemanticJSONEqual(current.Input, want), expected: objective.SafeJSON(want), actual: objective.SafeJSON(current.Input)}
}

func (e *executor) evalToolResultPath(_ context.Context, expectation probe.ScenarioV2Expectation) outcome {
	expected := objective.SafeJSON(expectation.Value)
	current, found := e.latestInvocation(expectation.Name)
	if !found {
		return outcome{expected: expected, actual: objective.Missing}
	}
	value, err := objective.JSONPathValue(current.Result.Output, expectation.Path)
	if err != nil {
		return outcome{expected: expected, actual: objective.Missing, err: err}
	}
	return outcome{passed: objective.SemanticJSONEqual(value, expectation.Value), expected: expected, actual: objective.SafeJSON(value)}
}

func (e *executor) evalToolStatus(_ context.Context, expectation probe.ScenarioV2Expectation) outcome {
	current, found := e.latestInvocation(expectation.Name)
	if !found {
		return outcome{expected: expectation.Status, actual: objective.Missing}
	}
	actual := string(current.Result.State)
	return outcome{passed: strings.EqualFold(actual, expectation.Status), expected: expectation.Status, actual: actual}
}

func (e *executor) evalStaleToolRejected(_ context.Context, expectation probe.ScenarioV2Expectation) outcome {
	for _, current := range e.invocations {
		if isStaleToolError(current.Err) && (expectation.ToolRef == "" || string(current.ToolRef) == expectation.ToolRef) {
			return outcome{passed: true, expected: objective.StaleToolRefLabel, actual: objective.StaleToolRefLabel}
		}
	}
	return outcome{expected: objective.StaleToolRefLabel, actual: objective.None}
}
