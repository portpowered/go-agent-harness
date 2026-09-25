package probe

import (
	"strings"
	"testing"
)

func metricsReconcileExpectation() ExpectedBehavior {
	return ExpectedBehavior{Type: ExpectMetricsReconcile, Kind: ExpectMetricsReconcile}
}

// Exact equality between emitted totals and observed delta sums passes, and a
// series with no activity on either side reconciles at zero.
func TestEvaluateMetricsReconcilePassesOnExactSums(t *testing.T) {
	observation := ObservationSnapshot{
		Metrics: []MetricsSeries{
			{Direction: "input", Modality: "text", ObservedDeltas: 29, ReportedTotal: 29},
			{Direction: "output", Modality: "audio", ObservedDeltas: 6, ReportedTotal: 6},
			{Direction: "output", Modality: "text", ObservedDeltas: 21, ReportedTotal: 21},
			{Direction: "output", Modality: "tool", ObservedDeltas: 16, ReportedTotal: 16},
			{Direction: "input", Modality: "audio", ObservedDeltas: 0, ReportedTotal: 0},
		},
	}
	if err := Evaluate(metricsReconcileExpectation(), observation); err != nil {
		t.Fatalf("exact reconciliation must pass, got %v", err)
	}
}

// Any divergence fails and names the offending series with both values.
func TestEvaluateMetricsReconcileFailsWithNamedSeriesDetail(t *testing.T) {
	observation := ObservationSnapshot{
		Metrics: []MetricsSeries{
			{Direction: "output", Modality: "audio", ObservedDeltas: 6, ReportedTotal: 6},
			{Direction: "output", Modality: "tool", ObservedDeltas: 16, ReportedTotal: 17},
		},
	}
	err := Evaluate(metricsReconcileExpectation(), observation)
	if err == nil {
		t.Fatal("off-by-one overcount must fail the metrics reconciliation")
	}
	var mismatchErr *ExpectationMismatchError
	if !asMismatch(err, &mismatchErr) {
		t.Fatalf("failure must be an expectation mismatch, got %T", err)
	}
	if mismatchErr.Kind != ExpectMetricsReconcile {
		t.Fatalf("mismatch kind = %q, want %q", mismatchErr.Kind, ExpectMetricsReconcile)
	}
	if got := mismatchErr.Error(); !strings.Contains(got, "output/tool") ||
		!strings.Contains(got, "16") || !strings.Contains(got, "17") {
		t.Fatalf("failure must name output/tool with expected vs actual values, got %q", got)
	}
}

// An undercount diverges in the other direction and fails identically.
func TestEvaluateMetricsReconcileFailsOnUndercount(t *testing.T) {
	observation := ObservationSnapshot{
		Metrics: []MetricsSeries{
			{Direction: "output", Modality: "text", ObservedDeltas: 21, ReportedTotal: 20},
		},
	}
	err := Evaluate(metricsReconcileExpectation(), observation)
	if err == nil || !strings.Contains(err.Error(), "output/text") {
		t.Fatalf("undercount must fail naming output/text, got %v", err)
	}
}

// Declaring the expectation without metric evidence is itself a failure.
func TestEvaluateMetricsReconcileFailsWithoutEvidence(t *testing.T) {
	err := Evaluate(metricsReconcileExpectation(), ObservationSnapshot{})
	if err == nil {
		t.Fatal("absent metric evidence must fail")
	}
	if !strings.Contains(err.Error(), "none provided") {
		t.Fatalf("absent evidence detail missing, got %v", err)
	}
}

func asMismatch(err error, target **ExpectationMismatchError) bool {
	for err != nil {
		if mismatch, ok := err.(*ExpectationMismatchError); ok {
			*target = mismatch
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}
