package agentruntime

import (
	"context"
	"fmt"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	sessionturnwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/wire"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

// prepareSessionTurnSeed transfers seed substitution and serialized output
// ownership to the session-turn service. The planner only replaces the
// provider edge and prompt value; it does not retain seed state.
func prepareSessionTurnSeed(plan *sessionRuntimePlan, seed sessionturn.Seed) (sessionturn.Runtime, error) {
	if plan == nil || plan.inferencer == nil {
		return nil, nil
	}
	runtime, err := sessionturnwire.NewDefaultService().Prepare(context.Background(), sessionturn.Request{
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
		runtime, err := sessionturnwire.NewDefaultService().Prepare(context.Background(), sessionturn.Request{
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

func prepareSessionRecordingTurnRuntime(plan *sessionRuntimePlan, seed SessionTextSeed) (sessionturn.Runtime, error) {
	turnRuntime := plan.turnRuntime
	if !seed.Present || (turnRuntime != nil && turnRuntime.WirePrompt() != "") {
		return turnRuntime, nil
	}
	return prepareSessionTurnSeed(plan, seed)
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

func prepareSessionTurnRuntime(opts SessionRunOptions, plan *sessionRuntimePlan, interactivePolicy runtimeTools.InteractiveToolPolicy) error {
	toolLifecycle := composeSessionToolLifecycleObserver(plan.loop.toolLifecycleObserver, plan.loop.observer, plan.runtime)
	runtimePolicy, err := runtimeToolsWire.NewInteractiveToolPolicy().Resolve(runtimeTools.InteractiveToolPolicyRequest{
		Settings: runtimeTools.InteractiveToolPolicySettings{
			FastReadTimeout:          interactivePolicy.Settings().FastReadTimeout,
			LongRunningTimeout:       interactivePolicy.Settings().LongRunningTimeout,
			AcknowledgementThreshold: interactivePolicy.Settings().AcknowledgementThreshold,
		},
		Definitions:        plan.loop.ToolDefinitions,
		BaseDefinitions:    opts.ToolDefinitionBase,
		DynamicLongRunning: opts.BrowserToolsInteractive,
	})
	if err != nil {
		return fmt.Errorf("prepare session-turn policy: %w", err)
	}
	turnRuntime, err := sessionturnwire.NewDefaultService().Prepare(context.Background(), sessionturn.Request{
		SessionInferencer:     plan.inferencer,
		ToolExecutor:          plan.loop.ToolExecutor,
		ToolDefinitions:       nil,
		InteractiveToolPolicy: runtimePolicy,
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
			recordSessionToolDiagnostic(opts.ToolDiagnostics, opts.ToolExecutor, call, err)
		},
	})
	if err != nil {
		return fmt.Errorf("prepare session-turn runtime: %w", err)
	}
	plan.turnRuntime = turnRuntime
	plan.loop.turnRuntime = turnRuntime
	plan.loop.turnBrowser = sessionTurnBrowserRequest(opts.BrowserWatch, opts.RefreshToolDefinitions)
	plan.loop.ToolExecutor = turnRuntime.ToolExecutor()
	plan.inferencer = turnRuntime.Inferencer()
	return nil
}

func prepareSessionRuntimeToolsAndAudio(opts SessionRunOptions, plan *sessionRuntimePlan, interactivePolicy runtimeTools.InteractiveToolPolicy) error {
	if err := prepareSessionTurnRuntime(opts, plan, interactivePolicy); err != nil {
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
