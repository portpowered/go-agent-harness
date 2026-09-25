package bundle

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/scenariov2/internal/objective"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	replaywire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/wire"
)

func providerOnlyInput(t *testing.T) Input {
	t.Helper()
	return Input{
		Destination: filepath.Join(t.TempDir(), "bundle"),
		Scenario: probe.ScenarioV2{
			SchemaVersion: probe.ScenarioV2Version,
			ID:            "provider-only",
			Steps:         []probe.ScenarioV2Step{{Type: probe.ScenarioV2StepClose}},
		},
		Replay:    replaywire.NewService(),
		PageState: json.RawMessage(`null`),
		Transport: "replay",
		ClockBase: "fake:0",
	}
}

func TestFinalizeWritesVerifiableProviderOnlyBundle(t *testing.T) {
	in := providerOnlyInput(t)
	out, err := Finalize(context.Background(), in)
	if err != nil {
		t.Fatalf("finalize provider-only bundle: %v", err)
	}
	if !out.Objective.Verified || out.Objective.ArtifactPath != objective.ObjectiveArtifactPath || out.Summary.BrowserEventsPath != "" {
		t.Fatalf("finalized output = %+v", out)
	}
	if out.Summary.ManifestPath != filepath.Join(in.Destination, manifestFileName) {
		t.Fatalf("manifest path = %q", out.Summary.ManifestPath)
	}
	if _, err := Verify(context.Background(), in.Replay, in.Destination, in.Scenario); err != nil {
		t.Fatalf("re-verify finalized bundle: %v", err)
	}
	other := in.Scenario
	other.ID = "different-run"
	if _, err := Verify(context.Background(), in.Replay, in.Destination, other); err == nil || !strings.Contains(err.Error(), "workspace snapshot") {
		t.Fatalf("verify against a different scenario = %v, want identity rejection", err)
	}
	if err := os.WriteFile(filepath.Join(in.Destination, objective.PageStateArtifactPath), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("tamper page state: %v", err)
	}
	if _, err := Verify(context.Background(), in.Replay, in.Destination, in.Scenario); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("verify tampered bundle = %v, want hash mismatch", err)
	}
}

func TestFinalizeRejectsIncompleteInputs(t *testing.T) {
	in := providerOnlyInput(t)
	in.Destination = " "
	if _, err := Finalize(context.Background(), in); err == nil || !strings.Contains(err.Error(), "destination is empty") {
		t.Fatalf("empty destination error = %v", err)
	}
	in = providerOnlyInput(t)
	in.Replay = nil
	if _, err := Finalize(context.Background(), in); err == nil || !strings.Contains(err.Error(), "replay service is required") {
		t.Fatalf("missing replay error = %v", err)
	}
	in = providerOnlyInput(t)
	in.Scenario.BrowserFixture = "fixture.browser.json"
	if _, err := Finalize(context.Background(), in); err == nil || !strings.Contains(err.Error(), "without browser event evidence") {
		t.Fatalf("missing browser evidence error = %v", err)
	}
	if _, err := Verify(context.Background(), replaywire.NewService(), t.TempDir(), in.Scenario); err == nil || !strings.Contains(err.Error(), "read manifest") {
		t.Fatalf("verify empty destination = %v", err)
	}
}
