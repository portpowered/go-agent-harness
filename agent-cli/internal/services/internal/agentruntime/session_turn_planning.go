package agentruntime

import (
	"context"
	"fmt"
	"io"

	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	sessiontransport "github.com/portpowered/go-agent-harness/agent-cli/internal/transport"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	sessionturnwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/wire"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// prepareSessionTurnSeed transfers seed substitution and serialized output
// ownership to the session-turn service. The planner only replaces the
// provider edge and prompt value; it does not retain seed state.
func prepareSessionTurnSeed(ctx context.Context, plan *sessionRuntimePlan, seed sessionturn.Seed) (sessionturn.Runtime, error) {
	if plan == nil || plan.inferencer == nil {
		return nil, nil
	}
	runtime, err := sessionturnwire.NewDefaultService().Prepare(ctx, sessionturn.Request{
		SessionInferencer: plan.inferencer,
		Seed:              seed,
		ToolExecutor:      plan.loop.ToolExecutor,
		ToolDefinitions:   nil,
	})
	if err != nil {
		return nil, fmt.Errorf("prepare session-turn seed: %w", err)
	}
	plan.turnRuntime = runtime
	plan.loop.turnRuntime = runtime
	plan.inferencer = runtime.Inferencer()
	if wirePrompt := runtime.WirePrompt(); wirePrompt != "" {
		plan.loop.Prompt = wirePrompt
	}
	return runtime, nil
}

func sessionLoopToolExecutor(opts sessionLoopOptions) messages.ToolExecutor {
	if opts.turnRuntime == nil {
		if opts.ToolExecutor == nil {
			return nil
		}
		if _, ok := opts.ToolExecutor.(sessionturn.ServiceOwnedToolExecutor); ok {
			return opts.ToolExecutor
		}
		runtime, err := prepareSessionTurnExecutorRuntime(context.Background(), nil, opts)
		if err != nil {
			return opts.ToolExecutor
		}
		return runtime.ToolExecutor()
	}
	return opts.turnRuntime.ToolExecutor()
}

func prepareSessionTurnExecutorRuntime(ctx context.Context, inferencer messages.SessionInferencer, opts sessionLoopOptions) (sessionturn.Runtime, error) {
	toolLifecycle := composeSessionToolLifecycleObserver(opts.toolLifecycleObserver, opts.observer, opts.runtime)
	return sessionturnwire.NewDefaultService().Prepare(ctx, sessionturn.Request{
		SessionInferencer:     inferencer,
		ToolExecutor:          opts.ToolExecutor,
		ToolDefinitions:       append([]messages.ToolDefinition(nil), opts.ToolDefinitions...),
		InteractiveToolPolicy: opts.InteractiveToolPolicy,
		ToolExecutionTimeout:  opts.ToolExecutionTimeout,
		ToolCallObserver: func(call messages.ToolCall) {
			if toolLifecycle != nil {
				toolLifecycle.observeToolCall(call)
			}
		},
		ToolResultObserver: func(call messages.ToolCall, response messages.ToolCallResponse, failed bool) {
			if toolLifecycle != nil {
				toolLifecycle.observeToolResult(call, response, failed)
			}
		},
		ToolDiagnostic: func(call messages.ToolCall, err error) {
			recordSessionToolDiagnostic(opts.toolDiagnostics, opts.ToolExecutor, call, err)
		},
		ToolLifecycle: func(event sessionturn.ToolLifecycleEvent) {
			if opts.observer != nil {
				opts.observer.observeSessionTurnLifecycle(event)
			}
		},
	})
}

func prepareSessionRecordingTurnRuntime(ctx context.Context, plan *sessionRuntimePlan, seed SessionTextSeed) (sessionturn.Runtime, error) {
	turnRuntime := plan.turnRuntime
	if !seed.Present || (turnRuntime != nil && turnRuntime.WirePrompt() != "") {
		return turnRuntime, nil
	}
	return prepareSessionTurnSeed(ctx, plan, seed)
}

func prepareSessionRecordingOutputs(plan *sessionRuntimePlan, out io.Writer, audioOutPath string, seed SessionTextSeed, turnRuntime sessionturn.Runtime) (*sessionAudioOutput, sessionturn.AudioOutputRuntime, sessionturn.Output, error) {
	if audioOutPath == "" {
		if seed.Present && turnRuntime != nil {
			return nil, nil, turnRuntime.NewOutput(out), nil
		}
		return nil, nil, nil, nil
	}
	audioOutput, err := newSessionAudioOutputForPlan(plan, audioOutPath, out, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("--audio-out %q: %w", audioOutPath, err)
	}
	if plan.inferencer == nil {
		return audioOutput, nil, nil, nil
	}
	audioWrapper, err := attachSessionAudioOutput(turnRuntime, plan, audioOutput)
	if err != nil {
		return nil, nil, nil, err
	}
	return audioOutput, audioWrapper, nil, nil
}

func prepareSessionTurnRuntime(ctx context.Context, opts SessionRunOptions, plan *sessionRuntimePlan) error {
	toolLifecycle := composeSessionToolLifecycleObserver(plan.loop.toolLifecycleObserver, plan.loop.observer, plan.runtime)
	request := sessionturn.Request{
		SessionInferencer: plan.inferencer,
		ToolExecutor:      plan.loop.ToolExecutor,
		InstructionsText:  opts.sessionInstructions,
		Instructions: sessionturn.InstructionRequest{Composition: &runtimeSession.InstructionComposition{
			ToolDefinitions:        append([]messages.ToolDefinition(nil), plan.loop.ToolDefinitions...),
			BrowserCapabilityState: runtimeSession.BrowserCapabilityState(string(opts.BrowserCapabilityState)),
			BrowserToolsEnabled:    opts.BrowserToolsEnabled,
			PageSightToolID:        cliTools.PageSightToolID,
		}},
		ToolExecutionTimeout: opts.ToolExecutionTimeout,
		ToolCallObserver: func(call messages.ToolCall) {
			if toolLifecycle != nil {
				toolLifecycle.observeToolCall(call)
			}
		},
		ToolResultObserver: func(call messages.ToolCall, response messages.ToolCallResponse, failed bool) {
			if toolLifecycle != nil {
				toolLifecycle.observeToolResult(call, response, failed)
			}
		},
		ToolDiagnostic: func(call messages.ToolCall, err error) {
			recordSessionToolDiagnostic(opts.ToolDiagnostics, opts.ToolExecutor, call, err)
		},
		ToolLifecycle: func(event sessionturn.ToolLifecycleEvent) {
			if plan.loop.observer != nil {
				plan.loop.observer.observeSessionTurnLifecycle(event)
			}
		},
		ImageCleanup: opts.sessionImageCleanup,
	}
	if opts.sessionImageRequest != nil {
		request.Seed = opts.sessionImageRequest.Seed
		request.Image = opts.sessionImageRequest.Image
	}
	if err := prepareSessionTurnImages(opts, plan, &request); err != nil {
		return err
	}
	if opts.ReplayPath == "" || opts.SessionInferencer != nil {
		request.ToolDefinitions = append([]messages.ToolDefinition(nil), plan.loop.ToolDefinitions...)
	}
	if err := configureSessionTurnToolPolicy(opts, &request); err != nil {
		return err
	}
	turnRuntime, err := sessionturnwire.NewDefaultService().Prepare(ctx, request)
	if err != nil {
		return fmt.Errorf("prepare session-turn runtime: %w", err)
	}
	plan.turnRuntime = turnRuntime
	plan.loop.turnRuntime = turnRuntime
	plan.loop.turnBrowser = sessiontransport.SessionTurnBrowserRequest(opts.BrowserWatch, opts.RefreshToolDefinitions)
	plan.loop.ToolExecutor = turnRuntime.ToolExecutor()
	plan.inferencer = turnRuntime.Inferencer()
	if opts.SessionInferencer != nil && (opts.sessionInstructions != "" || len(opts.ToolDefinitions) != 0) {
		plan.loop.AdvertiseToolDefinitions = false
	}
	return nil
}

func configureSessionTurnToolPolicy(opts SessionRunOptions, request *sessionturn.Request) error {
	request.InteractiveToolPolicy = opts.InteractiveToolPolicy
	if opts.InteractiveToolPolicy != nil {
		return nil
	}
	settings := runtimeTools.InteractiveToolPolicySettings{}
	if opts.LoadedConfig != nil {
		configured, err := opts.LoadedConfig.ResolveInteractiveToolConfig()
		if err != nil {
			return fmt.Errorf("resolve interactive tool policy settings: %w", err)
		}
		settings = runtimeTools.InteractiveToolPolicySettings{
			FastReadTimeout:          configured.FastReadTimeout,
			LongRunningTimeout:       configured.LongRunningTimeout,
			AcknowledgementThreshold: configured.AcknowledgementThreshold,
		}
	}
	request.ToolPolicySettings = &settings
	request.ToolDefinitionBase = append([]messages.ToolDefinition(nil), opts.ToolDefinitionBase...)
	request.DynamicToolPolicy = opts.BrowserToolsInteractive
	return nil
}

func prepareSessionTurnImages(opts SessionRunOptions, plan *sessionRuntimePlan, request *sessionturn.Request) error {
	model := plan.model
	if model == "" {
		model = opts.Model
	}
	configuredModel, err := loadSessionImageModelMetadata(opts.ConfigDir, model)
	if err != nil {
		return err
	}
	provider := plan.provider
	if provider == "" {
		provider = effectiveSessionProvider(opts)
	}
	request.ImageCapabilityRequest = &sessionturn.ImageCapabilityRequest{
		Provider: provider, Model: model, ModelProvided: opts.ModelProvided,
		ModelCatalog: opts.ModelCatalog, ConfiguredModel: configuredModel,
	}
	if opts.sessionImageCapabilities != nil {
		capabilities := *opts.sessionImageCapabilities
		capabilities.SupportedInputMIMETypes = append([]string(nil), capabilities.SupportedInputMIMETypes...)
		request.ImageCapabilities = &capabilities
	}
	return nil
}

func prepareSessionRuntimeToolsAndAudio(ctx context.Context, opts SessionRunOptions, plan *sessionRuntimePlan) error {
	if err := prepareSessionTurnRuntime(ctx, opts, plan); err != nil {
		return err
	}
	if opts.sessionImageRequest != nil && opts.sessionImageRequest.Image != nil {
		plan.loop.awaitFirstTurn = opts.sessionImageRequest.Image.FirstTurn
	}
	return configureSessionAudioContract(opts, plan)
}

type sessionToolLifecycleMux struct {
	recording sessionToolLifecycleObserver
	progress  *sessionProgressObserver
	runtime   *sessionRuntimeObservationRecorder
}

func (m sessionToolLifecycleMux) observeToolCall(call messages.ToolCall) {
	if m.runtime != nil {
		m.runtime.observeToolCall(call)
	}
	if m.progress != nil {
		// The executor boundary carries the canonical call ID and can win the
		// publication race with the provider stream observer. Register it here
		// before cancellation can tear down the pending result.
		m.progress.observeProviderToolCallWithIDForResponse(call.ID, call.Name, "")
		if m.progress.durationController != nil {
			m.progress.durationController.BeginLocalToolExecution()
		}
	}
	if m.recording != nil {
		m.recording.observeToolCall(call)
	}
}

func (m sessionToolLifecycleMux) observeToolResult(call messages.ToolCall, response messages.ToolCallResponse, failed bool) {
	if m.runtime != nil {
		m.runtime.observeToolResult(call, response, failed)
	}
	if m.recording != nil {
		m.recording.observeToolResult(call, response, failed)
	}
	if m.progress != nil {
		if m.progress.durationController != nil {
			m.progress.durationController.EndLocalToolExecution()
		}
	}
}

func composeSessionToolLifecycleObserver(recording sessionToolLifecycleObserver, progress *sessionProgressObserver, runtime *sessionRuntimeObservationRecorder) sessionToolLifecycleObserver {
	if recording == nil && progress == nil && runtime == nil {
		return nil
	}
	return sessionToolLifecycleMux{recording: recording, progress: progress, runtime: runtime}
}
