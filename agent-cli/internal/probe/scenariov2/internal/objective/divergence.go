// Package objective derives probe.scenario.v2 objective verdicts and redacted
// divergence diagnostics from persisted browser, page-state, and provider
// evidence. It performs no I/O: callers hand it decoded artifacts.
package objective

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

// Evidence artifact paths inside a finalized v2 recording bundle.
const (
	ProviderArtifactPath  = "provider.capture.json"
	PageStateArtifactPath = "page-state.json"
	WorkspaceArtifactPath = "workspace.snapshot.json"
	ObjectiveArtifactPath = "objective.evidence.json"
)

// Structural placeholders used in redacted expected/actual summaries.
const (
	Missing     = "<missing>"
	Unavailable = "<unavailable>"
	Unsupported = "unsupported"
	None        = "<none>"
)

// DivergenceClass is the stable class of every typed expectation divergence.
const DivergenceClass = "browser_expectation_divergence"

const (
	unsupportedExpectationCode = "unsupported_expectation"
	jsonPathNotFoundCode       = "jsonpath_not_found"
)

// Divergence is the stable, redacted explanation attached to a failed typed
// expectation. Expected and Actual are deliberately structural summaries:
// browser arguments, tool results, endpoint credentials, and raw CDP payloads
// never belong in a run result or objective artifact.
type Divergence struct {
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

func (d *Divergence) Error() string {
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

// Check is one observation of an expectation against captured evidence.
type Check struct {
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

// MakeDivergence projects a failed check onto the stable divergence shape.
func MakeDivergence(
	scenario probe.ScenarioV2,
	index int,
	expectation probe.ScenarioV2Expectation,
	check Check,
) *Divergence {
	artifact := check.EvidenceArtifact
	if artifact == "" {
		artifact = ExpectationArtifact(expectation.Type)
	}
	return &Divergence{
		Class:             DivergenceClass,
		ScenarioID:        scenario.ID,
		ExpectationType:   expectation.Type,
		ExpectationIndex:  index,
		EvidenceArtifact:  artifact,
		Path:              FirstNonEmpty(check.Path, expectation.Path, expectation.JSONPath),
		Expected:          FirstNonEmpty(check.Expected, Unavailable),
		Actual:            FirstNonEmpty(check.Actual, Unavailable),
		EventPosition:     check.EventPosition,
		OperationPosition: check.OperationPosition,
		ErrorCode:         FirstNonEmpty(check.ErrorCode),
		ToolName:          FirstNonEmpty(check.ToolName, expectation.Name),
		ToolRef:           FirstNonEmpty(check.ToolRef, expectation.ToolRef),
		Generation:        check.Generation,
	}
}

// FirstNonEmpty returns the first value that is not blank.
func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// ExpectationArtifact names the bundle artifact that proves an expectation.
func ExpectationArtifact(kind probe.ScenarioV2ExpectationType) string {
	if IsProviderObjective(kind) {
		return ProviderArtifactPath
	}
	if kind == probe.ScenarioV2ExpectationPageStateEquals {
		return PageStateArtifactPath
	}
	return transcript.BrowserArtifactDefaultPath
}

// DivergenceEqual reports whether two divergences serialize identically.
func DivergenceEqual(left, right *Divergence) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

// CheckFromError builds the failed check for a live expectation evaluation.
func CheckFromError(expectation probe.ScenarioV2Expectation, expected, actual string, err error) Check {
	return Check{
		Passed:    false,
		Expected:  expected,
		Actual:    actual,
		Path:      FirstNonEmpty(expectation.Path, expectation.JSONPath),
		ErrorCode: SafeError(err),
	}
}

// DivergenceForExpectation explains a failed live expectation, preferring the
// persisted browser evidence positions when the expectation is observable
// from the browser event stream.
func DivergenceForExpectation(
	scenario probe.ScenarioV2,
	index int,
	expectation probe.ScenarioV2Expectation,
	expected, actual string,
	err error,
	evidence *BrowserEvidence,
) *Divergence {
	check := CheckFromError(expectation, expected, actual, err)
	check.EvidenceArtifact = ExpectationArtifact(expectation.Type)
	if evidence != nil && IsBrowserObjective(expectation.Type) && expectation.Type != probe.ScenarioV2ExpectationPageStateEquals {
		check = mergeObservedCheck(BrowserCheck(*evidence, nil, expectation), check)
	}
	return MakeDivergence(scenario, index, expectation, check)
}

func mergeObservedCheck(observed, fallback Check) Check {
	if observed.EvidenceArtifact == "" {
		observed.EvidenceArtifact = fallback.EvidenceArtifact
	}
	if observed.Expected == "" {
		observed.Expected = fallback.Expected
	}
	if observed.Actual == "" {
		observed.Actual = fallback.Actual
	}
	if observed.ErrorCode == "" {
		observed.ErrorCode = fallback.ErrorCode
	}
	return observed
}
