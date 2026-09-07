package wire

import (
	"encoding/json"
	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	runtimeRoomsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/wire"
)

type roomReportingService struct{}

func NewRoomReportingService() runtimeRooms.ReportingService {
	return roomReportingService{}
}

func (roomReportingService) LatencyReport(destination string) (json.RawMessage, error) {
	report, err := runtimeRoomsWire.NewLatencyService().Report(destination)
	if err != nil {
		return nil, err
	}
	return json.Marshal(report)
}
