// Package bundle finalizes and verifies the probe.scenario.v2 evidence
// bundle: a v2 recording manifest plus provider capture, page-state oracle,
// workspace snapshot, optional browser events, and objective evidence.
package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/scenariov2/internal/objective"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

const (
	manifestFileName  = "manifest.json"
	objectiveVersion  = "probe.objective-evidence.v1"
	workspaceVersion  = "probe.workspace-snapshot.v1"
	defaultModelLabel = "fixture"
)

// Summary makes the run relationship inspectable from the result line. The
// manifest remains the authoritative source for each artifact's digest and
// contains every listed path exactly once.
type Summary struct {
	ScenarioID            string `json:"scenario_id"`
	ManifestPath          string `json:"manifest_path"`
	ProviderCapturePath   string `json:"provider_capture_path"`
	BrowserEventsPath     string `json:"browser_events_path,omitempty"`
	PageStatePath         string `json:"page_state_path"`
	WorkspaceSnapshotPath string `json:"workspace_snapshot_path"`
	ObjectiveEvidencePath string `json:"objective_evidence_path"`
}

type objectiveArtifact struct {
	Version               string                `json:"version"`
	ScenarioID            string                `json:"scenario_id"`
	CheckedClaim          string                `json:"checked_claim"`
	Verified              bool                  `json:"verified"`
	ProviderCapturePath   string                `json:"provider_capture_path"`
	BrowserEventsPath     string                `json:"browser_events_path,omitempty"`
	PageStatePath         string                `json:"page_state_path"`
	WorkspaceSnapshotPath string                `json:"workspace_snapshot_path"`
	Error                 string                `json:"error,omitempty"`
	Divergence            *objective.Divergence `json:"divergence,omitempty"`
}

type workspaceSnapshot struct {
	Version    string           `json:"version"`
	ScenarioID string           `json:"scenario_id"`
	Scenario   probe.ScenarioV2 `json:"scenario"`
}

func jsonLine(value any) ([]byte, error) {
	encoded, err := testkit.JSONValue(value)
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func browserRecordingArtifact(data []byte) *transcript.BrowserArtifact {
	digest := sha256.Sum256(data)
	return &transcript.BrowserArtifact{
		Format: transcript.BrowserEventsVersion,
		Path:   transcript.BrowserArtifactDefaultPath,
		Data:   append([]byte(nil), data...),
		SHA256: hex.EncodeToString(digest[:]),
		Redaction: transcript.BrowserRedactionPolicy{
			URLQuery:    true,
			URLFragment: true,
			RawCDP:      false,
		},
	}
}

func browserArtifactPath(artifact *transcript.BrowserArtifact) string {
	if artifact == nil {
		return ""
	}
	return artifact.Path
}

func optionalEvidencePath(destination, relative string) string {
	if relative == "" {
		return ""
	}
	return filepath.Join(destination, relative)
}

func readArtifact(destination, relative string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(relative)))
	if err != nil {
		return nil, fmt.Errorf("read evidence artifact %q: %w", relative, err)
	}
	return data, nil
}

func manifestBrowserArtifact(manifest transcript.RecordingManifest) *transcript.BrowserArtifact {
	if manifest.Browser == nil {
		return nil
	}
	return &transcript.BrowserArtifact{
		Format: manifest.Browser.Format,
		Path:   manifest.Browser.Artifact.Path,
		SHA256: manifest.Browser.Artifact.SHA256,
		Redaction: transcript.BrowserRedactionPolicy{
			URLQuery:           manifest.Browser.Redaction.URLQuery,
			URLFragment:        manifest.Browser.Redaction.URLFragment,
			ToolArguments:      append([]string(nil), manifest.Browser.Redaction.ToolArguments...),
			ResultJSONPointers: append([]string(nil), manifest.Browser.Redaction.ResultJSONPointers...),
			DigestTools:        append([]string(nil), manifest.Browser.Redaction.DigestTools...),
			RawCDP:             manifest.Browser.Redaction.RawCDP,
		},
	}
}
