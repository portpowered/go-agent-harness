// Package wire contains service-level providers. Keeping the private device
// implementation behind this package lets the application graph inject the
// public contract without importing services/internal from a transport.
package wire

import (
	"github.com/google/wire"
	serviceRuntime "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentruntime"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	serviceDevices "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"
	agentruntime "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime"
	sessionservice "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentsession"
	devicesservice "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/devices"
	toolsservice "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/tools"
	serviceRooms "github.com/portpowered/go-agent-harness/agent-cli/internal/services/rooms"
	serviceSelfPlay "github.com/portpowered/go-agent-harness/agent-cli/internal/services/selfplay"
	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

// NewDeviceService is the Wire-visible constructor for the public device
// contract. Its implementation package remains private to services.
func NewDeviceService(registry devicegw.DeviceRegistry) serviceDevices.DeviceService {
	return devicesservice.New(registry)
}

// NewDeviceProbeService keeps live probe execution behind the same private
// registry owner as enumeration and selection.
func NewDeviceProbeService(registry devicegw.DeviceRegistry) serviceDevices.DeviceProbeService {
	return devicesservice.New(registry)
}

// NewRoomService keeps room orchestration and its device registry behind the
// public room contract. The application graph supplies the registry once.
func NewRoomService(registry devicegw.DeviceRegistry, clockSource clock.Source, factory agentruntime.SessionRuntimeFactory) serviceRooms.Service {
	return agentruntime.NewRoomService(registry, clockSource, factory)
}

var RoomSet = wire.NewSet(NewRoomService)

// NewToolCapabilitiesService keeps session tool composition in the private
// service implementation while allowing the CLI to provide its browser seam.
func NewToolCapabilitiesService(staticExecutor messages.ToolExecutor, browserFactory serviceTools.BrowserFactory, displaySurface cliTools.DisplaySurface, displayProbe cliTools.DisplayCapabilityProbe) serviceTools.Service {
	return toolsservice.New(staticExecutor, browserFactory, displaySurface, displayProbe)
}

func NewToolCapabilitiesServiceForWire(staticExecutor messages.ToolExecutor, browserFactory serviceTools.BrowserFactory, displaySurface cliTools.DisplaySurface) serviceTools.Service {
	return toolsservice.New(staticExecutor, browserFactory, displaySurface, displaySurface)
}

// NewSelfPlayService keeps the self-play runtime implementation private while
// exposing only its value-oriented application contract to the CLI graph.
func NewSelfPlayService(factory agentruntime.SessionRuntimeFactory, clockSource clock.Source) serviceSelfPlay.Service {
	return agentruntime.NewSelfPlayService(factory, clockSource)
}

// DeviceSet is the device service's complete provider set. Application Wire
// composition includes this set alongside the existing registry provider.
var DeviceSet = wire.NewSet(NewDeviceService, NewDeviceProbeService)

// SessionDependencies are the process-scoped seams installed by application
// Wire. Invocation requests carry values only; runtime and capability owners
// stay in this graph.
type SessionDependencies struct {
	Clock             clock.Source
	PlanFactory       agentruntime.SessionRuntimeFactory
	ToolService       serviceTools.Service
	RuntimeFactory    agentruntime.SessionRTCRuntimeFactory
	SessionInferencer messages.SessionInferencer
	ToolExecutor      messages.ToolExecutor
	DeviceRegistry    devicegw.DeviceRegistry
	RuntimeObserver   agentruntime.SessionRuntimeObserver
	MetricSampler     observability.MetricSampler
	Logger            observability.Logger
	Runtime           serviceRuntime.Runtime
}

func NewSessionService(deps SessionDependencies) serviceSession.SessionService {
	return sessionservice.New(sessionservice.Dependencies{
		Clock: deps.Clock, Runtime: deps.Runtime,
	})
}

// NewSessionRuntime builds the private runtime implementation behind its
// public contract. Application Wire never imports services/internal.
func NewSessionRuntime(clockSource clock.Source, resolver serviceTools.Service, planFactory agentruntime.SessionRuntimeFactory, runtimeFactory agentruntime.SessionRTCRuntimeFactory, inferencer messages.SessionInferencer, toolExecutor messages.ToolExecutor, deviceRegistry devicegw.DeviceRegistry, observer agentruntime.SessionRuntimeObserver, metricSampler observability.MetricSampler, logger observability.Logger) serviceRuntime.Runtime {
	return agentruntime.New(agentruntime.Dependencies{
		Clock: clockSource, PlanFactory: planFactory, ToolService: resolver, RuntimeFactory: runtimeFactory,
		SessionInferencer: inferencer, ToolExecutor: toolExecutor,
		DeviceRegistry: deviceRegistry, RuntimeObserver: observer,
		Observability: observability.NewDependencies(metricSampler, logger),
	})
}

func NewSessionRuntimeFactory() agentruntime.SessionRuntimeFactory {
	return agentruntime.NewSessionRuntimeFactory()
}

var SessionSet = wire.NewSet(NewSessionRuntimeFactory, NewSessionRuntime, NewSessionService)

// SelfPlaySet is the self-play service's complete provider set.
var SelfPlaySet = wire.NewSet(NewSelfPlayService)
