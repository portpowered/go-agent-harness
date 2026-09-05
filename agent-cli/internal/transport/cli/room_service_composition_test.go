package cli

import (
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	servicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

func newTestRoomRunCommand(globalFlags *flags.GlobalFlags, registry devicegw.DeviceRegistry) *RoomRunCommand {
	return NewRoomRunCommand(globalFlags, servicewire.NewRoomService(registry, clock.Real{}, servicewire.NewSessionRuntimeFactory()))
}
