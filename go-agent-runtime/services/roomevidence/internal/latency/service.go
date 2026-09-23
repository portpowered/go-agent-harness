package latency

import (
	"path/filepath"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// Service is the private implementation of the public roomevidence latency
// capability. It keeps file I/O and observation storage behind the service
// boundary while allowing Wire to return only the service-owned contract.
type Service struct{}

func NewService() roomevidence.LatencyService { return Service{} }

func (Service) NewRecorder(source platformclock.Source, format rooms.AudioFormat) rooms.LatencyRecorder {
	return New(source, format)
}

func (Service) ReadBundle(path string) (rooms.RoomLatencyBundle, error) {
	return ReadBundle(path)
}

func (Service) AnalyzeBundle(bundle rooms.RoomLatencyBundle) (rooms.RoomLatencyReport, error) {
	return Analyze(bundle)
}

func (Service) Report(destination string) (rooms.RoomLatencyReport, error) {
	return AnalyzeFile(filepath.Join(destination, rooms.RoomLatencyArtifactPath))
}

var _ roomevidence.LatencyService = Service{}
