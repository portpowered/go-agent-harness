package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// observationFromSessionCapture validates and derives the probe evidence from
// one captured session. Replay and live executions use the same observation
// contract; the live path passes its freshly recorded capture here without
// replacing its provider-produced audio frames.
func observationFromSessionCapture(ctx context.Context, scenario probe.Scenario, capture gatewaytesting.SessionCapture, sourcePath string, validateSource bool) (probe.ObservationSnapshot, error) {
	var (
		report gatewaytesting.SessionReplayProbeReport
		err    error
	)
	if validateSource {
		report, err = gatewaytesting.RunSessionReplayProbe(ctx, sourcePath)
	} else {
		report, err = gatewaytesting.RunSessionReplayProbeFromCapture(ctx, capture)
	}
	if err != nil {
		return probe.ObservationSnapshot{}, err
	}
	observation := probe.ObservationSnapshot{
		FrameCount:     len(report.Observations),
		ObservedTick:   probe.LogicalTime(report.OutboundTicks),
		TerminalReason: report.Provenance,
	}
	observation.HasObservedTick = true
	if report.EndsWithDisconnect {
		observation.TerminalReason = "disconnect"
	}
	if classification := replayErrorClassificationFromCapture(capture); classification != "" {
		observation.TerminalReason = "error:" + classification
	}
	if scenarioDeclaresTerminalMetadata(scenario) {
		terminalReason, terminalProvenance, outputState := replayTerminalTriple(capture)
		observation.TerminalReason = terminalReason
		observation.TerminalProvenance = terminalProvenance
		observation.OutputState = outputState
	}
	observation.Transcript = replayTranscriptFromCapture(capture)
	if deriveErr := deriveToolResultObservationFromCapture(capture, &observation); deriveErr != nil {
		return probe.ObservationSnapshot{}, deriveErr
	}
	if deriveErr := deriveBargeInObservationFromCapture(capture, &observation); deriveErr != nil {
		return probe.ObservationSnapshot{}, deriveErr
	}
	if deriveErr := deriveResponseCancelObservationFromCapture(capture, &observation); deriveErr != nil {
		return probe.ObservationSnapshot{}, deriveErr
	}
	observation.BufferDisposition = replayBufferDispositionFromCapture(capture)
	if scenarioDeclaresMetricsReconciliation(scenario) {
		metricsSeries, metricsErr := collectReplayMetricsEvidence(ctx, sourcePath, scenarioSendText(scenario))
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

// deriveResponseCancelObservationFromCapture scans the capture for
// RESPONSE.CANCEL frames on the outbound client-to-provider path and fills
// the observation's barge-in cancel fields. A frame's logical tick is its
// 1-based ordinal among all client-to-server frames, matching the replay
// probe's outbound tick counting. The first observed cancel wins; later
// duplicates do not move the recorded tick.
func deriveResponseCancelObservationFromCapture(capture gatewaytesting.SessionCapture, observation *probe.ObservationSnapshot) error {
	tick := probe.LogicalTime(0)
	for _, record := range capture.Records {
		if record.Direction != gatewaytesting.DirectionClientToServer {
			continue
		}
		tick++
		if !observation.HasInterruptTick && isInputAudioAppendEventType(record.Type) {
			observation.HasInterruptTick = true
			observation.InterruptTick = tick
		}
		if observation.HasResponseCancel || !isResponseCancelEventType(record.Type) {
			continue
		}
		observation.HasResponseCancel = true
		observation.ResponseCancelTick = tick
	}
	return nil
}

func isInputAudioAppendEventType(eventType string) bool {
	return eventType == "input_audio_buffer.append"
}

// isResponseCancelEventType reports whether a fixture event type encodes a
// RESPONSE.CANCEL on either wire spelling: the raw provider websocket type or
// the stream-message type.
func isResponseCancelEventType(eventType string) bool {
	switch eventType {
	case "response.cancel", "RESPONSE.CANCEL":
		return true
	default:
		return false
	}
}

// scenarioDeclaresTerminalMetadata reports whether the scenario asks for the
// provider-authored terminal triple. Keeping this opt-in preserves the
// established terminal-reason vocabulary for older probe scenarios whose
// fixture provenance is their intentional fallback observation.
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

// replayTerminalTriple derives the stable probe-surface terminal vocabulary
// from a sanitized provider-wire fixture. The provider-close transport seam
// is exposed as "disconnect" at the probe layer; explicit response.done is
// exposed as "complete". Output state is based on whether a non-empty output
// delta was observed before the terminal boundary.
func replayTerminalTriple(capture gatewaytesting.SessionCapture) (reason, provenance, outputState string) {
	hasOutput, hasCompletion := false, false
	for _, record := range capture.Records {
		if record.Direction != gatewaytesting.DirectionServerToClient {
			continue
		}
		switch record.Type {
		case "response.text.delta", "response.output_text.delta", "response.audio_transcript.delta", "response.audio.delta",
			"response.output_audio.delta":
			var payload struct {
				Delta string `json:"delta"`
			}
			if json.Unmarshal(replayRecordPayload(record), &payload) == nil && payload.Delta != "" {
				hasOutput = true
			}
		case "response.done":
			hasCompletion = true
		}
	}

	if classification := replayErrorClassificationFromCapture(capture); classification != "" {
		return "error:" + classification, "provider", replayOutputState(hasOutput)
	}
	if capture.EndsWithDisconnect {
		return "disconnect", "provider", replayOutputState(hasOutput)
	}
	if hasCompletion {
		return "complete", "provider", "complete"
	}
	return "", "", ""
}

func replayOutputState(hasOutput bool) string {
	if hasOutput {
		return "partial"
	}
	return "none"
}

// observable disposition of the buffered input audio: an acknowledged commit
// or an explicit discard. The empty string means the capture ends with the
// buffer uncommitted, which buffer-disposition expectations treat as a
// failure.
func replayBufferDispositionFromCapture(capture gatewaytesting.SessionCapture) string {
	for _, record := range capture.Records {
		if record.Direction != gatewaytesting.DirectionServerToClient {
			continue
		}
		switch record.Type {
		case "input_audio_buffer.committed":
			return probe.BufferDispositionCommitted
		case "input_audio_buffer.discarded":
			return probe.BufferDispositionDiscarded
		}
	}
	return ""
}

// deriveToolResultObservationFromCapture scans the capture for tool-call
// lifecycle events and fills the observation's barge-in/tool-result fields:
// issued calls (server function_call_arguments.done), delivered results
// (client conversation.item.create carrying a function_call_output), and
// explicitly discarded results (client tool.result.discarded events).
func deriveToolResultObservationFromCapture(capture gatewaytesting.SessionCapture, observation *probe.ObservationSnapshot) error {
	for _, record := range capture.Records {
		var payload struct {
			CallID string `json:"call_id"`
			Item   struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
			} `json:"item"`
		}
		_ = json.Unmarshal(replayRecordPayload(record), &payload)
		switch {
		case record.Direction == gatewaytesting.DirectionServerToClient &&
			record.Type == "response.function_call_arguments.done" && payload.CallID != "":
			observation.ToolCalls = append(observation.ToolCalls, payload.CallID)
		case record.Direction == gatewaytesting.DirectionClientToServer &&
			record.Type == "conversation.item.create" &&
			payload.Item.Type == "function_call_output" && payload.Item.CallID != "":
			observation.ToolResultsDelivered = append(observation.ToolResultsDelivered, payload.Item.CallID)
		case record.Direction == gatewaytesting.DirectionClientToServer &&
			record.Type == "tool.result.discarded" && payload.CallID != "":
			observation.ToolResultsDiscarded = append(observation.ToolResultsDiscarded, payload.CallID)
		}
	}
	return nil
}

// deriveBargeInObservationFromCapture scans the capture for the
// repeated-barge-in lifecycle (s2s v3c) and fills the observation's
// reconciliation evidence: committed user turns (client
// input_audio_buffer.commit or a client conversation.item.create carrying a
// message item), created responses, delivered assistant turns (response.done
// on an uninterrupted response), cancellation events (client response.cancel),
// deltas leaking after their response was cancelled or outside any live
// response, and any response still streaming when the capture ends.
func deriveBargeInObservationFromCapture(capture gatewaytesting.SessionCapture, observation *probe.ObservationSnapshot) error {
	inFlight, cancelled := false, false
	for _, record := range capture.Records {
		switch {
		case record.Direction == gatewaytesting.DirectionClientToServer &&
			record.Type == "input_audio_buffer.commit":
			observation.UserTurnsCommitted++
		case record.Direction == gatewaytesting.DirectionClientToServer &&
			record.Type == "conversation.item.create":
			var payload struct {
				Item struct {
					Type string `json:"type"`
				} `json:"item"`
			}
			if json.Unmarshal(replayRecordPayload(record), &payload) == nil && payload.Item.Type == "message" {
				observation.UserTurnsCommitted++
			}
		case record.Direction == gatewaytesting.DirectionServerToClient &&
			record.Type == "response.created":
			observation.ResponsesCreated++
			inFlight, cancelled = true, false
		case record.Direction == gatewaytesting.DirectionServerToClient &&
			isTranscriptDeltaEventType(record.Type):
			if !inFlight || cancelled {
				observation.PostCancelDeltas++
			}
		case record.Direction == gatewaytesting.DirectionClientToServer &&
			record.Type == "response.cancel":
			observation.ResponseCancels++
			if !inFlight || cancelled {
				observation.SpuriousCancels++
			} else {
				observation.ResponsesCancelled++
				cancelled = true
			}
		case record.Direction == gatewaytesting.DirectionServerToClient &&
			record.Type == "response.done":
			if inFlight && !cancelled {
				observation.AssistantTurnsDelivered++
			}
			inFlight, cancelled = false, false
		}
	}
	observation.InFlightAtEnd = inFlight
	return nil
}

// isTranscriptDeltaEventType reports whether the wire event carries one
// streamed transcript delta of an in-flight response.
func isTranscriptDeltaEventType(eventType string) bool {
	switch eventType {
	case "response.text.delta", "response.audio_transcript.delta",
		"response.output_text.delta", "response.output_audio_transcript.delta":
		return true
	default:
		return false
	}
}

// replayErrorClassificationFromCapture classifies the first server-to-client
// failure record through the established provider error taxonomy. A
// server-to-client frame whose type carries the "malformed." prefix encodes an
// unparseable provider response and classifies as invalid_request — the same
// taxonomy class the gateway assigns when a live session parser rejects a
// provider event. Well-formed "error" records classify via their wire error
// type/code. It returns the empty string when the capture records no provider
// error or malformed frame, so healthy sessions keep their
// disconnect/provenance terminal reason.
func replayErrorClassificationFromCapture(capture gatewaytesting.SessionCapture) string {
	for _, record := range capture.Records {
		if record.Direction != gatewaytesting.DirectionServerToClient {
			continue
		}
		if strings.HasPrefix(record.Type, "malformed.") {
			return providers.ErrorClassInvalidRequest
		}
		if record.Type != "error" {
			continue
		}
		var payload struct {
			Error struct {
				Type string `json:"type"`
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(replayRecordPayload(record), &payload) != nil {
			continue
		}
		if classification := providers.SessionErrorClassification(payload.Error.Type, payload.Error.Code); classification != "" {
			return classification
		}
	}
	return ""
}
