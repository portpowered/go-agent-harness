package agentruntime

import (
	"context"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	roomreplayschedule "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplayschedule"
	roomReplayWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplayschedule/wire"
)

// roomReplaySchedule is the CLI's thin adapter around the credential-free
// runtime schedule. Mixer, coordinator, and diagnostic ownership stays here.
type roomReplaySchedule struct {
	schedule roomreplayschedule.Schedule
}

func newRoomReplaySchedule(ctx context.Context, replay RoomReplayPlan, plans []*roomParticipantPlan, format room.PCM16Format) (*roomReplaySchedule, error) {
	schedule, err := roomReplayWire.NewService().Build(ctx, roomReplayScheduleBuildRequest(replay, plans, format))
	if err != nil {
		return nil, err
	}
	if schedule == nil {
		return nil, nil
	}
	return &roomReplaySchedule{schedule: schedule}, nil
}

func (s *roomReplaySchedule) run(ctx context.Context, runtimes []*roomParticipantRuntime, coordinator *roomCoordinator, opts RoomRunOptions) error {
	if s == nil || s.schedule == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return s.schedule.Run(ctx, roomReplayScheduleRunRequest(runtimes, coordinator, opts))
}

func roomReplayScheduleBuildRequest(replay RoomReplayPlan, plans []*roomParticipantPlan, format room.PCM16Format) roomreplayschedule.BuildRequest {
	request := roomreplayschedule.BuildRequest{
		SourceFormat: roomreplayschedule.SourcePCM16Format{
			SampleRate:      replay.PCMFormat.SampleRate,
			Channels:        replay.PCMFormat.Channels,
			SampleWidthBits: replay.PCMFormat.SampleWidthBits,
			SampleWidthBit:  replay.PCMFormat.SampleWidthBit,
			ByteOrder:       replay.PCMFormat.ByteOrder,
			Encoding:        replay.PCMFormat.Encoding,
		},
		TargetFormat: roomreplayschedule.PCM16Format{
			SampleRate:    format.SampleRate,
			Channels:      format.Channels,
			FrameDuration: format.FrameDuration,
		},
		Participants: make([]roomreplayschedule.Participant, 0, len(replay.Participants)),
		Timeline:     make([]roomreplayschedule.TimelineEvent, 0, len(replay.Timeline)),
		TargetIDs:    make([]string, 0, len(plans)),
	}
	for _, participant := range replay.Participants {
		sentPath := ""
		if artifact, ok := roomReplayParticipantArtifact(participant, roomReplayArtifactRoleSentPCM); ok {
			sentPath = artifact.AbsolutePath
		}
		request.Participants = append(request.Participants, roomreplayschedule.Participant{
			ID: participant.ID, CapturePath: participant.CapturePath, SentPCMPath: sentPath,
		})
	}
	for _, event := range replay.Timeline {
		request.Timeline = append(request.Timeline, roomreplayschedule.TimelineEvent{
			Sequence: event.Sequence, OffsetMS: event.OffsetMS, OffsetNanos: event.OffsetNanos,
			Type: event.Type, ParticipantID: event.ParticipantID,
		})
	}
	for _, plan := range plans {
		if plan != nil && !roomParticipantIsHuman(plan) {
			request.TargetIDs = append(request.TargetIDs, plan.manifest.ID)
		}
	}
	return request
}

func roomReplayScheduleRunRequest(runtimes []*roomParticipantRuntime, coordinator *roomCoordinator, opts RoomRunOptions) roomreplayschedule.RunRequest {
	request := roomReplayScheduleRunHooks(coordinator, opts)
	for _, runtime := range runtimes {
		if runtime == nil || runtime.plan == nil {
			continue
		}
		request.WaitFor = append(request.WaitFor, roomreplayschedule.Waiter{ID: runtime.plan.manifest.ID, Done: runtime.participantDone})
		if target, ok := roomReplayTarget(runtime, coordinator); ok {
			request.Targets = append(request.Targets, target)
		}
	}
	return request
}

func roomReplayScheduleRunHooks(coordinator *roomCoordinator, opts RoomRunOptions) roomreplayschedule.RunRequest {
	request := roomreplayschedule.RunRequest{}
	if coordinator != nil {
		request.IsStopping = coordinator.isStopping
	}
	if opts.onParticipantAudioFanned != nil {
		request.OnContribution = func(contribution roomreplayschedule.Contribution) {
			opts.onParticipantAudioFanned(contribution.SourceID, contribution.TargetID, contribution.PCM)
		}
	}
	return request
}

func roomReplayTarget(runtime *roomParticipantRuntime, coordinator *roomCoordinator) (roomreplayschedule.Target, bool) {
	if runtime == nil || runtime.plan == nil || roomParticipantIsHuman(runtime.plan) {
		return roomreplayschedule.Target{}, false
	}
	targetRuntime := runtime
	targetID := runtime.plan.manifest.ID
	targetContext := targetRuntime.ctx
	return roomreplayschedule.Target{
		ID: targetID,
		Active: func() bool {
			return coordinator == nil || coordinator.isActive(targetID)
		},
		Release: func(ctx context.Context, sourceID string, pcm []byte) error {
			return routeRoomPeerPCM(ctx, sourceID, targetRuntime, pcm)
		},
		Advance: func(ctx context.Context) error {
			return targetRuntime.mixer.Advance(ctx)
		},
		AwaitAcknowledgement: func(ctx context.Context) error {
			if targetRuntime.replayFrameAcks == nil {
				return room.ErrMixerClosed
			}
			select {
			case <-targetRuntime.replayFrameAcks:
				return nil
			case <-targetContext.Done():
				return targetContext.Err()
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}, true
}
