package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

// scenarioDocument is the on-disk scenario JSON envelope.
type scenarioDocument struct {
	ID               string             `json:"id"`
	Name             string             `json:"name"`
	Description      string             `json:"description"`
	Steps            []probeStep        `json:"steps"`
	Expectations     []probeExpectation `json:"expectations"`
	ExpectedBehavior []probeExpectation `json:"expected_behavior"`
	Expected         []probeExpectation `json:"expected"`
}

type probeStep struct {
	Type       string          `json:"type"`
	Text       string          `json:"text"`
	CorpusID   string          `json:"corpus_id"`
	ToolCallID string          `json:"tool_call_id"`
	ToolName   string          `json:"tool_name"`
	Result     json.RawMessage `json:"result"`
	At         int64           `json:"at"`
	Duration   int64           `json:"duration"`
}

type probeExpectation struct {
	Type       string          `json:"type"`
	Text       string          `json:"text"`
	Value      string          `json:"value"`
	Count      int             `json:"count"`
	At         int64           `json:"at"`
	CorpusID   string          `json:"corpus_id"`
	ToolCallID string          `json:"tool_call_id"`
	ToolName   string          `json:"tool_name"`
	Result     json.RawMessage `json:"result"`
}

var stepKindAliases = map[string]probe.StepKind{
	"send_text":        probe.StepSendText,
	"send_audio":       probe.StepSendAudio,
	"send_tool_result": probe.StepSendToolResult,
	"advance_to":       probe.StepAdvanceTo,
	"wait":             probe.StepWait,
	"close":            probe.StepClose,
}

var expectationKindAliases = map[string]probe.ExpectationKind{
	"frame_count":              probe.ExpectFrameCount,
	"frame-count":              probe.ExpectFrameCount,
	"transcript_contains":      probe.ExpectTranscriptContains,
	"transcript-contains":      probe.ExpectTranscriptContains,
	"tool_called":              probe.ExpectToolCalled,
	"tool-called":              probe.ExpectToolCalled,
	"terminal_reason":          probe.ExpectTerminalReason,
	"terminal-reason":          probe.ExpectTerminalReason,
	"terminal_provenance":      probe.ExpectTerminalProvenance,
	"terminal-provenance":      probe.ExpectTerminalProvenance,
	"output_state":             probe.ExpectOutputState,
	"output-state":             probe.ExpectOutputState,
	"terminal_output_state":    probe.ExpectOutputState,
	"terminal-output-state":    probe.ExpectOutputState,
	"latency_within_ticks":     probe.ExpectLatencyWithinTicks,
	"latency-within-ticks":     probe.ExpectLatencyWithinTicks,
	"audio_energy":             probe.ExpectAudioEnergy,
	"audio-energy":             probe.ExpectAudioEnergy,
	"tool_result_delivered":    probe.ExpectToolResultDelivered,
	"tool-result-delivered":    probe.ExpectToolResultDelivered,
	"tool_result_discarded":    probe.ExpectToolResultDiscarded,
	"tool-result-discarded":    probe.ExpectToolResultDiscarded,
	"no_orphaned_tool_result":  probe.ExpectNoOrphanedToolResult,
	"no-orphaned-tool-result":  probe.ExpectNoOrphanedToolResult,
	"buffer_disposition":       probe.ExpectBufferDisposition,
	"buffer-disposition":       probe.ExpectBufferDisposition,
	"barge_in_cancel_once":     probe.ExpectBargeInCancelOnce,
	"barge-in-cancel-once":     probe.ExpectBargeInCancelOnce,
	"message_counts_reconcile": probe.ExpectMessageCountsReconcile,
	"message-counts-reconcile": probe.ExpectMessageCountsReconcile,
	"response_cancel":          probe.ExpectResponseCancel,
	"response-cancel":          probe.ExpectResponseCancel,
}

func measurableExpectationKind(name string) (probe.ExpectationKind, bool) {
	kind, ok := expectationKindAliases[strings.ToLower(strings.TrimSpace(name))]
	return kind, ok
}

// loadProbeScenario parses a scenario JSON document into a validated
// probe.Scenario. Expectations use the runner's measurable vocabulary.
func loadProbeScenario(data []byte) (probe.Scenario, error) {
	var document scenarioDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return probe.Scenario{}, fmt.Errorf("malformed scenario JSON: %w", err)
	}
	scenario := probe.Scenario{ID: document.ID, Name: document.Name, Description: document.Description}
	if len(document.Steps) == 0 {
		return probe.Scenario{}, fmt.Errorf("scenario must contain at least one step")
	}
	for _, raw := range document.Steps {
		kind, ok := stepKindAliases[raw.Type]
		if !ok {
			return probe.Scenario{}, fmt.Errorf("unknown step variant %q", raw.Type)
		}
		step := probe.Step{Type: kind, Kind: kind, Text: raw.Text, CorpusID: raw.CorpusID,
			ToolCallID: raw.ToolCallID, ToolName: raw.ToolName, Result: raw.Result,
			At: probe.LogicalTime(raw.At), Time: probe.LogicalTime(raw.At),
			Duration: probe.LogicalTime(raw.Duration)}
		scenario.Steps = append(scenario.Steps, step)
	}
	expectations := document.Expectations
	if expectations == nil {
		expectations = document.ExpectedBehavior
	}
	if expectations == nil {
		expectations = document.Expected
	}
	if len(expectations) == 0 {
		return probe.Scenario{}, fmt.Errorf("at least one expected behavior is required")
	}
	for _, raw := range expectations {
		kind, ok := measurableExpectationKind(raw.Type)
		if !ok {
			return probe.Scenario{}, fmt.Errorf("unknown expectation variant %q", raw.Type)
		}
		expectation := probe.ExpectedBehavior{
			Type: kind, Kind: kind, Text: raw.Text, Value: raw.Value, Count: raw.Count,
			At: probe.LogicalTime(raw.At), Time: probe.LogicalTime(raw.At), HasAt: raw.At != 0,
			CorpusID: raw.CorpusID, ToolCallID: raw.ToolCallID, ToolName: raw.ToolName,
			Result: raw.Result,
		}
		scenario.Expectations = append(scenario.Expectations, expectation)
	}
	scenario.Expected = scenario.Expectations
	scenario.ExpectedBehavior = scenario.Expectations
	if err := scenario.Validate(replayCorpusLookup{}); err != nil {
		return probe.Scenario{}, err
	}
	return scenario, nil
}
