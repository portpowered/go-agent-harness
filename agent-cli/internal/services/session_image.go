package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/input"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const sessionImageCapability = "image input"

var defaultSessionImageMIMETypes = []string{"image/png", "image/jpeg"}

var (
	ErrSessionImageMissingFile     = errors.New("session image file is missing")
	ErrSessionImageUnreadableFile  = errors.New("session image file is unreadable")
	ErrSessionImageUnsupportedMIME = errors.New("session image MIME type is unsupported")
	ErrSessionImageInvalidContent  = errors.New("session image content is invalid")
	ErrSessionImageEmptyFile       = errors.New("session image file is empty")
	ErrSessionImageCapability      = errors.New("session image capability is unsupported")
	ErrSessionImageSend            = errors.New("session image turn could not be sent")
)

// SessionImageRunOptions carries image paths alongside the existing session options.
type SessionImageRunOptions struct {
	SessionRunOptions
	ImagePaths []string
}

// SessionImageCapabilities is the model metadata used to validate attachments.
type SessionImageCapabilities struct {
	Model                   string
	SupportsImageInput      bool
	SupportedInputMIMETypes []string
}

// SessionImageCapabilityError reports a model without the requested capability.
type SessionImageCapabilityError struct {
	Model      string
	Capability string
}

func (e *SessionImageCapabilityError) Error() string {
	return fmt.Sprintf("model %q does not support %s capability", e.Model, e.Capability)
}

func (*SessionImageCapabilityError) Unwrap() error { return ErrSessionImageCapability }

type sessionImagePathError struct {
	Path  string
	Err   error
	Kind  error
	State string
}

func (e *sessionImagePathError) Error() string {
	return fmt.Sprintf("session image %q %s: %v", e.Path, e.State, e.Err)
}

func (e *sessionImagePathError) Unwrap() []error { return []error{e.Kind, e.Err} }

// SessionImageMissingFileError reports a path that does not exist.
type SessionImageMissingFileError struct{ *sessionImagePathError }

// SessionImageUnreadableFileError reports a path that exists but cannot be read.
type SessionImageUnreadableFileError struct{ *sessionImagePathError }

type sessionImageMIMEError struct {
	Path          string
	DetectedMIME  string
	SupportedMIME []string
	Err           error
	Kind          error
	State         string
}

func (e *sessionImageMIMEError) Error() string {
	if e.State == "invalid" {
		return fmt.Sprintf("session image %q is not valid %s content: %v", e.Path, e.DetectedMIME, e.Err)
	}
	return fmt.Sprintf("session image %q has unsupported MIME type %q (supported: %s)", e.Path, e.DetectedMIME, strings.Join(e.SupportedMIME, ", "))
}

func (e *sessionImageMIMEError) Unwrap() []error { return []error{e.Kind, e.Err} }

// SessionImageUnsupportedMIMEError reports a MIME type outside the model contract.
type SessionImageUnsupportedMIMEError struct{ *sessionImageMIMEError }

// SessionImageInvalidContentError reports bytes that are not a decodable image.
type SessionImageInvalidContentError struct{ *sessionImageMIMEError }

// SessionImageEmptyFileError reports a successfully read zero-byte file.
type SessionImageEmptyFileError struct{ Path string }

func (e *SessionImageEmptyFileError) Error() string { return fmt.Sprintf("session image %q is empty", e.Path) }
func (*SessionImageEmptyFileError) Unwrap() error   { return ErrSessionImageEmptyFile }

// SessionImageMessageSender is an optional seam for a complete multimodal user message.
type SessionImageMessageSender interface {
	SendMessage(context.Context, messages.Message) bool
}

// RunSessionWithImages validates attachments before constructing or connecting a session.
// With no images it delegates to the existing session path unchanged.
func RunSessionWithImages(ctx context.Context, out io.Writer, opts SessionImageRunOptions) error {
	paths := append([]string(nil), opts.ImagePaths...)
	if len(paths) == 0 {
		return RunSession(ctx, out, opts.SessionRunOptions)
	}
	if err := validateSessionRunOptions(opts.SessionRunOptions); err != nil {
		return err
	}
	metadata, err := resolveSessionImageCapabilities(opts.SessionRunOptions)
	if err != nil {
		return err
	}
	parts, err := PrepareSessionImageParts(paths, metadata)
	if err != nil {
		return err
	}
	plan, err := planSessionRuntime(opts.SessionRunOptions)
	if err != nil {
		return err
	}
	if plan.inferencer == nil {
		return errors.New("session image runtime has no session inferencer")
	}
	plan.inferencer = &sessionImageInferencer{inner: plan.inferencer, parts: cloneSessionImageParts(parts)}
	if opts.Prompt == "" {
		plan.loop.Prompt = sessionImageOnlyPrompt
	}
	return plan.run(ctx, out)
}

// PrepareSessionImageParts reads paths in occurrence order and preserves successful bytes.
func PrepareSessionImageParts(paths []string, metadata SessionImageCapabilities) ([]messages.ImagePart, error) {
	if !metadata.SupportsImageInput {
		return nil, &SessionImageCapabilityError{Model: metadata.Model, Capability: sessionImageCapability}
	}
	supported := append([]string(nil), metadata.SupportedInputMIMETypes...)
	if len(supported) == 0 {
		supported = append([]string(nil), defaultSessionImageMIMETypes...)
	}
	parts := make([]messages.ImagePart, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			return nil, missingSessionImage(path, os.ErrNotExist)
		}
		content, err := input.LoadContentPart(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, missingSessionImage(path, err)
			}
			return nil, unreadableSessionImage(path, err)
		}
		data, mediaType, isImage := sessionImageContent(content)
		if len(data) == 0 {
			return nil, &SessionImageEmptyFileError{Path: path}
		}
		if !isImage {
			return nil, unsupportedSessionImage(path, mediaType, supported)
		}
		if err := input.ValidateMimeType(mediaType, metadata.Model, supported); err != nil {
			return nil, unsupportedSessionImage(path, mediaType, supported)
		}
		if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
			return nil, &SessionImageInvalidContentError{&sessionImageMIMEError{
				Path: path, DetectedMIME: mediaType, Err: err, Kind: ErrSessionImageInvalidContent, State: "invalid",
			}}
		}
		parts = append(parts, messages.ImagePart{Bytes: append([]byte(nil), data...), MediaType: mediaType})
	}
	return parts, nil
}

func missingSessionImage(path string, err error) error {
	return &SessionImageMissingFileError{&sessionImagePathError{Path: path, Err: err, Kind: ErrSessionImageMissingFile, State: "is missing"}}
}

func unreadableSessionImage(path string, err error) error {
	return &SessionImageUnreadableFileError{&sessionImagePathError{Path: path, Err: err, Kind: ErrSessionImageUnreadableFile, State: "cannot be read"}}
}

func unsupportedSessionImage(path, mediaType string, supported []string) error {
	return &SessionImageUnsupportedMIMEError{&sessionImageMIMEError{
		Path: path, DetectedMIME: mediaType, SupportedMIME: append([]string(nil), supported...), Kind: ErrSessionImageUnsupportedMIME,
	}}
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
	realtimeModel, ok := LookupOpenAIRealtimeModel(model)
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
	return &SessionImageCapabilityError{Model: strings.TrimSpace(model), Capability: sessionImageCapability}
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
	if model == nil {
		return false
	}
	if len(model.InputModalities) > 0 {
		return slices.Contains(model.InputModalities, "image")
	}
	for _, mimeType := range model.SupportedInputMimeTypes {
		if strings.HasPrefix(mimeType, "image/") {
			return true
		}
	}
	return false
}

const sessionImageOnlyPrompt = "\x00agent-session-image-turn\x00"

type sessionImageInferencer struct {
	inner messages.SessionInferencer
	parts []messages.ImagePart
}

func (i *sessionImageInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	return &sessionImageSession{Session: session, parts: cloneSessionImageParts(i.parts)}, nil
}

type sessionImageSession struct {
	messages.Session
	parts []messages.ImagePart
	mu    sync.Mutex
}

func (s *sessionImageSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if msg.Type != messages.StreamTypeTextDelta && msg.Type != messages.StreamTypeAudioDelta {
		return s.Session.Send(ctx, msg)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.parts) == 0 {
		return s.Session.Send(ctx, msg)
	}
	parts := cloneSessionImageParts(s.parts)
	s.parts = nil
	if value, ok := msg.Value.(*messages.TextDeltaValue); ok && value != nil {
		text := value.Content
		if text == sessionImageOnlyPrompt {
			text = ""
		}
		return sendSessionImageTurn(ctx, s.Session, text, parts)
	}
	if !s.Session.Send(ctx, msg) {
		return false
	}
	return sendSessionImageParts(ctx, s.Session, parts)
}

// SendSessionImageTurn attaches validated parts to one reusable user turn.
func SendSessionImageTurn(ctx context.Context, session messages.Session, text string, parts []messages.ImagePart) error {
	if sendSessionImageTurn(ctx, session, text, parts) {
		return nil
	}
	return fmt.Errorf("%w: provider session rejected image turn", ErrSessionImageSend)
}

func sendSessionImageTurn(ctx context.Context, session messages.Session, text string, parts []messages.ImagePart) bool {
	if sender, ok := session.(SessionImageMessageSender); ok {
		content := make([]messages.ContentPart, 0, len(parts)+1)
		if text != "" {
			content = append(content, messages.TextPart{Text: text})
		}
		for _, part := range parts {
			content = append(content, messages.ImagePart{Bytes: append([]byte(nil), part.Bytes...), MediaType: part.MediaType})
		}
		return sender.SendMessage(ctx, messages.Message{Role: messages.RoleUser, ContentParts: content})
	}
	if text != "" && !session.Send(ctx, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(text)}) {
		return false
	}
	return sendSessionImageParts(ctx, session, parts)
}

func sendSessionImageParts(ctx context.Context, session messages.Session, parts []messages.ImagePart) bool {
	for _, part := range parts {
		for _, event := range []messages.StreamMessage{
			{Type: messages.StreamTypeImageStart, Value: messages.NewImageStartValue(part.MediaType)},
			{Type: messages.StreamTypeImageDelta, Value: messages.NewImageDeltaValue(append([]byte(nil), part.Bytes...))},
			{Type: messages.StreamTypeImageEnd, Value: messages.NewImageEndValue()},
		} {
			if !session.Send(ctx, event) {
				return false
			}
		}
	}
	return true
}

func cloneSessionImageParts(parts []messages.ImagePart) []messages.ImagePart {
	if len(parts) == 0 {
		return nil
	}
	cloned := make([]messages.ImagePart, len(parts))
	for i, part := range parts {
		cloned[i] = messages.ImagePart{URL: part.URL, MediaType: part.MediaType, Bytes: append([]byte(nil), part.Bytes...)}
	}
	return cloned
}
