package scenario

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/replay"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/scenariov2"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

// NoSelectionMessage is the operator guidance for a run without scenarios.
const NoSelectionMessage = scenariov2.NoSelectionMessage

// Selections merges positional and --scenario selections in order, dropping
// exact duplicates.
func Selections(positional, flags []string) []string {
	raw := append(append([]string{}, positional...), flags...)
	selections := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, selection := range raw {
		if _, exists := seen[selection]; exists {
			continue
		}
		seen[selection] = struct{}{}
		selections = append(selections, selection)
	}
	return selections
}

// Resolve resolves one selection into zero or more scenarios, in order:
// (1) a scenario file on disk, (2) an exact match against a registered
// scenario's ID or name, (3) a suite prefix match that expands to every
// registered scenario whose ID extends the selection with "-" (e.g.
// s2s-v6a-error-auth selects both of its cases).
func Resolve(selection string) ([]probe.Scenario, error) {
	if _, statErr := os.Stat(selection); statErr == nil {
		scenario, loadErr := LoadFile(selection)
		if loadErr != nil {
			return nil, fmt.Errorf("load probe scenario %q: %w", selection, loadErr)
		}
		return []probe.Scenario{scenario}, nil
	}
	registered := probe.Scenarios()
	for _, scenario := range registered {
		if scenario.ID == selection || replay.ScenarioName(scenario) == selection {
			return []probe.Scenario{scenario}, nil
		}
	}
	suite := make([]probe.Scenario, 0)
	for _, scenario := range registered {
		if strings.HasPrefix(scenario.ID, selection+"-") {
			suite = append(suite, scenario)
		}
	}
	sort.Slice(suite, func(i, j int) bool { return suite[i].ID < suite[j].ID })
	if len(suite) > 0 {
		return suite, nil
	}
	return nil, fmt.Errorf("unknown probe scenario %q: no such file and no registered scenario matches", selection)
}

// ResolveAll resolves every selection and keeps the first occurrence of each
// scenario identity. Every unknown selection is reported by name.
func ResolveAll(selections []string) ([]probe.Scenario, error) {
	if len(selections) == 0 {
		return nil, errors.New(NoSelectionMessage)
	}
	seen := make(map[string]struct{}, len(selections))
	scenarios := make([]probe.Scenario, 0, len(selections))
	for _, selection := range selections {
		resolved, err := Resolve(selection)
		if err != nil {
			return nil, err
		}
		for _, scenario := range resolved {
			key := scenario.ID + "\x00" + scenario.Name
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			scenarios = append(scenarios, scenario)
		}
	}
	return scenarios, nil
}

// ReplayPlan resolves the selected scenarios, validates every recorded
// fixture through the replay service, and returns the fixture-backed
// execution function.
func ReplayPlan(ctx context.Context, selections []string, executor replay.Executor) ([]probe.Scenario, probe.ExecFunc, error) {
	scenarios, err := ResolveAll(selections)
	if err != nil {
		return nil, nil, err
	}
	if executor.Service == nil {
		return nil, nil, fmt.Errorf("replay service is not configured")
	}
	for _, fixture := range executor.Fixtures {
		if _, err := executor.Service.InspectCapture(ctx, fixture); err != nil {
			return nil, nil, fmt.Errorf("invalid replay fixture %q: %w", fixture, err)
		}
	}
	return scenarios, executor.Exec, nil
}
