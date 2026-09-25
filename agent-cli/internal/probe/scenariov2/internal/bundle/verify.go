package bundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/scenariov2/internal/objective"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
)

// Verify re-reads a finalized bundle from destination, checks every listed
// artifact digest and the run identity, and recomputes the objective verdict
// from the captured artifacts alone. The persisted objective evidence must be
// reproducible from those artifacts.
func Verify(ctx context.Context, replay runtimeReplay.Service, destination string, expected probe.ScenarioV2) (objective.Verification, error) {
	manifest, err := readManifest(destination)
	if err != nil {
		return objective.Verification{}, err
	}
	if err := verifyArtifactDigests(destination, manifest); err != nil {
		return objective.Verification{}, err
	}
	capture, err := readProviderCapture(ctx, replay, destination)
	if err != nil {
		return objective.Verification{}, err
	}
	pageState, err := readPageState(destination)
	if err != nil {
		return objective.Verification{}, err
	}
	workspace, err := readWorkspace(destination, expected)
	if err != nil {
		return objective.Verification{}, err
	}
	events, err := readBrowserEvents(destination, manifest, expected)
	if err != nil {
		return objective.Verification{}, err
	}
	persisted, err := readObjective(destination, manifest, expected)
	if err != nil {
		return objective.Verification{}, err
	}
	computed := objective.VerifyEvidenceData(workspace.Scenario, events, pageState, capture, manifest.Browser != nil)
	if persisted.CheckedClaim != computed.CheckedClaim || persisted.Verified != computed.Verified || persisted.Error != computed.Error || !objective.DivergenceEqual(persisted.Divergence, computed.Divergence) {
		return objective.Verification{}, errors.New("objective evidence is not reproducible from captured artifacts")
	}
	return computed, nil
}

func readManifest(destination string) (transcript.RecordingManifest, error) {
	manifestBytes, err := os.ReadFile(filepath.Join(destination, manifestFileName))
	if err != nil {
		return transcript.RecordingManifest{}, fmt.Errorf("read manifest: %w", err)
	}
	var manifest transcript.RecordingManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return transcript.RecordingManifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return transcript.RecordingManifest{}, fmt.Errorf("validate manifest: %w", err)
	}
	if manifest.FormatVersion != transcript.RecordingManifestV2Version {
		return transcript.RecordingManifest{}, fmt.Errorf("want recording manifest v%d, got %d", transcript.RecordingManifestV2Version, manifest.FormatVersion)
	}
	return manifest, nil
}

func verifyArtifactDigests(destination string, manifest transcript.RecordingManifest) error {
	hashes := make(map[string]string, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		if _, exists := hashes[artifact.Path]; exists {
			return fmt.Errorf("duplicate artifact %q", artifact.Path)
		}
		data, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(artifact.Path)))
		if err != nil {
			return fmt.Errorf("read artifact %q: %w", artifact.Path, err)
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != artifact.SHA256 {
			return fmt.Errorf("hash mismatch for artifact %q", artifact.Path)
		}
		hashes[artifact.Path] = artifact.SHA256
	}
	for _, path := range []string{
		objective.ProviderArtifactPath,
		objective.PageStateArtifactPath,
		objective.WorkspaceArtifactPath,
		objective.ObjectiveArtifactPath,
	} {
		if _, ok := hashes[path]; !ok {
			return fmt.Errorf("required evidence artifact %q is not listed", path)
		}
	}
	return nil
}

func readProviderCapture(ctx context.Context, replay runtimeReplay.Service, destination string) (runtimeReplay.CaptureProbeObservation, error) {
	providerData, err := readArtifact(destination, objective.ProviderArtifactPath)
	if err != nil {
		return runtimeReplay.CaptureProbeObservation{}, err
	}
	if replay == nil {
		return runtimeReplay.CaptureProbeObservation{}, errors.New("replay service is required to verify provider capture evidence")
	}
	return replay.InspectProbeDocument(ctx, objective.ProviderArtifactPath, providerData)
}

func readPageState(destination string) (json.RawMessage, error) {
	pageStateData, err := readArtifact(destination, objective.PageStateArtifactPath)
	if err != nil {
		return nil, err
	}
	pageState, err := testkit.JSONValue(json.RawMessage(pageStateData))
	if err != nil {
		return nil, fmt.Errorf("validate page-state oracle snapshot: %w", err)
	}
	return pageState, nil
}

func readWorkspace(destination string, expected probe.ScenarioV2) (workspaceSnapshot, error) {
	workspaceData, err := readArtifact(destination, objective.WorkspaceArtifactPath)
	if err != nil {
		return workspaceSnapshot{}, err
	}
	var workspace workspaceSnapshot
	if err := json.Unmarshal(workspaceData, &workspace); err != nil {
		return workspaceSnapshot{}, fmt.Errorf("decode workspace snapshot: %w", err)
	}
	if workspace.Version != workspaceVersion || workspace.ScenarioID == "" || workspace.ScenarioID != expected.ID || workspace.Scenario.ID != expected.ID || workspace.Scenario.SchemaVersion != expected.SchemaVersion {
		return workspaceSnapshot{}, errors.New("workspace snapshot does not identify the executed scenario")
	}
	return workspace, nil
}

func readBrowserEvents(destination string, manifest transcript.RecordingManifest, expected probe.ScenarioV2) ([]testkit.Event, error) {
	hasBrowserArtifact := manifest.Browser != nil
	if expected.BrowserFixture == "" {
		if hasBrowserArtifact {
			return nil, errors.New("provider-only v2 recording unexpectedly contains browser evidence")
		}
		return nil, nil
	}
	if !hasBrowserArtifact {
		return nil, errors.New("browser evidence is missing from the v2 manifest")
	}
	if manifest.Browser.Artifact.Path != transcript.BrowserArtifactDefaultPath {
		return nil, errors.New("browser evidence path is not the canonical artifact")
	}
	browserData, err := readArtifact(destination, manifest.Browser.Artifact.Path)
	if err != nil {
		return nil, err
	}
	events, err := testkit.ValidateEventStream(browserData)
	if err != nil {
		return nil, fmt.Errorf("validate persisted browser events: %w", err)
	}
	return events, nil
}

func readObjective(destination string, manifest transcript.RecordingManifest, expected probe.ScenarioV2) (objectiveArtifact, error) {
	objectiveData, err := readArtifact(destination, objective.ObjectiveArtifactPath)
	if err != nil {
		return objectiveArtifact{}, err
	}
	var persisted objectiveArtifact
	if err := json.Unmarshal(objectiveData, &persisted); err != nil {
		return objectiveArtifact{}, fmt.Errorf("decode objective evidence: %w", err)
	}
	if persisted.Version != objectiveVersion ||
		persisted.ScenarioID != expected.ID ||
		persisted.ProviderCapturePath != objective.ProviderArtifactPath ||
		persisted.PageStatePath != objective.PageStateArtifactPath ||
		persisted.WorkspaceSnapshotPath != objective.WorkspaceArtifactPath ||
		persisted.BrowserEventsPath != browserArtifactPath(manifestBrowserArtifact(manifest)) {
		return objectiveArtifact{}, errors.New("objective evidence references a different run or artifact set")
	}
	return persisted, nil
}
