package agentruntime

import (
	"context"
	"io"
	"log"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/input"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	sessiontransport "github.com/portpowered/go-agent-harness/agent-cli/internal/transport"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	runtimeSessionWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	sessionturnwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/wire"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

// This file only forwards runtime plan values into sessionturn requests; the
// turn, tool, publication, image, instruction, and seed decisions are owned by
// the service.

type (
	InteractiveToolPolicy                    = runtimeTools.InteractiveToolPolicy
	SessionImageCapabilities                 = sessionturn.ImageCapabilities
	SessionImageMessageSender                = sessionturn.CompleteMessageSender
	SessionImageMessageSenderWithoutResponse = sessionturn.CompleteMessageWithoutResponseSender
	sessionToolLifecycleObserver             = sessionturn.ToolLifecycleObserver
)

const (
	InteractiveToolClassFastRead           = runtimeTools.InteractiveToolClassFastRead
	InteractiveToolClassBoundedLongRunning = runtimeTools.InteractiveToolClassBoundedLongRunning
)

func newTurnService() sessionturn.Service {
	return sessionturnwire.NewService(runtimeSessionWire.NewInstructionService(), runtimeToolsWire.NewInteractiveToolPolicy(), runtimeToolsWire.NewImageStaging())
}

// NewInteractiveToolPolicyForSession resolves the session policy from one
// loaded configuration snapshot and the advertised definition surface.
func NewInteractiveToolPolicyForSession(settings config.InteractiveToolConfig, definitions, base []messages.ToolDefinition, browserDynamic bool) (InteractiveToolPolicy, error) {
	return newTurnService().ResolveInteractivePolicy(sessionturn.InteractivePolicyRequest{
		Settings: sessiontransport.SessionTurnInteractiveSettings(settings), Definitions: definitions, BaseDefinitions: base, DynamicLongRunning: browserDynamic,
	})
}

func newSessionLoopToolExecutor(opts sessionLoopOptions) messages.ToolExecutor {
	service := newTurnService()
	policy := opts.InteractiveToolPolicy
	if policy == nil {
		if defaults, err := service.ResolveInteractivePolicy(sessionturn.InteractivePolicyRequest{}); err == nil {
			policy = defaults
		}
	}
	lifecycle := sessionturn.ToolLifecycle{Recording: opts.toolLifecycleObserver}
	if opts.observer != nil {
		lifecycle.Progress = opts.observer
	}
	if opts.runtime != nil {
		lifecycle.Runtime = opts.runtime
	}
	return service.NewToolExecutor(sessionturn.ToolExecutorRequest{
		Inner: opts.ToolExecutor, Timeout: opts.ToolExecutionTimeout, Policy: policy, Lifecycle: lifecycle,
		Cancellation: opts.cancellationIntent, Diagnostics: opts.toolDiagnostics, Presentation: sessiontransport.SessionTurnToolPresentation(),
	})
}

// startSessionDynamicToolPublisher publishes refreshed surfaces as loop
// session events.
func startSessionDynamicToolPublisher(ctx context.Context, loop *agentloop.AgentLoop, opts sessionLoopOptions) (sessionturn.Publication, <-chan error) {
	publication := newTurnService().StartPublication(ctx, sessionturn.PublicationRequest{
		StaticStableDefinitions: opts.ToolDefinitionBase, InitialDefinitions: opts.ToolDefinitions, TimerFactory: opts.PublicationTimerFactory,
		Watch: sessiontransport.SessionTurnBrowserWatch(opts.BrowserWatch), Refresh: opts.RefreshToolDefinitions,
		Publish: func(ctx context.Context, definitions []messages.ToolDefinition) error {
			return loop.SendSessionEvent(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionUpdate, Value: messages.NewSessionUpdateValue(&messages.SessionUpdateConfig{Tools: definitions})})
		},
	})
	return publication, publication.Errors()
}

func completeMessageCapabilities(session messages.Session) (complete, withoutResponse bool) {
	return newTurnService().CompleteMessageSupport(session)
}

func newSessionInstructionsInferencer(inner messages.SessionInferencer, instructions string, definitions []messages.ToolDefinition) messages.SessionInferencer {
	return newTurnService().NewInstructionsInferencer(inner, instructions, definitions)
}

func composeSessionInstructions(opts SessionRunOptions, instructions string) string {
	return newTurnService().ComposeInstructions(runtimeSession.InstructionComposition{
		Instructions: instructions, ToolDefinitions: append([]messages.ToolDefinition(nil), opts.ToolDefinitions...),
		BrowserCapabilityState: runtimeSession.BrowserCapabilityState(string(opts.BrowserCapabilityState)),
		BrowserToolsEnabled:    opts.BrowserToolsEnabled, PageSightToolID: cliTools.PageSightToolID,
	})
}

func resolveSessionInstructions(ctx context.Context, opts SessionRunOptions, systemPrompt string) (string, error) {
	request := sessionturn.InstructionsRequest{
		Prompt: systemPrompt, WorkDir: opts.WorkDir, ConfigDir: opts.ConfigDir,
		ResolveWorkspace: sessiontransport.SessionTurnWorkspaceResolver(opts.AllowPaths), Loader: sessiontransport.SessionTurnInstructionLoader(opts.ConfigDir),
	}
	if opts.FilesystemPolicy != nil {
		request.Scope = &sessionturn.FilesystemScope{Description: opts.FilesystemPolicy.ScopeDescription()}
	}
	return newTurnService().ResolveInstructions(ctx, request)
}

func resolveSessionImageCapabilities(opts SessionRunOptions) (SessionImageCapabilities, error) {
	request := sessionturn.ImageCapabilityRequest{
		Provider: effectiveSessionProvider(opts), Model: opts.Model, ModelProvided: opts.ModelProvided,
		DefaultModel: func() (string, error) {
			resolved, err := resolveOpenAIRealtimeSessionConfig(opts)
			return resolved.Model, err
		},
		RealtimeImageInput: func(model string) (bool, bool) {
			realtime, known := lookupOpenAIRealtimeModel(opts, model)
			return realtime.SupportsImageInput, known
		},
		ConfiguredModel: sessiontransport.SessionTurnConfiguredImageModel(opts.ConfigDir),
	}
	if opts.ReplayPath != "" {
		request.ReplayModel = openAIRealtimeModel
	}
	return newTurnService().ResolveImageCapabilities(request)
}

func prepareSessionImageParts(paths []string, capabilities SessionImageCapabilities) ([]messages.ImagePart, error) {
	return newTurnService().PrepareImageParts(sessionturn.ImagePartsRequest{Paths: paths, Capabilities: capabilities, Load: input.LoadContentPart})
}

// bindSessionImageToolExecutor binds read_image to the capability snapshot
// resolved once for this session, preferring the plan's provider and model.
func bindSessionImageToolExecutor(opts SessionRunOptions, plan sessionRuntimePlan) messages.ToolExecutor {
	return newTurnService().BindImageTools(sessionturn.ImageToolBinding{
		Executor: opts.ToolExecutor, Definitions: opts.ToolDefinitions, Load: input.LoadContentPart,
		Resolve: func() (SessionImageCapabilities, error) {
			if opts.sessionImageCapabilities != nil {
				return *opts.sessionImageCapabilities, nil
			}
			if plan.provider != "" {
				opts.Provider = plan.provider
			}
			if plan.model != "" {
				opts.Model, opts.ModelProvided = plan.model, true
			}
			return resolveSessionImageCapabilities(opts)
		},
	})
}

func prepareSessionImageToolAccess(ctx context.Context, opts SessionRunOptions, sourcePaths []string, parts []messages.ImagePart) (SessionRunOptions, func(), error) {
	staged, err := newTurnService().StageImageTools(ctx, sessionturn.ImageStagingRequest{
		SourcePaths: sourcePaths, Parts: parts, StagingRoot: sessiontransport.SessionTurnStagingRoot(opts.ConfigDir),
		ToolDefinitions: opts.ToolDefinitions, RefreshToolDefinitions: opts.RefreshToolDefinitions,
	})
	if err != nil {
		return opts, func() {}, err
	}
	opts.ToolDefinitions, opts.RefreshToolDefinitions = staged.ToolDefinitions, staged.RefreshToolDefinitions
	return opts, func() {
		if err := staged.Cleanup(); err != nil {
			log.Printf("stage session images: cleanup: %v", err)
		}
	}, nil
}

// runSessionPlanWithSeed runs a planned session through the service-owned
// text-seed placement.
func runSessionPlanWithSeed(ctx context.Context, out io.Writer, plan sessionRuntimePlan, maxDuration time.Duration, seed SessionTextSeed, wirePrompt string) error {
	return newTurnService().RunSeeded(sessionturn.SeededRunRequest{
		Seed: seed, WirePrompt: wirePrompt, Bounded: maxDuration > 0, Output: out, Inferencer: plan.inferencer,
		SetPrompt:     func(prompt string) { plan.loop.Prompt = prompt },
		SetInferencer: func(inferencer messages.SessionInferencer) { plan.inferencer = inferencer },
		Run: func(out io.Writer, wrap func(messages.SessionInferencer) messages.SessionInferencer) error {
			return runSessionPlanWithDuration(ctx, out, plan, maxDuration, nil, wrap)
		},
	})
}
