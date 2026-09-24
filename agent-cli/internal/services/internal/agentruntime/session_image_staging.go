package agentruntime

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/input"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

// prepareSessionImageToolAccess gives read_image a stable, session-owned copy
// of each initial image and advertises those exact paths to the provider. The
// inline image turn still uses the validated parts supplied by the caller;
// staging is only needed for a later model-issued read_image call.
func prepareSessionImageToolAccess(opts SessionRunOptions, sourcePaths []string, parts []messages.ImagePart) (SessionRunOptions, func(), error) {
	if !sessionHasTool(opts.ToolDefinitions, runtimeTools.ReadImageToolID) {
		return opts, noOpSessionImageCleanup, nil
	}
	if len(sourcePaths) != len(parts) {
		return opts, noOpSessionImageCleanup, fmt.Errorf("stage session images: source path count %d does not match image part count %d", len(sourcePaths), len(parts))
	}

	configDir, err := sessionImageStagingConfigDir(opts.ConfigDir)
	if err != nil {
		return opts, noOpSessionImageCleanup, fmt.Errorf("stage session images: %w", err)
	}
	staged, err := runtimeToolsWire.NewImageStaging().Stage(context.Background(), runtimeTools.ImageStagingRequest{
		StagingRoot:            configDir,
		SourcePaths:            sourcePaths,
		ImageParts:             parts,
		ToolDefinitions:        opts.ToolDefinitions,
		RefreshToolDefinitions: opts.RefreshToolDefinitions,
	})
	if err != nil {
		return opts, noOpSessionImageCleanup, err
	}
	opts.ToolDefinitions = staged.ToolDefinitions
	opts.RefreshToolDefinitions = staged.RefreshToolDefinitions
	cleanup := func() {
		if staged.Cleanup != nil {
			if err := staged.Cleanup(); err != nil {
				log.Printf("stage session images: cleanup: %v", err)
			}
		}
	}
	return opts, cleanup, nil
}

func noOpSessionImageCleanup() {}

func sessionImageStagingConfigDir(configDir string) (string, error) {
	configDir = strings.TrimSpace(configDir)
	if configDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		configDir = filepath.Join(homeDir, config.ConfigDirName)
	}
	abs, err := filepath.Abs(configDir)
	if err != nil {
		return "", fmt.Errorf("resolve config directory %q: %w", configDir, err)
	}
	return filepath.Clean(abs), nil
}

type sessionImageErrorKind string

func (e sessionImageErrorKind) Error() string { return string(e) }

const (
	ErrSessionImageMissingFile     sessionImageErrorKind = "session image file is missing"
	ErrSessionImageUnreadableFile  sessionImageErrorKind = "session image file is unreadable"
	ErrSessionImageUnsupportedMIME sessionImageErrorKind = "session image MIME type is unsupported"
	ErrSessionImageInvalidContent  sessionImageErrorKind = "session image content is invalid"
	ErrSessionImageEmptyFile       sessionImageErrorKind = "session image file is empty"
	ErrSessionImageCapability      sessionImageErrorKind = "session image capability is unsupported"
	ErrSessionImageSend            sessionImageErrorKind = "session image turn could not be sent"
)

type SessionImageRunOptions struct {
	SessionRunOptions
	ImagePaths   []string
	AudioOutPath string
	MaxDuration  time.Duration
	TextSeed     SessionTextSeed
	SystemPrompt string
}
type SessionImageCapabilities struct {
	Model                   string
	SupportsImageInput      bool
	SupportedInputMIMETypes []string
}
type SessionImageCapabilityError struct{ Model, Capability string }

func (e *SessionImageCapabilityError) Error() string {
	return fmt.Sprintf("model %q does not support %s capability", e.Model, e.Capability)
}
func (*SessionImageCapabilityError) Unwrap() error { return ErrSessionImageCapability }

type sessionImageError struct {
	Path, DetectedMIME string
	SupportedMIME      []string
	Err, kind          error
	text               string
}

func (e *sessionImageError) Error() string   { return e.text }
func (e *sessionImageError) Unwrap() []error { return []error{e.kind, e.Err} }
func newSessionImageError(kind error, path, detected, text string, supported []string, err error) *sessionImageError {
	return &sessionImageError{Path: path, DetectedMIME: detected, SupportedMIME: supported, Err: err, kind: kind, text: text}
}

type (
	SessionImageMissingFileError     struct{ *sessionImageError }
	SessionImageUnreadableFileError  struct{ *sessionImageError }
	SessionImageUnsupportedMIMEError struct{ *sessionImageError }
	SessionImageInvalidContentError  struct{ *sessionImageError }
	SessionImageEmptyFileError       struct{ Path string }
)

func (e *SessionImageEmptyFileError) Error() string {
	return fmt.Sprintf("session image %q is empty", e.Path)
}
func (*SessionImageEmptyFileError) Unwrap() error { return ErrSessionImageEmptyFile }

func PrepareSessionImageParts(paths []string, metadata SessionImageCapabilities) ([]messages.ImagePart, error) {
	if !metadata.SupportsImageInput {
		return nil, &SessionImageCapabilityError{Model: metadata.Model, Capability: "image input"}
	}
	supported := append([]string(nil), metadata.SupportedInputMIMETypes...)
	if len(supported) == 0 {
		supported = []string{"image/png", "image/jpeg"}
	}
	parts := make([]messages.ImagePart, 0, len(paths))
	for _, path := range paths {
		part, err := prepareSessionImagePart(path, metadata.Model, supported)
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	return parts, nil
}

func prepareSessionImagePart(path, model string, supported []string) (messages.ImagePart, error) {
	if path == "" {
		return messages.ImagePart{}, missingSessionImage(path, os.ErrNotExist)
	}
	content, err := input.LoadContentPart(path)
	if err != nil {
		return messages.ImagePart{}, sessionImageLoadError(path, err)
	}
	data, mediaType, isImage := sessionImageContent(content)
	if len(data) == 0 {
		return messages.ImagePart{}, &SessionImageEmptyFileError{Path: path}
	}
	if !isImage || input.ValidateMimeType(mediaType, model, supported) != nil {
		return messages.ImagePart{}, unsupportedSessionImage(path, mediaType, supported)
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return messages.ImagePart{}, invalidSessionImageContent(path, mediaType, err)
	}
	return messages.ImagePart{Bytes: append([]byte(nil), data...), MediaType: mediaType}, nil
}

func sessionImageLoadError(path string, err error) error {
	if os.IsNotExist(err) {
		return missingSessionImage(path, err)
	}
	return unreadableSessionImage(path, err)
}

func invalidSessionImageContent(path, mediaType string, err error) error {
	return &SessionImageInvalidContentError{newSessionImageError(
		ErrSessionImageInvalidContent, path, mediaType,
		fmt.Sprintf("session image %q is not valid %s content: %v", path, mediaType, err), nil, err,
	)}
}
func sessionImageContent(content messages.ContentPart) (data []byte, mediaType string, isImage bool) {
	switch part := content.(type) {
	case messages.ImagePart:
		return part.Bytes, part.MediaType, true
	case messages.AudioPart:
		return part.Bytes, part.MediaType, false
	case messages.VideoPart:
		return part.Bytes, part.MediaType, false
	case messages.FilePart:
		return part.Bytes, part.MediaType, false
	default:
		return nil, "", false
	}
}
func missingSessionImage(path string, err error) error {
	return &SessionImageMissingFileError{newSessionImageError(ErrSessionImageMissingFile, path, "", fmt.Sprintf("session image %q is missing: %v", path, err), nil, err)}
}
func unreadableSessionImage(path string, err error) error {
	return &SessionImageUnreadableFileError{newSessionImageError(ErrSessionImageUnreadableFile, path, "", fmt.Sprintf("session image %q cannot be read: %v", path, err), nil, err)}
}
func unsupportedSessionImage(path, mediaType string, supported []string) error {
	return &SessionImageUnsupportedMIMEError{newSessionImageError(ErrSessionImageUnsupportedMIME, path, mediaType, fmt.Sprintf("session image %q has unsupported MIME type %q (supported: %s)", path, mediaType, strings.Join(supported, ", ")), append([]string(nil), supported...), nil)}
}
func resolveSessionImageCapabilities(opts SessionRunOptions) (SessionImageCapabilities, error) {
	if !strings.EqualFold(strings.TrimSpace(effectiveSessionProvider(opts)), sessionProviderOpenAI) {
		return SessionImageCapabilities{}, sessionImageCapabilityError(opts.Model)
	}
	model := strings.TrimSpace(opts.Model)
	if model == "" && opts.ModelProvided {
		return SessionImageCapabilities{}, sessionImageCapabilityError(model)
	}
	if model == "" && opts.ReplayPath != "" {
		model = openAIRealtimeModel
	}
	if model == "" {
		resolved, err := resolveOpenAIRealtimeSessionConfig(opts)
		if err != nil {
			return SessionImageCapabilities{}, err
		}
		model = resolved.Model
	}
	realtimeModel, ok := lookupOpenAIRealtimeModel(opts, model)
	if !ok || !realtimeModel.SupportsImageInput {
		return SessionImageCapabilities{}, sessionImageCapabilityError(model)
	}
	info, err := loadSessionImageModelInfo(opts.ConfigDir, model)
	if err != nil {
		return SessionImageCapabilities{}, err
	}
	supported := []string(nil)
	if info != nil {
		if !configuredModelSupportsImageInput(info) {
			return SessionImageCapabilities{}, sessionImageCapabilityError(model)
		}
		supported = append(supported, info.SupportedInputMimeTypes...)
	}
	return SessionImageCapabilities{Model: model, SupportsImageInput: true, SupportedInputMIMETypes: supported}, nil
}
func sessionImageCapabilityError(model string) error {
	return &SessionImageCapabilityError{Model: strings.TrimSpace(model), Capability: "image input"}
}
func loadSessionImageModelInfo(configDir, model string) (*config.ModelInfo, error) {
	storage, err := config.NewModelsConfigStorage(configDir)
	if err != nil {
		return nil, fmt.Errorf("initialize model capability metadata: %w", err)
	}
	models, err := storage.Load()
	if err != nil {
		return nil, fmt.Errorf("load model capability metadata: %w", err)
	}
	return models.Lookup(model), nil
}
func configuredModelSupportsImageInput(model *config.ModelInfo) bool {
	return model != nil && (slices.Contains(model.InputModalities, "image") ||
		(len(model.InputModalities) == 0 && slices.ContainsFunc(model.SupportedInputMimeTypes, func(mime string) bool {
			return strings.HasPrefix(mime, "image/")
		})))
}
