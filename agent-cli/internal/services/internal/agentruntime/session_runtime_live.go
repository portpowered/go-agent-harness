package agentruntime

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimedevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	duration "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func (p *sessionRuntimePlan) bindRTC(ctx context.Context, finalizer duration.Finalizer) error {
	if p.deviceService == nil {
		if p.rtcDeviceRequest.HasDevices() {
			return runtimedevices.ErrUnavailable
		}
		return nil
	}
	p.rtcDeviceRequest.Inferencer = p.inferencer
	binding, err := p.deviceService.BindRTC(ctx, p.rtcDeviceRequest)
	if err != nil {
		return err
	}
	p.rtcBinding = binding
	if binding == nil {
		return nil
	}
	p.inferencer = binding.Inferencer()
	p.loop.rtcDeviceBinding = binding
	finalizer.SetDeviceBinding(binding.Close)
	if selected, ok := binding.(runtimedevices.RTCBindingDeviceSelection); ok {
		inputDevice, outputDevice := selected.SelectedDeviceIDs()
		if p.rtcDeviceRequest.InputDevice == "" {
			p.rtcDeviceRequest.InputDevice = inputDevice
		}
		if p.rtcDeviceRequest.OutputDevice == "" {
			p.rtcDeviceRequest.OutputDevice = outputDevice
		}
	}
	return nil
}

func (p *sessionRuntimePlan) writeAnnouncements(out io.Writer, includeBrowser bool) error {
	// These startup disclosures are best-effort and must not pre-empt the
	// session's own run/drain failure when their writer is unavailable.
	writeFilesystemScopeAnnouncement(out, p.filesystemPolicy)
	writeSessionToolAnnouncement(out, p.toolDefinitionsForAnnouncement())
	announcement := p.announce
	if p.loop.BareLive {
		announcement, p.loop.ListeningBanner = p.bareLiveOutput()
	} else if includeBrowser && p.loop.BrowserToolsInteractive {
		announcement, p.loop.ListeningBanner = p.browserLiveOutput()
	}
	if announcement == "" {
		return nil
	}
	_, err := fmt.Fprintln(out, announcement)
	return err
}

// planBareLiveSessionRuntime builds the alternate-free live voice path. The
// resolver has already supplied the provider, model, credential, audio policy,
// and device presence bits; this planner only creates the existing
// audio-capable WebSocket inferencer and leaves device acquisition to the
// common session plan runner.
func planBareLiveSessionRuntime(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	provider := effectiveSessionProvider(opts)
	opts.Provider = provider
	if provider != sessionProviderOpenAI && provider != sessionProviderGrok {
		return sessionRuntimePlan{}, fmt.Errorf("bare live sessions require provider %q or %q; got %q", sessionProviderOpenAI, sessionProviderGrok, provider)
	}

	build := factory.newBareLiveSessionInferencer
	if build == nil {
		build = func(options SessionRunOptions) (messages.SessionInferencer, string, error) {
			return NewLiveSessionInferencer(options, "")
		}
	}
	inferencer, model, err := build(opts)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	if inferencer == nil {
		return sessionRuntimePlan{}, fmt.Errorf("bare live session provider %q returned no session inferencer", provider)
	}

	return sessionRuntimePlan{
		mode:       sessionRuntimeModeBareLive,
		provider:   provider,
		model:      model,
		inferencer: inferencer,
		loop: sessionLoopOptions{
			WaitForClose:             true,
			BareLive:                 true,
			AdvertiseToolDefinitions: false,
		},
	}, nil
}

func browserToolsInteractiveLive(opts SessionRunOptions) bool {
	return opts.BrowserToolsInteractive && opts.BrowserToolsEnabled &&
		opts.RecordPath == "" && opts.ReplayPath == "" &&
		!opts.PromptProvided && strings.TrimSpace(opts.Prompt) == "" &&
		len(opts.AudioInputs) == 0 && !opts.ClientOwnsAudioTurnBoundaries
}

// planBrowserLiveSessionRuntime plans an explicitly browser-enabled live
// session without wrapping its provider transport in a capture recorder. The
// browser capability is still carried by opts.ToolDefinitions and
// opts.ToolExecutor; this planner only supplies the non-recording provider
// runtime that the new admission path needs.
func planBrowserLiveSessionRuntime(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	interactive := browserToolsInteractiveLive(opts)
	provider := effectiveSessionProvider(opts)
	opts.Provider = provider
	var (
		openAISessionCfg config.OpenAIConfig
		grokSessionCfg   config.GrokConfig
		model            string
		err              error
	)
	switch provider {
	case sessionProviderOpenAI:
		openAISessionCfg, err = resolveOpenAIRealtimeSessionConfig(opts)
		model = openAISessionCfg.Model
	case sessionProviderGrok:
		grokSessionCfg, err = resolveGrokSessionConfig(opts)
		model = grokSessionCfg.Model
	default:
		return sessionRuntimePlan{}, unsupportedRealtimeSessionProviderError(provider)
	}
	if err != nil {
		return sessionRuntimePlan{}, err
	}

	liveDialer := opts.WebSocketDialer
	if liveDialer == nil && factory.newDefaultLiveDialer != nil {
		liveDialer = factory.newDefaultLiveDialer()
	}
	if liveDialer == nil {
		return sessionRuntimePlan{}, missingOwnedSessionDialerError(provider)
	}

	liveDialer = observeSessionWire(liveDialer, opts)

	var inferencer messages.SessionInferencer
	switch provider {
	case sessionProviderOpenAI:
		clientOwnedAudio := opts.ClientOwnsAudioTurnBoundaries || len(opts.AudioInputs) > 0
		inputAudioTranscription, resolveErr := resolveSessionTranscription(opts, provider, interactive || clientOwnedAudio || opts.RTCBinding.HasInput())
		if resolveErr != nil {
			return sessionRuntimePlan{}, resolveErr
		}
		inferencer, err = factory.newOpenAISessionInferencerForTools(openAISessionCfg, opts.Voice, liveDialer, opts.ToolDefinitions, clientOwnedAudio, inputAudioTranscription)
	case sessionProviderGrok:
		inferencer, err = factory.newGrokSessionInferencerForTools(grokSessionCfg, liveDialer, opts.ToolDefinitions)
	}
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	if inferencer == nil {
		if opts.BrowserToolsEnabled {
			return sessionRuntimePlan{}, fmt.Errorf("--browser-tools live session provider %q returned no session inferencer", provider)
		}
		return sessionRuntimePlan{}, fmt.Errorf("live session provider %q returned no session inferencer", provider)
	}

	return sessionRuntimePlan{
		mode:       sessionRuntimeModeInjectedLive,
		provider:   provider,
		model:      model,
		inferencer: inferencer,
		loop: sessionLoopOptions{
			Prompt:                   opts.Prompt,
			CloseAfterOpen:           !interactive && !opts.WaitForClose && len(opts.AudioInputs) == 0,
			WaitForClose:             interactive || opts.WaitForClose || len(opts.AudioInputs) > 0,
			CloseAfterScheduledAudio: len(opts.AudioInputs) > 0,
			BrowserToolsInteractive:  interactive,
			// The provider-backed constructor receives the stable definitions in
			// its initial Realtime session configuration. Do not send a second
			// generic SESSION.UPDATE for the same surface after SESSION.CREATED;
			// page-catalog changes remain result data and never re-advertise it.
			AdvertiseToolDefinitions: false,
			RequireSessionUpdated:    len(opts.AudioInputs) > 0 && provider == sessionProviderOpenAI,
		},
	}, nil
}

// planLiveSessionRuntime plans a live session without wrapping its provider
// transport in a capture recorder. Capture ownership is selected by the
// caller's explicit --record/--replay path; an empty path is the intentional
// no-capture mode.
func planLiveSessionRuntime(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	provider := strings.ToLower(strings.TrimSpace(effectiveSessionProvider(opts)))
	var (
		openAISessionCfg config.OpenAIConfig
		grokSessionCfg   config.GrokConfig
		model            string
		err              error
	)
	switch provider {
	case sessionProviderOpenAI:
		openAISessionCfg, err = resolveOpenAIRealtimeSessionConfig(opts)
		model = openAISessionCfg.Model
	case sessionProviderGrok:
		grokSessionCfg, err = resolveGrokSessionConfig(opts)
		model = grokSessionCfg.Model
	default:
		return sessionRuntimePlan{}, unsupportedRealtimeSessionProviderError(provider)
	}
	if err != nil {
		return sessionRuntimePlan{}, err
	}

	liveDialer := opts.WebSocketDialer
	if liveDialer == nil && factory.newDefaultLiveDialer != nil {
		liveDialer = factory.newDefaultLiveDialer()
	}
	if liveDialer == nil {
		return sessionRuntimePlan{}, missingOwnedSessionDialerError(provider)
	}

	liveDialer = observeSessionWire(liveDialer, opts)

	var inferencer messages.SessionInferencer
	switch provider {
	case sessionProviderOpenAI:
		clientOwnedAudio := opts.ClientOwnsAudioTurnBoundaries || len(opts.AudioInputs) > 0
		inputAudioTranscription, resolveErr := resolveSessionTranscription(opts, provider, clientOwnedAudio || opts.RTCBinding.HasInput())
		if resolveErr != nil {
			return sessionRuntimePlan{}, resolveErr
		}
		inferencer, err = factory.newOpenAISessionInferencerForTools(openAISessionCfg, opts.Voice, liveDialer, opts.ToolDefinitions, clientOwnedAudio, inputAudioTranscription)
	case sessionProviderGrok:
		inferencer, err = factory.newGrokSessionInferencerForTools(grokSessionCfg, liveDialer, opts.ToolDefinitions)
	}
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	if inferencer == nil {
		return sessionRuntimePlan{}, fmt.Errorf("live session provider %q returned no session inferencer", provider)
	}

	return sessionRuntimePlan{
		mode:       sessionRuntimeModeInjectedLive,
		provider:   provider,
		model:      model,
		inferencer: inferencer,
		loop: sessionLoopOptions{
			Prompt:                   opts.Prompt,
			CloseAfterOpen:           !opts.WaitForClose && len(opts.AudioInputs) == 0,
			WaitForClose:             opts.WaitForClose || len(opts.AudioInputs) > 0,
			CloseAfterScheduledAudio: len(opts.AudioInputs) > 0,
			// The provider-backed constructor receives the stable definitions in
			// its initial Realtime session configuration. Do not send a second
			// generic SESSION.UPDATE for the same surface after SESSION.CREATED;
			// page-catalog changes remain result data and never re-advertise it.
			AdvertiseToolDefinitions: false,
			RequireSessionUpdated:    len(opts.AudioInputs) > 0 && provider == sessionProviderOpenAI,
		},
	}, nil
}

func planSessionWithResolvedInstructions(opts SessionRunOptions, instructions string) (sessionRuntimePlan, error) {
	return planSessionWithResolvedInstructionsContext(context.Background(), opts, instructions)
}

func planSessionRuntime(opts SessionRunOptions) (sessionRuntimePlan, error) {
	return planSessionRuntimeWithContext(context.Background(), opts)
}

func planSessionRuntimeWithContext(ctx context.Context, opts SessionRunOptions) (sessionRuntimePlan, error) {
	factory := opts.runtimeFactory
	if !factory.configured() {
		factory = newDefaultSessionRuntimeFactory()
	}
	return planSessionRuntimeWithFactory(ctx, opts, factory)
}

func planSessionWithResolvedInstructionsContext(ctx context.Context, opts SessionRunOptions, instructions string) (sessionRuntimePlan, error) {
	opts.ToolDefinitions = messages.CanonicalToolDefinitions(opts.ToolDefinitions)
	opts.sessionInstructions = instructions
	planFactory := opts.runtimeFactory
	if !planFactory.configured() {
		planFactory = newDefaultSessionRuntimeFactory()
	}
	plan, err := planSessionRuntimeWithFactory(ctx, opts, planFactory)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	return plan, nil
}

// sessionRuntimeFactoryWithInstructions carries resolved instructions into
// the provider's initial SessionConfig. The generic session adapter remains
// the fallback for injected session seams, while live providers receive the
// same value before ConnectSession can send their initial wire update.
func sessionRuntimeFactoryWithInstructions(base sessionRuntimeFactory, instructions string) sessionRuntimeFactory {
	factory := base
	factory.newBareLiveSessionInferencer = func(opts SessionRunOptions) (messages.SessionInferencer, string, error) {
		return NewLiveSessionInferencer(opts, instructions)
	}
	factory.newGrokSessionInferencer = func(sessionCfg config.GrokConfig, dialer transport.Dialer) (messages.SessionInferencer, error) {
		return buildGrokSessionInferencerWithInstructions(sessionCfg, dialer, instructions)
	}
	factory.newOpenAISessionInf = func(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, inputAudioTranscription models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
		return buildOpenAIRealtimeSessionInferencerWithInstructionsAndInputAudioTranscription(sessionCfg, voice, dialer, instructions, inputAudioTranscription)
	}
	factory.newGrokSessionWithTools = func(sessionCfg config.GrokConfig, dialer transport.Dialer, toolDefinitions []messages.ToolDefinition) (messages.SessionInferencer, error) {
		return buildGrokSessionInferencerWithInstructionsAndTools(sessionCfg, dialer, instructions, toolDefinitions)
	}
	factory.newOpenAISessionWithTools = func(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, toolDefinitions []messages.ToolDefinition, inputAudioTranscription models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
		return buildOpenAIRealtimeSessionInferencerWithInstructionsAndToolsAndInputAudioTranscription(sessionCfg, voice, dialer, instructions, toolDefinitions, inputAudioTranscription)
	}
	factory.newOpenAIScheduledSessionWithTools = func(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, toolDefinitions []messages.ToolDefinition, inputAudioTranscription models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
		return buildOpenAIRealtimeSessionInferencerWithInstructionsAndToolsAndScheduledAudioAndInputAudioTranscription(sessionCfg, voice, dialer, instructions, toolDefinitions, inputAudioTranscription)
	}
	return factory
}

func buildGrokSessionInferencerWithInstructions(sessionCfg config.GrokConfig, dialer transport.Dialer, instructions string) (messages.SessionInferencer, error) {
	return buildGrokSessionInferencerWithInstructionsAndTools(sessionCfg, dialer, instructions, nil)
}

func buildGrokSessionInferencerWithInstructionsAndTools(sessionCfg config.GrokConfig, dialer transport.Dialer, instructions string, toolDefinitions []messages.ToolDefinition) (messages.SessionInferencer, error) {
	if dialer == nil {
		return nil, missingOwnedSessionDialerError(sessionProviderGrok)
	}
	providerOpts := []grok.Option{grok.WithAPIKey(sessionCfg.APIKey), grok.WithWebSocketDialer(dialer)}
	if strings.TrimSpace(sessionCfg.BaseURL) != "" {
		providerOpts = append(providerOpts, grok.WithBaseURL(sessionCfg.BaseURL))
	}
	sessionGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(grok.New(providerOpts...)))
	if err != nil {
		return nil, fmt.Errorf("create Grok session gateway: %w", err)
	}
	inferenceOpts := []inference.SessionOption{
		inference.WithSessionModel(sessionCfg.Model),
		inference.WithSessionInstructions(instructions),
	}
	if len(toolDefinitions) > 0 {
		inferenceOpts = append(inferenceOpts, inference.WithSessionTools(toolDefinitions))
	}
	return inference.NewSessionGatewayInferencer(sessionGateway, inferenceOpts...), nil
}

//lint:ignore U1000 package tests exercise the OpenAI factory seam.
func buildOpenAIRealtimeSessionInferencerWithInstructionsAndTools(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, instructions string, toolDefinitions []messages.ToolDefinition) (messages.SessionInferencer, error) {
	return buildOpenAIRealtimeSessionInferencerWithInstructionsAndToolsAndInputAudioTranscription(sessionCfg, voice, dialer, instructions, toolDefinitions, models.InputAudioTranscriptionConfig{})
}

func buildOpenAIRealtimeSessionInferencerWithInstructionsAndInputAudioTranscription(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, instructions string, inputAudioTranscription models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
	return buildOpenAIRealtimeSessionInferencerWithInstructionsAndToolsAndInputAudioTranscription(sessionCfg, voice, dialer, instructions, nil, inputAudioTranscription)
}

func buildOpenAIRealtimeSessionInferencerWithInstructionsAndToolsAndInputAudioTranscription(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, instructions string, toolDefinitions []messages.ToolDefinition, inputAudioTranscription models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
	return buildOpenAIRealtimeSessionInferencerWithInstructionsAndToolsAndInputAudioTranscriptionAndOptions(sessionCfg, voice, dialer, instructions, toolDefinitions, inputAudioTranscription)
}

func buildOpenAIRealtimeSessionInferencerWithInstructionsAndToolsAndScheduledAudioAndInputAudioTranscription(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, instructions string, toolDefinitions []messages.ToolDefinition, inputAudioTranscription models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
	return buildOpenAIRealtimeSessionInferencerWithInstructionsAndToolsAndInputAudioTranscriptionAndOptions(sessionCfg, voice, dialer, instructions, toolDefinitions, inputAudioTranscription, oaiprovider.WithClientOwnedAudioTurnBoundaries())
}

func buildOpenAIRealtimeSessionInferencerWithInstructionsAndToolsAndInputAudioTranscriptionAndOptions(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, instructions string, toolDefinitions []messages.ToolDefinition, inputAudioTranscription models.InputAudioTranscriptionConfig, extra ...oaiprovider.Option) (messages.SessionInferencer, error) {
	if dialer == nil {
		return nil, missingOwnedSessionDialerError(sessionProviderOpenAI)
	}
	providerOpts := []oaiprovider.Option{
		oaiprovider.WithAPIKey(sessionCfg.APIKey),
		oaiprovider.WithModel(sessionCfg.Model),
		oaiprovider.WithRealtimeBaseURL(openAIRealtimeURL(sessionCfg)),
		oaiprovider.WithWebSocketDialer(dialer),
	}
	providerOpts = append(providerOpts, extra...)
	sessionGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(oaiprovider.New(providerOpts...)))
	if err != nil {
		return nil, fmt.Errorf("create OpenAI realtime session gateway: %w", err)
	}
	inferenceOpts := []inference.SessionOption{
		inference.WithSessionModel(sessionCfg.Model),
		inference.WithSessionInstructions(instructions),
		inference.WithSessionInputAudioTranscription(inputAudioTranscription),
	}
	if sessionCfg.ReasoningEffort != "" {
		inferenceOpts = append(inferenceOpts, inference.WithSessionReasoningEffort(sessionCfg.ReasoningEffort))
	}
	if voice != "" {
		inferenceOpts = append(inferenceOpts, inference.WithSessionVoice(voice))
	}
	if len(toolDefinitions) > 0 {
		inferenceOpts = append(inferenceOpts, inference.WithSessionTools(toolDefinitions))
	}
	return inference.NewSessionGatewayInferencer(sessionGateway, inferenceOpts...), nil
}
