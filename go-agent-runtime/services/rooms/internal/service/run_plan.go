package service

import (
	"fmt"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

// ResolveRunPlan admits either one offline replay bundle or one live launch.
// The two sources are mutually exclusive so a replay can never silently
// inherit live credentials, devices, or config.
func (s *Service) ResolveRunPlan(options rooms.RoomRunPlanOptions) (rooms.RoomRunPlan, error) {
	if s == nil {
		return rooms.RoomRunPlan{}, rooms.ErrRoomServiceUnavailable
	}
	replayPath := strings.TrimSpace(options.ReplayPath)
	if replayPath == "" {
		launch, err := s.ResolveLaunchPlan(options.Launch)
		if err != nil {
			return rooms.RoomRunPlan{}, err
		}
		return rooms.RoomRunPlan{LaunchPlan: &launch, Manifest: launch.Manifest}, nil
	}
	if strings.TrimSpace(options.Launch.ConfigPath) != "" || strings.TrimSpace(options.Launch.ManifestPath) != "" {
		return rooms.RoomRunPlan{}, fmt.Errorf("%w: --replay cannot be combined with --config or --manifest", rooms.ErrReplaySourceConflict)
	}
	replay, err := s.LoadReplayPlan(replayPath)
	if err != nil {
		return rooms.RoomRunPlan{}, err
	}
	return rooms.RoomRunPlan{ReplayPlan: &replay, ReplayPath: replayPath, Manifest: s.ReplayManifest(replay)}, nil
}

// ResolveRunOutput applies the evidence destination policy. Replays always
// record; a live room records only when its manifest enables recording, uses
// the manifest directory unless the caller chose one explicitly, and gives a
// bare room a fresh child of its config directory.
func (s *Service) ResolveRunOutput(plan rooms.RoomRunPlan, requested string, explicit bool) (string, error) {
	requested = strings.TrimSpace(requested)
	if plan.Replay() {
		return defaultOutput(requested), nil
	}
	if plan.LaunchPlan == nil || !plan.LaunchPlan.Manifest.Room.RecordingEnabled() {
		return "", nil
	}
	if !explicit {
		if destination := plan.LaunchPlan.Manifest.Room.RecordingDirectory(); destination != "" {
			return destination, nil
		}
		if plan.LaunchPlan.Mode == rooms.RoomLaunchModeBare {
			return s.CreateFreshRunDirectory(plan.LaunchPlan.ConfigDir)
		}
	}
	return defaultOutput(requested), nil
}

func defaultOutput(requested string) string {
	if requested == "" {
		return rooms.DefaultRoomOutputDir
	}
	return requested
}

// ValidateRunOutput rejects an unusable evidence destination before any
// participant starts. An empty destination means recording is disabled.
func (s *Service) ValidateRunOutput(plan rooms.RoomRunPlan, destination string) error {
	if destination == "" {
		return nil
	}
	if plan.Replay() {
		if err := s.ValidateReplayOutput(*plan.ReplayPlan, destination); err != nil {
			return err
		}
	}
	return s.ValidateEvidenceOutput(destination)
}

// allParticipantsFailed turns a structurally successful run in which every
// participant failed into a typed room error. A partial failure keeps the
// surviving peer's success, so this fires only when nobody survived.
func allParticipantsFailed(result rooms.RoomResult) error {
	if len(result.Participants) == 0 {
		return nil
	}
	details := make([]rooms.ParticipantFailureDetail, 0, len(result.Participants))
	for id, participant := range result.Participants {
		if participant.TerminationReason != rooms.ParticipantTerminationError {
			return nil
		}
		details = append(details, rooms.ParticipantFailureDetail{ParticipantID: id, Error: participant.Error})
	}
	sort.Slice(details, func(i, j int) bool { return details[i].ParticipantID < details[j].ParticipantID })
	return &rooms.AllParticipantsFailedError{Participants: details}
}
