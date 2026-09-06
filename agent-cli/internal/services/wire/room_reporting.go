package wire

import (
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services/rooms"
)

func NewRoomReportingService() rooms.ReportingService {
	return agentruntime.NewRoomReportingService()
}
