package agentruntime

import (
	"encoding/json"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/services/rooms"
)

type roomReportingService struct{}

func NewRoomReportingService() rooms.ReportingService { return roomReportingService{} }

func (roomReportingService) LatencyReport(destination string) (json.RawMessage, error) {
	report, err := ReadRoomLatencyReport(destination)
	if err != nil {
		return nil, err
	}
	return json.Marshal(report)
}
