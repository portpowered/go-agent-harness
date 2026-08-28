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
