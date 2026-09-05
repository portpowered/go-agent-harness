package agentruntime

import "github.com/portpowered/go-agent-harness/agent-cli/internal/room"

func roomFormatForOptions(opts RoomRunOptions) room.PCM16Format {
	format := roomMixerConfigForOptions(opts).Format
	if format == (room.PCM16Format{}) {
		return room.DefaultPCM16Format()
	}
	return format
}
