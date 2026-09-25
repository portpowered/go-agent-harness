// Package wire contains service-level providers. Keeping the private device
// implementation behind this package lets the application graph inject the
// public contract without importing services/internal from a transport.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceDevices "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"
	devicesservice "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/devices"
	toolsservice "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/tools"
	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audioiowire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	runtimeDevicesWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/wire"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	runtimeRoomEvidenceWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence/wire"
	runtimeRoomReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay"
	runtimeRoomReplayWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay/wire"
	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	runtimeRoomsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/wire"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

// NewDeviceService is the Wire-visible constructor for the public device
// contract. Its implementation package remains private to services.
func NewDeviceService(registry devicegw.DeviceRegistry) serviceDevices.DeviceService {
	return devicesservice.New(registry)
}

// NewDeviceProbeService exposes the reusable runtime probe service through the
// CLI's device contract; registry and media workers remain private to runtime.
func NewDeviceProbeService(registry devicegw.DeviceRegistry, sessionFactory serviceDevices.DeviceProbeSessionFactory) serviceDevices.DeviceProbeService {
	return runtimeDevicesWire.NewProbeService(registry, sessionFactory)
}

// NewRoomEvidenceService exposes the runtime room evidence owner.
func NewRoomEvidenceService() roomevidence.Service {
	return runtimeRoomEvidenceWire.NewService()
}

func NewRoomLatencyService() roomevidence.LatencyService {
	return runtimeRoomEvidenceWire.NewLatencyService()
}

// NewRoomService keeps room orchestration behind the public room contract. The
// application graph supplies the live session and media ports explicitly;
// launch planning receives host devices per invocation through the contract.
func NewRoomService(live runtimeSession.LiveService, media runtimeRooms.MediaFactory, clockSource clock.Scheduler, replay runtimeRoomReplay.Service, evidence roomevidence.Service, latency roomevidence.LatencyService) runtimeRooms.Service {
	return runtimeRoomsWire.NewService(runtimeRoomsWire.Dependencies{
		Live: live, Media: media, Clock: clockSource, Replay: replay,
		Evidence: evidence, Latency: latency,
	})
}

// NewRoomServiceWithDevices lets application composition inject the complete
// device service. The media adapter is constructed by the runtime room wire,
// keeping device registries and gateway workers out of room policy.
func NewRoomServiceWithDevices(live runtimeSession.LiveService, deviceService runtimeDevices.Service, clockSource clock.Scheduler, replay runtimeRoomReplay.Service, evidence roomevidence.Service, latency roomevidence.LatencyService) runtimeRooms.Service {
	var media runtimeRooms.MediaFactory
	if deviceService != nil {
		media = runtimeRoomsWire.NewMediaFactory(deviceService)
	}
	return NewRoomService(live, media, clockSource, replay, evidence, latency)
}

// NewRoomReplayService composes room bundle admission with the shared replay inspector.
func NewRoomReplayService(replayService runtimeReplay.Service) runtimeRoomReplay.Service {
	return runtimeRoomReplayWire.NewService(replayService)
}

var RoomSet = wire.NewSet(NewRoomReplayService, NewRoomEvidenceService, NewRoomLatencyService, NewRoomServiceWithDevices) //nolint:gochecknoglobals // immutable Wire provider metadata

// NewToolCapabilitiesService keeps session tool composition in the private
// service implementation while allowing the CLI to provide its browser seam.
func NewToolCapabilitiesService(staticExecutor messages.ToolExecutor, browserFactory serviceTools.BrowserFactory, displaySurface cliTools.DisplaySurface, displayProbe cliTools.DisplayCapabilityProbe, runtimeService runtimeTools.Service) serviceTools.Service {
	return toolsservice.New(staticExecutor, browserFactory, displaySurface, displayProbe, runtimeService)
}

func NewToolCapabilitiesServiceForWire(toolExecutor messages.ToolExecutor, browserFactory serviceTools.BrowserFactory, displaySurface cliTools.DisplaySurface, runtimeService runtimeTools.Service) serviceTools.Service {
	// The reusable tools service is the sole owner of the primitive registry.
	// The composition root still receives the tool executor for commands that
	// explicitly replace it, but the session capability path must resolve a
	// fresh runtime surface from each normalized config snapshot.
	base := toolsservice.New(nil, browserFactory, displaySurface, displaySurface, runtimeService)
	if replacement, ok := toolExecutor.(interface{ AllowUnadvertisedTools() bool }); ok && replacement.AllowUnadvertisedTools() {
		return legacyToolCapabilitiesService{base: base, executor: toolExecutor}
	}
	return base
}

// legacyToolCapabilitiesService preserves the executor-only composition API.
// That API predates the request-scoped capability service and its executor is
// therefore the complete injected surface; the service still supplies the
// host's lifecycle and catalog metadata around it.
type legacyToolCapabilitiesService struct {
	base     serviceTools.Service
	executor messages.ToolExecutor
}

func (s legacyToolCapabilitiesService) Resolve(cfg *config.Config) (serviceTools.Capabilities, error) {
	capabilities, err := s.base.Resolve(cfg)
	if err != nil {
		return serviceTools.Capabilities{}, err
	}
	capabilities.Executor = s.executor
	return capabilities, nil
}

// DeviceSet is the device service's complete provider set. Application Wire
// composition includes this set alongside the existing registry provider.
var DeviceSet = wire.NewSet(NewDeviceService, NewDeviceProbeSessionFactory, NewDeviceProbeService, audioiowire.NewService, runtimeDevicesWire.NewService) //nolint:gochecknoglobals // immutable Wire provider metadata
