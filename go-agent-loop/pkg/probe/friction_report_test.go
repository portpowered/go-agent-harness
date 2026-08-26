package probe

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func reportInput(name string, lines ...string) FrictionReportInput {
	return FrictionReportInput{Name: name, Reader: strings.NewReader(strings.Join(lines, "\n"))}
}

func reportResult(name string, pass bool, terminalReason, message string) string {
	return reportResultWithExpectations(name, pass, terminalReason, message)
}

func reportResultWithExpectations(name string, pass bool, terminalReason, message string, outcomes ...ScenarioExpectationOutcome) string {
	result := ScenarioResult{
		Name:                        name,
		Pass:                        pass,
		ScenarioExpectationOutcomes: outcomes,
		TerminalReason:              terminalReason,
		Error:                       message,
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

const reportSummary = `{"total":2,"passed":1,"failed":1,"status":"fail"}`

func TestAggregateFrictionReportRollsUpResultsAndSkipsSummaries(t *testing.T) {
	report, err := AggregateFrictionReport(
		reportInput("run-b.jsonl",
			reportResult("checkout", true, "disconnect", ""),
			reportResult("checkout", false, "error:authentication", "authentication failed"),
			reportSummary,
		),
		reportInput("run-a.jsonl",
			reportResult("search", false, "error:rate_limited", ""),
			reportResult("checkout", true, "disconnect", ""),
		),
	)
	if err != nil {
		t.Fatalf("AggregateFrictionReport failed: %v", err)
	}
	if report.Total != 4 || report.Passed != 2 || report.Failed != 2 || report.Stuck != 0 {
		t.Fatalf("unexpected totals: %+v", report)
	}
	wantScenarios := []ScenarioRollup{
		{Name: "checkout", Total: 3, Passed: 2, Failed: 1},
		{Name: "search", Total: 1, Failed: 1},
	}
	if !reflect.DeepEqual(report.Scenarios, wantScenarios) {
		t.Fatalf("scenarios = %#v, want %#v", report.Scenarios, wantScenarios)
	}
	wantReasons := []TerminalReasonCount{
		{Reason: "disconnect", Count: 2},
		{Reason: "error:authentication", Count: 1},
		{Reason: "error:rate_limited", Count: 1},
	}
	if !reflect.DeepEqual(report.TerminalReasons, wantReasons) {
		t.Fatalf("terminal reasons = %#v, want %#v", report.TerminalReasons, wantReasons)
	}
	wantClasses := []ErrorClassCount{
		{Class: "authentication", Count: 1},
		{Class: "rate_limited", Count: 1},
	}
	if !reflect.DeepEqual(report.ErrorClasses, wantClasses) {
		t.Fatalf("error classes = %#v, want %#v", report.ErrorClasses, wantClasses)
	}
}

func TestAggregateFrictionReportStuckIsASeparateFailureBucket(t *testing.T) {
	report, err := AggregateFrictionReport(reportInput("stuck.jsonl",
		reportResult("waiting", true, StuckTerminalReason, ""),
		reportResult("waiting", false, StuckTerminalReason, "dead-session guard"),
	))
	if err != nil {
		t.Fatalf("AggregateFrictionReport failed: %v", err)
	}
	if report.Total != 2 || report.Passed != 0 || report.Failed != 2 || report.Stuck != 2 {
		t.Fatalf("stuck totals = %+v, want total=2 passed=0 failed=2 stuck=2", report)
	}
	if got := report.Scenarios[0]; got.Total != 2 || got.Passed != 0 || got.Failed != 2 || got.Stuck != 2 {
		t.Fatalf("stuck scenario rollup = %+v", got)
	}
}

func TestAggregateFrictionReportIncludesExpectationMissesAndTopFrictions(t *testing.T) {
	report, err := AggregateFrictionReport(reportInput("mixed.jsonl",
		reportResultWithExpectations("healthy", true, "disconnect", "", ScenarioExpectationOutcome{
			Kind:   ExpectFrameCount,
			Passed: true,
		}),
		reportResultWithExpectations("transcript-miss", false, "error:transport", "connection reset",
			ScenarioExpectationOutcome{Kind: ExpectTranscriptContains, Passed: false, Expected: "ready", Actual: ""},
			ScenarioExpectationOutcome{Kind: ExpectFrameCount, Passed: false, Expected: "2", Actual: "0"},
		),
		reportResultWithExpectations("transcript-miss", false, "disconnect", "",
			ScenarioExpectationOutcome{Kind: ExpectTranscriptContains, Passed: false, Expected: "ready", Actual: "busy"},
		),
		reportResult("stuck-session", true, StuckTerminalReason, ""),
	))
	if err != nil {
		t.Fatalf("AggregateFrictionReport failed: %v", err)
	}

	wantMisses := []ExpectationMissCount{
		{Kind: ExpectFrameCount, Count: 1, Scenarios: []string{"transcript-miss"}},
		{Kind: ExpectTranscriptContains, Count: 2, Scenarios: []string{"transcript-miss"}},
	}
	if !reflect.DeepEqual(report.ExpectationMisses, wantMisses) {
		t.Fatalf("expectation misses = %#v, want %#v", report.ExpectationMisses, wantMisses)
	}

	if len(report.TopFrictions) != 6 {
		t.Fatalf("top frictions = %#v, want six categories", report.TopFrictions)
	}
	wantTop := []TopFriction{
		{Category: FrictionCategoryExpectation, Key: string(ExpectTranscriptContains), Count: 2, Scenarios: []string{"transcript-miss"}},
		{Category: FrictionCategoryErrorClass, Key: "transport", Count: 1, Scenarios: []string{"transcript-miss"}},
		{Category: FrictionCategoryExpectation, Key: string(ExpectFrameCount), Count: 1, Scenarios: []string{"transcript-miss"}},
		{Category: FrictionCategoryStuck, Key: StuckTerminalReason, Count: 1, Scenarios: []string{"stuck-session"}},
		{Category: FrictionCategoryTerminalReason, Key: "disconnect", Count: 1, Scenarios: []string{"transcript-miss"}},
		{Category: FrictionCategoryTerminalReason, Key: "error:transport", Count: 1, Scenarios: []string{"transcript-miss"}},
	}
	if !reflect.DeepEqual(report.TopFrictions, wantTop) {
		t.Fatalf("top frictions = %#v, want %#v", report.TopFrictions, wantTop)
	}
}

func TestAggregateFrictionReportUsesLexicographicTieBreaks(t *testing.T) {
	buildInputs := func() []FrictionReportInput {
		return []FrictionReportInput{
			reportInput("ties.jsonl",
				reportResult("zeta", false, "z-reason", "z failure"),
				reportResult("alpha", false, "a-reason", "a failure"),
				reportResult("middle", false, "m-reason", "m failure"),
			),
		}
	}
	first, err := AggregateFrictionReport(buildInputs()...)
	if err != nil {
		t.Fatalf("first aggregate failed: %v", err)
	}
	second, err := AggregateFrictionReport(buildInputs()...)
	if err != nil {
		t.Fatalf("second aggregate failed: %v", err)
	}
	firstBytes, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal first: %v", err)
	}
	secondBytes, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("marshal second: %v", err)
	}
	if string(firstBytes) != string(secondBytes) {
		t.Fatalf("same input changed JSON bytes:\n%s\n%s", firstBytes, secondBytes)
	}
	if got := first.Scenarios[0].Name; got != "alpha" {
		t.Fatalf("scenario order starts with %q, want alpha", got)
	}
	if got := first.TerminalReasons[0].Reason; got != "a-reason" {
		t.Fatalf("terminal reason order starts with %q, want a-reason", got)
	}
	if got := first.ErrorClasses[0].Class; got != "unknown" {
		t.Fatalf("error class order starts with %q, want unknown", got)
	}
}

func TestAggregateFrictionReportEmptyInputIsValid(t *testing.T) {
	for _, inputs := range [][]FrictionReportInput{
		nil,
		{reportInput("empty.jsonl", "", "  \t")},
	} {
		report, err := AggregateFrictionReport(inputs...)
		if err != nil {
			t.Fatalf("empty aggregate failed: %v", err)
		}
		if report.Total != 0 || report.Passed != 0 || report.Failed != 0 || report.Stuck != 0 {
			t.Fatalf("empty totals = %+v", report)
		}
		if report.Scenarios == nil || report.TerminalReasons == nil || report.ErrorClasses == nil ||
			report.ExpectationMisses == nil || report.TopFrictions == nil {
			t.Fatalf("empty report contains nil collections: %+v", report)
		}
	}
}

func TestAggregateFrictionReportMalformedLineHasSourceAndLine(t *testing.T) {
	_, err := AggregateFrictionReport(reportInput("broken.jsonl", reportResult("ok", true, "disconnect", ""), "not json"))
	var reportErr *FrictionReportError
	if !errors.As(err, &reportErr) {
		t.Fatalf("error = %v, want *FrictionReportError", err)
	}
	if reportErr.Source != "broken.jsonl" || reportErr.Line != 2 {
		t.Fatalf("error context = %+v, want broken.jsonl:2", reportErr)
	}
	if !errors.Is(err, ErrMalformedReport) {
		t.Fatalf("error = %v, want ErrMalformedReport", err)
	}
	if !strings.Contains(err.Error(), "broken.jsonl:2") {
		t.Fatalf("error lacks source:line context: %q", err)
	}
}

func TestAggregateFrictionReportMissingReaderIsTyped(t *testing.T) {
	_, err := AggregateFrictionReport(FrictionReportInput{Name: "missing.jsonl"})
	var reportErr *FrictionReportError
	if !errors.As(err, &reportErr) {
		t.Fatalf("error = %v, want *FrictionReportError", err)
	}
	if reportErr.Line != 0 || !errors.Is(err, ErrMissingReportReader) {
		t.Fatalf("error = %+v, want input-level ErrMissingReportReader", reportErr)
	}
}

func TestAggregateFrictionReportRejectsNullPassWithSourceAndLine(t *testing.T) {
	_, err := AggregateFrictionReport(reportInput("null-pass.jsonl", `{"name":"broken","pass":null}`))
	var reportErr *FrictionReportError
	if !errors.As(err, &reportErr) {
		t.Fatalf("error = %v, want *FrictionReportError", err)
	}
	if reportErr.Source != "null-pass.jsonl" || reportErr.Line != 1 {
		t.Fatalf("error context = %+v, want null-pass.jsonl:1", reportErr)
	}
	if !errors.Is(err, ErrMalformedReport) || !strings.Contains(err.Error(), "pass field must be a boolean") {
		t.Fatalf("error = %v, want typed malformed boolean error", err)
	}
}

func TestAggregateFrictionReportClassifiesFallbackErrorMessages(t *testing.T) {
	cases := []struct {
		class   string
		message string
	}{
		{class: "authentication", message: "request was unauthorized"},
		{class: "rate_limited", message: "too many requests"},
		{class: "unsupported_request", message: "unsupported model"},
		{class: "invalid_request", message: "bad request body"},
		{class: "replay_mismatch", message: "replay divergence detected"},
		{class: "replay_incomplete", message: "incomplete replay"},
		{class: "cancellation", message: "request canceled by caller"},
		{class: "transport", message: "network connection reset"},
		{class: "timeout", message: "deadline exceeded"},
		{class: "panic", message: "provider panic"},
		{class: "unknown", message: "unexpected provider failure"},
	}
	results := make([]ScenarioResult, 0, len(cases))
	for _, testCase := range cases {
		results = append(results, ScenarioResult{
			Name:  testCase.class,
			Error: testCase.message,
		})
	}

	report := AggregateScenarioResults(results)
	if report.Total != len(cases) || report.Failed != len(cases) || report.Passed != 0 {
		t.Fatalf("fallback classification totals = %+v", report)
	}
	if len(report.ErrorClasses) != len(cases) {
		t.Fatalf("error classes = %#v, want %d classes", report.ErrorClasses, len(cases))
	}
	wantClasses := make(map[string]struct{}, len(cases))
	for _, testCase := range cases {
		wantClasses[testCase.class] = struct{}{}
	}
	for _, got := range report.ErrorClasses {
		if _, ok := wantClasses[got.Class]; !ok || got.Count != 1 {
			t.Fatalf("unexpected error class = %+v", got)
		}
	}
}

func TestAggregateAliasUsesScenarioAggregation(t *testing.T) {
	report := Aggregate([]ScenarioResult{{Name: "alias", Pass: true}})
	if report.Total != 1 || report.Passed != 1 || report.Failed != 0 || len(report.Scenarios) != 1 {
		t.Fatalf("Aggregate result = %+v", report)
	}
}

func TestFrictionReportErrorNilMethods(t *testing.T) {
	var reportErr *FrictionReportError
	if got := reportErr.Error(); got != "<nil>" {
		t.Fatalf("nil Error() = %q, want <nil>", got)
	}
	if got := reportErr.Unwrap(); got != nil {
		t.Fatalf("nil Unwrap() = %v, want nil", got)
	}
}
