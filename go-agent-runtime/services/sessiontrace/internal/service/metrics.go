package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

type metricsCollector struct {
	options sessiontrace.MetricsCollectorOptions
}

func NewReplayMetricsCollector(options sessiontrace.MetricsCollectorOptions) sessiontrace.MetricsCollector {
	return metricsCollector{options: options}
}

func (c metricsCollector) Collect(ctx context.Context, fixture, prompt string) ([]probe.MetricsSeries, error) {
	if c.options.Clock == nil {
		return nil, fmt.Errorf("metrics collector requires an injected clock")
	}
	if c.options.FactoryReady != nil && !c.options.FactoryReady() {
		return nil, fmt.Errorf("metrics collector requires an injected session runtime factory")
	}
	if c.options.Runner == nil {
		return nil, fmt.Errorf("metrics collector requires an injected replay runner")
	}
	snapshot, err := c.options.Runner(ctx, fixture, prompt)
	if err != nil {
		return nil, fmt.Errorf("replay %s for metrics: %w", fixture, err)
	}
	observed, err := observedFixtureDeltaSums(fixture)
	if err != nil {
		return nil, err
	}
	series := make([]probe.MetricsSeries, 0, len(snapshot.Series))
	for _, entry := range snapshot.Series {
		key := string(entry.Direction) + "/" + string(entry.Modality)
		series = append(series, probe.MetricsSeries{
			Direction:      string(entry.Direction),
			Modality:       string(entry.Modality),
			ObservedDeltas: observed[key],
			ReportedTotal:  int64(entry.TotalBytes),
		})
		delete(observed, key)
	}
	for key, deltaSum := range observed {
		parts := strings.SplitN(key, "/", 2)
		if len(parts) != 2 {
			continue
		}
		series = append(series, probe.MetricsSeries{Direction: parts[0], Modality: parts[1], ObservedDeltas: deltaSum})
	}
	return series, nil
}

func observedFixtureDeltaSums(fixture string) (map[string]int64, error) {
	capture, err := gatewaytesting.LoadSessionCapture(fixture)
	if err != nil {
		return nil, fmt.Errorf("load replay fixture %q: %w", fixture, err)
	}
	sums := map[string]int64{}
	toolDeltaSeen := map[string]bool{}
	for _, record := range capture.Records {
		payload, ok := decodeFixturePayload(record.Payload)
		if !ok {
			continue
		}
		observeFixtureRecord(record.Direction, payload, sums, toolDeltaSeen)
	}
	return sums, nil
}

type fixturePayload struct {
	Type      string `json:"type"`
	Delta     string `json:"delta"`
	Audio     string `json:"audio"`
	CallID    string `json:"call_id"`
	Args      string `json:"arguments"`
	Synthetic string `json:"synthetic_audio"`
	Item      struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"item"`
}

func decodeFixturePayload(raw json.RawMessage) (fixturePayload, bool) {
	var payload fixturePayload
	return payload, json.Unmarshal(raw, &payload) == nil
}

func addFixtureMetric(sums map[string]int64, direction string, modality metrics.Modality, n int) {
	if n > 0 {
		sums[direction+"/"+string(modality)] += int64(n)
	}
}

func observeFixtureRecord(direction gatewaytesting.SessionEventDirection, payload fixturePayload, sums map[string]int64, toolDeltaSeen map[string]bool) {
	switch direction {
	case gatewaytesting.DirectionServerToClient:
		observeServerFixtureRecord(payload, sums, toolDeltaSeen)
	case gatewaytesting.DirectionClientToServer:
		observeClientFixtureRecord(payload, sums)
	}
}

func observeServerFixtureRecord(payload fixturePayload, sums map[string]int64, toolDeltaSeen map[string]bool) {
	switch payload.Type {
	case "response.audio.delta", "response.output_audio.delta":
		addFixtureMetric(sums, "output", metrics.ModalityAudio, decodedBase64Len(payload.Delta))
	case "response.text.delta", "response.output_text.delta", "response.audio_transcript.delta", "response.output_audio_transcript.delta":
		addFixtureMetric(sums, "output", metrics.ModalityText, len(payload.Delta))
	case "response.function_call_arguments.delta":
		addFixtureMetric(sums, "output", metrics.ModalityTool, len(payload.Delta))
		toolDeltaSeen[payload.CallID] = true
	case "response.function_call_arguments.done":
		if !toolDeltaSeen[payload.CallID] {
			addFixtureMetric(sums, "output", metrics.ModalityTool, len(payload.Args))
			toolDeltaSeen[payload.CallID] = true
		}
	}
}

func observeClientFixtureRecord(payload fixturePayload, sums map[string]int64) {
	switch payload.Type {
	case "conversation.item.create":
		for _, part := range payload.Item.Content {
			if part.Type == "input_text" {
				addFixtureMetric(sums, "input", metrics.ModalityText, len(part.Text))
			}
		}
	case "input_audio_buffer.append":
		audio := payload.Audio
		if audio == "" {
			audio = payload.Synthetic
		}
		addFixtureMetric(sums, "input", metrics.ModalityAudio, decodedBase64Len(audio))
	}
}

func decodedBase64Len(encoded string) int {
	if encoded == "" {
		return 0
	}
	decoded, err := codec.DecodeBase64(encoded)
	if err != nil {
		return len(encoded)
	}
	return len(decoded)
}
