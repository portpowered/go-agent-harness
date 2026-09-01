//go:build wireinject
// +build wireinject

//go:generate wire

package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/agent"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/fleet"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// provideSessionRTCRuntimeFactory installs the service-owned WebRTC runtime
// composition in the generated CLI graph. The component functions keep
// signaling, peer/data, and media implementations behind the service's
// provider-neutral contracts while leaving runtime side effects lazy until a
// session actually starts.
func provideSessionRTCRuntimeFactory(components services.SessionRTCComponents, metricSampler MetricSampler, logger Logger) services.SessionRTCRuntimeFactory {
	return services.NewSessionRTCRuntimeFactoryWithObservability(components, metricSampler, logger)
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

// provideProbeDeviceRegistries adapts the shared application registry to the
// variadic probe constructor. Keeping the adapter in the source graph makes
// the generated call retain the same device implementation as the session
// command and device routes.
func provideProbeDeviceRegistries(registry DeviceRegistry) []audio.DeviceRegistry {
	return []audio.DeviceRegistry{registry}
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
	cli.NewProbeRunCommand,
	cli.NewProbeGateCommand,
	cli.NewProbeReportCommand,
	cli.NewProbeFleetCommand,
	provideFleetEntryExecutors,
	provideAcceptanceCommands,
	provideProbeDeviceRegistries,
	provideSessionToolCapabilitiesFactory,
	provideSessionRTCRuntimeFactory,
	cli.NewSessionCommandWithRuntimeAndDeviceRegistryAndToolCapabilitiesAndRTCRuntimeAndObservability,
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
	rtcComponents services.SessionRTCComponents,
	relaxModelValidation bool,
	observer assemblyObserver,
) (*cli.AgentCLI, error) {
	wire.Build(CliSet, provideModelValidation, agent.NewExecutor)
	return nil, nil
}
