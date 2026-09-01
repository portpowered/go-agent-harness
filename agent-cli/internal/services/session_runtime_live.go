package services

import (
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// planBareLiveSessionRuntime builds the alternate-free live voice path. The
// resolver has already supplied the provider, model, credential, audio policy,
// and device presence bits; this planner only creates the existing
// audio-capable WebSocket inferencer and leaves device acquisition to the
// common session plan runner.
func planBareLiveSessionRuntime(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	provider := strings.ToLower(strings.TrimSpace(opts.Provider))
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
		return sessionRuntimePlan{}, fmt.Errorf("--browser-tools live sessions require provider %q or %q; got %q", sessionProviderOpenAI, sessionProviderGrok, provider)
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

	var inferencer messages.SessionInferencer
	switch provider {
	case sessionProviderOpenAI:
		clientOwnedAudio := opts.ClientOwnsAudioTurnBoundaries || len(opts.AudioInputs) > 0
		inputAudioTranscription := resolveInputAudioTranscriptionPolicy(opts, provider, interactive || clientOwnedAudio || opts.RTCDeviceBinding.inputSelected())
		inferencer, err = factory.newOpenAISessionInferencerForTools(openAISessionCfg, opts.Voice, liveDialer, opts.ToolDefinitions, clientOwnedAudio, inputAudioTranscription)
	case sessionProviderGrok:
		inferencer, err = factory.newGrokSessionInferencerForTools(grokSessionCfg, liveDialer, opts.ToolDefinitions)
	}
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	if inferencer == nil {
		return sessionRuntimePlan{}, fmt.Errorf("--browser-tools live session provider %q returned no session inferencer", provider)
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
