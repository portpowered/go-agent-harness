package probe

import (
	"encoding/json"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func observeProbeCapture(report replayProbeReport, capture gatewaytesting.SessionCapture) replay.CaptureProbeObservation {
	observation := replay.CaptureProbeObservation{
		Provider:           capture.Provider.Name,
		Model:              capture.Provider.Model,
		FixtureProvenance:  capture.Session.FixtureProvenance,
		InboundFrames:      report.InboundFrames,
		OutboundTicks:      report.OutboundTicks,
		EndsWithDisconnect: capture.EndsWithDisconnect,
		TerminalReason:     capture.Session.FixtureProvenance,
		Transcript:         probeTranscript(capture),
		Observations:       make([]replay.CaptureProbeEvent, 0, len(report.Observations)),
	}
	for _, event := range report.Observations {
		observation.Observations = append(observation.Observations, replay.CaptureProbeEvent{
			Sequence: event.Sequence, Direction: string(event.Direction), Type: event.Type,
		})
	}
	setProbeTerminalReason(report, capture, &observation)
	observation.TerminalMetadataReason, observation.TerminalProvenance, observation.OutputState = probeTerminalTriple(capture)
	observeProbeRecords(capture, &observation)
	observeProbeBargeIn(capture, &observation)
	observation.BufferDisposition = probeBufferDisposition(capture)
	observeProbeAudioBounds(capture, &observation)
	return observation
}

func setProbeTerminalReason(report replayProbeReport, capture gatewaytesting.SessionCapture, observation *replay.CaptureProbeObservation) {
	if report.EndsWithDisconnect {
		observation.TerminalReason = "disconnect"
	}
	if classification := probeErrorClassification(capture); classification != "" {
		observation.TerminalReason = "error:" + classification
	}
}

func observeProbeRecords(capture gatewaytesting.SessionCapture, observation *replay.CaptureProbeObservation) {
	outboundTick := 0
	for _, record := range capture.Records {
		observeProbeOutboundTick(record, observation, &outboundTick)
		observeProbeToolEvent(record, observation)
	}
}

func observeProbeOutboundTick(record gatewaytesting.CapturedSessionEvent, observation *replay.CaptureProbeObservation, tick *int) {
	if record.Direction != gatewaytesting.DirectionClientToServer {
		return
	}
	*tick = *tick + 1
	if !observation.HasInterruptTick && record.Type == probeAudioAppend {
		observation.HasInterruptTick = true
		observation.InterruptTick = *tick
	}
	if !observation.HasResponseCancel && isProbeResponseCancel(record.Type) {
		observation.HasResponseCancel = true
		observation.ResponseCancelTick = *tick
	}
}

func observeProbeToolEvent(record gatewaytesting.CapturedSessionEvent, observation *replay.CaptureProbeObservation) {
	var payload struct {
		CallID string `json:"call_id"`
		Item   struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
		} `json:"item"`
	}
	if err := json.Unmarshal(probeRecordPayload(record), &payload); err != nil {
		return
	}
	switch {
	case record.Direction == gatewaytesting.DirectionServerToClient && record.Type == "response.function_call_arguments.done" && payload.CallID != "":
		observation.ToolCalls = append(observation.ToolCalls, payload.CallID)
	case record.Direction == gatewaytesting.DirectionClientToServer && record.Type == probeWireConversationItemCreate && payload.Item.Type == "function_call_output" && payload.Item.CallID != "":
		observation.ToolResultsDelivered = append(observation.ToolResultsDelivered, payload.Item.CallID)
	case record.Direction == gatewaytesting.DirectionClientToServer && record.Type == "tool.result.discarded" && payload.CallID != "":
		observation.ToolResultsDiscarded = append(observation.ToolResultsDiscarded, payload.CallID)
	}
}

func observeProbeBargeIn(capture gatewaytesting.SessionCapture, observation *replay.CaptureProbeObservation) {
	inFlight, cancelled := false, false
	for _, record := range capture.Records {
		if record.Direction == gatewaytesting.DirectionClientToServer {
			observeProbeClientBargeIn(record, observation, &inFlight, &cancelled)
		}
		if record.Direction == gatewaytesting.DirectionServerToClient {
			observeProbeServerBargeIn(record, observation, &inFlight, &cancelled)
		}
	}
	observation.InFlightAtEnd = inFlight
}

func observeProbeClientBargeIn(record gatewaytesting.CapturedSessionEvent, observation *replay.CaptureProbeObservation, inFlight, cancelled *bool) {
	switch record.Type {
	case "input_audio_buffer.commit":
		observation.UserTurnsCommitted++
	case probeWireConversationItemCreate:
		if probeItemIsUserMessage(record) {
			observation.UserTurnsCommitted++
		}
	case probeResponseCancel:
		observation.ResponseCancels++
		if !*inFlight || *cancelled {
			observation.SpuriousCancels++
			return
		}
		observation.ResponsesCancelled++
		*cancelled = true
	}
}

func probeItemIsUserMessage(record gatewaytesting.CapturedSessionEvent) bool {
	var payload struct {
		Item struct {
			Type string `json:"type"`
		} `json:"item"`
	}
	return json.Unmarshal(probeRecordPayload(record), &payload) == nil && payload.Item.Type == "message"
}

func observeProbeServerBargeIn(record gatewaytesting.CapturedSessionEvent, observation *replay.CaptureProbeObservation, inFlight, cancelled *bool) {
	switch {
	case record.Type == "response.created":
		observation.ResponsesCreated++
		*inFlight, *cancelled = true, false
	case isProbeTranscriptDelta(record.Type):
		if !*inFlight || *cancelled {
			observation.PostCancelDeltas++
		}
	case record.Type == "response.done":
		if *inFlight && !*cancelled {
			observation.AssistantTurnsDelivered++
		}
		*inFlight, *cancelled = false, false
	}
}

func observeProbeAudioBounds(capture gatewaytesting.SessionCapture, observation *replay.CaptureProbeObservation) {
	for index, record := range capture.Records {
		if record.Direction != gatewaytesting.DirectionServerToClient {
			continue
		}
		position := record.Sequence
		if position <= 0 {
			position = index + 1
		}
		switch record.Type {
		case "response.output_audio.delta", probeWireResponseAudioDelta:
			observation.AssistantAudioStarted = true
			if observation.AssistantAudioStartEvent == 0 {
				observation.AssistantAudioStartEvent = position
			}
		case "response.output_audio.done", "response.audio.done":
			if observation.AssistantAudioStarted {
				observation.AssistantAudioStopped = true
				observation.AssistantAudioStopEvent = position
			}
		}
	}
}

func probeTranscript(capture gatewaytesting.SessionCapture) string {
	var builder strings.Builder
	for _, record := range capture.Records {
		if record.Direction != gatewaytesting.DirectionServerToClient {
			continue
		}
		switch record.Type {
		case probeWireResponseTextDelta, probeWireResponseOutputTextDelta, probeWireResponseAudioTranscriptDelta:
		default:
			continue
		}
		var payload struct {
			Delta string `json:"delta"`
		}
		if json.Unmarshal(probeRecordPayload(record), &payload) == nil {
			builder.WriteString(payload.Delta)
		}
	}
	return builder.String()
}

func probeTerminalTriple(capture gatewaytesting.SessionCapture) (reason, provenance, outputState string) {
	hasOutput, hasCompletion := false, false
	for _, record := range capture.Records {
		if record.Direction != gatewaytesting.DirectionServerToClient {
			continue
		}
		switch record.Type {
		case probeWireResponseTextDelta, probeWireResponseOutputTextDelta, probeWireResponseAudioTranscriptDelta, probeWireResponseAudioDelta, "response.output_audio.delta":
			var payload struct {
				Delta string `json:"delta"`
			}
			if json.Unmarshal(probeRecordPayload(record), &payload) == nil && payload.Delta != "" {
				hasOutput = true
			}
		case "response.done":
			hasCompletion = true
		}
	}
	if classification := probeErrorClassification(capture); classification != "" {
		return "error:" + classification, probeProvenanceProvider, probeOutputState(hasOutput)
	}
	if capture.EndsWithDisconnect {
		return "disconnect", probeProvenanceProvider, probeOutputState(hasOutput)
	}
	if hasCompletion {
		return "complete", probeProvenanceProvider, "complete"
	}
	return "", "", ""
}

func probeOutputState(hasOutput bool) string {
	if hasOutput {
		return "partial"
	}
	return "none"
}

func probeBufferDisposition(capture gatewaytesting.SessionCapture) string {
	for _, record := range capture.Records {
		if record.Direction != gatewaytesting.DirectionServerToClient {
			continue
		}
		switch record.Type {
		case "input_audio_buffer.committed":
			return "committed"
		case "input_audio_buffer.discarded":
			return "discarded"
		}
	}
	return ""
}

func probeErrorClassification(capture gatewaytesting.SessionCapture) string {
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
		if json.Unmarshal(probeRecordPayload(record), &payload) != nil {
			continue
		}
		if classification := providers.SessionErrorClassification(payload.Error.Type, payload.Error.Code); classification != "" {
			return classification
		}
	}
	return ""
}

func isProbeResponseCancel(eventType string) bool {
	return eventType == "response.cancel" || eventType == "RESPONSE.CANCEL"
}

func isProbeTranscriptDelta(eventType string) bool {
	switch eventType {
	case probeWireResponseTextDelta, probeWireResponseAudioTranscriptDelta, probeWireResponseOutputTextDelta, "response.output_audio_transcript.delta":
		return true
	default:
		return false
	}
}

func probeRecordPayload(record gatewaytesting.CapturedSessionEvent) []byte {
	if len(record.Payload) != 0 {
		return record.Payload
	}
	return record.Data
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}
