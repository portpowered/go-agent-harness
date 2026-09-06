package agentruntime

import sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"

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
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
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

// sessionCompleteMessageCapabilities is implemented by provider sessions and
// forwarded by session wrappers so the agent loop can distinguish an optional
// capability from a wrapper method that merely returns false when unsupported.
type sessionCompleteMessageCapabilities interface {
	SupportsCompleteMessages() bool
	SupportsCompleteMessagesWithoutResponse() bool
}

func completeMessageCapabilities(session messages.Session) (complete, withoutResponse bool) {
	if capabilities, ok := session.(sessionCompleteMessageCapabilities); ok {
		return capabilities.SupportsCompleteMessages(), capabilities.SupportsCompleteMessagesWithoutResponse()
	}
	_, complete = session.(SessionImageMessageSender)
	_, withoutResponse = session.(SessionImageMessageSenderWithoutResponse)
	return complete, withoutResponse
}

func RunSessionWithImages(ctx context.Context, out io.Writer, opts SessionImageRunOptions) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts.SessionRunOptions, coordinator = prepareSessionCapabilityCoordinator(opts.SessionRunOptions)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	paths := append([]string(nil), opts.ImagePaths...)
	if len(paths) == 0 {
		return RunSession(ctx, out, opts.SessionRunOptions)
	}
	if err := sessioncontract.ValidateSessionMaxDuration(opts.MaxDuration); err != nil {
		return err
	}
	if err := validateSessionRunOptions(opts.SessionRunOptions); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts.SessionRunOptions)
	if err != nil {
		return err
	}
	defer func() { _ = claim.release() }()
	metadata, err := resolveSessionImageCapabilities(opts.SessionRunOptions)
	if err != nil {
		return err
	}
	opts.SessionRunOptions.sessionImageCapabilities = cloneSessionImageCapabilities(&metadata)
	parts, err := PrepareSessionImageParts(paths, metadata)
	if err != nil {
		return err
	}
	if opts.TextSeed.Present {
		opts.SessionRunOptions.Prompt = opts.TextSeed.Value
		opts.SessionRunOptions.PromptProvided = true
	}
	if opts.AudioOutPath != "" {
		opts.SessionRunOptions.AudioOutputRequested = true
	}
	var imageCleanup func()
	opts.SessionRunOptions, imageCleanup, err = prepareSessionImageToolAccess(opts.SessionRunOptions, paths, parts)
	if err != nil {
		return err
	}
	defer imageCleanup()
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
	var coordinator SessionCapabilityCoordinator
	opts.SessionRunOptions, coordinator = prepareSessionCapabilityCoordinator(opts.SessionRunOptions)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if !sessionAudioInputSelected(input) {
		return RunSessionWithImages(ctx, out, opts)
	}
	paths := append([]string(nil), opts.ImagePaths...)
	if len(paths) == 0 {
		return RunSessionWithInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(ctx, out, opts.SessionRunOptions, opts.AudioOutPath, opts.MaxDuration, opts.TextSeed, input, opts.SystemPrompt)
	}
	if err := sessioncontract.ValidateSessionMaxDuration(opts.MaxDuration); err != nil {
		return err
	}
	if err := validateSessionRunOptions(opts.SessionRunOptions); err != nil {
		return err
	}
	if err := validateSessionAudioInput(input); err != nil {
		return err
	}
	if err := validateSessionRunOptions(opts.SessionRunOptions); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts.SessionRunOptions)
	if err != nil {
		return err
	}
	defer func() { _ = claim.release() }()
	metadata, err := resolveSessionImageCapabilities(opts.SessionRunOptions)
	if err != nil {
		return err
	}
	opts.SessionRunOptions.sessionImageCapabilities = cloneSessionImageCapabilities(&metadata)
	parts, err := PrepareSessionImageParts(paths, metadata)
	if err != nil {
		return err
	}
	if opts.TextSeed.Present {
		opts.SessionRunOptions.Prompt = opts.TextSeed.Value
		opts.SessionRunOptions.PromptProvided = true
	}
	var imageCleanup func()
	opts.SessionRunOptions, imageCleanup, err = prepareSessionImageToolAccess(opts.SessionRunOptions, paths, parts)
	if err != nil {
		return err
	}
	defer imageCleanup()
	audioSource, err := openSessionAudioInput(input)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := audioSource.Close(); closeErr != nil {
			runErr = errors.Join(runErr, closeErr)
		}
	}()

	// The finite source sends MESSAGE.END after its final frame. Disable
	// provider-side turn detection before planning the live runtime so that
	// this path owns the single commit and response boundary.
	opts.SessionRunOptions.ClientOwnsAudioTurnBoundaries = true
	if opts.AudioOutPath != "" {
		opts.SessionRunOptions.AudioOutputRequested = true
	}
	plan, wirePrompt, err := planSessionImageRuntime(opts.SessionRunOptions, parts, opts.TextSeed, opts.SystemPrompt, true)
	if err != nil {
		return err
	}
	plan.loop.CloseAfterOpen = false
	plan.loop.AudioIn = audioSource
	plan.loop.MaxDuration = opts.MaxDuration
	plan.loop.RequireAssistantResponse = true
	plan.loop.RequireTerminalAssistantResponse = true
	return runSessionImagePlan(ctx, out, plan, opts, wirePrompt)
}

func planSessionImageRuntime(opts SessionRunOptions, parts []messages.ImagePart, seed SessionTextSeed, systemPrompt string, deferResponse bool) (sessionRuntimePlan, string, error) {
	var (
		plan         sessionRuntimePlan
		err          error
		instructions string
	)
	if opts.ReplayPath != "" {
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
	return attachSessionImageRuntime(plan, parts, seed, deferResponse, opts.Prompt)
}

// planSessionImageRuntimeForDirectory keeps directory recording independent
// from the optional provider capture file. In particular, --record-dir alone
// needs the live provider runtime without giving its capture finalizer an
// empty path to flush. The directory planner owns that distinction and still
// preserves explicit --record and --replay behavior.
func planSessionImageRuntimeForDirectory(opts SessionRunOptions, parts []messages.ImagePart, seed SessionTextSeed, systemPrompt string, deferResponse bool) (sessionRuntimePlan, string, func(), error) {
	plan, cleanup, err := planSessionForDirectoryRecordingWithInstructions(opts, systemPrompt, true)
	if err != nil {
		return sessionRuntimePlan{}, "", func() {}, err
	}
	plan, wirePrompt, err := attachSessionImageRuntime(plan, parts, seed, deferResponse, opts.Prompt)
	if err != nil {
		cleanup()
		return sessionRuntimePlan{}, "", func() {}, err
	}
	return plan, wirePrompt, cleanup, nil
}

func attachSessionImageRuntime(plan sessionRuntimePlan, parts []messages.ImagePart, seed SessionTextSeed, deferResponse bool, prompt string) (sessionRuntimePlan, string, error) {
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
	if prompt == "" {
		plan.loop.Prompt = sessionImageOnlyPrompt
	}
	return plan, "", nil
}
func runSessionImagePlan(ctx context.Context, out io.Writer, plan sessionRuntimePlan, opts SessionImageRunOptions, wirePrompt string) (runErr error) {
	if opts.AudioOutPath != "" {
		audioOut, err := newSessionAudioOutputForPlan(&plan, opts.AudioOutPath, out, nil)
		if err != nil {
			return fmt.Errorf("--audio-out %q: %w", opts.AudioOutPath, err)
		}
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
			plan.inferencer = &sessionTextSeedInferencer{inner: plan.inferencer, wirePrompt: wirePrompt, value: opts.TextSeed.Value}
			return errors.Join(plan.run(ctx, output), output.errorValue())
		}
		durationCtx, err := prepareSessionDurationArtifacts(ctx)
		if err != nil {
			return err
		}
		admission := newSessionDurationAdmission()
		admittedInferencer := &sessionDurationAdmissionInferencer{inner: plan.inferencer, admission: admission, closeDone: make(chan struct{})}
		plan.inferencer = &sessionTextSeedInferencer{inner: admittedInferencer, wirePrompt: wirePrompt, value: opts.TextSeed.Value}
		err = runSessionDurationPlanWithAdmission(durationCtx, output, plan, opts.MaxDuration, realSessionDurationClock{}, admittedInferencer)
		return errors.Join(err, output.errorValue())
	}
	if opts.MaxDuration == 0 {
		return plan.run(ctx, out)
	}
	if plan.loop.AudioIn != nil {
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

const sessionImageOnlyPrompt = "\x00agent-session-image-turn\x00"

// sessionImageDeferredInstruction gives a deferred image item useful context
// before the separately committed spoken user item arrives. Realtime accepts
// image-only user items, but the explicit same-item instruction keeps the
// image's purpose visible to the model when no text seed was supplied.
const sessionImageDeferredInstruction = "Use the attached image to answer the user's next spoken question."

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

func (s *sessionImageSession) TerminalError() error {
	if s == nil {
		return nil
	}
	return terminalSessionError(s.Session)
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
			if s.deferResponse {
				text = sessionImageDeferredInstruction
			}
		}
		sent := sendSessionImageTurn(ctx, s.Session, text, parts, !s.deferResponse)
		s.signalFirstTurn(sent)
		return sent
	}
	s.signalFirstTurn(false)
	return false
}

// RequestResponse forwards the optional explicit response capability after a
// tool result when the image-turn wrapper has no user text to send.
func (s *sessionImageSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	return messages.RequestSessionResponse(ctx, s.Session)
}

func (s *sessionImageSession) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.Session)
}

// SendMessage forwards the optional complete-message capability of the
// provider session. The image-turn wrapper embeds only the stream Session
// interface, so optional multimodal delivery must be forwarded explicitly for
// a later read_image tool result to reach the same provider connection.
func (s *sessionImageSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(SessionImageMessageSender)
	return ok && sender.SendMessage(ctx, msg)
}

// SendMessageWithoutResponse forwards the deferred complete-message path used
// when a tool batch contains more than one rich result.
func (s *sessionImageSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(SessionImageMessageSenderWithoutResponse)
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (s *sessionImageSession) SupportsCompleteMessages() bool {
	complete, _ := completeMessageCapabilities(s.Session)
	return complete
}

func (s *sessionImageSession) SupportsCompleteMessagesWithoutResponse() bool {
	_, withoutResponse := completeMessageCapabilities(s.Session)
	return withoutResponse
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

func (s *sessionImageSession) rtcMedia() (RTCMediaEndpoints, bool) {
	return rtcMediaFromSession(s.Session)
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

func cloneSessionImageCapabilities(capabilities *SessionImageCapabilities) *SessionImageCapabilities {
	if capabilities == nil {
		return nil
	}
	clone := *capabilities
	clone.SupportedInputMIMETypes = append([]string(nil), capabilities.SupportedInputMIMETypes...)
	return &clone
}

func sessionHasTool(definitions []messages.ToolDefinition, name string) bool {
	for _, definition := range definitions {
		if definition.Name == name {
			return true
		}
	}
	return false
}

// bindSessionImageToolExecutor resolves image capability metadata once for a
// session and binds a private preparer to the registry executor. Capability
// failures are captured by the preparer so a provider that cannot inspect
// images can still continue the session and receive a correlated tool failure.
func bindSessionImageToolExecutor(opts SessionRunOptions, plan sessionRuntimePlan) messages.ToolExecutor {
	if opts.ToolExecutor == nil || !sessionHasTool(opts.ToolDefinitions, runtimeTools.ReadImageToolID) {
		return opts.ToolExecutor
	}
	binder, ok := opts.ToolExecutor.(runtimeTools.SessionImagePreparerBinder)
	if !ok {
		return opts.ToolExecutor
	}

	capabilities := cloneSessionImageCapabilities(opts.sessionImageCapabilities)
	var resolveErr error
	if capabilities == nil {
		capabilityOpts := opts
		if plan.provider != "" {
			capabilityOpts.Provider = plan.provider
		}
		if plan.model != "" {
			capabilityOpts.Model = plan.model
			capabilityOpts.ModelProvided = true
		}
		var resolved SessionImageCapabilities
		resolved, resolveErr = resolveSessionImageCapabilities(capabilityOpts)
		capabilities = cloneSessionImageCapabilities(&resolved)
	}

	metadata := SessionImageCapabilities{}
	if capabilities != nil {
		metadata = *capabilities
		metadata.SupportedInputMIMETypes = append([]string(nil), capabilities.SupportedInputMIMETypes...)
	}
	preparer := runtimeTools.ImagePartPreparer(func(paths []string) ([]messages.ImagePart, error) {
		if resolveErr != nil {
			return nil, resolveErr
		}
		return PrepareSessionImageParts(paths, metadata)
	})
	return binder.WithSessionImagePreparer(preparer)
}
