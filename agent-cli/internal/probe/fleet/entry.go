package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/scenario"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

// runEntry loads the entry's scenario and runs it once through the JSONL
// probe runner under the deadguard, mapping the single result onto an entry
// outcome.
func runEntry(ctx context.Context, entry Entry, exec probe.ExecFunc) (EntryOutcome, error) {
	loaded, err := loadEntryScenario(entry)
	if err != nil {
		return EntryOutcome{}, err
	}
	var output bytes.Buffer
	runner := scenario.NewRunner(scenario.Deadguard(exec, scenario.DefaultDeadline), &output)
	summary, err := runner.Run(ctx, []probe.Scenario{loaded})
	if err != nil {
		return EntryOutcome{}, err
	}
	result, err := decodeSingleResult(output.Bytes())
	if err != nil {
		return EntryOutcome{}, err
	}
	if summary.Total != 1 {
		return EntryOutcome{}, fmt.Errorf("probe runner returned total %d for fleet entry %q", summary.Total, entry.ID)
	}
	if result.Pass {
		return EntryOutcome{Pass: true}, nil
	}
	return EntryOutcome{Err: resultError(result)}, nil
}

func loadEntryScenario(entry Entry) (probe.Scenario, error) {
	loaded, err := scenario.LoadFile(entry.ScenarioPath)
	if err != nil {
		return probe.Scenario{}, fmt.Errorf("load scenario %q: %w", entry.ScenarioPath, err)
	}
	if !scenarioMatchesEntry(loaded, entry) {
		return probe.Scenario{}, fmt.Errorf("scenario %q does not match manifest entry %q", entry.ScenarioPath, entry.ID)
	}
	return loaded, nil
}

func scenarioMatchesEntry(loaded probe.Scenario, entry Entry) bool {
	return strings.TrimSpace(loaded.ID) == entry.ScenarioID || strings.TrimSpace(loaded.Name) == entry.ScenarioID
}

func decodeSingleResult(data []byte) (probe.ScenarioResult, error) {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var result probe.ScenarioResult
		if err := json.Unmarshal([]byte(line), &result); err != nil {
			return probe.ScenarioResult{}, fmt.Errorf("decode fleet probe result: %w", err)
		}
		if result.Name == "" {
			continue
		}
		return result, nil
	}
	return probe.ScenarioResult{}, errors.New("probe runner returned no scenario result for fleet entry")
}

// resultError explains a failed result by its error or first failed
// expectation.
func resultError(result probe.ScenarioResult) error {
	if result.Error != "" {
		return errors.New(result.Error)
	}
	for _, outcome := range result.ScenarioExpectationOutcomes {
		if !outcome.Passed {
			if outcome.Error != "" {
				return errors.New(outcome.Error)
			}
			return fmt.Errorf("probe expectation %d failed", outcome.Index)
		}
	}
	return errors.New("probe expectations failed")
}
