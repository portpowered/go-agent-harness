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
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
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
	if err := root.Execute(); err != nil {
		exitCode = 1
		writeSimulatedMainError(&stderr, err)
	}
	return cliExecution{exitCode: exitCode, stdout: stdout.String(), stderr: stderr.String()}
}

func executeCLIWithProbeFleetCommand(command *ProbeFleetCommand, args ...string) cliExecution {
	root := newTestRootCommandWithProbeFleetCommand(command)
	var stdout, stderr strings.Builder
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)

	exitCode := 0
	if err := root.Execute(); err != nil {
		exitCode = 1
		writeSimulatedMainError(&stderr, err)
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

func TestProbeFleetDispatchesLiveEntryThroughSessionRuntime(t *testing.T) {
	dir := t.TempDir()
	scenario := writeProbeScenario(t, dir, "session_healthy_multiturn_audio", len(probeFixtureObservation(t).Observations))
	manifest, err := fleet.Compose(fleet.ComposeInput{
		ScenarioFiles: []string{scenario},
		Transports:    []fleet.Transport{fleet.TransportLive},
		RepeatCount:   1,
		Concurrency:   1,
	})
	if err != nil {
		t.Fatalf("compose fleet: %v", err)
	}
	manifestPath := filepath.Join(dir, "fleet.json")
	writeFleetManifest(t, manifestPath, manifest)

	var gotOptions services.SessionRunOptions
	var gotInput services.SessionAudioInput
	command := NewProbeFleetCommand()
	command.LiveSessionRunner = func(_ context.Context, _ io.Writer, options services.SessionRunOptions, input services.SessionAudioInput) error {
		gotOptions = options
		gotInput = input
		capture, readErr := os.ReadFile(probeSessionFixture)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(options.RecordPath, capture, 0o600)
	}

	run := executeCLIWithProbeFleetCommand(command,
		"--config-dir", filepath.Join(dir, "config"),
		"probe", "fleet", "--manifest", manifestPath,
		"--provider", "grok", "--model", "grok-test", "--api-key", "test-key", "--base-url", "wss://grok.test",
	)
	if run.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	if !strings.Contains(run.stdout, "fleet: pass scenario=session_healthy_multiturn_audio transport=live repeat=0") {
		t.Fatalf("live entry was not reported as passing: %q", run.stdout)
	}
	if gotOptions.Provider != "grok" || gotOptions.Model != "grok-test" || !gotOptions.ModelProvided || gotOptions.APIKey != "test-key" || gotOptions.BaseURL != "wss://grok.test" {
		t.Fatalf("live session options = %+v, want command flags", gotOptions)
	}
	if gotOptions.ConfigDir != filepath.Join(dir, "config") || filepath.Ext(gotOptions.RecordPath) != ".json" {
		t.Fatalf("live session config/capture = %q/%q, want inherited config dir and JSON capture", gotOptions.ConfigDir, gotOptions.RecordPath)
	}
	if gotInput.Present || gotInput.Path != "" {
		t.Fatalf("text-only live scenario audio input = %+v, want absent", gotInput)
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
	// Regression guard: the failure line must be rendered exactly once (by
	// cmd/agent's main.go, simulated here via writeSimulatedMainError),
	// never once by Cobra's own error printing and again by main.go.
	if got := strings.Count(run.stderr, "1 of 2 fleet entries failed"); got != 1 {
		t.Fatalf("failure summary appeared %d times in stderr, want exactly once: %q", got, run.stderr)
	}
}

// TestProbeFleetCommandSetsSilenceErrors is a direct, cobra-version-agnostic
// regression guard for the "probe fleet prints its own error twice" defect:
// without SilenceErrors, Cobra prints "Error: ..." itself in addition to
// cmd/agent's main.go, which prints "Error: %s" for every error Execute()
// returns.
func TestProbeFleetCommandSetsSilenceErrors(t *testing.T) {
	cmd := NewProbeFleetCommand().Generate()
	if !cmd.SilenceErrors {
		t.Fatal("probe fleet command does not set SilenceErrors: a failure would print twice (once from Cobra, once from cmd/agent's main.go)")
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

func TestProbeFleetRunsCommittedS2SScenariosThroughRealArgv(t *testing.T) {
	manifest, err := fleet.Compose(fleet.ComposeInput{
		ScenarioFiles: []string{v2aNoRespScenario, v2aHappyScenario},
		Transports:    []fleet.Transport{fleet.TransportReplay},
		RepeatCount:   2,
		Concurrency:   2,
	})
	if err != nil {
		t.Fatalf("compose committed s2s fleet: %v", err)
	}
	manifestPath := filepath.Join(t.TempDir(), "fleet.json")
	writeFleetManifest(t, manifestPath, manifest)

	run := executeCLI(
		"probe", "fleet", "--manifest", manifestPath,
		"--replay", "testdata/probe-fixtures", "--json",
	)
	if run.exitCode != 1 {
		t.Fatalf("exit code = %d, want 1 for committed negative control; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}

	var result fleet.Result
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatalf("decode committed fleet result %q: %v", run.stdout, err)
	}
	if result.Total != manifest.EntryCount() || result.Passed != 2 || result.Failed != 2 || result.Passed+result.Failed != result.Total {
		t.Fatalf("committed fleet counts = passed %d failed %d total %d, want 2/2/%d", result.Passed, result.Failed, result.Total, manifest.EntryCount())
	}
	if len(result.Entries) != manifest.EntryCount() {
		t.Fatalf("committed fleet result entries = %d, want %d", len(result.Entries), manifest.EntryCount())
	}

	seen := make(map[string]struct{}, len(result.Entries))
	for index, got := range result.Entries {
		want := manifest.Entries[index]
		if _, exists := seen[got.ID]; exists {
			t.Fatalf("duplicate committed fleet result ID %q", got.ID)
		}
		seen[got.ID] = struct{}{}
		if got.ID != want.ID || got.ScenarioID != want.ScenarioID || got.Transport != want.Transport || got.RepeatIndex != want.RepeatIndex {
			t.Fatalf("committed fleet result[%d] = %+v, want coordinates %+v", index, got, want)
		}
		wantPass := got.ScenarioID == "s2s-v2a-audio-in-basic"
		if got.Pass != wantPass {
			t.Fatalf("committed fleet result[%d] pass = %t for %s, want %t", index, got.Pass, got.ScenarioID, wantPass)
		}
	}
	if len(seen) != manifest.EntryCount() {
		t.Fatalf("unique committed fleet result IDs = %d, want %d", len(seen), manifest.EntryCount())
	}
}
