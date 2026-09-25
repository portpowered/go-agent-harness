package replay

import (
	"context"
	"fmt"

	serviceprobes "github.com/portpowered/go-agent-harness/agent-cli/internal/services/probes"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
)

// Observe adapts the replay service's bounded capture projection to the probe
// runner. Capture parsing, replay ordering, terminal classification, and
// lifecycle interpretation stay inside replay internals.
func Observe(
	ctx context.Context,
	scenario probe.Scenario,
	service runtimeReplay.Service,
	request runtimeReplay.CaptureProbeRequest,
	metrics serviceprobes.MetricsCollector,
) (probe.ObservationSnapshot, error) {
	_, observation, err := analyze(ctx, service, scenario, request, metrics)
	return observation, err
}

// Analyze replays one provider capture and returns both the service report
// and its probe observation. It matches the scenario.v2 provider analyzer
// seam and never collects metrics evidence.
func Analyze(
	ctx context.Context,
	service runtimeReplay.Service,
	scenario probe.Scenario,
	request runtimeReplay.CaptureProbeRequest,
) (runtimeReplay.CaptureProbeObservation, probe.ObservationSnapshot, error) {
	return analyze(ctx, service, scenario, request, nil)
}

func analyze(
	ctx context.Context,
	service runtimeReplay.Service,
	scenario probe.Scenario,
	request runtimeReplay.CaptureProbeRequest,
	metrics serviceprobes.MetricsCollector,
) (runtimeReplay.CaptureProbeObservation, probe.ObservationSnapshot, error) {
	if service == nil {
		return runtimeReplay.CaptureProbeObservation{}, probe.ObservationSnapshot{}, fmt.Errorf("replay service is not configured")
	}
	report, err := service.AnalyzeProbe(ctx, request)
	if err != nil {
		return runtimeReplay.CaptureProbeObservation{}, probe.ObservationSnapshot{}, err
	}
	observation, err := observationFromReport(ctx, scenario, request, report, metrics)
	return report, observation, err
}

func observationFromReport(
	ctx context.Context,
	scenario probe.Scenario,
	request runtimeReplay.CaptureProbeRequest,
	report runtimeReplay.CaptureProbeObservation,
	metrics serviceprobes.MetricsCollector,
) (probe.ObservationSnapshot, error) {
	observation := snapshotFromReport(report)
	if declaresTerminalMetadata(scenario) {
		observation.TerminalReason = report.TerminalMetadataReason
		observation.TerminalProvenance = report.TerminalProvenance
		observation.OutputState = report.OutputState
	}
	if declaresMetricsReconciliation(scenario) {
		series, err := collectMetricsEvidence(ctx, metrics, request.SourcePath, scenarioSendText(scenario))
		if err != nil {
			return probe.ObservationSnapshot{}, fmt.Errorf("collect metrics evidence: %w", err)
		}
		if scenario.ID == probe.ScenarioIDS2SV7AMetricsModalityOvercount {
			injectMetricsOvercount(series)
		}
		observation.Metrics = series
	}
	return observation, nil
}

func snapshotFromReport(report runtimeReplay.CaptureProbeObservation) probe.ObservationSnapshot {
	return probe.ObservationSnapshot{
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
}

// declaresTerminalMetadata reports whether any expectation set asks for the
// terminal provenance or output state, which switches the terminal reason to
// the metadata-derived value.
func declaresTerminalMetadata(scenario probe.Scenario) bool {
	expectationSets := [][]probe.ExpectedBehavior{scenario.Expectations, scenario.ExpectedBehavior, scenario.Expected}
	for _, expectations := range expectationSets {
		for _, expectation := range expectations {
			kind := expectation.Type
			if kind == "" {
				kind = expectation.Kind
			}
			if kind == probe.ExpectTerminalProvenance || kind == probe.ExpectOutputState {
				return true
			}
		}
	}
	return false
}
