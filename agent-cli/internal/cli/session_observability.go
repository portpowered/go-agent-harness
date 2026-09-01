package cli

import (
	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/observability"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	platformclock "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/platform/clock"
)

// NewSessionCommandWithRuntimeAndDeviceRegistryAndToolCapabilitiesAndRTCRuntimeAndObservability
// is the complete production composition constructor. Compatibility
// constructors normalize omitted observers to no-ops.
func NewSessionCommandWithRuntimeAndDeviceRegistryAndToolCapabilitiesAndRTCRuntimeAndObservability(
	askFlags *flags.AskFlags,
	globalFlags *flags.GlobalFlags,
	toolExecutorOverride messages.ToolExecutor,
	sessionInferencerOverride messages.SessionInferencer,
	clockSource platformclock.Source,
	runtimeObserver services.SessionRuntimeObserver,
	sessionToolCapabilities SessionToolCapabilitiesFactory,
	deviceRegistry audio.DeviceRegistry,
	rtcRuntimeFactory services.SessionRTCRuntimeFactory,
	metricSampler observability.MetricSampler,
	logger observability.Logger,
) *SessionCommand {
	return &SessionCommand{
		askFlags:                  askFlags,
		globalFlags:               globalFlags,
		toolExecutorOverride:      toolExecutorOverride,
		sessionInferencerOverride: sessionInferencerOverride,
		sessionToolCapabilities:   sessionToolCapabilities,
		rtcRuntimeFactory:         rtcRuntimeFactory,
		clockSource:               clockSource,
		runtimeObserver:           runtimeObserver,
		observability:             observability.NewDependencies(metricSampler, logger),
		deviceRegistry:            deviceRegistry,
	}
}
