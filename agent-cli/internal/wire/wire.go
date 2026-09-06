//go:build wireinject
// +build wireinject

//go:generate wire

package wire

import (
	"context"
	"fmt"

	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/fleet"
	hostServices "github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	serviceRuntime "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentruntime"
	rtcontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentruntime/transports"
	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	servicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli"
	looplogging "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeDevicesWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/wire"
	runtimeproviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	providerswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/wire"
	runtimeRecording "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	recordingwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording/wire"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	runtimeReplayWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"net/http"
)

// provideSessionRTCRuntimeFactory installs the service-owned WebRTC runtime
// composition in the generated CLI graph. The component functions keep
// signaling, peer/data, and media implementations behind the service's
// provider-neutral contracts while leaving runtime side effects lazy until a
// session actually starts.
func provideSessionRTCRuntimeFactory(components rtcontract.SessionRTCComponents, metricSampler MetricSampler, logger Logger) rtcontract.SessionRTCRuntimeFactory {
	return servicewire.NewSessionRTCRuntimeFactory(components, metricSampler, logger)
}

type modelValidation struct {
	relax bool
}

func provideModelValidation(
	relaxModelValidation bool,
	observer assemblyObserver,
	toolExecutor messages.ToolExecutor,
	transportDialer transport.Dialer,
	deviceRegistry DeviceRegistry,
	audioSource AudioSource,
	audioSink AudioSink,
	clockSource Clock,
	runtimeObserver SessionRuntimeObserver,
	metricSampler MetricSampler,
	logger Logger,
	inferencer messages.Inferencer,
	sessionInferencer messages.SessionInferencer,
) modelValidation {
	if observer != nil {
		observer(compositionValues{
			toolExecutor:      toolExecutor,
			transportDialer:   transportDialer,
			deviceRegistry:    deviceRegistry,
			audioSource:       audioSource,
			audioSink:         audioSink,
			clockSource:       clockSource,
			runtimeObserver:   runtimeObserver,
			metricSampler:     metricSampler,
			logger:            logger,
			inferencer:        inferencer,
			sessionInferencer: sessionInferencer,
		})
	}
	return modelValidation{relax: relaxModelValidation}
}

// FlagsSet provides global and command-specific CLI flags.
var FlagsSet = wire.NewSet(
	flags.NewGlobalFlags,
	flags.NewAskFlags,
	flags.NewChatFlags,
	flags.NewLoopFlags,
)

// provideFleetEntryExecutors keeps the production fleet command on its
// default transport dispatcher while leaving the executor injectable for
// hermetic command tests.
func provideFleetEntryExecutors() []fleet.EntryExecutor { return nil }

// provideAcceptanceCommands keeps the production router on its default
// acceptance runner while leaving the command injectable for route tests.
func provideAcceptanceCommands() []*cli.ProbeAcceptanceCommand { return nil }

func provideSessionBrowserCapabilityFactory() serviceTools.BrowserFactory {
	return func(browser config.BrowserConfig, configDir string) (serviceTools.BrowserCapability, error) {
		broker, err := cli.NewSessionBrowserBrokerWithConfigDir(browser, configDir)
		if err != nil {
			return serviceTools.BrowserCapability{}, err
		}
		return cli.NewSessionBrowserCapability(broker), nil
	}
}

// provideSessionDisplaySurface installs the platform display boundary in the
// application graph. The capability service receives it explicitly; private
// service construction never creates a host display surface implicitly.
func provideSessionDisplaySurface() cliTools.DisplaySurface {
	return cliTools.NewHostDisplaySurface()
}

// provideTextSessionService is the CLI composition edge for the reusable
// session owner. Global flags are translated by the host resolver and never
// enter the public runtime request.
func provideTextSessionService(
	globalFlags *flags.GlobalFlags,
	fileStoreFactory session.FileStoreFactory,
	toolExecutor messages.ToolExecutor,
	toolDefs []messages.ToolDefinition,
	inferencer messages.Inferencer,
	validation modelValidation,
	providerService runtimeproviders.Service,
	loopLogger looplogging.Logger,
) session.Service {
	return sessionwire.NewService(sessionwire.Dependencies{
		ToolExecutor:    toolExecutor,
		ToolDefinitions: toolDefs,
		Inferencer:      inferencer,
		RelaxValidation: validation.relax,
		Resolver:        hostServices.NewSessionResolverWithStoreFactory(globalFlags, fileStoreFactory),
		ProviderService: providerService,
		Logger:          loopLogger,
	})
}

// provideSessionLogger adapts the application's explicit logging port to the
// neutral loop logger contract. The runtime receives only this narrow port and
// never discovers the CLI logger or its filesystem policy.
func provideSessionLogger(logger Logger) looplogging.Logger {
	return sessionLoopLogger{sink: logger}
}

type sessionLoopLogger struct{ sink observability.Logger }

func (l sessionLoopLogger) emit(level, message string, fields []looplogging.Field) {
	if l.sink == nil {
		return
	}
	values := make(observability.Fields, len(fields))
	for _, field := range fields {
		values[field.Key] = fmt.Sprint(field.Value)
	}
	_ = l.sink.Log(context.Background(), observability.LogRecord{Level: level, Message: message, Fields: values})
}

func (l sessionLoopLogger) Debug(message string, fields ...looplogging.Field) {
	l.emit("debug", message, fields)
}

func (l sessionLoopLogger) Info(message string, fields ...looplogging.Field) {
	l.emit("info", message, fields)
}

func (l sessionLoopLogger) Warn(message string, fields ...looplogging.Field) {
	l.emit("warn", message, fields)
}

func (l sessionLoopLogger) Error(message string, fields ...looplogging.Field) {
	l.emit("error", message, fields)
}

func (l sessionLoopLogger) Fatal(message string, fields ...looplogging.Field) {
	l.emit("fatal", message, fields)
}

func (l sessionLoopLogger) Panic(message string, fields ...looplogging.Field) {
	l.emit("panic", message, fields)
}

// provideProviderService keeps the concrete provider graph at the host
// composition edge. The runtime receives only providers.Service and never
// discovers an HTTP client or credential source on its own.
func provideRecordingService(source Clock) runtimeRecording.Service {
	return recordingwire.NewService(source)
}

func provideProviderService(clockSource Clock, recordingService runtimeRecording.Service) (runtimeproviders.FullService, error) {
	timerSource, err := clock.RequireTimerSource(clockSource)
	if err != nil {
		return nil, fmt.Errorf("provider clock: %w", err)
	}
	return providerswire.NewService(providerswire.Dependencies{
		HTTPClient: http.DefaultClient,
		Recording:  recordingService,
		Clock:      timerSource,
	}), nil
}

func provideProviderServiceRole(service runtimeproviders.FullService) runtimeproviders.Service {
	return service
}

func provideProviderSessionServiceRole(service runtimeproviders.FullService) runtimeproviders.SessionService {
	return service
}

func provideProviderModelAdmission(service runtimeproviders.FullService) runtimeproviders.ModelAdmission {
	return service
}

func provideProviderModelCatalog(service runtimeproviders.FullService) runtimeproviders.ModelCatalog {
	return service
}

// provideRoomClock narrows the shared host clock to the scheduler role needed
// by room duration and media orchestration. Production clocks implement the
// complete contract; a timestamp-only test clock leaves rooms unavailable
// instead of silently changing their timing domain.
func provideRoomClock(source Clock) clock.Scheduler {
	if scheduler, ok := source.(clock.Scheduler); ok {
		return scheduler
	}
	return nil
}

// provideFileDeviceService keeps finite file conversion and pump ownership in
// the reusable runtime device service. The CLI opens paths into canonical
// audio ports, then injects those ports at invocation time.
func provideFileDeviceService(source Clock) cli.FileDeviceService {
	var scheduler clock.Scheduler
	if value, ok := source.(clock.Scheduler); ok {
		scheduler = value
	}
	return cli.FileDeviceService{Service: runtimeDevicesWire.NewFileService(), Scheduler: scheduler}
}

func provideLiveReplayService() runtimeReplay.Service {
	return runtimeReplayWire.NewService()
}

func provideSessionDependencies(clockSource Clock, resolver serviceTools.Service, runtimeFactory rtcontract.SessionRTCRuntimeFactory, inferencer messages.SessionInferencer, toolExecutor messages.ToolExecutor, deviceRegistry DeviceRegistry, observer SessionRuntimeObserver, metricSampler MetricSampler, logger Logger, runtime serviceRuntime.Runtime) servicewire.SessionDependencies {
	return servicewire.SessionDependencies{Clock: clockSource, ToolService: resolver, RuntimeFactory: runtimeFactory, SessionInferencer: inferencer, ToolExecutor: toolExecutor, DeviceRegistry: deviceRegistry, RuntimeObserver: observer, MetricSampler: metricSampler, Logger: logger, Runtime: runtime}
}

// CliSet provides CLI commands, router, and root.
var CliSet = wire.NewSet(
	FlagsSet,
	cli.NewRootCommand,
	cli.NewAskCommand,
	cli.NewChatCommand,
	cli.NewToolCommand,
	cli.NewInteractionCommand,
	cli.NewInteractionReplayCommand,
	cli.NewProbeCommand,
	servicewire.DeviceSet,
	servicewire.RoomSet,
	servicewire.SessionSet,
	servicewire.NewReplayClockFactory,
	servicewire.NewReplayService,
	servicewire.NewMetricsCollector,
	runtimeToolsWire.NewService,
	sessionwire.NewFileStoreFactory,
	provideRecordingService,
	provideSessionBrowserCapabilityFactory,
	provideSessionDisplaySurface,
	provideTextSessionService,
	provideSessionLogger,
	provideProviderService,
	provideProviderServiceRole,
	provideProviderSessionServiceRole,
	provideProviderModelAdmission,
	provideProviderModelCatalog,
	provideLiveCredentialVault,
	provideLiveCredentialReference,
	provideLiveService,
	provideFileDeviceService,
	provideLiveReplayService,
	provideRoomClock,
	provideSessionDependencies,
	servicewire.NewToolCapabilitiesServiceForWire,
	cli.NewProbeRunCommandWithDeviceService,
	cli.NewProbeGateCommand,
	cli.NewProbeReportCommand,
	cli.NewProbeFleetCommand,
	provideFleetEntryExecutors,
	provideAcceptanceCommands,
	provideSessionRTCRuntimeFactory,
	cli.NewSessionToolCapabilitiesFactoryFromService,
	cli.NewSessionCommandWithLive,
	servicewire.SelfPlaySet,
	cli.NewSessionReplayCommand,
	cli.NewRoomRunCommand,
	cli.NewSessionShowCommand,
	cli.NewSessionListCommand,
	cli.NewSessionDeleteCommand,
	cli.NewConfigCommand,
	cli.NewConfigAddLocalCommand,
	cli.NewRouter,
	cli.NewAgentCLI,
)

// assembleAgentCLI is the generated implementation shared by production and
// mock composition. Its parameters are explicit so the generated graph cannot
// hide a dependency behind a bag or locator.
func assembleAgentCLI(
	toolExecutor messages.ToolExecutor,
	transportDialer transport.Dialer,
	deviceRegistry DeviceRegistry,
	audioSource AudioSource,
	audioSink AudioSink,
	clockSource Clock,
	runtimeObserver SessionRuntimeObserver,
	metricSampler MetricSampler,
	logger Logger,
	toolDefs []messages.ToolDefinition,
	inferencer messages.Inferencer,
	sessionInferencer messages.SessionInferencer,
	rtcComponents rtcontract.SessionRTCComponents,
	relaxModelValidation bool,
	observer assemblyObserver,
) (*cli.AgentCLI, error) {
	wire.Build(CliSet, provideModelValidation)
	return nil, nil
}
