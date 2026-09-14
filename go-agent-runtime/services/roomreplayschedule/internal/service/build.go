package service

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplayschedule"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
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

type speechSegment struct {
	startNanos int64
	endNanos   int64
	hasEnd     bool
	sequence   int64
}

// Build admits capture barriers and file-backed sent PCM before producing an
// immutable schedule. Text-only captures intentionally return a nil schedule.
func (*Service) Build(ctx context.Context, request roomreplayschedule.BuildRequest) (roomreplayschedule.Schedule, error) {
	ctx = nonNilContext(ctx)
	targetFormat, frameBytes, err := normalizeTargetFormat(request.TargetFormat)
	if err != nil {
		return nil, err
	}
	participants, err := participantIndex(request.Participants)
	if err != nil {
		return nil, err
	}
	targetIDs, expectedFrames, hasInboundAudio, err := inspectTargets(ctx, request.TargetIDs, participants)
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

func normalizeTargetFormat(format roomreplayschedule.PCM16Format) (roomreplayschedule.PCM16Format, int, error) {
	if format == (roomreplayschedule.PCM16Format{}) {
		format = roomreplayschedule.PCM16Format{SampleRate: defaultSampleRate, Channels: defaultChannels, FrameDuration: defaultFrameDuration}
	}
	frameBytes, err := audio.PCM16FrameBytes(format.SampleRate, format.Channels, format.FrameDuration)
	if err != nil {
		return roomreplayschedule.PCM16Format{}, 0, fmt.Errorf("%w: %w", roomreplayschedule.ErrInvalidFormat, err)
	}
	return format, frameBytes, nil
}

func participantIndex(participants []roomreplayschedule.Participant) (map[string]roomreplayschedule.Participant, error) {
	index := make(map[string]roomreplayschedule.Participant, len(participants))
	for _, participant := range participants {
		id := strings.TrimSpace(participant.ID)
		if id == "" {
			return nil, fmt.Errorf("%w: participant ID is empty", roomreplayschedule.ErrInvalidRequest)
		}
		if _, exists := index[id]; exists {
			return nil, fmt.Errorf("%w: duplicate participant %q", roomreplayschedule.ErrInvalidRequest, id)
		}
		participant.ID = id
		index[id] = participant
	}
	return index, nil
}

func inspectTargets(ctx context.Context, ids []string, participants map[string]roomreplayschedule.Participant) ([]string, int, bool, error) {
	targetIDs := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	expectedFrames := 0
	hasInboundAudio := false
	for _, rawID := range ids {
		id, appendCount, err := inspectTarget(ctx, rawID, seen, participants)
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

func inspectTarget(ctx context.Context, rawID string, seen map[string]struct{}, participants map[string]roomreplayschedule.Participant) (string, int, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	id := strings.TrimSpace(rawID)
	if id == "" {
		return "", 0, fmt.Errorf("%w: target ID is empty", roomreplayschedule.ErrInvalidRequest)
	}
	if _, exists := seen[id]; exists {
		return "", 0, fmt.Errorf("%w: duplicate target %q", roomreplayschedule.ErrInvalidRequest, id)
	}
	seen[id] = struct{}{}
	participant, ok := participants[id]
	if !ok {
		return "", 0, fmt.Errorf("%w: %q", roomreplayschedule.ErrParticipantMissing, id)
	}
	if strings.TrimSpace(participant.CapturePath) == "" {
		return "", 0, fmt.Errorf("%w for participant %q", roomreplayschedule.ErrCaptureUnavailable, id)
	}
	capture, err := gwtesting.LoadSessionCapture(participant.CapturePath)
	if err != nil {
		return "", 0, fmt.Errorf("%w for participant %q: %w", roomreplayschedule.ErrCaptureUnavailable, id, err)
	}
	return id, inboundAudioAppendCount(capture), nil
}

func inboundAudioAppendCount(capture gwtesting.SessionCapture) int {
	count := 0
	for _, record := range capture.Records {
		if record.Direction == gwtesting.DirectionClientToServer && strings.EqualFold(strings.TrimSpace(record.Type), "input_audio_buffer.append") {
			count++
		}
	}
	return count
}

func buildParticipantContributions(ctx context.Context, request roomreplayschedule.BuildRequest, targetFormat roomreplayschedule.PCM16Format, frameBytes int) (map[int][]contribution, int, error) {
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

func scheduleParticipant(ctx context.Context, participant roomreplayschedule.Participant, request roomreplayschedule.BuildRequest, targetFormat roomreplayschedule.PCM16Format, frameBytes, order int) ([]frameContribution, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, order, err
	}
	if strings.TrimSpace(participant.ID) == "" {
		return nil, order, fmt.Errorf("%w: participant ID is empty", roomreplayschedule.ErrInvalidRequest)
	}
	if strings.TrimSpace(participant.SentPCMPath) == "" {
		return nil, order, fmt.Errorf("%w: participant %q has no sent PCM path", roomreplayschedule.ErrSentPCMUnavailable, participant.ID)
	}
	pcm, err := os.ReadFile(participant.SentPCMPath)
	if err != nil {
		return nil, order, fmt.Errorf("%w for participant %q: %w", roomreplayschedule.ErrSentPCMUnavailable, participant.ID, err)
	}
	pcm, err = normalizePCM(pcm, request.SourceFormat, targetFormat)
	if err != nil {
		return nil, order, fmt.Errorf("%w for participant %q: %w", roomreplayschedule.ErrInvalidPCM, participant.ID, err)
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

func scheduleParticipantFrames(pcm []byte, participantID string, segments []speechSegment, targetFormat roomreplayschedule.PCM16Format, frameBytes, order int) ([]frameContribution, int, error) {
	frameCount := (len(pcm)-1)/frameBytes + 1
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

func assembleSchedule(contributionsByFrame map[int][]contribution, targetIDs []string, expectedFrames, maxFrame int) (roomreplayschedule.Schedule, error) {
	totalFrames := expectedFrames
	if maxFrame+1 > totalFrames {
		totalFrames = maxFrame + 1
	}
	if totalFrames == 0 {
		return nil, fmt.Errorf("%w: inbound captures have no replayable sent PCM", roomreplayschedule.ErrSentPCMUnavailable)
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

func speechSegments(timeline []roomreplayschedule.TimelineEvent, participantID string) []speechSegment {
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

func normalizePCM(pcm []byte, source roomreplayschedule.SourcePCM16Format, target roomreplayschedule.PCM16Format) ([]byte, error) {
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
