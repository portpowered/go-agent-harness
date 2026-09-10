package tools

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ImageStagingRequest is the host-resolved input for one session's staged
// image lifecycle. The tools service never discovers a config directory or
// reads host configuration; the caller supplies the explicit root and the
// provider-validated image buffers.
type ImageStagingRequest struct {
	StagingRoot string
	SourcePaths []string
	ImageParts  []messages.ImagePart

	// ToolDefinitions is the complete current tool snapshot. The returned
	// snapshot is independently owned by the staging implementation.
	ToolDefinitions []messages.ToolDefinition
	// RefreshToolDefinitions returns a complete replacement snapshot. The
	// staging implementation decorates every successful snapshot with the
	// session-owned image paths while preserving callback errors.
	RefreshToolDefinitions func(context.Context) ([]messages.ToolDefinition, error)
}

// ImageStagingResult is the isolated output of one staging operation.
// Cleanup is safe to call more than once and removes only this operation's
// private staging directory.
type ImageStagingResult struct {
	StagedPaths []string

	ToolDefinitions        []messages.ToolDefinition
	RefreshToolDefinitions func(context.Context) ([]messages.ToolDefinition, error)
	Cleanup                func() error
}

// ImageStaging owns the session-scoped filesystem lifecycle needed by the
// read_image tool. Its implementation is private to the tools service and is
// constructed through services/tools/wire.
type ImageStaging interface {
	Stage(context.Context, ImageStagingRequest) (ImageStagingResult, error)
}

// ImageStager is a descriptive alias for embedders that prefer the role name.
type ImageStager = ImageStaging
