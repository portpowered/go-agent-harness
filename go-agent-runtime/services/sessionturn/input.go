package sessionturn

import (
	"context"
	"fmt"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const (
	// TextSeedWirePrefix marks the loop prompt that carries an explicit text
	// seed through a loop that triggers on a non-empty prompt.
	TextSeedWirePrefix = "\x00agent-cli-session-text-seed:"
	// ImageOnlyPrompt is the loop trigger for an image turn without text.
	ImageOnlyPrompt = "\x00agent-session-image-turn\x00"
	// ImageDeferredInstruction gives a deferred image item context before the
	// separately committed spoken question arrives.
	ImageDeferredInstruction = "Use the attached image to answer the user's next spoken question."
	// ImageInputCapability names the capability in image capability errors.
	ImageInputCapability = "image input"
	// ImageInputProvider is the realtime provider that accepts image turns.
	ImageInputProvider = "openai"
	// ImageInputModality is the configured model modality for image input.
	ImageInputModality = "image"
)

// Seed carries an explicit text seed and its presence separately, so an
// explicitly empty seed stays distinct from an omitted one.
type Seed struct {
	Value   string
	Present bool
}

// Output is a writer that retains its first write failure.
type Output interface {
	io.Writer
	Err() error
}

// CompleteMessageSender sends a complete message and requests a response.
type CompleteMessageSender interface {
	SendMessage(context.Context, messages.Message) bool
}

// CompleteMessageWithoutResponseSender queues a complete message without
// starting a model response.
type CompleteMessageWithoutResponseSender interface {
	SendMessageWithoutResponse(context.Context, messages.Message) bool
}

// CompleteMessageCapabilities lets a wrapper report whether its provider
// supports the optional complete-message paths.
type CompleteMessageCapabilities interface {
	SupportsCompleteMessages() bool
	SupportsCompleteMessagesWithoutResponse() bool
}

// InstructionsRequest is the host input for instruction resolution.
type InstructionsRequest struct {
	// Prompt is the explicit system prompt value.
	Prompt string
	// WorkDir is the host-selected workspace; empty with no Scope falls back
	// to ConfigDir.
	WorkDir   string
	ConfigDir string
	// Scope is the launch-captured filesystem policy, when present.
	Scope *FilesystemScope
	// ResolveWorkspace validates a host-selected workspace without a launch
	// policy and returns its primary root.
	ResolveWorkspace func(string) (string, error)
	// Loader supplies instruction I/O for one resolved workspace.
	Loader func(workspaceDir string) session.InstructionLoader
}

// FilesystemScope is the launch-captured filesystem policy disclosed to the
// model. Its presence also marks WorkDir as already validated.
type FilesystemScope struct {
	Description string
}

// ImageCapabilities is the resolved image input capability of one model.
type ImageCapabilities struct {
	Model                   string
	SupportsImageInput      bool
	SupportedInputMIMETypes []string
}

// ImageModelMetadata is host-configured model capability metadata.
type ImageModelMetadata struct {
	InputModalities         []string
	SupportedInputMIMETypes []string
}

// ImageCapabilityRequest carries the host facts needed to decide whether a
// session model accepts image input.
type ImageCapabilityRequest struct {
	Provider      string
	Model         string
	ModelProvided bool
	// ReplayModel is used for an unnamed model in a replay.
	ReplayModel string
	// DefaultModel resolves the provider's configured default model.
	DefaultModel func() (string, error)
	// RealtimeImageInput reports whether a known realtime model accepts
	// images; known is false for an unknown model.
	RealtimeImageInput func(model string) (supported, known bool)
	// ConfiguredModel returns configured metadata, or nil when unconfigured.
	ConfiguredModel func(model string) (*ImageModelMetadata, error)
}

// ImageContentLoader reads and classifies one local file.
type ImageContentLoader func(path string) (messages.ContentPart, error)

// ImagePartsRequest validates and loads session images.
type ImagePartsRequest struct {
	Paths        []string
	Capabilities ImageCapabilities
	Load         ImageContentLoader
}

// ImageAttachRequest binds validated image parts to the first user turn.
type ImageAttachRequest struct {
	Inferencer messages.SessionInferencer
	Parts      []messages.ImagePart
	Seed       Seed
	// Prompt is the loop prompt already selected by the host.
	Prompt string
	// DeferResponse queues the image item for a later audio commit.
	DeferResponse bool
}

// ImageAttachment is the image-bound session runtime.
type ImageAttachment struct {
	Inferencer messages.SessionInferencer
	// FirstTurn reports once whether the image turn reached the provider.
	FirstTurn <-chan error
	// Prompt replaces the loop prompt when non-empty.
	Prompt string
	// WirePrompt is the seed sentinel when a text seed is present.
	WirePrompt string
}

// ImageToolBinding binds read_image to one session capability snapshot.
type ImageToolBinding struct {
	Executor    messages.ToolExecutor
	Definitions []messages.ToolDefinition
	// Resolve returns the session capability snapshot. It runs once, at
	// binding time, and only for an executor that advertises read_image. A
	// failure is reported by read_image, so the session continues with a
	// correlated tool failure.
	Resolve func() (ImageCapabilities, error)
	Load    ImageContentLoader
}

// ImageStagingRequest stages the initial images for later read_image calls.
type ImageStagingRequest struct {
	SourcePaths []string
	Parts       []messages.ImagePart
	// StagingRoot resolves the host root only when read_image is advertised.
	StagingRoot            func() (string, error)
	ToolDefinitions        []messages.ToolDefinition
	RefreshToolDefinitions func(context.Context) ([]messages.ToolDefinition, error)
}

// SeededRunRequest runs one planned session with an optional explicit text
// seed. SetPrompt, SetInferencer, and Run bind the host plan.
type SeededRunRequest struct {
	Seed Seed
	// WirePrompt reuses a sentinel already bound to the plan; empty allocates
	// a new one.
	WirePrompt string
	// Bounded places the seed wrapper inside the host's duration admission
	// boundary, so the sentinel never leaks past the provider edge.
	Bounded bool
	Output  io.Writer
	// Inferencer is the plan's provider seam; nil leaves an unbounded plan
	// unwrapped.
	Inferencer    messages.SessionInferencer
	SetPrompt     func(string)
	SetInferencer func(messages.SessionInferencer)
	// Run runs the plan. A non-nil wrap decorates the provider inferencer
	// after the host's evidence setup.
	Run func(out io.Writer, wrap func(messages.SessionInferencer) messages.SessionInferencer) error
}

// InputService owns initial turn input: text seeds, instructions, and images.
type InputService interface {
	// RunSeeded carries an explicit seed through a loop that triggers on a
	// non-empty prompt, and joins the first output write failure to the run
	// error. Without a seed the plan runs unchanged.
	RunSeeded(SeededRunRequest) error
	NextWirePrompt() string
	NewTextSeedInferencer(inner messages.SessionInferencer, wirePrompt, value string) messages.SessionInferencer
	NewOutput(io.Writer) Output

	ResolveInstructions(context.Context, InstructionsRequest) (string, error)
	ComposeInstructions(session.InstructionComposition) string
	NewInstructionsInferencer(inner messages.SessionInferencer, instructions string, definitions []messages.ToolDefinition) messages.SessionInferencer

	ResolveImageCapabilities(ImageCapabilityRequest) (ImageCapabilities, error)
	PrepareImageParts(ImagePartsRequest) ([]messages.ImagePart, error)
	AttachImages(ImageAttachRequest) (ImageAttachment, error)
	SendImageTurn(ctx context.Context, session messages.Session, text string, parts []messages.ImagePart) error
	BindImageTools(ImageToolBinding) messages.ToolExecutor
	StageImageTools(context.Context, ImageStagingRequest) (tools.ImageStagingResult, error)

	CompleteMessageSupport(messages.Session) (complete, withoutResponse bool)
}

// ImageCapabilityError reports a model without a required capability.
type ImageCapabilityError struct{ Model, Capability string }

func (e *ImageCapabilityError) Error() string {
	return fmt.Sprintf("model %q does not support %s capability", e.Model, e.Capability)
}

func (*ImageCapabilityError) Unwrap() error { return ErrImageCapability }

// ImageFileError reports one rejected image path. Kind is the sentinel.
type ImageFileError struct {
	Path, DetectedMIME string
	SupportedMIME      []string
	Kind               Error
	Cause              error
	Message            string
}

func (e *ImageFileError) Error() string { return e.Message }

func (e *ImageFileError) Unwrap() []error { return []error{e.Kind, e.Cause} }

// ImageEmptyFileError reports a zero-byte image path.
type ImageEmptyFileError struct{ Path string }

func (e *ImageEmptyFileError) Error() string {
	return fmt.Sprintf("session image %q is empty", e.Path)
}

func (*ImageEmptyFileError) Unwrap() error { return ErrImageEmptyFile }
