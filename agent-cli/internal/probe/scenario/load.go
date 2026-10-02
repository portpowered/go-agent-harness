// Package scenario loads, selects, and plans probe scenarios for the offline
// JSONL probe runner. Every scenario document is probe.scenario.v2: a
// provider-only document runs here as its provider plan, and every other
// document belongs to the browser-aware scenariov2 executor.
package scenario

import (
	"fmt"
	"os"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/replay"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/scenariov2"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

// LoadV2File validates a probe.scenario.v2 document with the offline
// committed-corpus lookup, resolving fixture references relative to the
// document's canonical containing directory.
func LoadV2File(path string) (probe.ScenarioV2, error) {
	return probe.LoadScenarioV2File(path, replay.Lookup{})
}

// LoadFile loads one provider-only probe.scenario.v2 document as the
// provider runner's execution plan.
func LoadFile(path string) (probe.Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return probe.Scenario{}, fmt.Errorf("read scenario %q: %w", path, err)
	}
	if !scenariov2.HasEnvelope(data) {
		return probe.Scenario{}, fmt.Errorf("scenario %q is not a %s document (it has no schema_version)", path, probe.ScenarioV2Version)
	}
	versioned, err := LoadV2File(path)
	if err != nil {
		return probe.Scenario{}, err
	}
	return versioned.ProviderScenario(replay.Lookup{})
}

// NeedsBrowserExecutor reports whether any selection names a probe.scenario.v2
// document that is not provider-only (or does not load), which routes the
// whole run to the browser-aware executor.
func NeedsBrowserExecutor(selections []string) (bool, error) {
	for _, selection := range selections {
		isV2, err := scenariov2.FileIsV2(selection)
		if err != nil {
			return false, err
		}
		if isV2 && !providerOnlyFile(selection) {
			return true, nil
		}
	}
	return false, nil
}

// providerOnlyFile reports whether path loads as a provider-only document. A
// document that fails to load is not: the browser-aware executor reports its
// load error as a failed result line.
func providerOnlyFile(path string) bool {
	versioned, err := LoadV2File(path)
	return err == nil && versioned.ProviderOnly()
}
