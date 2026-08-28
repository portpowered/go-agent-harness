package cli

import (
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

// loadProbeScenarioV2 validates a browser-aware probe document with the
// offline CLI's existing committed-corpus lookup. The legacy
// loadProbeScenario path remains unchanged so its accepted aliases continue
// to load the same public values.
func loadProbeScenarioV2(data []byte, scenarioPath string) (probe.ScenarioV2, error) {
	return probe.LoadScenarioV2(data, scenarioPath, replayCorpusLookup{})
}

// loadProbeScenarioV2File is the path-aware counterpart used by callers that
// want the loader to read the scenario and resolve both fixture references
// relative to its canonical containing directory.
func loadProbeScenarioV2File(path string) (probe.ScenarioV2, error) {
	return probe.LoadScenarioV2File(path, replayCorpusLookup{})
}
