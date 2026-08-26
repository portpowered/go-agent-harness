package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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

func executeCLIWithFleetExecutor(executor fleet.EntryExecutor, args ...string) cliExecution {
	root := newTestRootCommand(executor)
	var stdout, stderr strings.Builder
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)

	exitCode := 0
	if root.Execute() != nil {
		exitCode = 1
	}
	return cliExecution{exitCode: exitCode, stdout: stdout.String(), stderr: stderr.String()}
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

func TestProbeFleetJSONReconcilesEveryManifestEntry(t *testing.T) {
	dir := t.TempDir()
	observationCount := len(probeFixtureObservation(t).Observations)
	first := writeProbeScenario(t, dir, "fleet-first", observationCount)
	second := writeProbeScenario(t, dir, "fleet-second", observationCount)
	manifest, err := fleet.Compose(fleet.ComposeInput{
		ScenarioFiles: []string{first, second},
		Transports:    []fleet.Transport{fleet.TransportLive, fleet.TransportReplay},
		RepeatCount:   2,
		Concurrency:   3,
	})
	if err != nil {
		t.Fatalf("compose fleet: %v", err)
	}
	manifestPath := filepath.Join(dir, "fleet.json")
	writeFleetManifest(t, manifestPath, manifest)

	failedID := manifest.Entries[len(manifest.Entries)-1].ID
	run := executeCLIWithFleetExecutor(func(_ context.Context, entry fleet.Entry) (fleet.EntryOutcome, error) {
		if entry.ID == failedID {
			return fleet.EntryOutcome{}, errors.New("synthetic fleet failure")
		}
		return fleet.EntryOutcome{Pass: true}, nil
	}, "probe", "fleet", "--manifest", manifestPath, "--json")
	if run.exitCode != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}

	var result fleet.Result
	decoder := json.NewDecoder(strings.NewReader(run.stdout))
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("decode fleet JSON: %v; stdout=%q", err, run.stdout)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("fleet JSON contains multiple documents: err=%v trailing=%v", err, trailing)
	}

	wantTotal := 2 * 2 * 2
	if result.Total != wantTotal || result.Total != manifest.EntryCount() {
		t.Fatalf("total = %d, want manifest entry count %d", result.Total, manifest.EntryCount())
	}
	if result.Passed != wantTotal-1 || result.Failed != 1 || result.Passed+result.Failed != result.Total {
		t.Fatalf("counts = passed %d failed %d total %d, want %d/%d/%d", result.Passed, result.Failed, result.Total, wantTotal-1, 1, wantTotal)
	}
	if result.Status != "fail" {
		t.Fatalf("status = %q, want fail", result.Status)
	}
	if len(result.Entries) != manifest.EntryCount() {
		t.Fatalf("result entry count = %d, want %d", len(result.Entries), manifest.EntryCount())
	}

	seen := make(map[string]struct{}, len(result.Entries))
	for index, got := range result.Entries {
		want := manifest.Entries[index]
		if _, exists := seen[got.ID]; exists {
			t.Fatalf("duplicate result ID %q", got.ID)
		}
		seen[got.ID] = struct{}{}
		if got.ID != want.ID || got.ScenarioID != want.ScenarioID || got.ScenarioPath != want.ScenarioPath || got.Transport != want.Transport || got.RepeatIndex != want.RepeatIndex {
			t.Fatalf("result[%d] coordinates = %+v, want entry %+v", index, got, want)
		}
		wantPass := want.ID != failedID
		if got.Pass != wantPass {
			t.Fatalf("result[%d] pass = %t, want %t", index, got.Pass, wantPass)
		}
	}
	if len(seen) != manifest.EntryCount() {
		t.Fatalf("unique result IDs = %d, want %d", len(seen), manifest.EntryCount())
	}
}
