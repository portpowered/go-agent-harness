package sessionturn

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
)

type ImageCapabilities struct {
	Model                   string
	SupportsImageInput      bool
	SupportedInputMIMETypes []string
}
type ImageModelMetadata struct {
	InputModalities         []string
	SupportedInputMIMETypes []string
}
type ImageCapabilityRequest struct {
	Provider        string
	Model           string
	ModelProvided   bool
	ModelCatalog    providers.ModelCatalog
	ConfiguredModel *ImageModelMetadata
}

const (
	MaxImageCount      = 16
	MaxImageBytes      = 8 << 20
	MaxImageTotalBytes = 32 << 20
)

// ImagePreparationRequest is the complete host-resolved input for one image surface. The session-turn service validates the provider capability, reads bounded image content, binds read_image to that same capability snapshot, staging a private copy when the tool is advertised.
type ImagePreparationRequest struct {
	SourcePaths            []string
	Capabilities           ImageCapabilities
	CapabilityRequest      *ImageCapabilityRequest
	StagingRoot            string
	ToolExecutor           messages.ToolExecutor
	ToolDefinitions        []messages.ToolDefinition
	RefreshToolDefinitions func(context.Context) ([]messages.ToolDefinition, error)
}

// ImagePreparationResult is independently owned by the service caller. The
// cleanup function is idempotent when supplied by the staging implementation.
type ImagePreparationResult struct {
	Parts                  []messages.ImagePart
	Capabilities           ImageCapabilities
	ToolExecutor           messages.ToolExecutor
	ToolDefinitions        []messages.ToolDefinition
	RefreshToolDefinitions func(context.Context) ([]messages.ToolDefinition, error)
	Cleanup                func() error
}

type ImageRequest struct {
	Parts               []messages.ImagePart
	DeferResponse       bool
	FirstTurn           chan error
	PromptSentinel      string
	DeferredInstruction string
}

const (
	ImageOnlyPrompt          = "\x00agent-session-image-turn\x00"
	DeferredImageInstruction = "Use the attached image to answer the user's next spoken question."
)

const (
	ErrImageMissingFile     sentinelError = "session image file is missing"
	ErrImageUnreadableFile  sentinelError = "session image file is unreadable"
	ErrImageUnsupportedMIME sentinelError = "session image MIME type is unsupported"
	ErrImageInvalidContent  sentinelError = "session image content is invalid"
	ErrImageEmptyFile       sentinelError = "session image file is empty"
	ErrImageCountLimit      sentinelError = "session image count exceeds the limit"
	ErrImageAggregateLimit  sentinelError = "session image aggregate exceeds the limit"
	ErrImageCapability      sentinelError = "session image capability is unsupported"
	ErrImageSend            sentinelError = "session image turn could not be sent"
)

type ImageCapabilityError struct{ Model, Capability string }

func (e *ImageCapabilityError) Error() string {
	return fmt.Sprintf("model %q does not support %s capability", e.Model, e.Capability)
}
func (*ImageCapabilityError) Unwrap() error { return ErrImageCapability }

type ImageFileError struct {
	Path, DetectedMIME string
	SupportedMIME      []string
	Cause, Kind        error
	Message            string
}

func (e *ImageFileError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return e.Message
}
func (e *ImageFileError) Unwrap() []error {
	if e == nil {
		return nil
	}
	return []error{e.Kind, e.Cause}
}

type ImageEmptyFileError struct{ Path string }

func (e *ImageEmptyFileError) Error() string { return fmt.Sprintf("session image %q is empty", e.Path) }
func (*ImageEmptyFileError) Unwrap() error   { return ErrImageEmptyFile }
