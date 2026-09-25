package replay

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

const (
	sessionFixtureSuffix = ".session.json"
	jsonSuffix           = ".json"
)

// LoadFixtures resolves a --replay value into named session fixture paths.
// A file names one fixture; a directory contributes every *.session.json
// entry keyed by its stem.
func LoadFixtures(replay string) (map[string]string, error) {
	info, statErr := os.Stat(replay)
	if statErr != nil {
		return nil, fmt.Errorf("replay fixture %q is missing or unreadable: %w", replay, statErr)
	}
	fixtures := map[string]string{}
	if !info.IsDir() {
		fixtures[fixtureStem(replay)] = replay
		return fixtures, nil
	}
	entries, readErr := os.ReadDir(replay)
	if readErr != nil {
		return nil, fmt.Errorf("read replay fixture directory %q: %w", replay, readErr)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), sessionFixtureSuffix) {
			continue
		}
		path := filepath.Join(replay, entry.Name())
		fixtures[fixtureStem(path)] = path
	}
	if len(fixtures) == 0 {
		return nil, fmt.Errorf("replay fixture directory %q contains no recorded session fixtures", replay)
	}
	return fixtures, nil
}

func fixtureStem(path string) string {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, sessionFixtureSuffix)
	return strings.TrimSuffix(base, jsonSuffix)
}

// FixtureForScenario resolves both the authored name and the filename
// spelling used by committed S2S fixtures. A single fixture matches any
// scenario.
func FixtureForScenario(fixtures map[string]string, scenario probe.Scenario) (string, error) {
	for _, candidate := range []string{scenario.Name, scenario.ID, ScenarioName(scenario)} {
		if fixture, ok := fixtures[candidate]; ok {
			return fixture, nil
		}
	}
	matched, err := normalizedFixtureMatch(fixtures, scenario)
	if err != nil || matched != "" {
		return matched, err
	}
	if len(fixtures) == 1 {
		for _, only := range fixtures {
			return only, nil
		}
	}
	return "", fmt.Errorf("no recorded fixture matches scenario %q", ScenarioName(scenario))
}

func normalizedFixtureMatch(fixtures map[string]string, scenario probe.Scenario) (string, error) {
	want := normalizeFixtureName(ScenarioName(scenario))
	var matched string
	for key, fixture := range fixtures {
		if normalizeFixtureName(key) != want {
			continue
		}
		if matched != "" && matched != fixture {
			return "", fmt.Errorf("multiple recorded fixtures match scenario %q", ScenarioName(scenario))
		}
		matched = fixture
	}
	return matched, nil
}

func normalizeFixtureName(value string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "_", "-")
}
