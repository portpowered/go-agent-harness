package cli

import (
	"context"
	"fmt"

	serviceprobes "github.com/portpowered/go-agent-harness/agent-cli/internal/services/probes"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
)

// observationFromSessionCapture adapts the replay service's bounded capture
// projection to the probe runner. Capture parsing, replay ordering, terminal
// classification, and lifecycle interpretation stay inside replay internals.
func observationFromSessionCapture(
	ctx context.Context,
	scenario probe.Scenario,
	replayService runtimeReplay.Service,
	request runtimeReplay.CaptureProbeRequest,
	collectors ...serviceprobes.MetricsCollector,
) (probe.ObservationSnapshot, error) {
	_, observation, err := analyzeCaptureProbe(ctx, replayService, scenario, request, collectors...)
	return observation, err
}

func analyzeCaptureProbe(
	ctx context.Context,
	replayService runtimeReplay.Service,
	scenario probe.Scenario,
	request runtimeReplay.CaptureProbeRequest,
	collectors ...serviceprobes.MetricsCollector,
) (runtimeReplay.CaptureProbeObservation, probe.ObservationSnapshot, error) {
	if replayService == nil {
		return runtimeReplay.CaptureProbeObservation{}, probe.ObservationSnapshot{}, fmt.Errorf("replay service is not configured")
	}
	report, err := replayService.AnalyzeProbe(ctx, request)
	if err != nil {
		return runtimeReplay.CaptureProbeObservation{}, probe.ObservationSnapshot{}, err
	}
	observation, err := observationFromCaptureProbe(ctx, scenario, request, report, collectors...)
	return report, observation, err
}

func observationFromCaptureProbe(
	ctx context.Context,
	scenario probe.Scenario,
	request runtimeReplay.CaptureProbeRequest,
	report runtimeReplay.CaptureProbeObservation,
	collectors ...serviceprobes.MetricsCollector,
) (probe.ObservationSnapshot, error) {
	observation := probe.ObservationSnapshot{
		Transcript:              report.Transcript,
		ToolCalls:               append([]string(nil), report.ToolCalls...),
		ToolResultsDelivered:    append([]string(nil), report.ToolResultsDelivered...),
		ToolResultsDiscarded:    append([]string(nil), report.ToolResultsDiscarded...),
		ObservedTick:            probe.LogicalTime(report.OutboundTicks),
		HasObservedTick:         true,
		TerminalReason:          report.TerminalReason,
		FrameCount:              report.InboundFrames + report.OutboundTicks,
		InterruptTick:           probe.LogicalTime(report.InterruptTick),
		HasInterruptTick:        report.HasInterruptTick,
		ResponseCancelTick:      probe.LogicalTime(report.ResponseCancelTick),
		HasResponseCancel:       report.HasResponseCancel,
		UserTurnsCommitted:      report.UserTurnsCommitted,
		AssistantTurnsDelivered: report.AssistantTurnsDelivered,
		ResponsesCreated:        report.ResponsesCreated,
		ResponsesCancelled:      report.ResponsesCancelled,
		ResponseCancels:         report.ResponseCancels,
		SpuriousCancels:         report.SpuriousCancels,
		PostCancelDeltas:        report.PostCancelDeltas,
		InFlightAtEnd:           report.InFlightAtEnd,
		BufferDisposition:       report.BufferDisposition,
	}
	if scenarioDeclaresTerminalMetadata(scenario) {
		observation.TerminalReason = report.TerminalMetadataReason
		observation.TerminalProvenance = report.TerminalProvenance
		observation.OutputState = report.OutputState
	}
	if scenarioDeclaresMetricsReconciliation(scenario) {
		var collector serviceprobes.MetricsCollector
		if len(collectors) > 0 {
			collector = collectors[0]
		}
		metricsSeries, metricsErr := collectReplayMetricsEvidence(ctx, collector, request.SourcePath, scenarioSendText(scenario))
		if metricsErr != nil {
			return probe.ObservationSnapshot{}, fmt.Errorf("collect metrics evidence: %w", metricsErr)
		}
		if scenario.ID == probe.ScenarioIDS2SV7AMetricsModalityOvercount {
			injectMetricsOvercount(metricsSeries)
		}
		observation.Metrics = metricsSeries
	}
	return observation, nil
}

func scenarioDeclaresTerminalMetadata(scenario probe.Scenario) bool {
	expectationSets := [][]probe.ExpectedBehavior{scenario.Expectations, scenario.ExpectedBehavior, scenario.Expected}
	for _, expectations := range expectationSets {
		for _, expectation := range expectations {
			kind := expectation.Type
			if kind == "" {
				kind = expectation.Kind
			}
			switch kind {
			case probe.ExpectTerminalProvenance, probe.ExpectOutputState:
				return true
			}
		}
	}
	return false
}
