package agentruntime

import sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/input"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/imageinput"
	imageinputwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/imageinput/wire"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const (
	ErrSessionImageMissingFile     = imageinput.ErrMissingFile
	ErrSessionImageUnreadableFile  = imageinput.ErrUnreadableFile
	ErrSessionImageUnsupportedMIME = imageinput.ErrUnsupportedMIME
	ErrSessionImageInvalidContent  = imageinput.ErrInvalidContent
	ErrSessionImageEmptyFile       = imageinput.ErrEmptyFile
	ErrSessionImageCapability      = imageinput.ErrCapability
	ErrSessionImageSend            = imageinput.ErrSend
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
type (
	SessionImageCapabilityError      = imageinput.CapabilityError
	SessionImageMissingFileError     = imageinput.MissingFileError
	SessionImageUnreadableFileError  = imageinput.UnreadableFileError
	SessionImageUnsupportedMIMEError = imageinput.UnsupportedMIMEError
	SessionImageInvalidContentError  = imageinput.InvalidContentError
	SessionImageEmptyFileError       = imageinput.EmptyFileError
)
type SessionImageMessageSender = imageinput.MessageSender
type SessionImageMessageSenderWithoutResponse = imageinput.MessageSenderWithoutResponse

func completeMessageCapabilities(session messages.Session) (complete, withoutResponse bool) {
	if capabilities, ok := session.(imageinput.CompleteMessageCapabilities); ok {
		return capabilities.SupportsCompleteMessages(), capabilities.SupportsCompleteMessagesWithoutResponse()
	}
	_, completeSender := session.(SessionImageMessageSender)
	_, completeErrorSender := session.(imageinput.MessageSenderWithError)
	_, withoutResponseSender := session.(SessionImageMessageSenderWithoutResponse)
	_, withoutResponseErrorSender := session.(imageinput.MessageSenderWithoutResponseWithError)
	complete = completeSender || completeErrorSender
	withoutResponse = withoutResponseSender || withoutResponseErrorSender
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
	imageService := newSessionImageService()
	attachment, err := imageService.Attach(plan.inferencer, parts, imageinput.TurnOptions{DeferResponse: deferResponse})
	if err != nil {
		return sessionRuntimePlan{}, "", err
	}
	plan.inferencer = &sessionImageInferencer{inner: plan.inferencer, attached: attachment.Inferencer}
	plan.loop.awaitFirstTurn = attachment.FirstTurn
	if seed.Present {
		wirePrompt := nextSessionTextWirePrompt()
		plan.loop.Prompt = wirePrompt
		return plan, wirePrompt, nil
	}
	if prompt == "" {
		plan.loop.Prompt = imageinput.ImageOnlyPrompt
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

type sessionImageContentLoader struct{}

func (sessionImageContentLoader) Load(_ context.Context, path string) (messages.ContentPart, error) {
	return input.LoadContentPart(path)
}
func newSessionImageService() imageinput.Service {
	return imageinputwire.NewService(sessionImageContentLoader{})
}

// Deprecated: use imageinput.Service.Prepare through imageinput/wire.
func PrepareSessionImageParts(paths []string, metadata SessionImageCapabilities) ([]messages.ImagePart, error) {
	return newSessionImageService().Prepare(context.Background(), paths, imageinput.Capabilities{
		Model:                   metadata.Model,
		SupportsImageInput:      metadata.SupportsImageInput,
		SupportedInputMIMETypes: append([]string(nil), metadata.SupportedInputMIMETypes...),
	})
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

type sessionImageInferencer struct {
	inner    messages.SessionInferencer
	attached messages.SessionInferencer
}

func (i *sessionImageInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	attached := i.attached
	if attached == nil {
		attached = i.inner
	}
	session, err := attached.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	forwarder, ok := session.(imageinput.ForwardingSession)
	if !ok {
		return session, nil
	}
	return &sessionImageSession{ForwardingSession: forwarder, provider: forwarder.UnderlyingSession()}, nil
}

type sessionImageSession struct {
	imageinput.ForwardingSession
	provider messages.Session
}

func (s *sessionImageSession) rtcMedia() (RTCMediaEndpoints, bool) {
	return rtcMediaFromSession(s.provider)
}

// Deprecated: use imageinput.Service.Send through imageinput/wire.
func SendSessionImageTurn(ctx context.Context, session messages.Session, text string, parts []messages.ImagePart) error {
	return newSessionImageService().Send(ctx, session, text, parts, imageinput.TurnOptions{})
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
	return slices.ContainsFunc(definitions, func(definition messages.ToolDefinition) bool { return definition.Name == name })
}
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
	metadata := *capabilities
	metadata.SupportedInputMIMETypes = append([]string(nil), capabilities.SupportedInputMIMETypes...)
	preparer := runtimeTools.ImagePartPreparer(func(paths []string) ([]messages.ImagePart, error) {
		if resolveErr != nil {
			return nil, resolveErr
		}
		return PrepareSessionImageParts(paths, metadata)
	})
	return binder.WithSessionImagePreparer(preparer)
}
