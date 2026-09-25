// Package instructions normalizes the host workspace facts that select a
// session's instruction value before the session service resolves it.
package instructions

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

const errNoResolver sessionturn.Error = "session instruction service is not configured"

// Resolve selects the workspace, validates a host-selected workspace that has
// no launch policy, and resolves the instruction value. A missing workspace is
// a configuration error, not an empty prompt.
func Resolve(ctx context.Context, service session.InstructionService, request sessionturn.InstructionsRequest) (string, error) {
	if service == nil {
		return "", errNoResolver
	}
	normalized, err := Normalize(request)
	if err != nil {
		return "", err
	}
	result, err := service.Resolve(ctx, normalized)
	if err != nil {
		return "", err
	}
	return result.Instructions, nil
}

// Normalize builds the session instruction request. Without a launch policy
// an empty workspace falls back to the config directory, preserving the
// direct service API's historical behavior.
func Normalize(request sessionturn.InstructionsRequest) (session.InstructionRequest, error) {
	workDir := request.WorkDir
	if workDir == "" && request.Scope == nil {
		workDir = request.ConfigDir
	}
	if workDir != "" && request.Scope == nil && request.ResolveWorkspace != nil {
		root, err := request.ResolveWorkspace(workDir)
		if err != nil {
			return session.InstructionRequest{}, fmt.Errorf("resolve filesystem scope: %w", err)
		}
		workDir = root
	}
	normalized := session.InstructionRequest{Prompt: request.Prompt, WorkspaceDir: workDir}
	if request.Loader != nil {
		normalized.Loader = request.Loader(workDir)
	}
	if request.Scope != nil {
		normalized.FilesystemScopeSet = true
		normalized.FilesystemScopeDescription = request.Scope.Description
	}
	return normalized, nil
}
