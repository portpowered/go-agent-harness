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
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/input"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

var (
	ErrSessionImageMissingFile, ErrSessionImageUnreadableFile     = errors.New("session image file is missing"), errors.New("session image file is unreadable")
	ErrSessionImageUnsupportedMIME, ErrSessionImageInvalidContent = errors.New("session image MIME type is unsupported"), errors.New("session image content is invalid")
	ErrSessionImageEmptyFile, ErrSessionImageCapability           = errors.New("session image file is empty"), errors.New("session image capability is unsupported")
	ErrSessionImageSend                                           = errors.New("session image turn could not be sent")
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

type SessionImageMessageSender interface {
	SendMessage(context.Context, messages.Message) bool
}

func RunSessionWithImages(ctx context.Context, out io.Writer, opts SessionImageRunOptions) (runErr error) {
	paths := append([]string(nil), opts.ImagePaths...)
	if len(paths) == 0 {
		return RunSession(ctx, out, opts.SessionRunOptions)
	}
	if err := ValidateSessionMaxDuration(opts.MaxDuration); err != nil {
		return err
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
	if opts.TextSeed.Present {
		opts.SessionRunOptions.Prompt = opts.TextSeed.Value
	}
	plan, wirePrompt, err := planSessionImageRuntime(opts.SessionRunOptions, parts, opts.TextSeed, opts.SystemPrompt)
	if err != nil {
		return err
	}
	return runSessionImagePlan(ctx, out, plan, opts, wirePrompt)
}
func planSessionImageRuntime(opts SessionRunOptions, parts []messages.ImagePart, seed SessionTextSeed, systemPrompt string) (sessionRuntimePlan, string, error) {
	var (
		plan         sessionRuntimePlan
		err          error
		instructions string
	)
	if opts.SessionInferencer != nil || opts.ReplayPath != "" {
		plan, err = planSessionRuntime(opts)
	} else {
		instructions, err = resolveSessionInstructions(opts, systemPrompt)
		if err == nil {
			plan, err = planSessionWithResolvedInstructions(opts, instructions)
		}
	}
	if err != nil {
		return sessionRuntimePlan{}, "", err
	}
	if plan.inferencer == nil {
		return sessionRuntimePlan{}, "", errors.New("session image runtime has no session inferencer")
	}
	plan.inferencer = &sessionImageInferencer{inner: plan.inferencer, parts: cloneSessionImageParts(parts)}
	if seed.Present {
		wirePrompt := nextSessionTextWirePrompt()
		plan.loop.Prompt = wirePrompt
		return plan, wirePrompt, nil
	}
	if opts.Prompt == "" {
		plan.loop.Prompt = sessionImageOnlyPrompt
	}
	return plan, "", nil
}
func runSessionImagePlan(ctx context.Context, out io.Writer, plan sessionRuntimePlan, opts SessionImageRunOptions, wirePrompt string) (runErr error) {
	if opts.AudioOutPath != "" {
		sink, err := newSessionAudioSink(opts.AudioOutPath, out)
		if err != nil {
			return fmt.Errorf("--audio-out %q: %w", opts.AudioOutPath, err)
		}
		audioOut := &sessionAudioOutput{sink: sink}
		defer func() {
			if closeErr := audioOut.close(); closeErr != nil {
				runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", opts.AudioOutPath, closeErr))
			}
		}()
		wrapped := newSessionAudioOutputInferencer(plan.inferencer, audioOut, wirePrompt, opts.TextSeed.Value)
		plan.inferencer = wrapped
		if opts.AudioOutPath == "-" {
			out = io.Discard
		}
		if opts.MaxDuration == 0 {
			runErr = plan.run(ctx, out)
		} else {
			runErr = runSessionImageDuration(ctx, out, plan, opts.MaxDuration)
		}
		wrapped.wait()
		if outputErr := wrapped.err(); outputErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", opts.AudioOutPath, outputErr))
		}
		return runErr
	}
	if opts.TextSeed.Present {
		output := &sessionTextOutput{writer: out}
		if opts.MaxDuration == 0 {
			plan.inferencer = &sessionTextSeedInferencer{inner: plan.inferencer, wirePrompt: wirePrompt, value: opts.TextSeed.Value, audioOut: output}
			return errors.Join(plan.run(ctx, output), output.errorValue())
		}
		durationCtx, err := prepareSessionDurationArtifacts(ctx)
		if err != nil {
			return err
		}
		admission := newSessionDurationAdmission()
		admittedInferencer := &sessionDurationAdmissionInferencer{inner: plan.inferencer, admission: admission, closeDone: make(chan struct{})}
		plan.inferencer = &sessionTextSeedInferencer{inner: admittedInferencer, wirePrompt: wirePrompt, value: opts.TextSeed.Value, audioOut: output}
		err = runSessionDurationPlanWithAdmission(durationCtx, output, plan, opts.MaxDuration, realSessionDurationClock{}, admittedInferencer)
		return errors.Join(err, output.errorValue())
	}
	if opts.MaxDuration == 0 {
		return plan.run(ctx, out)
	}
	return runSessionImageDuration(ctx, out, plan, opts.MaxDuration)
}
func runSessionImageDuration(ctx context.Context, out io.Writer, plan sessionRuntimePlan, maxDuration time.Duration) error {
	durationCtx, err := prepareSessionDurationArtifacts(ctx)
	if err != nil {
		return err
	}
	return runSessionDurationPlan(durationCtx, out, plan, maxDuration, realSessionDurationClock{})
}
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
			return nil, &SessionImageInvalidContentError{newSessionImageError(
				ErrSessionImageInvalidContent, path, mediaType,
				fmt.Sprintf("session image %q is not valid %s content: %v", path, mediaType, err), nil, err,
			)}
		}
		parts = append(parts, messages.ImagePart{Bytes: append([]byte(nil), data...), MediaType: mediaType})
	}
	return parts, nil
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
	if msg.Type != messages.StreamTypeTextDelta {
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
	return false
}

// SendSessionImageTurn attaches validated parts to one reusable user turn.
func SendSessionImageTurn(ctx context.Context, session messages.Session, text string, parts []messages.ImagePart) error {
	if sendSessionImageTurn(ctx, session, text, parts) {
		return nil
	}
	return fmt.Errorf("%w: provider session rejected image turn", ErrSessionImageSend)
}
func sendSessionImageTurn(ctx context.Context, session messages.Session, text string, parts []messages.ImagePart) bool {
	sender, ok := session.(SessionImageMessageSender)
	if !ok {
		return false
	}
	content := make([]messages.ContentPart, 0, len(parts)+1)
	if text != "" {
		content = append(content, messages.TextPart{Text: text})
	}
	for _, part := range parts {
		content = append(content, messages.ImagePart{Bytes: append([]byte(nil), part.Bytes...), MediaType: part.MediaType})
	}
	return sender.SendMessage(ctx, messages.Message{Role: messages.RoleUser, ContentParts: content})
}
func cloneSessionImageParts(parts []messages.ImagePart) []messages.ImagePart {
	cloned := slices.Clone(parts)
	for i := range cloned {
		cloned[i].Bytes = append([]byte(nil), cloned[i].Bytes...)
	}
	return cloned
}
