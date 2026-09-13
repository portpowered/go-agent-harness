package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplayschedule"
)

func (s *schedule) Run(ctx context.Context, request roomreplayschedule.RunRequest) error {
	if s == nil {
		return nil
	}
	ctx = nonNilContext(ctx)
	targets, err := targetIndex(request.Targets)
	if err != nil {
		return err
	}
	if err := validateTargets(s.targetIDs, targets); err != nil {
		return err
	}
	for frameIndex, frame := range s.frames {
		if err := runFrame(ctx, request, s.targetIDs, targets, frameIndex, frame); err != nil {
			return err
		}
	}
	return waitForParticipants(ctx, request)
}

func targetIndex(targets []roomreplayschedule.Target) (map[string]roomreplayschedule.Target, error) {
	byID := make(map[string]roomreplayschedule.Target, len(targets))
	for _, target := range targets {
		id := strings.TrimSpace(target.ID)
		if id == "" {
			return nil, fmt.Errorf("%w: target ID is empty", roomreplayschedule.ErrTargetUncontrolled)
		}
		if _, exists := byID[id]; exists {
			return nil, fmt.Errorf("%w: duplicate target %q", roomreplayschedule.ErrInvalidRequest, id)
		}
		byID[id] = target
	}
	return byID, nil
}

func validateTargets(ids []string, targets map[string]roomreplayschedule.Target) error {
	for _, targetID := range ids {
		target, ok := targets[targetID]
		if !ok {
			return fmt.Errorf("%w: %q", roomreplayschedule.ErrTargetMissing, targetID)
		}
		if target.Active == nil || target.Release == nil || target.Advance == nil || target.AwaitAcknowledgement == nil {
			return fmt.Errorf("%w: %q", roomreplayschedule.ErrTargetUncontrolled, targetID)
		}
	}
	return nil
}

func runFrame(ctx context.Context, request roomreplayschedule.RunRequest, targetIDs []string, targets map[string]roomreplayschedule.Target, frameIndex int, frame scheduledFrame) error {
	if stopping(request) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ensureTargetsActive(request, targetIDs, targets); err != nil {
		return err
	}
	if err := releaseFrame(ctx, request, targetIDs, targets, frameIndex, frame); err != nil {
		return err
	}
	return advanceFrame(ctx, request, targetIDs, targets, frameIndex)
}

func ensureTargetsActive(request roomreplayschedule.RunRequest, targetIDs []string, targets map[string]roomreplayschedule.Target) error {
	for _, targetID := range targetIDs {
		target := targets[targetID]
		if target.Active() {
			continue
		}
		if stopping(request) {
			return nil
		}
		return fmt.Errorf("%w: %q", roomreplayschedule.ErrTargetInactive, targetID)
	}
	return nil
}

func releaseFrame(ctx context.Context, request roomreplayschedule.RunRequest, targetIDs []string, targets map[string]roomreplayschedule.Target, frameIndex int, frame scheduledFrame) error {
	for _, source := range frame.contributions {
		for _, targetID := range targetIDs {
			if targetID == source.sourceID {
				continue
			}
			target := targets[targetID]
			if err := releaseContribution(ctx, request, target, frameIndex, targetID, source); err != nil {
				return err
			}
		}
	}
	return nil
}

func releaseContribution(ctx context.Context, request roomreplayschedule.RunRequest, target roomreplayschedule.Target, frameIndex int, targetID string, source contribution) error {
	if !target.Active() {
		if stopping(request) {
			return nil
		}
		return fmt.Errorf("%w: %q before logical frame %d", roomreplayschedule.ErrTargetInactive, targetID, frameIndex)
	}
	pcm := append([]byte(nil), source.pcm...)
	if err := target.Release(ctx, source.sourceID, pcm); err != nil {
		if stopping(request) {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("release logical frame %d from %q to %q: %w", frameIndex, source.sourceID, targetID, err)
	}
	if request.OnContribution != nil {
		request.OnContribution(roomreplayschedule.Contribution{Frame: frameIndex, SourceID: source.sourceID, TargetID: targetID, PCM: append([]byte(nil), pcm...)})
	}
	return nil
}

func advanceFrame(ctx context.Context, request roomreplayschedule.RunRequest, targetIDs []string, targets map[string]roomreplayschedule.Target, frameIndex int) error {
	for _, targetID := range targetIDs {
		if err := advanceTarget(ctx, request, targets[targetID], targetID, frameIndex); err != nil {
			return err
		}
	}
	return nil
}

func advanceTarget(ctx context.Context, request roomreplayschedule.RunRequest, target roomreplayschedule.Target, targetID string, frameIndex int) error {
	if err := target.Advance(ctx); err != nil {
		if stopping(request) {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("advance target %q at logical frame %d: %w", targetID, frameIndex, err)
	}
	if err := target.AwaitAcknowledgement(ctx); err != nil {
		if stopping(request) {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %q before logical frame %d: %w", roomreplayschedule.ErrTargetStopped, targetID, frameIndex, err)
	}
	return nil
}

func waitForParticipants(ctx context.Context, request roomreplayschedule.RunRequest) error {
	for _, waiter := range request.WaitFor {
		if waiter.Done == nil {
			continue
		}
		select {
		case <-waiter.Done:
		case <-ctx.Done():
			if stopping(request) {
				return nil
			}
			return ctx.Err()
		}
	}
	return nil
}

func stopping(request roomreplayschedule.RunRequest) bool {
	return request.IsStopping != nil && request.IsStopping()
}
