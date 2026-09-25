package scenariov2

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

type allowAllCorpus struct{}

func (allowAllCorpus) Has(string) bool { return true }

func writeScenarioFile(t *testing.T, dir, name, document string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestLoadSelectionsKeepsInvalidDocumentsAndDeduplicates(t *testing.T) {
	dir := t.TempDir()
	valid := `{"schema_version":"probe.scenario.v2","id":"dup","steps":[{"type":"close"}],"expectations":[{"type":"browser_connection_closed"}]}`
	first := writeScenarioFile(t, dir, "first.json", valid)
	second := writeScenarioFile(t, dir, "second.json", valid)
	broken := writeScenarioFile(t, dir, "broken.json", `{"schema_version":"probe.scenario.v2","id":"broken","browser_fixture":"../outside.json","steps":[{"type":"close"}],"expectations":[{"type":"browser_connection_closed"}]}`)
	entries, err := LoadSelections([]string{first, second, broken}, allowAllCorpus{})
	if err != nil {
		t.Fatalf("load selections: %v", err)
	}
	if len(entries) != 2 || entries[0].Scenario.ID != "dup" || entries[1].Err == nil {
		t.Fatalf("entries = %+v, want deduplicated valid entry and failed broken entry", entries)
	}
	result := testRunner().Execute(context.Background(), entries[1], "")
	if result.Pass || result.ID != broken || result.SchemaVersion != probe.ScenarioV2Version {
		t.Fatalf("broken selection result = %+v", result)
	}

	legacy := writeScenarioFile(t, dir, "legacy.json", `{"id":"legacy"}`)
	if _, err := LoadSelections([]string{first, legacy}, allowAllCorpus{}); err == nil || !strings.Contains(err.Error(), "cannot mix") {
		t.Fatalf("mixed selection error = %v", err)
	}
	if _, err := LoadSelections(nil, allowAllCorpus{}); err == nil {
		t.Fatal("empty selection was accepted")
	}
	if _, err := LoadSelections([]string{dir}, allowAllCorpus{}); err == nil || !strings.Contains(err.Error(), "path is a directory") {
		t.Fatalf("directory selection error = %v", err)
	}
}

func TestToLegacyProjectsProviderSteps(t *testing.T) {
	versioned := probe.ScenarioV2{
		SchemaVersion: probe.ScenarioV2Version,
		ID:            "legacy-projection",
		Steps: []probe.ScenarioV2Step{
			{Type: probe.ScenarioV2StepSendText, Text: "hello"},
			{Type: probe.ScenarioV2StepSleepFake, DurationMS: 5},
			{Type: probe.ScenarioV2StepClose},
		},
		Expectations: []probe.ScenarioV2Expectation{{Type: probe.ScenarioV2ExpectationTranscriptContains, Text: "hello"}},
	}
	legacy, err := ToLegacy(versioned, allowAllCorpus{})
	if err != nil {
		t.Fatalf("project v2 scenario: %v", err)
	}
	if len(legacy.Steps) != 3 || legacy.Steps[0].Type != probe.StepSendText || legacy.Steps[1].Duration != 5 {
		t.Fatalf("projected steps = %+v", legacy.Steps)
	}
	browser := versioned
	browser.Steps = []probe.ScenarioV2Step{{Type: probe.ScenarioV2StepBrowserConnect}}
	if _, err := ToLegacy(browser, allowAllCorpus{}); err == nil || !strings.Contains(err.Error(), "browser-aware probe executor") {
		t.Fatalf("browser step projection error = %v", err)
	}
	fixture := versioned
	fixture.BrowserFixture = "browser.json"
	if _, err := ToLegacy(fixture, allowAllCorpus{}); err == nil {
		t.Fatal("fixture-bearing scenario was projected")
	}
}

func TestRecordingDirectoryIsRunScopedAndSlugged(t *testing.T) {
	if got := RecordingDirectory("", 0, Selection{}); got != "" {
		t.Fatalf("empty root directory = %q", got)
	}
	got := RecordingDirectory("/root", 1, Selection{Scenario: probe.ScenarioV2{ID: "a b/c."}})
	if got != filepath.Join("/root", "002-a_b_c") {
		t.Fatalf("slugged directory = %q", got)
	}
	if got := RecordingDirectory("/root", 0, Selection{Selection: "..."}); got != filepath.Join("/root", "001-scenario") {
		t.Fatalf("fallback directory = %q", got)
	}
	root, err := PrepareRecordingRoot(filepath.Join(t.TempDir(), "nested", "root"), 1)
	if err != nil || !filepath.IsAbs(root) {
		t.Fatalf("prepare configured root = %q, %v", root, err)
	}
	if info, statErr := os.Stat(root); statErr != nil || !info.IsDir() {
		t.Fatalf("configured root not created: %v", statErr)
	}
}
