package plan

import (
	"encoding/json"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// initialToolNames observes the first client advertisement. It never grants
// permission to execute tools and does not replace the caller's capability set.
func initialToolNames(records []gatewaytesting.CapturedSessionEvent) ([]string, bool) {
	for _, record := range records {
		if record.Direction != gatewaytesting.DirectionClientToServer || record.Type != replaySessionUpdate {
			continue
		}
		var envelope struct {
			Session struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"session"`
		}
		if err := json.Unmarshal(replayRecordPayload(record), &envelope); err != nil {
			return nil, false
		}
		names := make([]string, 0, len(envelope.Session.Tools))
		for _, tool := range envelope.Session.Tools {
			if name := strings.TrimSpace(tool.Name); name != "" {
				names = append(names, name)
			}
		}
		return names, true
	}
	return nil, false
}

type captureFactPayload struct {
	Type      string `json:"type"`
	Delta     string `json:"delta"`
	Audio     string `json:"audio"`
	CallID    string `json:"call_id"`
	Arguments string `json:"arguments"`
	Synthetic string `json:"synthetic_audio"`
	Item      struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"item"`
}

func captureFacts(capture gatewaytesting.SessionCapture) replay.CaptureFacts {
	facts := replay.CaptureFacts{Version: capture.Version, EventCount: len(capture.Records)}
	totals := make(map[metrics.SeriesKey]int64)
	toolDeltaSeen := make(map[string]bool)
	for _, record := range capture.Records {
		if record.Direction == gatewaytesting.DirectionClientToServer && strings.EqualFold(strings.TrimSpace(record.Type), replayAppend) {
			facts.ClientAudioAppendCount++
		}
		addCaptureRecordFacts(totals, toolDeltaSeen, record)
	}
	facts.MetricDeltas = captureMetricDeltas(totals)
	return facts
}

func addCaptureRecordFacts(totals map[metrics.SeriesKey]int64, toolDeltaSeen map[string]bool, record gatewaytesting.CapturedSessionEvent) {
	payloadBytes := record.Payload
	if len(payloadBytes) == 0 {
		payloadBytes = record.Data
	}
	var payload captureFactPayload
	if json.Unmarshal(payloadBytes, &payload) != nil {
		return
	}
	switch record.Direction {
	case gatewaytesting.DirectionServerToClient:
		addServerCaptureFacts(totals, toolDeltaSeen, payload)
	case gatewaytesting.DirectionClientToServer:
		addClientCaptureFacts(totals, payload)
	}
}

func addServerCaptureFacts(totals map[metrics.SeriesKey]int64, toolDeltaSeen map[string]bool, payload captureFactPayload) {
	switch payload.Type {
	case "response.audio.delta", "response.output_audio.delta":
		addCaptureMetric(totals, metrics.DirectionOutput, metrics.ModalityAudio, decodedCaptureBase64Len(payload.Delta))
	case "response.text.delta", "response.output_text.delta", "response.audio_transcript.delta", "response.output_audio_transcript.delta":
		addCaptureMetric(totals, metrics.DirectionOutput, metrics.ModalityText, len(payload.Delta))
	case "response.function_call_arguments.delta":
		addCaptureMetric(totals, metrics.DirectionOutput, metrics.ModalityTool, len(payload.Delta))
		toolDeltaSeen[payload.CallID] = true
	case "response.function_call_arguments.done":
		if !toolDeltaSeen[payload.CallID] {
			addCaptureMetric(totals, metrics.DirectionOutput, metrics.ModalityTool, len(payload.Arguments))
			toolDeltaSeen[payload.CallID] = true
		}
	}
}

func addClientCaptureFacts(totals map[metrics.SeriesKey]int64, payload captureFactPayload) {
	switch payload.Type {
	case "conversation.item.create":
		for _, part := range payload.Item.Content {
			if part.Type == "input_text" {
				addCaptureMetric(totals, metrics.DirectionInput, metrics.ModalityText, len(part.Text))
			}
		}
	case replayAppend:
		audio := payload.Audio
		if audio == "" {
			audio = payload.Synthetic
		}
		addCaptureMetric(totals, metrics.DirectionInput, metrics.ModalityAudio, decodedCaptureBase64Len(audio))
	}
}

func captureMetricDeltas(totals map[metrics.SeriesKey]int64) []replay.CaptureMetricDelta {
	var deltas []replay.CaptureMetricDelta
	for _, direction := range metrics.SupportedDirections() {
		for _, modality := range metrics.SupportedModalities() {
			key := metrics.SeriesKey{Direction: direction, Modality: modality}
			if bytes := totals[key]; bytes > 0 {
				deltas = append(deltas, replay.CaptureMetricDelta{Direction: direction, Modality: modality, Bytes: bytes})
			}
		}
	}
	return deltas
}

func addCaptureMetric(totals map[metrics.SeriesKey]int64, direction metrics.Direction, modality metrics.Modality, bytes int) {
	if bytes > 0 {
		totals[metrics.SeriesKey{Direction: direction, Modality: modality}] += int64(bytes)
	}
}

func decodedCaptureBase64Len(encoded string) int {
	if encoded == "" {
		return 0
	}
	decoded, err := codec.DecodeBase64(encoded)
	if err != nil {
		return len(encoded)
	}
	return len(decoded)
}
