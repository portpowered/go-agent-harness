package cli

import (
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	servicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"
	audioiowire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
	runtimeDevicesWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/wire"
	captureReplayWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

func newTestRoomRunCommand(globalFlags *flags.GlobalFlags, registry devicegw.DeviceRegistry) *RoomRunCommand {
	return NewRoomRunCommand(globalFlags, servicewire.NewRoomServiceWithDevices(
		nil, runtimeDevicesWire.NewService(registry, audioiowire.NewService()), registry, clock.Real{},
		servicewire.NewRoomReplayService(captureReplayWire.NewService()),
		servicewire.NewRoomEvidenceService(), servicewire.NewRoomLatencyService(),
	))
}
