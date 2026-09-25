package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli"
	agentwire "github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audioiowire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
	runtimedeviceswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/wire"
	recordingwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording/wire"
	replaywire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	sessiontracewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
	sessionclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	"github.com/spf13/cobra"
)

func newTestSessionRootCommand(t testing.TB, swaps ...agentwire.PortSwap) *cobra.Command {
	t.Helper()
	var agentCLI *cli.AgentCLI
	var err error
	if len(swaps) == 0 {
		agentCLI, err = agentwire.InitializeAgentCLI()
	} else {
		agentCLI, err = agentwire.InitializeMockAgentCLIWithPorts(swaps...)
	}
	if err != nil {
		t.Fatalf("initialize composed agent CLI: %v", err)
	}
	return agentCLI.Generate()
}

// newTestLiveSessionCommand composes the session command with the same live,
// replay, recording, and device services as the application graph. The
// injected inferencer replaces provider construction like the application's
// session-inferencer port.
func newTestLiveSessionCommand(globalFlags *flags.GlobalFlags, registry devicegw.DeviceRegistry, inferencer messages.SessionInferencer, capabilities cli.SessionToolCapabilitiesFactory) *cli.SessionCommand {
	clockSource := sessionclock.Real{}
	audioService := audioiowire.NewService()
	recordingService := recordingwire.NewService(clockSource)
	liveService := sessionwire.NewLiveService(sessionwire.LiveDependencies{
		InferencerFactory: func(_ context.Context, request session.LiveRequest) (messages.SessionInferencer, error) {
			path := strings.TrimSpace(request.Replay.OutputCapturePath)
			if path == "" || !request.Replay.InjectedCaptureAllowed {
				return inferencer, nil
			}
			return recordingService.TrackInjectedSession(inferencer, path)
		},
		Clock:     clockSource.Now,
		Scheduler: clockSource,
	})
	return cli.NewSessionCommandWithLive(
		flags.NewAskFlags(), globalFlags, nil,
		liveService, replaywire.NewService(), runtimedeviceswire.NewService(registry, audioService),
		cli.FileDeviceService{Service: runtimedeviceswire.NewFileService(audioService), Scheduler: clockSource, TraceService: sessiontracewire.NewService()},
		capabilities, nil, nil, recordingService, nil,
	)
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
		ReplayService:   replaywire.NewService(),
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
