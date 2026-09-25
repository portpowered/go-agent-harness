package scenario

import (
	"fmt"
	"os"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/replay"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/scenariov2"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

// LoadV2File validates a browser-aware probe document with the offline
// committed-corpus lookup, resolving fixture references relative to the
// document's canonical containing directory.
func LoadV2File(path string) (probe.ScenarioV2, error) {
	return probe.LoadScenarioV2File(path, replay.Lookup{})
}

// LoadFile loads one legacy scenario document. A provider-only
// probe.scenario.v2 document is projected onto the legacy shape; the legacy
// Parse path remains unchanged so its accepted aliases continue to load the
// same public values.
func LoadFile(path string) (probe.Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return probe.Scenario{}, fmt.Errorf("read scenario %q: %w", path, err)
	}
	if scenariov2.HasEnvelope(data) {
		versioned, loadErr := LoadV2File(path)
		if loadErr != nil {
			return probe.Scenario{}, loadErr
		}
		return scenariov2.ToLegacy(versioned, replay.Lookup{})
	}
	return Parse(data)
}

// ContainsV2 reports whether any selection names a probe.scenario.v2
// document, which routes the whole run to the browser-aware executor.
func ContainsV2(selections []string) (bool, error) {
	containsV2 := false
	for _, selection := range selections {
		isV2, err := scenariov2.FileIsV2(selection)
		if err != nil {
			return false, err
		}
		containsV2 = containsV2 || isV2
	}
	return containsV2, nil
}
