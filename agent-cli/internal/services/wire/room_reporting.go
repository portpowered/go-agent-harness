package wire

import (
	"encoding/json"
	roomevidencewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence/wire"
	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

type roomReportingService struct{}

func NewRoomReportingService() runtimeRooms.ReportingService {
	return roomReportingService{}
}

func (roomReportingService) LatencyReport(destination string) (json.RawMessage, error) {
	report, err := roomevidencewire.NewLatencyService().Report(destination)
	if err != nil {
		return nil, err
	}
	return json.Marshal(report)
}
