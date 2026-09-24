package images

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const stagePhase = "stage session images"

// HasTool reports whether definitions advertise name.
func HasTool(definitions []messages.ToolDefinition, name string) bool {
	for _, definition := range definitions {
		if definition.Name == name {
			return true
		}
	}
	return false
}

// BindTools binds a private image preparer to an executor that advertises
// read_image. A capability failure is captured by the preparer so the session
// continues with a correlated tool failure.
func BindTools(binding sessionturn.ImageToolBinding) messages.ToolExecutor {
	if binding.Executor == nil || !HasTool(binding.Definitions, tools.ReadImageToolID) {
		return binding.Executor
	}
	binder, ok := binding.Executor.(tools.SessionImagePreparerBinder)
	if !ok {
		return binding.Executor
	}
	var capabilities sessionturn.ImageCapabilities
	var resolveErr error
	if binding.Resolve != nil {
		capabilities, resolveErr = binding.Resolve()
	}
	capabilities = CloneCapabilities(capabilities)
	load := binding.Load
	return binder.WithSessionImagePreparer(func(paths []string) ([]messages.ImagePart, error) {
		if resolveErr != nil {
			return nil, resolveErr
		}
		return PrepareParts(sessionturn.ImagePartsRequest{Paths: paths, Capabilities: capabilities, Load: load})
	})
}

// Stage gives read_image a session-owned copy of each initial image and
// advertises those exact paths. It resolves the host root only when
// read_image is advertised.
func Stage(ctx context.Context, staging tools.ImageStaging, request sessionturn.ImageStagingRequest) (tools.ImageStagingResult, error) {
	unchanged := tools.ImageStagingResult{
		ToolDefinitions:        request.ToolDefinitions,
		RefreshToolDefinitions: request.RefreshToolDefinitions,
		Cleanup:                noCleanup,
	}
	if !HasTool(request.ToolDefinitions, tools.ReadImageToolID) {
		return unchanged, nil
	}
	if len(request.SourcePaths) != len(request.Parts) {
		return unchanged, fmt.Errorf("%s: source path count %d does not match image part count %d", stagePhase, len(request.SourcePaths), len(request.Parts))
	}
	if request.StagingRoot == nil || staging == nil {
		return unchanged, fmt.Errorf("%s: %w", stagePhase, errNoStaging)
	}
	root, err := request.StagingRoot()
	if err != nil {
		return unchanged, fmt.Errorf("%s: %w", stagePhase, err)
	}
	staged, err := staging.Stage(ctx, tools.ImageStagingRequest{
		StagingRoot:            root,
		SourcePaths:            request.SourcePaths,
		ImageParts:             request.Parts,
		ToolDefinitions:        request.ToolDefinitions,
		RefreshToolDefinitions: request.RefreshToolDefinitions,
	})
	if err != nil {
		return unchanged, err
	}
	if staged.Cleanup == nil {
		staged.Cleanup = noCleanup
	}
	return staged, nil
}

const errNoStaging sessionturn.Error = "image staging is not configured"

func noCleanup() error { return nil }
