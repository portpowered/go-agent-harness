package agentruntime

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimedevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func (p *sessionRuntimePlan) bindRTC(ctx context.Context, finalizer *sessionRuntimeFinalizer) error {
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
	finalizer.setRTCBinding(binding)
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
	// Interactive browser sessions own input turn boundaries and stay open for
	// the page capability lifecycle. The provider construction remains shared
	// with ordinary live sessions.
	return planLiveSessionRuntimeMode(opts, factory, interactive)
}

// planLiveSessionRuntime plans a live session without wrapping its provider
// transport in a capture recorder. Capture ownership is selected by the
// caller's explicit --record/--replay path; an empty path is the intentional
// no-capture mode.
func planLiveSessionRuntime(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	return planLiveSessionRuntimeMode(opts, factory, false)
}

type liveSessionProviderConfig struct {
	provider string
	model    string
	openAI   config.OpenAIConfig
	grok     config.GrokConfig
}

func planLiveSessionRuntimeMode(opts SessionRunOptions, factory sessionRuntimeFactory, interactive bool) (sessionRuntimePlan, error) {
	providerConfig, err := resolveLiveSessionProviderConfig(opts)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	inferencer, err := newLiveSessionInferencer(opts, factory, providerConfig, interactive)
	if err != nil {
		return sessionRuntimePlan{}, err
	}

	return sessionRuntimePlan{
		mode:       sessionRuntimeModeInjectedLive,
		provider:   providerConfig.provider,
		model:      providerConfig.model,
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
			RequireSessionUpdated:    len(opts.AudioInputs) > 0 && providerConfig.provider == sessionProviderOpenAI,
		},
	}, nil
}

func resolveLiveSessionProviderConfig(opts SessionRunOptions) (liveSessionProviderConfig, error) {
	provider := strings.ToLower(strings.TrimSpace(effectiveSessionProvider(opts)))
	resolved := liveSessionProviderConfig{provider: provider}
	var err error
	switch provider {
	case sessionProviderOpenAI:
		resolved.openAI, err = resolveOpenAIRealtimeSessionConfig(opts)
		resolved.model = resolved.openAI.Model
	case sessionProviderGrok:
		resolved.grok, err = resolveGrokSessionConfig(opts)
		resolved.model = resolved.grok.Model
	default:
		return liveSessionProviderConfig{}, unsupportedRealtimeSessionProviderError(provider)
	}
	return resolved, err
}

func newLiveSessionInferencer(
	opts SessionRunOptions,
	factory sessionRuntimeFactory,
	providerConfig liveSessionProviderConfig,
	interactive bool,
) (messages.SessionInferencer, error) {
	dialer, err := resolveLiveSessionDialer(opts, factory, providerConfig.provider)
	if err != nil {
		return nil, err
	}
	var inferencer messages.SessionInferencer
	switch providerConfig.provider {
	case sessionProviderOpenAI:
		inferencer, err = newLiveOpenAISessionInferencer(opts, factory, providerConfig.openAI, dialer, interactive)
	case sessionProviderGrok:
		inferencer, err = factory.newGrokSessionInferencerForTools(providerConfig.grok, dialer, opts.ToolDefinitions)
	}
	if err != nil {
		return nil, err
	}
	if inferencer == nil {
		if opts.BrowserToolsEnabled {
			return nil, fmt.Errorf("--browser-tools live session provider %q returned no session inferencer", providerConfig.provider)
		}
		return nil, fmt.Errorf("live session provider %q returned no session inferencer", providerConfig.provider)
	}
	return inferencer, nil
}

func resolveLiveSessionDialer(opts SessionRunOptions, factory sessionRuntimeFactory, provider string) (transport.Dialer, error) {
	dialer := opts.WebSocketDialer
	if dialer == nil && factory.newDefaultLiveDialer != nil {
		dialer = factory.newDefaultLiveDialer()
	}
	if dialer == nil {
		return nil, missingOwnedSessionDialerError(provider)
	}
	return observeSessionWire(dialer, opts), nil
}

func newLiveOpenAISessionInferencer(
	opts SessionRunOptions,
	factory sessionRuntimeFactory,
	sessionConfig config.OpenAIConfig,
	dialer transport.Dialer,
	interactive bool,
) (messages.SessionInferencer, error) {
	clientOwnedAudio := opts.ClientOwnsAudioTurnBoundaries || len(opts.AudioInputs) > 0
	transcription, err := resolveSessionTranscription(opts, sessionProviderOpenAI, interactive || clientOwnedAudio || opts.RTCBinding.HasInput())
	if err != nil {
		return nil, err
	}
	return factory.newOpenAISessionInferencerForTools(sessionConfig, opts.Voice, dialer, opts.ToolDefinitions, clientOwnedAudio, transcription)
}

func planSessionWithResolvedInstructions(opts SessionRunOptions, instructions string) (sessionRuntimePlan, error) {
	// This is the single service-owned boundary between prompt resolution and
	// provider construction. The tool definitions in opts are the same snapshot
	// that the runtime planner passes to the provider, so the grounding contract
	// cannot drift from the advertised tool surface.
	opts.ToolDefinitions = messages.CanonicalToolDefinitions(opts.ToolDefinitions)
	instructions = composeSessionInstructions(opts, instructions)
	planFactory := opts.runtimeFactory
	if !planFactory.configured() {
		planFactory = newDefaultSessionRuntimeFactory()
	}
	useInitialProviderInstructions := instructions != "" && opts.SessionInferencer == nil
	if useInitialProviderInstructions {
		planFactory = sessionRuntimeFactoryWithInstructions(planFactory, instructions)
	}
	plan, err := planSessionRuntimeWithFactory(opts, planFactory)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	// Caller-owned/injected sessions do not have a provider factory that can
	// receive the resolved tool surface. Configure them whenever either
	// instructions or tools are present; an empty instruction remains empty and
	// does not synthesize a default prompt.
	if opts.SessionInferencer != nil && plan.inferencer != nil && !useInitialProviderInstructions && (instructions != "" || len(opts.ToolDefinitions) > 0) {
		plan.inferencer = newSessionInstructionsInferencer(plan.inferencer, instructions, opts.ToolDefinitions)
		// The wrapper above owns the complete injected-session configuration.
		// Suppress ModelRunner's separate tool-only update, which otherwise races
		// an identical second SESSION.UPDATE onto the provider wire.
		plan.loop.AdvertiseToolDefinitions = false
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
