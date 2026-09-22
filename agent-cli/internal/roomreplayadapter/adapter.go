package roomreplayadapter

import (
	"context"
	"errors"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay"
)

func LoadPlan(service roomreplay.Service, supplied *roomreplay.RoomReplayPlan, path string) (roomreplay.RoomReplayPlan, bool, error) {
	if supplied != nil {
		if service == nil {
			return roomreplay.RoomReplayPlan{}, true, errors.New("room replay service is required")
		}
		plan, err := service.Load(supplied.BundlePath)
		return plan, true, err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return roomreplay.RoomReplayPlan{}, false, nil
	}
	if service == nil {
		return roomreplay.RoomReplayPlan{}, true, errors.New("room replay service is required for replay path admission")
	}
	plan, err := service.Load(path)
	return plan, true, err
}

func BuildSchedule(ctx context.Context, service roomreplay.Service, plan *roomreplay.RoomReplayPlan, targetIDs []string, targetFormat roomreplay.PCM16Format) (roomreplay.Schedule, error) {
	if service == nil {
		return nil, errors.New("room replay service is required")
	}
	return service.Build(ctx, roomreplay.BuildRequest{ReplayPlan: plan, TargetIDs: targetIDs, TargetFormat: targetFormat})
}
