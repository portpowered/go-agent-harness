package cli

// This file owns the s2s-v7a metrics-reconciliation evidence seam for offline
// probe runs. The emitted side of every series comes from the production
// observation path: the real session runner replays the fixture with a
// metrics recorder injected, and the recorder's terminal snapshot is the
// emitted metric matrix. The observed side sums the same fixture's raw wire
// deltas independently, so a regression in the session observer (a missing,
// duplicated, or misattributed series) breaks exact reconciliation.

import (
	"context"
	"fmt"
	"strings"

	serviceprobes "github.com/portpowered/go-agent-harness/agent-cli/internal/services/probes"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

// scenarioDeclaresMetricsReconciliation reports whether one scenario declares
// a metrics-reconcile expectation and therefore needs metric evidence.
func scenarioDeclaresMetricsReconciliation(scenario probe.Scenario) bool {
	for _, expectation := range scenario.Expectations {
		if expectation.Type == probe.ExpectMetricsReconcile || expectation.Kind == probe.ExpectMetricsReconcile {
			return true
		}
	}
	return false
}

// scenarioSendText returns the first send_text step's text, which the replayed
// session runner seeds as the user prompt.
func scenarioSendText(scenario probe.Scenario) string {
	for _, step := range scenario.Steps {
		if step.Type == probe.StepSendText && strings.TrimSpace(step.Text) != "" {
			return step.Text
		}
	}
	return ""
}

// collectReplayMetricsEvidence drives the real session runner over the
// recorded fixture with a metrics sink injected and pairs the resulting
// emitted matrix with an independent wire-level sum of the same fixture's
// structured deltas.
func collectReplayMetricsEvidence(ctx context.Context, collector serviceprobes.MetricsCollector, fixture, prompt string) ([]probe.MetricsSeries, error) {
	if collector == nil {
		return nil, fmt.Errorf("metrics collector is not configured")
	}
	return collector.Collect(ctx, fixture, prompt)
}

// injectMetricsOvercount applies the negative control's declared fault: the
// output/tool reported total is raised exactly one above the observed delta
// sum while every other series stays reconciled. The metrics-reconcile
// expectation must fail naming output/tool with both values.
func injectMetricsOvercount(series []probe.MetricsSeries) {
	for index := range series {
		if series[index].Direction == string(metrics.DirectionOutput) && series[index].Modality == string(metrics.ModalityTool) {
			series[index].ReportedTotal++
			return
		}
	}
}
