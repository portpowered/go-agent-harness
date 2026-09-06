package agentruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	serviceprobes "github.com/portpowered/go-agent-harness/agent-cli/internal/services/probes"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// NewMetricsCollector returns the production metrics reconciliation service.
// It uses the same RunSession path as live sessions, with only a replay
// capture and in-memory metrics sink supplied by the caller.
func NewMetricsCollector(clockSource clock.Source, factory SessionRuntimeFactory) serviceprobes.MetricsCollector {
	return metricsCollector{clock: clockSource, factory: factory}
}

type metricsCollector struct {
	clock   clock.Source
	factory SessionRuntimeFactory
}

func (c metricsCollector) Collect(ctx context.Context, fixture, prompt string) ([]serviceprobes.MetricsSeries, error) {
	if c.clock == nil {
		return nil, fmt.Errorf("metrics collector requires an injected clock")
	}
	if !c.factory.configured() {
		return nil, fmt.Errorf("metrics collector requires an injected session runtime factory")
	}
	sink, err := metrics.NewInMemorySink()
	if err != nil {
		return nil, fmt.Errorf("construct metrics sink: %w", err)
	}
	if err := RunSession(ctx, io.Discard, SessionRunOptions{
		ReplayPath:      fixture,
		Prompt:          prompt,
		Clock:           c.clock,
		runtimeFactory:  c.factory,
		MetricsRecorder: sink,
	}); err != nil {
		return nil, fmt.Errorf("replay %s for metrics: %w", fixture, err)
	}
	snapshot := sink.Snapshot()
	observed, err := observedFixtureDeltaSums(fixture)
	if err != nil {
		return nil, err
	}
	series := make([]serviceprobes.MetricsSeries, 0, len(snapshot.Series))
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
		series = append(series, probe.MetricsSeries{
			Direction:      parts[0],
			Modality:       parts[1],
			ObservedDeltas: deltaSum,
		})
	}
	return series, nil
}

// observedFixtureDeltaSums is deliberately independent from the runtime
// observer. It is the wire-level oracle used to catch missing or duplicated
// accounting in the production metrics path.
func observedFixtureDeltaSums(fixture string) (map[string]int64, error) {
	capture, err := gatewaytesting.LoadSessionCapture(fixture)
	if err != nil {
		return nil, fmt.Errorf("load replay fixture %q: %w", fixture, err)
	}
	sums := map[string]int64{}
	add := func(direction string, modality metrics.Modality, n int) {
		if n > 0 {
			sums[direction+"/"+string(modality)] += int64(n)
		}
	}
	toolDeltaSeen := map[string]bool{}
	for _, record := range capture.Records {
		var payload struct {
			Type      string `json:"type"`
			Delta     string `json:"delta"`
			Audio     string `json:"audio"`
			CallID    string `json:"call_id"`
			Args      string `json:"arguments"`
			Synthetic string `json:"synthetic_audio"`
			Item      struct {
				Type    string `json:"type"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"item"`
		}
		if json.Unmarshal(record.Payload, &payload) != nil {
			continue
		}
		switch record.Direction {
		case gatewaytesting.DirectionServerToClient:
			switch payload.Type {
			case "response.audio.delta", "response.output_audio.delta":
				add("output", metrics.ModalityAudio, decodedBase64Len(payload.Delta))
			case "response.text.delta", "response.output_text.delta", "response.audio_transcript.delta", "response.output_audio_transcript.delta":
				add("output", metrics.ModalityText, len(payload.Delta))
			case "response.function_call_arguments.delta":
				add("output", metrics.ModalityTool, len(payload.Delta))
				toolDeltaSeen[payload.CallID] = true
			case "response.function_call_arguments.done":
				if !toolDeltaSeen[payload.CallID] {
					add("output", metrics.ModalityTool, len(payload.Args))
					toolDeltaSeen[payload.CallID] = true
				}
			}
		case gatewaytesting.DirectionClientToServer:
			switch payload.Type {
			case "conversation.item.create":
				for _, part := range payload.Item.Content {
					if part.Type == "input_text" {
						add("input", metrics.ModalityText, len(part.Text))
					}
				}
			case "input_audio_buffer.append":
				audio := payload.Audio
				if audio == "" {
					audio = payload.Synthetic
				}
				add("input", metrics.ModalityAudio, decodedBase64Len(audio))
			}
		}
	}
	return sums, nil
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
