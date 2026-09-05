//go:build wireinject
// +build wireinject

//go:generate wire

package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/agent"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/fleet"
	serviceRuntime "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentruntime"
	rtcontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentruntime/transports"
	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	servicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// provideSessionRTCRuntimeFactory installs the service-owned WebRTC runtime
// composition in the generated CLI graph. The component functions keep
// signaling, peer/data, and media implementations behind the service's
// provider-neutral contracts while leaving runtime side effects lazy until a
// session actually starts.
func provideSessionRTCRuntimeFactory(components rtcontract.SessionRTCComponents, metricSampler MetricSampler, logger Logger) rtcontract.SessionRTCRuntimeFactory {
	return servicewire.NewSessionRTCRuntimeFactory(components, metricSampler, logger)
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
) []bool {
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
	return []bool{relaxModelValidation}
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
	provideSessionBrowserCapabilityFactory,
	provideSessionDisplaySurface,
	provideSessionDependencies,
	servicewire.NewToolCapabilitiesServiceForWire,
	cli.NewProbeRunCommandWithDeviceService,
	cli.NewProbeGateCommand,
	cli.NewProbeReportCommand,
	cli.NewProbeFleetCommand,
	provideFleetEntryExecutors,
	provideAcceptanceCommands,
	provideSessionRTCRuntimeFactory,
	cli.NewSessionCommand,
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
	wire.Build(CliSet, provideModelValidation, agent.NewExecutor)
	return nil, nil
}
