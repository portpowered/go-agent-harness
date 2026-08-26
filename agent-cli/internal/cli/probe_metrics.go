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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
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
func collectReplayMetricsEvidence(ctx context.Context, fixture, prompt string) ([]probe.MetricsSeries, error) {
	sink, err := metrics.NewInMemorySink()
	if err != nil {
		return nil, fmt.Errorf("construct metrics sink: %w", err)
	}
	runOpts := services.SessionRunOptions{
		ReplayPath:      fixture,
		Prompt:          prompt,
		MetricsRecorder: sink,
	}
	if err := services.RunSession(ctx, io.Discard, runOpts); err != nil {
		return nil, fmt.Errorf("replay %s for metrics: %w", fixture, err)
	}
	snapshot := sink.Snapshot()
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
		// A series the production sink never carried but the raw wire stream
		// did is itself a reconciliation divergence.
		series = append(series, probe.MetricsSeries{
			Direction:      parts[0],
			Modality:       parts[1],
			ObservedDeltas: deltaSum,
			ReportedTotal:  0,
		})
	}
	return series, nil
}

// observedFixtureDeltaSums sums the raw wire delta payloads of one recorded
// fixture per direction/modality key. It mirrors the session observer's
// accounting rules at the wire layer without sharing its code: base64 audio
// counts decoded bytes, streamed tool-call argument deltas count once, and a
// terminal arguments-done payload counts only when no delta preceded it.
func observedFixtureDeltaSums(fixture string) (map[string]int64, error) {
	capture, err := gatewaytesting.LoadSessionCapture(fixture)
	if err != nil {
		return nil, fmt.Errorf("load replay fixture %q: %w", fixture, err)
	}
	sums := map[string]int64{}
	add := func(direction string, modality metrics.Modality, n int) {
		if n <= 0 {
			return
		}
		sums[direction+"/"+string(modality)] += int64(n)
	}
	toolDeltaSeen := map[string]bool{}
	for _, record := range capture.Records {
		var payload struct {
			Type      string `json:"type"`
			Delta     string `json:"delta"`
			Audio     string `json:"audio"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
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
			case "response.audio.delta":
				add("output", metrics.ModalityAudio, decodedBase64Len(payload.Delta))
			case "response.text.delta", "response.audio_transcript.delta":
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

// decodedBase64Len returns the decoded byte length of a base64 wire payload.
func decodedBase64Len(encoded string) int {
	if encoded == "" {
		return 0
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return len(encoded)
	}
	return len(decoded)
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
