package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"

	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	runtimeSessionWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiondiagnostics"
	sessiondiagnosticswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiondiagnostics/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	sessionturnwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/wire"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

func newSessionTurnService() sessionturn.Service {
	return sessionturnwire.NewService(sessionturnwire.Dependencies{
		PolicyFactory:      runtimeToolsWire.NewInteractiveToolPolicy(),
		ImageStaging:       runtimeToolsWire.NewImageStaging(),
		InstructionService: runtimeSessionWire.NewInstructionService(),
		LifecycleFactory: func() sessiondiagnostics.Service {
			return sessiondiagnosticswire.NewService(sessiondiagnostics.Options{})
		},
	})
}

// prepareSessionTurnSeed transfers seed substitution and serialized output
// ownership to the session-turn service. The planner only replaces the
// provider edge and prompt value; it does not retain seed state.
func prepareSessionTurnSeed(ctx context.Context, plan *sessionRuntimePlan, seed sessionturn.Seed) (sessionturn.Runtime, error) {
	if plan == nil || plan.inferencer == nil {
		return nil, nil
	}
	runtime, err := newSessionTurnService().Prepare(ctx, sessionturn.Request{
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
		toolLifecycle := composeSessionToolLifecycleObserver(opts.toolLifecycleObserver, opts.observer, opts.runtime)
		runtime, err := newSessionTurnService().Prepare(context.Background(), sessionturn.Request{
			ToolExecutor:          opts.ToolExecutor,
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
		})
		if err != nil {
			return opts.ToolExecutor
		}
		return runtime.ToolExecutor()
	}
	return opts.turnRuntime.ToolExecutor()
}

func prepareSessionRecordingTurnRuntime(ctx context.Context, plan *sessionRuntimePlan, seed SessionTextSeed) (sessionturn.Runtime, error) {
	turnRuntime := plan.turnRuntime
	if !seed.Present || (turnRuntime != nil && turnRuntime.WirePrompt() != "") {
		return turnRuntime, nil
	}
	return prepareSessionTurnSeed(ctx, plan, seed)
}

func prepareSessionRecordingOutputs(plan *sessionRuntimePlan, out io.Writer, audioOutPath string, seed SessionTextSeed, turnRuntime sessionturn.Runtime) (*sessionAudioOutput, *sessionAudioOutputInferencer, sessionturn.Output, error) {
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
	wirePrompt := ""
	if turnRuntime != nil {
		wirePrompt = turnRuntime.WirePrompt()
	}
	audioWrapper := newSessionAudioOutputInferencer(plan.inferencer, audioOutput, wirePrompt, seed.Value)
	plan.inferencer = audioWrapper
	return audioOutput, audioWrapper, nil, nil
}

func prepareSessionTurnRuntime(ctx context.Context, opts SessionRunOptions, plan *sessionRuntimePlan) error {
	toolLifecycle := composeSessionToolLifecycleObserver(plan.loop.toolLifecycleObserver, plan.loop.observer, plan.runtime)
	imageCapabilities, err := resolveSessionTurnImageCapabilities(opts, plan)
	if err != nil {
		return err
	}
	request := sessionturn.Request{
		SessionInferencer: plan.inferencer,
		ToolExecutor:      plan.loop.ToolExecutor,
		ImageCapabilities: imageCapabilities,
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
	}
	if opts.ReplayPath == "" || opts.SessionInferencer != nil {
		request.ToolDefinitions = append([]messages.ToolDefinition(nil), plan.loop.ToolDefinitions...)
	}
	if opts.InteractiveToolPolicy != nil {
		request.InteractiveToolPolicy = opts.InteractiveToolPolicy
	} else {
		settings, err := sessionInteractiveToolSettings(opts)
		if err != nil {
			return err
		}
		request.ToolPolicyRequest = &runtimeTools.InteractiveToolPolicyRequest{
			Settings: runtimeTools.InteractiveToolPolicySettings{
				FastReadTimeout:          settings.FastReadTimeout,
				LongRunningTimeout:       settings.LongRunningTimeout,
				AcknowledgementThreshold: settings.AcknowledgementThreshold,
			},
			Definitions:     plan.loop.ToolDefinitions,
			BaseDefinitions: opts.ToolDefinitionBase,
			ExplicitLongRunningNames: []string{
				"webmcp_select_tab", "webmcp_invoke", "webmcp_list_tools", "webmcp_list_tabs",
				"webmcp_get_context", "webmcp_cancel", "webmcp_list_cast_devices", "webmcp_cast_tab",
				"webmcp_stop_casting",
			},
			DynamicLongRunning: opts.BrowserToolsInteractive,
		}
	}
	turnRuntime, err := newSessionTurnService().Prepare(ctx, request)
	if err != nil {
		return fmt.Errorf("prepare session-turn runtime: %w", err)
	}
	plan.turnRuntime = turnRuntime
	plan.loop.turnRuntime = turnRuntime
	plan.loop.turnBrowser = sessionTurnBrowserRequest(opts.BrowserWatch, opts.RefreshToolDefinitions)
	plan.loop.ToolExecutor = turnRuntime.ToolExecutor()
	plan.inferencer = turnRuntime.Inferencer()
	if opts.SessionInferencer != nil && (opts.sessionInstructions != "" || len(opts.ToolDefinitions) != 0) {
		plan.loop.AdvertiseToolDefinitions = false
	}
	return nil
}

func resolveSessionTurnImageCapabilities(opts SessionRunOptions, plan *sessionRuntimePlan) (*sessionturn.ImageCapabilities, error) {
	if !sessionHasTool(opts.ToolDefinitions, runtimeTools.ReadImageToolID) {
		return nil, nil
	}
	if opts.sessionImageCapabilities != nil {
		capabilities := *opts.sessionImageCapabilities
		capabilities.SupportedInputMIMETypes = append([]string(nil), capabilities.SupportedInputMIMETypes...)
		return &capabilities, nil
	}
	provider := plan.provider
	if provider == "" {
		provider = effectiveSessionProvider(opts)
	}
	model := plan.model
	if model == "" {
		var err error
		model, err = resolveSessionImageModel(opts)
		if err != nil {
			return nil, err
		}
	}
	configuredModel, err := loadSessionImageModelMetadata(opts.ConfigDir, model)
	if err != nil {
		return nil, err
	}
	resolved, err := newSessionTurnService().ResolveImageCapabilities(sessionturn.ImageCapabilityRequest{
		Provider:        provider,
		Model:           model,
		ModelProvided:   opts.ModelProvided,
		ModelCatalog:    opts.ModelCatalog,
		ConfiguredModel: configuredModel,
	})
	if err == nil {
		return &resolved, nil
	}
	var capabilityErr *sessionturn.ImageCapabilityError
	if !errors.As(err, &capabilityErr) {
		return nil, err
	}
	return &sessionturn.ImageCapabilities{Model: capabilityErr.Model}, nil
}

func prepareSessionRuntimeToolsAndAudio(ctx context.Context, opts SessionRunOptions, plan *sessionRuntimePlan) error {
	if err := prepareSessionTurnRuntime(ctx, opts, plan); err != nil {
		return err
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
		m.progress.beginLocalToolExecution()
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
		m.progress.endLocalToolExecution()
	}
}

func composeSessionToolLifecycleObserver(recording sessionToolLifecycleObserver, progress *sessionProgressObserver, runtime *sessionRuntimeObservationRecorder) sessionToolLifecycleObserver {
	if recording == nil && progress == nil && runtime == nil {
		return nil
	}
	return sessionToolLifecycleMux{recording: recording, progress: progress, runtime: runtime}
}
