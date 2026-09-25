// Package scenario loads, selects, and plans legacy probe scenarios for the
// offline JSONL probe runner. A provider-only probe.scenario.v2 document is
// projected onto the legacy shape; browser-aware v2 documents belong to the
// scenariov2 executor.
package scenario

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/replay"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

// document is the on-disk legacy scenario JSON envelope.
type document struct {
	ID               string        `json:"id"`
	Name             string        `json:"name"`
	Description      string        `json:"description"`
	Steps            []step        `json:"steps"`
	Expectations     []expectation `json:"expectations"`
	ExpectedBehavior []expectation `json:"expected_behavior"`
	Expected         []expectation `json:"expected"`
}

type step struct {
	Type       string          `json:"type"`
	Text       string          `json:"text"`
	CorpusID   string          `json:"corpus_id"`
	ToolCallID string          `json:"tool_call_id"`
	ToolName   string          `json:"tool_name"`
	Result     json.RawMessage `json:"result"`
	At         int64           `json:"at"`
	Duration   int64           `json:"duration"`
}

type expectation struct {
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

// Parse decodes a legacy scenario JSON document into a validated
// probe.Scenario. Expectations use the runner's measurable vocabulary.
func Parse(data []byte) (probe.Scenario, error) {
	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		return probe.Scenario{}, fmt.Errorf("malformed scenario JSON: %w", err)
	}
	scenario := probe.Scenario{ID: doc.ID, Name: doc.Name, Description: doc.Description}
	steps, err := parseSteps(doc.Steps)
	if err != nil {
		return probe.Scenario{}, err
	}
	scenario.Steps = steps
	expectations, err := parseExpectations(doc.declaredExpectations())
	if err != nil {
		return probe.Scenario{}, err
	}
	scenario.Expectations = expectations
	scenario.Expected = scenario.Expectations
	scenario.ExpectedBehavior = scenario.Expectations
	if err := scenario.Validate(replay.Lookup{}); err != nil {
		return probe.Scenario{}, err
	}
	return scenario, nil
}

// declaredExpectations honors the three accepted spellings in precedence
// order: expectations, expected_behavior, expected.
func (d document) declaredExpectations() []expectation {
	if d.Expectations != nil {
		return d.Expectations
	}
	if d.ExpectedBehavior != nil {
		return d.ExpectedBehavior
	}
	return d.Expected
}

func parseSteps(raw []step) ([]probe.Step, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("scenario must contain at least one step")
	}
	aliases := stepKindAliases()
	steps := make([]probe.Step, 0, len(raw))
	for _, value := range raw {
		kind, ok := aliases[value.Type]
		if !ok {
			return nil, fmt.Errorf("unknown step variant %q", value.Type)
		}
		steps = append(steps, probe.Step{Type: kind, Kind: kind, Text: value.Text, CorpusID: value.CorpusID,
			ToolCallID: value.ToolCallID, ToolName: value.ToolName, Result: value.Result,
			At: probe.LogicalTime(value.At), Time: probe.LogicalTime(value.At),
			Duration: probe.LogicalTime(value.Duration)})
	}
	return steps, nil
}

func parseExpectations(raw []expectation) ([]probe.ExpectedBehavior, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("at least one expected behavior is required")
	}
	expectations := make([]probe.ExpectedBehavior, 0, len(raw))
	for _, value := range raw {
		kind, ok := measurableExpectationKind(value.Type)
		if !ok {
			return nil, fmt.Errorf("unknown expectation variant %q", value.Type)
		}
		expectations = append(expectations, probe.ExpectedBehavior{
			Type: kind, Kind: kind, Text: value.Text, Value: value.Value, Count: value.Count,
			At: probe.LogicalTime(value.At), Time: probe.LogicalTime(value.At), HasAt: value.At != 0,
			CorpusID: value.CorpusID, ToolCallID: value.ToolCallID, ToolName: value.ToolName,
			Result: value.Result,
		})
	}
	return expectations, nil
}

func measurableExpectationKind(name string) (probe.ExpectationKind, bool) {
	kind, ok := expectationKindAliases()[strings.ToLower(strings.TrimSpace(name))]
	return kind, ok
}

func stepKindAliases() map[string]probe.StepKind {
	return map[string]probe.StepKind{
		"send_text":        probe.StepSendText,
		"send_audio":       probe.StepSendAudio,
		"send_tool_result": probe.StepSendToolResult,
		"advance_to":       probe.StepAdvanceTo,
		"wait":             probe.StepWait,
		"close":            probe.StepClose,
	}
}

// expectationKindAliases accepts both the snake_case and kebab-case
// spelling of every measurable expectation.
func expectationKindAliases() map[string]probe.ExpectationKind {
	kinds := []probe.ExpectationKind{
		probe.ExpectFrameCount, probe.ExpectTranscriptContains, probe.ExpectToolCalled,
		probe.ExpectTerminalReason, probe.ExpectTerminalProvenance, probe.ExpectOutputState,
		probe.ExpectLatencyWithinTicks, probe.ExpectAudioEnergy, probe.ExpectToolResultDelivered,
		probe.ExpectToolResultDiscarded, probe.ExpectNoOrphanedToolResult, probe.ExpectBufferDisposition,
		probe.ExpectBargeInCancelOnce, probe.ExpectMessageCountsReconcile, probe.ExpectResponseCancel,
	}
	aliases := make(map[string]probe.ExpectationKind, len(kinds)*2+2)
	for _, kind := range kinds {
		kebab := string(kind)
		aliases[kebab] = kind
		aliases[strings.ReplaceAll(kebab, "-", "_")] = kind
	}
	aliases["terminal_output_state"] = probe.ExpectOutputState
	aliases["terminal-output-state"] = probe.ExpectOutputState
	return aliases
}
