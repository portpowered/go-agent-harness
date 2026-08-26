package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/fleet"
)

func writeFleetManifest(t *testing.T, path string, manifest fleet.Manifest) {
	t.Helper()
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshal fleet manifest: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write fleet manifest: %v", err)
	}
}

func TestProbeFleetRunsEveryEntryAndPrintsCoordinates(t *testing.T) {
	dir := t.TempDir()
	scenario := writeProbeScenario(t, dir, "session_healthy_multiturn_audio", len(probeFixtureObservation(t).Observations))
	manifest, err := fleet.Compose(fleet.ComposeInput{
		ScenarioFiles: []string{scenario},
		Transports:    []fleet.Transport{fleet.TransportReplay},
		RepeatCount:   2,
		Concurrency:   2,
	})
	if err != nil {
		t.Fatalf("compose fleet: %v", err)
	}
	manifestPath := filepath.Join(dir, "fleet.json")
	writeFleetManifest(t, manifestPath, manifest)

	run := executeCLI("probe", "fleet", "--manifest", manifestPath, "--replay", probeSessionFixture)
	if run.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	lines := strings.Split(strings.TrimSpace(run.stdout), "\n")
	if len(lines) != manifest.EntryCount() {
		t.Fatalf("entry line count = %d, want %d:\n%s", len(lines), manifest.EntryCount(), run.stdout)
	}
	for _, entry := range manifest.Entries {
		want := "fleet: pass scenario=" + entry.ScenarioID + " transport=" + string(entry.Transport) + " repeat="
		if !strings.Contains(run.stdout, want) || !strings.Contains(run.stdout, "id="+entry.ID) {
			t.Fatalf("output missing passing coordinates for %q: %q", entry.ID, run.stdout)
		}
	}
	if !strings.Contains(run.stderr, "fleet: 2/2 entries passed (pass)") {
		t.Fatalf("summary missing: %q", run.stderr)
	}
}

func TestProbeFleetReportsFailureAndContinuesOtherEntries(t *testing.T) {
	dir := t.TempDir()
	observationCount := len(probeFixtureObservation(t).Observations)
	failing := writeProbeScenario(t, dir, "fleet-failing", 999999)
	passing := writeProbeScenario(t, dir, "session_healthy_multiturn_audio", observationCount)
	manifest, err := fleet.Compose(fleet.ComposeInput{
		ScenarioFiles: []string{failing, passing},
		Transports:    []fleet.Transport{fleet.TransportReplay},
		RepeatCount:   1,
		Concurrency:   2,
	})
	if err != nil {
		t.Fatalf("compose fleet: %v", err)
	}
	manifestPath := filepath.Join(dir, "fleet.json")
	writeFleetManifest(t, manifestPath, manifest)

	run := executeCLI("probe", "fleet", "--manifest", manifestPath, "--replay", probeSessionFixture)
	if run.exitCode != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	if !strings.Contains(run.stdout, "fleet: fail scenario=fleet-failing transport=replay repeat=0") {
		t.Fatalf("failed entry is not reported: %q", run.stdout)
	}
	if !strings.Contains(run.stdout, "fleet: pass scenario=session_healthy_multiturn_audio transport=replay repeat=0") {
		t.Fatalf("unrelated passing entry did not run: %q", run.stdout)
	}
	if !strings.Contains(run.stderr, "fleet: 1/2 entries passed (fail)") || !strings.Contains(run.stderr, "1 of 2 fleet entries failed") {
		t.Fatalf("failure summary missing: %q", run.stderr)
	}
}
