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

// SessionImageMessageSenderWithoutResponse queues a multimodal user item
// without starting a model response. Audio-enabled image sessions use this
// seam so the subsequent audio end-of-turn can commit the complete voice and
// image turn and request exactly one response.
type SessionImageMessageSenderWithoutResponse interface {
	SendMessageWithoutResponse(context.Context, messages.Message) bool
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
	plan, wirePrompt, err := planSessionImageRuntime(opts.SessionRunOptions, parts, opts.TextSeed, opts.SystemPrompt, false)
	if err != nil {
		return err
	}
	return runSessionImagePlan(ctx, out, plan, opts, wirePrompt)
}

// RunSessionWithImagesAndAudioInput composes the ordinary image session path
// with the production file/stdin audio source. The image item is queued
// without a response request; the finite audio source owns the single
// end-of-turn commit and response boundary.
func RunSessionWithImagesAndAudioInput(ctx context.Context, out io.Writer, opts SessionImageRunOptions, input SessionAudioInput) (runErr error) {
	if !sessionAudioInputSelected(input) {
		return RunSessionWithImages(ctx, out, opts)
	}
	paths := append([]string(nil), opts.ImagePaths...)
	if len(paths) == 0 {
		return RunSessionWithInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(ctx, out, opts.SessionRunOptions, opts.AudioOutPath, opts.MaxDuration, opts.TextSeed, input, opts.SystemPrompt)
	}
	if err := ValidateSessionMaxDuration(opts.MaxDuration); err != nil {
		return err
	}
	if err := validateSessionRunOptions(opts.SessionRunOptions); err != nil {
		return err
	}
	if err := validateSessionAudioInput(input); err != nil {
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
	audioSource, err := openSessionAudioInput(input)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := audioSource.Close(); closeErr != nil {
			runErr = errors.Join(runErr, closeErr)
		}
	}()

	plan, wirePrompt, err := planSessionImageRuntime(opts.SessionRunOptions, parts, opts.TextSeed, opts.SystemPrompt, true)
	if err != nil {
		return err
	}
	plan.loop.CloseAfterOpen = false
	plan.loop.AudioIn = audioSource
	plan.loop.MaxDuration = opts.MaxDuration
	return runSessionImagePlan(ctx, out, plan, opts, wirePrompt)
}

func planSessionImageRuntime(opts SessionRunOptions, parts []messages.ImagePart, seed SessionTextSeed, systemPrompt string, deferResponse bool) (sessionRuntimePlan, string, error) {
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
	firstTurn := make(chan error, 1)
	plan.inferencer = &sessionImageInferencer{
		inner:         plan.inferencer,
		parts:         cloneSessionImageParts(parts),
		firstTurn:     firstTurn,
		deferResponse: deferResponse,
	}
	plan.loop.awaitFirstTurn = firstTurn
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
		if opts.MaxDuration == 0 || plan.loop.AudioIn != nil {
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
		if opts.MaxDuration == 0 || plan.loop.AudioIn != nil {
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
	// firstTurn reports the outcome of the one reusable image user turn:
	// nil once its wire events reached the provider session's outbound
	// queue, or the rejection error. Buffered so signaling never blocks the
	// model runner goroutine; nil keeps direct construction sites unchanged.
	firstTurn chan error
	// deferResponse queues only the image item when audio input will complete
	// the turn. False preserves the immediate response for image-only and
	// text+image sessions.
	deferResponse bool
}

func (i *sessionImageInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	return &sessionImageSession{
		Session:       session,
		parts:         cloneSessionImageParts(i.parts),
		firstTurn:     i.firstTurn,
		deferResponse: i.deferResponse,
	}, nil
}

type sessionImageSession struct {
	messages.Session
	parts []messages.ImagePart
	mu    sync.Mutex
	// firstTurn is the shared acceptance signal consumed by the session
	// loop's awaitFirstTurn; see sessionImageInferencer.
	firstTurn     chan error
	deferResponse bool
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
		sent := sendSessionImageTurn(ctx, s.Session, text, parts, !s.deferResponse)
		s.signalFirstTurn(sent)
		return sent
	}
	s.signalFirstTurn(false)
	return false
}

// signalFirstTurn reports the image-turn outcome to awaitSessionFirstTurn
// exactly once. The buffered channel makes this non-blocking, and callers
// without a waiter (plain RunSessionWithImages without streamed audio) only
// leave the buffered result unconsumed.
func (s *sessionImageSession) signalFirstTurn(sent bool) {
	if s.firstTurn == nil {
		return
	}
	if sent {
		s.firstTurn <- nil
		return
	}
	s.firstTurn <- fmt.Errorf("%w: provider session rejected image turn", ErrSessionImageSend)
}

// SendSessionImageTurn attaches validated parts to one reusable user turn.
func SendSessionImageTurn(ctx context.Context, session messages.Session, text string, parts []messages.ImagePart) error {
	if sendSessionImageTurn(ctx, session, text, parts, true) {
		return nil
	}
	return fmt.Errorf("%w: provider session rejected image turn", ErrSessionImageSend)
}
func sendSessionImageTurn(ctx context.Context, session messages.Session, text string, parts []messages.ImagePart, requestResponse bool) bool {
	content := make([]messages.ContentPart, 0, len(parts)+1)
	if text != "" {
		content = append(content, messages.TextPart{Text: text})
	}
	for _, part := range parts {
		content = append(content, messages.ImagePart{Bytes: append([]byte(nil), part.Bytes...), MediaType: part.MediaType})
	}
	message := messages.Message{Role: messages.RoleUser, ContentParts: content}
	if requestResponse {
		sender, ok := session.(SessionImageMessageSender)
		return ok && sender.SendMessage(ctx, message)
	}
	sender, ok := session.(SessionImageMessageSenderWithoutResponse)
	return ok && sender.SendMessageWithoutResponse(ctx, message)
}
func cloneSessionImageParts(parts []messages.ImagePart) []messages.ImagePart {
	cloned := slices.Clone(parts)
	for i := range cloned {
		cloned[i].Bytes = append([]byte(nil), cloned[i].Bytes...)
	}
	return cloned
}
