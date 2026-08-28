package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

func TestLoadProbeScenarioV2UsesOfflineCorpusLookup(t *testing.T) {
	data := []byte(`{"schema_version":"probe.scenario.v2","id":"cli-v2","steps":[{"type":"send_audio","corpus_id":"utterance-hello-there"}],"expectations":[{"type":"no_pending_invocations"}]}`)
	scenario, err := loadProbeScenarioV2(data, "")
	if err != nil {
		t.Fatalf("load v2 scenario: %v", err)
	}
	if scenario.ID != "cli-v2" || scenario.Steps[0].CorpusID != "utterance-hello-there" {
		t.Fatalf("loaded scenario = %#v", scenario)
	}

	unknown := []byte(`{"schema_version":"probe.scenario.v2","id":"cli-v2-unknown","steps":[{"type":"send_audio","corpus_id":"not-committed"}],"expectations":[{"type":"no_pending_invocations"}]}`)
	if _, err := loadProbeScenarioV2(unknown, ""); err == nil || !errors.Is(err, probe.ErrScenarioV2UnknownCorpus) {
		t.Fatalf("unknown corpus error = %v", err)
	}

	path := filepath.Join(t.TempDir(), "scenario.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	fromFile, err := loadProbeScenarioV2File(path)
	if err != nil || fromFile.ID != scenario.ID {
		t.Fatalf("load v2 scenario file = %#v, %v", fromFile, err)
	}
}

func TestResolveProbeSelectionDispatchesVersionedScenarioLoader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "versioned.scenario.json")
	data := []byte(`{"schema_version":"probe.scenario.v2","id":"cli-v2-selection","steps":[{"type":"send_text","text":"hello"},{"type":"close"}],"expectations":[{"type":"transcript_contains","text":"hello"}]}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write versioned scenario: %v", err)
	}
	scenarios, err := resolveProbeSelection(path)
	if err != nil {
		t.Fatalf("resolve versioned scenario: %v", err)
	}
	if len(scenarios) != 1 || scenarios[0].ID != "cli-v2-selection" || scenarios[0].Steps[0].Type != probe.StepSendText {
		t.Fatalf("resolved scenarios = %#v, want projected v2 session scenario", scenarios)
	}

	unsafePath := filepath.Join(dir, "unsafe.scenario.json")
	unsafe := []byte(`{"schema_version":"probe.scenario.v2","id":"cli-v2-unsafe","browser_fixture":"../outside.json","steps":[{"type":"close"}],"expectations":[{"type":"no_pending_invocations"}]}`)
	if err := os.WriteFile(unsafePath, unsafe, 0o600); err != nil {
		t.Fatalf("write unsafe versioned scenario: %v", err)
	}
	if _, err := resolveProbeSelection(unsafePath); err == nil || !errors.Is(err, probe.ErrScenarioV2FixturePath) {
		t.Fatalf("unsafe v2 selection error = %v, want contained-path error", err)
	}
}
