package integration

import (
	"context"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	sessionservicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

// Tests compose the same runtime and use-case services as the application graph.
func newTestSessionService(deps sessionservicewire.SessionDependencies) agentsession.SessionService {
	deps.Runtime = sessionservicewire.NewSessionRuntime(deps.Clock, deps.ToolService, sessionservicewire.NewSessionRuntimeFactory(), deps.RuntimeFactory, deps.SessionInferencer, deps.ToolExecutor, deps.DeviceRegistry, deps.RuntimeObserver, deps.MetricSampler, deps.Logger)
	return sessionservicewire.NewSessionService(deps)
}

func newChatSessionID(t interface {
	Helper()
	Fatalf(string, ...any)
}, service session.Service, cfg session.Request) string {
	t.Helper()
	id, err := service.NewSessionID(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewChatSessionID: %v", err)
	}
	return id
}

// newPublicTextSessionService composes the reusable session service through
// its public wire package. The CLI resolver and its session-store adapter stay
// on the host side of this test package, just as they do in the application
// graph; no internal runtime executor is exposed to integration tests.
func newPublicTextSessionService(globalFlags *flags.GlobalFlags, toolExecutor messages.ToolExecutor, inferencer messages.Inferencer, toolDefinitions []messages.ToolDefinition) session.Service {
	resolver := services.NewSessionResolverWithStoreFactory(globalFlags, sessionwire.NewFileStoreFactory())
	return sessionwire.NewService(sessionwire.Dependencies{
		ToolExecutor:    toolExecutor,
		ToolDefinitions: toolDefinitions,
		Inferencer:      inferencer,
		RelaxValidation: true,
		Resolver:        resolver,
	})
}

// newPublicIterativeSessionService composes the same public service for loop
// integration tests, including the request-scoped dispatch tool supplied by
// the reusable tools service.
func newPublicIterativeSessionService(tmpDir string, inferencer messages.Inferencer) (session.Service, *flags.GlobalFlags) {
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = tmpDir
	globalFlags.WorkDirPath = tmpDir
	return newPublicIterativeServiceWithFlags(globalFlags, inferencer), globalFlags
}

func newPublicIterativeServiceWithFlags(globalFlags *flags.GlobalFlags, inferencer messages.Inferencer) session.Service {
	capability, err := runtimeToolsWire.NewService().Resolve(context.Background(), runtimeTools.Request{
		WorkDir:        globalFlags.WorkDir(),
		Inferencer:     inferencer,
		UseDefaultTool: true,
	})
	if err != nil {
		panic(err)
	}
	return newPublicTextSessionService(globalFlags, capability.Executor, inferencer, capability.Definitions)
}

func newPublicIterativeRequest(globalFlags *flags.GlobalFlags) *session.Request {
	askFlags := flags.NewAskFlags()
	return services.BuildAgentConfigFromFlags(globalFlags, askFlags, nil, "")
}

func publicTraceStore(t interface {
	Helper()
	Fatalf(string, ...any)
}, globalFlags *flags.GlobalFlags) session.TraceStore {
	store, err := services.NewSessionStoreWithFactory(globalFlags, sessionwire.NewFileStoreFactory())
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	return store
}
