package service

import (
	"errors"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
)

func Validate(service roomevidence.Service, plan RoomReplayPlan) error {
	if service == nil {
		return errors.New("room evidence service is required")
	}
	_, err := service.Load(plan)
	return err
}
