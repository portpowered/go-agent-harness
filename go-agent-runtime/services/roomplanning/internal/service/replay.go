package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomplanning"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

func planReplay(ctx context.Context, options roomplanning.Options) (roomplanning.PlanResult, error) {
	if options.ReplayPlanner == nil {
		return roomplanning.PlanResult{}, roomplanning.ErrReplayPlanner
	}
	replay := options.ReplayPlan
	manifest := replay.Manifest()
	plans := make([]*roomplanning.ParticipantPlan, 0, len(replay.Participants))
	for index, recorded := range replay.Participants {
		if err := ctx.Err(); err != nil {
			return roomplanning.PlanResult{Plans: plans}, errors.Join(err, closeCapabilities(plans))
		}
		plan, err := replayParticipant(ctx, options, manifest, index, recorded)
		if err != nil {
			return roomplanning.PlanResult{Plans: plans}, errors.Join(err, closeCapabilities(plans))
		}
		plans = append(plans, plan)
	}
	return roomplanning.PlanResult{Plans: plans}, nil
}

func replayParticipant(ctx context.Context, options roomplanning.Options, manifest rooms.Manifest, index int, recorded rooms.RoomReplayParticipant) (*roomplanning.ParticipantPlan, error) {
	if index >= len(manifest.Participants) {
		return nil, participantError(options, recorded.ID, errors.New("replay participant projection is incomplete"))
	}
	participant := manifest.Participants[index]
	plan := &roomplanning.ParticipantPlan{Participant: participant, Replay: true}
	if normalizeKind(recorded.Kind) == rooms.ParticipantKindHuman {
		return plan, nil
	}
	if recorded.CapturePath == "" {
		return nil, participantError(options, recorded.ID, errors.New("replay provider capture path is empty"))
	}
	sessionOptions := replaySessionOptions(recorded)
	replaySession, err := options.ReplayPlanner(ctx, roomplanning.ReplayRequest{Participant: participant, Recorded: recorded, Options: sessionOptions})
	if err != nil {
		return nil, participantError(options, recorded.ID, fmt.Errorf("plan replay session: %w", err))
	}
	if isNil(replaySession.Inferencer) {
		return nil, participantError(options, recorded.ID, errors.New("replay session planner returned a nil inferencer"))
	}
	plan.Options = sessionOptions
	plan.Inferencer = replaySession.Inferencer
	replaySession.MaxDuration = 0
	plan.ReplaySession = replaySession
	return plan, nil
}

func replaySessionOptions(recorded rooms.RoomReplayParticipant) roomplanning.SessionOptions {
	return roomplanning.SessionOptions{
		Provider: recorded.Provider, Model: recorded.Model, ModelProvided: true,
		ReplayPath: recorded.CapturePath, RoomReplay: true,
		Prompt: recorded.OpeningPrompt, PromptProvided: recorded.OpeningPrompt != "", Voice: recorded.Voice,
		WaitForClose: false,
	}
}
