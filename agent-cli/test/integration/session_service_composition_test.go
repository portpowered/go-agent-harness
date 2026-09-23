package integration

import (
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services/servicetest"
	sessionservicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audioiowire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
	runtimedeviceswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/wire"
	runtimeProviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	providerswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/wire"
	recordingwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording/wire"
	replaywire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	sessiondurationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/wire"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
	sessionclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	runtimeModels "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

// Tests compose the same runtime and use-case services as the application graph.
func newTestSessionService(deps sessionservicewire.SessionDependencies) agentsession.SessionService {
	deps.Runtime = sessionservicewire.NewSessionRuntime(audioiowire.NewService(), deps.Clock, deps.ToolService, sessionservicewire.NewSessionRuntimeFactory(), deps.RuntimeFactory, deps.SessionInferencer, deps.ToolExecutor, runtimedeviceswire.NewService(deps.DeviceRegistry, audioiowire.NewService()), deps.RuntimeObserver, deps.MetricSampler, deps.Logger, providerswire.NewModelCatalog(), recordingwire.NewService(deps.Clock), recordingwire.NewProviderCaptureService(deps.Clock), replaywire.NewService())
	return sessionservicewire.NewSessionService(deps)
}

func withTestSessionRuntimeServices(options servicetest.SessionRunOptions) servicetest.SessionRunOptions {
	options.AudioService = audioiowire.NewService()
	options.RecordingService = recordingwire.NewService(sessionclock.Real{})
	options.ProviderCaptureService = recordingwire.NewProviderCaptureService(sessionclock.Real{})
	options.ReplayService = replaywire.NewService()
	if options.ModelCatalog == nil {
		options.ModelCatalog = providerswire.NewModelCatalog()
	}
	return options
}

// newTestLiveSessionCommand drives the same service-owned continuous-session
// path as the application graph. Replay fixtures therefore exercise the CLI
// boundary instead of the retired text-session route.
func newTestLiveSessionCommand(t *testing.T, globalFlags *flags.GlobalFlags) *cli.SessionCommand {
	t.Helper()
	if globalFlags == nil {
		globalFlags = flags.NewGlobalFlags()
	}
	if globalFlags.ConfigDirPath == "" {
		globalFlags.ConfigDirPath = t.TempDir()
	}
	if globalFlags.WorkDirPath == "" {
		globalFlags.WorkDirPath = t.TempDir()
	}
	replayService := replaywire.NewService()
	recordingService := recordingwire.NewService(sessionclock.Real{})
	providerCaptureService := recordingwire.NewProviderCaptureService(sessionclock.Real{})
	providerService := providerswire.NewService(providerswire.Dependencies{
		Replay: replayService, Recording: recordingService, ProviderCapture: providerCaptureService,
	})
	liveService := sessionwire.NewLiveService(sessionwire.LiveDependencies{
		InferencerFactory: func(ctx context.Context, request session.LiveRequest) (messages.SessionInferencer, error) {
			var tools []messages.ToolDefinition
			if request.Capabilities != nil {
				tools = append(tools, request.Capabilities.Definitions...)
			}
			return providerService.BuildSession(ctx, runtimeProviders.SessionConfig{
				Provider: request.Provider, Model: request.Model, APIKey: "replay", BaseURL: request.BaseURL,
				RealtimeURL: request.RealtimeURL, Instructions: request.Instructions, Voice: request.Voice,
				ReasoningEffort: request.ReasoningEffort, InputAudioFormat: runtimeModels.AudioFormat(request.InputAudioFormat),
				OutputAudioFormat:     runtimeModels.AudioFormat(request.OutputAudioFormat),
				InputAudioSampleRate:  runtimeModels.SampleRate(request.InputAudioSampleRate),
				OutputAudioSampleRate: runtimeModels.SampleRate(request.OutputAudioSampleRate),
				Tools:                 tools, ClientOwnsAudioTurnBoundaries: request.ClientOwnsAudioTurnBoundaries,
				ReplayPath: request.Replay.InputCapturePath, ReplayTiming: "fast",
				SessionMessageReplay: request.Replay.Kind == session.LiveReplayKindTurn,
				RecordPath:           request.Replay.OutputCapturePath,
			})
		},
		Clock: func() time.Time { return time.Now() }, Scheduler: sessionclock.Real{},
		DurationService: sessiondurationwire.NewService(),
	})
	audioService := audioiowire.NewService()
	return cli.NewSessionCommandWithLive(
		flags.NewAskFlags(), globalFlags,
		newTestSessionService(sessionservicewire.SessionDependencies{Clock: sessionclock.Real{}}), nil,
		liveService, replayService,
		runtimedeviceswire.NewService(nil, audioService),
		cli.FileDeviceService{Service: runtimedeviceswire.NewFileService(audioService), Scheduler: sessionclock.Real{}},
		cli.NewSessionToolCapabilitiesFactory(nil, nil), nil, sessionwire.NewFileStoreFactory(), recordingService,
		providerswire.NewModelAdmission(providerswire.NewModelCatalog()),
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
