package service

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const (
	defaultSampleRate    = 24000
	defaultChannels      = 1
	defaultFrameDuration = 20 * time.Millisecond
)

type schedule struct {
	frames    []scheduledFrame
	targetIDs []string
}

type scheduledFrame struct {
	contributions []contribution
}

type contribution struct {
	sourceID string
	sequence int64
	order    int
	pcm      []byte
}

type frameContribution struct {
	frame int
	value contribution
}

type participantFrameState struct {
	frames             []frameContribution
	cursor             int
	order              int
	lastScheduledFrame int
}

// Build admits capture barriers and file-backed sent PCM before producing an
// immutable schedule. Text-only captures intentionally return a nil schedule.
func (s *Service) Build(ctx context.Context, request roomreplay.BuildRequest) (roomreplay.Schedule, error) {
	ctx = nonNilContext(ctx)
	request, err := prepareReplayBuildRequest(request)
	if err != nil {
		return nil, err
	}
	targetFormat, frameBytes, err := normalizeTargetFormat(request.TargetFormat)
	if err != nil {
		return nil, err
	}
	participants, err := participantIndex(request.Participants)
	if err != nil {
		return nil, err
	}
	targetIDs, expectedFrames, hasInboundAudio, err := inspectTargets(ctx, request.TargetIDs, participants, s.replayService)
	if err != nil {
		return nil, err
	}
	if len(targetIDs) == 0 || !hasInboundAudio {
		return nil, nil
	}
	contributions, maxFrame, err := buildParticipantContributions(ctx, request, targetFormat, frameBytes)
	if err != nil {
		return nil, err
	}
	return assembleSchedule(contributions, targetIDs, expectedFrames, maxFrame)
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func normalizeTargetFormat(format roomreplay.PCM16Format) (roomreplay.PCM16Format, int, error) {
	if format == (roomreplay.PCM16Format{}) {
		format = roomreplay.PCM16Format{SampleRate: defaultSampleRate, Channels: defaultChannels, FrameDuration: defaultFrameDuration}
	}
	frameBytes, err := audio.PCM16FrameBytes(format.SampleRate, format.Channels, format.FrameDuration)
	if err != nil {
		return roomreplay.PCM16Format{}, 0, fmt.Errorf("%w: %w", roomreplay.ErrInvalidFormat, err)
	}
	return format, frameBytes, nil
}

func participantIndex(participants []roomreplay.Participant) (map[string]roomreplay.Participant, error) {
	index := make(map[string]roomreplay.Participant, len(participants))
	for _, participant := range participants {
		id := strings.TrimSpace(participant.ID)
		if id == "" {
			return nil, fmt.Errorf("%w: participant ID is empty", roomreplay.ErrInvalidRequest)
		}
		if _, exists := index[id]; exists {
			return nil, fmt.Errorf("%w: duplicate participant %q", roomreplay.ErrInvalidRequest, id)
		}
		participant.ID = id
		index[id] = participant
	}
	return index, nil
}

func inspectTargets(ctx context.Context, ids []string, participants map[string]roomreplay.Participant, replayService replay.CaptureInspector) ([]string, int, bool, error) {
	targetIDs := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	expectedFrames := 0
	hasInboundAudio := false
	for _, rawID := range ids {
		id, appendCount, err := inspectTarget(ctx, rawID, seen, participants, replayService)
		if err != nil {
			return nil, 0, false, err
		}
		targetIDs = append(targetIDs, id)
		if appendCount > 0 {
			hasInboundAudio = true
		}
		if appendCount > expectedFrames {
			expectedFrames = appendCount
		}
	}
	return targetIDs, expectedFrames, hasInboundAudio, nil
}

func inspectTarget(ctx context.Context, rawID string, seen map[string]struct{}, participants map[string]roomreplay.Participant, replayService replay.CaptureInspector) (string, int, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	id := strings.TrimSpace(rawID)
	if id == "" {
		return "", 0, fmt.Errorf("%w: target ID is empty", roomreplay.ErrInvalidRequest)
	}
	if _, exists := seen[id]; exists {
		return "", 0, fmt.Errorf("%w: duplicate target %q", roomreplay.ErrInvalidRequest, id)
	}
	seen[id] = struct{}{}
	participant, ok := participants[id]
	if !ok {
		return "", 0, fmt.Errorf("%w: %q", roomreplay.ErrParticipantMissing, id)
	}
	if strings.TrimSpace(participant.CapturePath) == "" {
		return "", 0, fmt.Errorf("%w for participant %q", roomreplay.ErrCaptureUnavailable, id)
	}
	if replayService == nil {
		return "", 0, fmt.Errorf("%w: replay service is unavailable", roomreplay.ErrInvalidRequest)
	}
	inspection, err := replayService.InspectCapture(ctx, participant.CapturePath)
	if err != nil {
		return "", 0, fmt.Errorf("%w for participant %q: %w", roomreplay.ErrCaptureUnavailable, id, err)
	}
	return id, inspection.Facts.ClientAudioAppendCount, nil
}

func buildParticipantContributions(ctx context.Context, request roomreplay.BuildRequest, targetFormat roomreplay.PCM16Format, frameBytes int) (map[int][]contribution, int, error) {
	contributionsByFrame := make(map[int][]contribution)
	maxFrame := -1
	order := 0
	for _, participant := range request.Participants {
		entries, nextOrder, err := scheduleParticipant(ctx, participant, request, targetFormat, frameBytes, order)
		if err != nil {
			return nil, -1, err
		}
		for _, entry := range entries {
			contributionsByFrame[entry.frame] = append(contributionsByFrame[entry.frame], entry.value)
			if entry.frame > maxFrame {
				maxFrame = entry.frame
			}
		}
		order = nextOrder
	}
	return contributionsByFrame, maxFrame, nil
}

func scheduleParticipant(ctx context.Context, participant roomreplay.Participant, request roomreplay.BuildRequest, targetFormat roomreplay.PCM16Format, frameBytes, order int) ([]frameContribution, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, order, err
	}
	if strings.TrimSpace(participant.ID) == "" {
		return nil, order, fmt.Errorf("%w: participant ID is empty", roomreplay.ErrInvalidRequest)
	}
	if strings.TrimSpace(participant.SentPCMPath) == "" {
		return nil, order, fmt.Errorf("%w: participant %q has no sent PCM path", roomreplay.ErrSentPCMUnavailable, participant.ID)
	}
	pcm, err := os.ReadFile(participant.SentPCMPath)
	if err != nil {
		return nil, order, fmt.Errorf("%w for participant %q: %w", roomreplay.ErrSentPCMUnavailable, participant.ID, err)
	}
	pcm, err = normalizePCM(pcm, request.SourceFormat, targetFormat)
	if err != nil {
		return nil, order, fmt.Errorf("%w for participant %q: %w", roomreplay.ErrInvalidPCM, participant.ID, err)
	}
	if len(pcm) == 0 {
		return nil, order, nil
	}
	segments := speechSegments(request.Timeline, participant.ID)
	if len(segments) == 0 {
		segments = []speechSegment{{sequence: math.MaxInt64}}
	}
	return scheduleParticipantFrames(pcm, participant.ID, segments, targetFormat, frameBytes, order)
}

func scheduleParticipantFrames(pcm []byte, participantID string, segments []speechSegment, targetFormat roomreplay.PCM16Format, frameBytes, order int) ([]frameContribution, int, error) {
	frameCount := (len(pcm)-1)/frameBytes + 1
	if frameCount > roomReplayMaxScheduleFrames {
		return nil, order, scheduleTooLong(frameCount)
	}
	state := participantFrameState{order: order, lastScheduledFrame: -1}
	for segmentIndex, segment := range segments {
		if state.cursor >= frameCount {
			break
		}
		limit := segmentFrameLimit(segmentIndex, segments, frameCount-state.cursor, targetFormat.FrameDuration)
		state.appendSegment(pcm, participantID, segment, frameIndex(segment.startNanos, targetFormat.FrameDuration), limit, frameBytes)
	}
	state.appendRemainder(pcm, participantID, segments[len(segments)-1], targetFormat.FrameDuration, frameCount, frameBytes)
	return state.frames, state.order, nil
}

func segmentFrameLimit(index int, segments []speechSegment, remaining int, frameDuration time.Duration) int {
	segment := segments[index]
	limit := remaining
	if segment.hasEnd {
		limit = segmentFrameCount(segment.startNanos, segment.endNanos, frameDuration)
	} else if index+1 < len(segments) {
		limit = segmentFrameCount(segment.startNanos, segments[index+1].startNanos, frameDuration)
	}
	if limit < 1 {
		return 1
	}
	if limit > remaining {
		return remaining
	}
	return limit
}

func (state *participantFrameState) appendSegment(pcm []byte, participantID string, segment speechSegment, startFrame, limit, frameBytes int) {
	for frameOffset := 0; frameOffset < limit; frameOffset++ {
		state.appendFrame(startFrame+frameOffset, participantID, segment.sequence, pcmFrame(pcm, state.cursor, frameBytes))
	}
}

func (state *participantFrameState) appendFrame(frame int, participantID string, sequence int64, pcm []byte) {
	state.frames = append(state.frames, frameContribution{frame: frame, value: contribution{sourceID: participantID, sequence: sequence, order: state.order, pcm: pcm}})
	state.order++
	state.cursor++
	if frame > state.lastScheduledFrame {
		state.lastScheduledFrame = frame
	}
}

func (state *participantFrameState) appendRemainder(pcm []byte, participantID string, segment speechSegment, frameDuration time.Duration, frameCount, frameBytes int) {
	if state.cursor >= frameCount {
		return
	}
	startFrame := frameIndex(segment.startNanos, frameDuration)
	if state.lastScheduledFrame >= startFrame {
		startFrame = state.lastScheduledFrame + 1
	}
	for state.cursor < frameCount {
		state.appendFrame(startFrame, participantID, segment.sequence, pcmFrame(pcm, state.cursor, frameBytes))
		startFrame++
	}
}

func assembleSchedule(contributionsByFrame map[int][]contribution, targetIDs []string, expectedFrames, maxFrame int) (roomreplay.Schedule, error) {
	if maxFrame >= roomReplayMaxScheduleFrames {
		return nil, scheduleTooLong(maxFrame)
	}
	totalFrames := max(expectedFrames, maxFrame+1)
	if totalFrames > roomReplayMaxScheduleFrames {
		return nil, scheduleTooLong(totalFrames)
	}
	if totalFrames == 0 {
		return nil, fmt.Errorf("%w: inbound captures have no replayable sent PCM", roomreplay.ErrSentPCMUnavailable)
	}
	frames := make([]scheduledFrame, totalFrames)
	for frameIndex, contributions := range contributionsByFrame {
		if frameIndex < 0 || frameIndex >= len(frames) {
			continue
		}
		sort.SliceStable(contributions, func(i, j int) bool {
			if contributions[i].sequence != contributions[j].sequence {
				return contributions[i].sequence < contributions[j].sequence
			}
			return contributions[i].order < contributions[j].order
		})
		frames[frameIndex].contributions = contributions
	}
	return &schedule{frames: frames, targetIDs: targetIDs}, nil
}

func frameIndex(offsetNanos int64, frameDuration time.Duration) int {
	if offsetNanos <= 0 || frameDuration <= 0 {
		return 0
	}
	return int(offsetNanos / int64(frameDuration))
}

func segmentFrameCount(startNanos, endNanos int64, frameDuration time.Duration) int {
	if endNanos <= startNanos || frameDuration <= 0 {
		return 1
	}
	frames := int(math.Ceil(float64(endNanos-startNanos) / float64(frameDuration)))
	if frames < 1 {
		return 1
	}
	return frames
}

func pcmFrame(pcm []byte, frameIndex, frameBytes int) []byte {
	frame := make([]byte, frameBytes)
	start := frameIndex * frameBytes
	if start >= len(pcm) {
		return frame
	}
	copy(frame, pcm[start:])
	return frame
}

func normalizePCM(pcm []byte, source roomreplay.SourcePCM16Format, target roomreplay.PCM16Format) ([]byte, error) {
	sourceChannels := source.Channels
	if sourceChannels <= 0 {
		sourceChannels = 1
	}
	sampleWidth := source.SampleWidthBits
	if sampleWidth == 0 {
		sampleWidth = source.SampleWidthBit
	}
	if sampleWidth != 16 || source.SampleRate <= 0 || sourceChannels <= 0 {
		return nil, fmt.Errorf("source is not signed PCM16")
	}
	if target.SampleRate <= 0 || target.Channels <= 0 {
		return nil, fmt.Errorf("target format is invalid")
	}
	if len(pcm)%(2*sourceChannels) != 0 {
		return nil, fmt.Errorf("PCM byte count %d is not aligned to %d channels", len(pcm), sourceChannels)
	}
	if len(pcm) == 0 {
		return []byte{}, nil
	}
	if source.SampleRate == target.SampleRate && sourceChannels == target.Channels {
		return append([]byte(nil), pcm...), nil
	}
	converted, err := audio.ConvertPCM16Bytes(pcm, sourceChannels, source.SampleRate, target.Channels, target.SampleRate)
	if err != nil {
		return nil, fmt.Errorf("convert PCM16: %w", err)
	}
	return converted, nil
}
