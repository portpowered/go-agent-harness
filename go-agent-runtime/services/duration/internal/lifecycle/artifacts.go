package lifecycle

import (
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/duration/internal/artifacts"
)

func prepareArtifacts(req RunRequest) (ArtifactLifecycle, error) {
	artifactLifecycle := req.Artifacts
	if artifactLifecycle == nil && req.ArtifactPaths != nil {
		created, err := artifacts.New(*req.ArtifactPaths)
		if err != nil {
			return nil, fmt.Errorf("open duration artifacts: %w", err)
		}
		artifactLifecycle = created
	}
	if artifactLifecycle != nil && req.TerminalRecorder != nil {
		artifactLifecycle = artifacts.WithTerminalRecorder(artifactLifecycle, req.TerminalRecorder)
	}
	return artifactLifecycle, nil
}
