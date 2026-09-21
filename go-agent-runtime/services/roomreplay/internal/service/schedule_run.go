package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay"
)

// At the default cadence this permits more than 33 minutes of replay while
// bounding retained schedule state for sparse or adversarial timelines.
const roomReplayMaxScheduleFrames = 100_000

func scheduleTooLong(frameCount int) error {
	return fmt.Errorf("%w: %d frames exceeds maximum %d", roomreplay.ErrScheduleTooLong, frameCount, roomReplayMaxScheduleFrames)
}

type speechSegment struct {
	startNanos int64
	endNanos   int64
	hasEnd     bool
	sequence   int64
}

func speechSegments(timeline []roomreplay.TimelineEvent, participantID string) []speechSegment {
	segments := make([]speechSegment, 0)
	open := make([]int, 0, 1)
	for _, event := range timeline {
		if event.ParticipantID != participantID {
			continue
		}
		eventType := normalizeEventType(event.Type)
		offsetNanos := event.OffsetNanos
		if offsetNanos == 0 && event.OffsetMS != 0 {
			offsetNanos = event.OffsetMS * int64(time.Millisecond)
		}
		switch eventType {
		case "speech_start", "audio_start", "response_audio_start", "output_audio_start", "speaking_start":
			segments = append(segments, speechSegment{startNanos: offsetNanos, sequence: event.Sequence})
			open = append(open, len(segments)-1)
		case "speech_end", "audio_end", "response_audio_end", "output_audio_end", "speaking_end":
			if len(open) == 0 {
				continue
			}
			index := open[len(open)-1]
			open = open[:len(open)-1]
			segments[index].endNanos = offsetNanos
			segments[index].hasEnd = true
		}
	}
	return segments
}

func normalizeEventType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, ".", "_")
	return strings.ReplaceAll(value, "-", "_")
}

func (s *schedule) Run(ctx context.Context, request roomreplay.RunRequest) error {
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
		if stopping(request) {
			return nil
		}
		if err := runFrame(ctx, request, s.targetIDs, targets, frameIndex, frame); err != nil {
			return err
		}
	}
	if stopping(request) {
		return nil
	}
	return waitForParticipants(ctx, request)
}

func targetIndex(targets []roomreplay.Target) (map[string]roomreplay.Target, error) {
	byID := make(map[string]roomreplay.Target, len(targets))
	for _, target := range targets {
		id := strings.TrimSpace(target.ID)
		if id == "" {
			return nil, fmt.Errorf("%w: target ID is empty", roomreplay.ErrTargetUncontrolled)
		}
		if _, exists := byID[id]; exists {
			return nil, fmt.Errorf("%w: duplicate target %q", roomreplay.ErrInvalidRequest, id)
		}
		byID[id] = target
	}
	return byID, nil
}

func validateTargets(ids []string, targets map[string]roomreplay.Target) error {
	for _, targetID := range ids {
		target, ok := targets[targetID]
		if !ok {
			return fmt.Errorf("%w: %q", roomreplay.ErrTargetMissing, targetID)
		}
		if target.Active == nil || target.Release == nil || target.Advance == nil || target.AwaitAcknowledgement == nil {
			return fmt.Errorf("%w: %q", roomreplay.ErrTargetUncontrolled, targetID)
		}
	}
	return nil
}

func runFrame(ctx context.Context, request roomreplay.RunRequest, targetIDs []string, targets map[string]roomreplay.Target, frameIndex int, frame scheduledFrame) error {
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

func ensureTargetsActive(request roomreplay.RunRequest, targetIDs []string, targets map[string]roomreplay.Target) error {
	for _, targetID := range targetIDs {
		target := targets[targetID]
		if target.Active() {
			continue
		}
		if stopping(request) {
			return nil
		}
		return fmt.Errorf("%w: %q", roomreplay.ErrTargetInactive, targetID)
	}
	return nil
}

func releaseFrame(ctx context.Context, request roomreplay.RunRequest, targetIDs []string, targets map[string]roomreplay.Target, frameIndex int, frame scheduledFrame) error {
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

func releaseContribution(ctx context.Context, request roomreplay.RunRequest, target roomreplay.Target, frameIndex int, targetID string, source contribution) error {
	if !target.Active() {
		if stopping(request) {
			return nil
		}
		return fmt.Errorf("%w: %q before logical frame %d", roomreplay.ErrTargetInactive, targetID, frameIndex)
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
		request.OnContribution(roomreplay.Contribution{Frame: frameIndex, SourceID: source.sourceID, TargetID: targetID, PCM: append([]byte(nil), pcm...)})
	}
	return nil
}

func advanceFrame(ctx context.Context, request roomreplay.RunRequest, targetIDs []string, targets map[string]roomreplay.Target, frameIndex int) error {
	for _, targetID := range targetIDs {
		if err := advanceTarget(ctx, request, targets[targetID], targetID, frameIndex); err != nil {
			return err
		}
	}
	return nil
}

func advanceTarget(ctx context.Context, request roomreplay.RunRequest, target roomreplay.Target, targetID string, frameIndex int) error {
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
		return fmt.Errorf("%w: %q before logical frame %d: %w", roomreplay.ErrTargetStopped, targetID, frameIndex, err)
	}
	return nil
}

func waitForParticipants(ctx context.Context, request roomreplay.RunRequest) error {
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

func stopping(request roomreplay.RunRequest) bool {
	return request.IsStopping != nil && request.IsStopping()
}
