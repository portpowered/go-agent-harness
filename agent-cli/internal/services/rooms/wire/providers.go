// Package wire assembles the CLI host edges around the embeddable room
// service. The runtime owns room lifecycle, evidence, replay, and media
// orchestration; this package only resolves CLI launch paths and binds the
// host device service.
package wire

import (
	"context"
	"io"

	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services/rooms/internal/launch"
	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	roomevidencewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay"
	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	runtimeWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

// Dependencies are the public runtime roles and host adapters needed by one
// CLI room service. No CLI configuration or provider implementation crosses
// into the embeddable room package.
type Dependencies struct {
	Live     session.LiveService
	Devices  runtimeDevices.Service
	Media    runtimeRooms.MediaFactory
	Replay   roomreplay.Service
	Registry devicegw.DeviceRegistry
	Clock    platformclock.Scheduler
	Tools    runtimeTools.Service
}

// NewService returns the runtime room service with a CLI launch resolver. The
// resolver remains host-owned because bare launch needs the device registry;
// configured execution and every room lifecycle operation stay in runtime.
func NewService(deps Dependencies) runtimeRooms.Service {
	media := deps.Media
	if media == nil && deps.Devices != nil {
		media = runtimeWire.NewMediaFactory(deps.Devices)
	}
	runtimeService := runtimeWire.NewService(runtimeWire.Dependencies{
		Live: deps.Live, Media: media, Replay: deps.Replay, Clock: deps.Clock, Tools: deps.Tools, Evidence: roomevidencewire.NewService(),
	})
	return &service{runtime: runtimeService, replay: deps.Replay, launch: launch.NewPlanner(deps.Registry)}
}

type service struct {
	runtime runtimeRooms.Service
	replay  roomreplay.Service
	launch  *launch.Planner
}

// ReplayService exposes the already-composed public replay owner to the CLI
// transport adapter without exposing the runtime implementation graph.
func (s *service) ReplayService() roomreplay.Service {
	if s == nil {
		return nil
	}
	return s.replay
}

func (s *service) Run(ctx context.Context, out io.Writer, options runtimeRooms.RoomRunOptions) (runtimeRooms.RoomResult, error) {
	if s == nil || s.runtime == nil {
		return runtimeRooms.RoomResult{}, runtimeRooms.ErrRoomServiceUnavailable
	}
	return s.runtime.Run(ctx, out, options)
}

func (s *service) ResolveLaunchPlan(options runtimeRooms.RoomLaunchOptions) (runtimeRooms.RoomLaunchPlan, error) {
	if s == nil || s.launch == nil {
		return runtimeRooms.RoomLaunchPlan{}, runtimeRooms.ErrRoomServiceUnavailable
	}
	return s.launch.Resolve(options)
}

func (s *service) LoadReplayPlan(path string) (runtimeRooms.RoomReplayPlan, error) {
	if s == nil || s.runtime == nil {
		return runtimeRooms.RoomReplayPlan{}, runtimeRooms.ErrRoomServiceUnavailable
	}
	return s.runtime.LoadReplayPlan(path)
}

func (s *service) ReplayManifest(plan runtimeRooms.RoomReplayPlan) runtimeRooms.Manifest {
	if s == nil || s.runtime == nil {
		return runtimeRooms.Manifest{}
	}
	return s.runtime.ReplayManifest(plan)
}

func (s *service) ValidateReplayOutput(plan runtimeRooms.RoomReplayPlan, destination string) error {
	if s == nil || s.runtime == nil {
		return runtimeRooms.ErrRoomServiceUnavailable
	}
	return s.runtime.ValidateReplayOutput(plan, destination)
}

func (s *service) ValidateEvidenceOutput(destination string) error {
	if s == nil || s.runtime == nil {
		return runtimeRooms.ErrRoomServiceUnavailable
	}
	return s.runtime.ValidateEvidenceOutput(destination)
}

func (s *service) CreateFreshRunDirectory(configDir string) (string, error) {
	if s == nil || s.runtime == nil {
		return "", runtimeRooms.ErrRoomServiceUnavailable
	}
	return s.runtime.CreateFreshRunDirectory(configDir)
}

var _ runtimeRooms.Service = (*service)(nil)

var Set = wire.NewSet(NewService) //nolint:gochecknoglobals // immutable Wire provider metadata
