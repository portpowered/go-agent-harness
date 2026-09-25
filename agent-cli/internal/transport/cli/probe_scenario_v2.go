package cli

import (
	"fmt"
	"os"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/scenariov2"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

// loadProbeScenarioV2File validates a browser-aware probe document with the
// offline CLI's committed-corpus lookup, resolving fixture references
// relative to the document's canonical containing directory.
func loadProbeScenarioV2File(path string) (probe.ScenarioV2, error) {
	return probe.LoadScenarioV2File(path, replayCorpusLookup{})
}

// loadProbeScenarioFile loads one legacy scenario document. A provider-only
// probe.scenario.v2 document is projected onto the legacy shape; the legacy
// loadProbeScenario path remains unchanged so its accepted aliases continue
// to load the same public values.
func loadProbeScenarioFile(path string) (probe.Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return probe.Scenario{}, fmt.Errorf("read scenario %q: %w", path, err)
	}
	if scenariov2.HasEnvelope(data) {
		versioned, loadErr := loadProbeScenarioV2File(path)
		if loadErr != nil {
			return probe.Scenario{}, loadErr
		}
		return scenariov2.ToLegacy(versioned, replayCorpusLookup{})
	}
	return loadProbeScenario(data)
}

// probeScenarioFileIsV2 reports whether a selection names a v2 document.
func probeScenarioFileIsV2(path string) (bool, error) {
	return scenariov2.FileIsV2(path)
}
