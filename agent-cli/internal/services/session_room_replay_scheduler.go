package services

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// roomReplaySchedule is the one room-owned clock for a bundle replay. It is
// built before any mixer frame is released, so participant goroutines only
// consume frames and never make cross-participant timing decisions.
type roomReplaySchedule struct {
	frameDuration time.Duration
	frameBytes    int
	frames        []roomReplayScheduledFrame
	targetIDs     []string
}

type roomReplayScheduledFrame struct {
	contributions []roomReplayContribution
}

type roomReplayContribution struct {
	sourceID string
	sequence int64
	order    int
	pcm      []byte
}

type roomReplaySpeechSegment struct {
	startNanos int64
	endNanos   int64
	hasEnd     bool
	sequence   int64
}

// newRoomReplaySchedule loads only validated, bundle-relative PCM and strict
// capture metadata. A schedule is unnecessary for text-only captures that
// contain no provider audio-input appends; retaining that no-audio path keeps
// older finalized bundles compatible while audio bundles use the deterministic
// room clock below.
func newRoomReplaySchedule(ctx context.Context, replay RoomReplayPlan, plans []*roomParticipantPlan, format room.PCM16Format) (*roomReplaySchedule, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if format == (room.PCM16Format{}) {
		format = room.DefaultPCM16Format()
	}
	frameBytes, err := format.FrameBytes()
	if err != nil {
		return nil, fmt.Errorf("room replay mixer format: %w", err)
	}

	recordedByID := make(map[string]RoomReplayParticipant, len(replay.Participants))
	for _, participant := range replay.Participants {
		recordedByID[participant.ID] = participant
	}

	targetIDs := make([]string, 0, len(plans))
	expectedFrames := 0
	hasRecordedInboundAudio := false
	for _, plan := range plans {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if plan == nil || roomParticipantIsHuman(plan) {
			continue
		}
		recorded, ok := recordedByID[plan.manifest.ID]
		if !ok || strings.TrimSpace(recorded.CapturePath) == "" {
			return nil, fmt.Errorf("room replay participant %q has no recorded capture", plan.manifest.ID)
		}
		capture, captureErr := gwtesting.LoadSessionCapture(recorded.CapturePath)
		if captureErr != nil {
			return nil, fmt.Errorf("load room replay participant %q capture: %w", plan.manifest.ID, captureErr)
		}
		appendCount := 0
		for _, record := range capture.Records {
			if record.Direction == gwtesting.DirectionClientToServer && strings.EqualFold(strings.TrimSpace(record.Type), inputAudioBufferAppendEventType) {
				appendCount++
			}
		}
		targetIDs = append(targetIDs, plan.manifest.ID)
		if appendCount > 0 {
			hasRecordedInboundAudio = true
		}
		if appendCount > expectedFrames {
			expectedFrames = appendCount
		}
	}
	if len(targetIDs) == 0 || !hasRecordedInboundAudio {
		return nil, nil
	}

	contributionsByFrame := make(map[int][]roomReplayContribution)
	maxFrame := -1
	order := 0
	for _, participant := range replay.Participants {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		artifact, ok := roomReplayParticipantArtifact(participant, roomReplayArtifactRoleSentPCM)
		if !ok {
			return nil, fmt.Errorf("room replay participant %q has no sent PCM artifact", participant.ID)
		}
		pcm, readErr := os.ReadFile(artifact.AbsolutePath)
		if readErr != nil {
			return nil, fmt.Errorf("read room replay participant %q sent PCM: %w", participant.ID, readErr)
		}
		pcm, readErr = normalizeRoomReplayPCM(pcm, replay.PCMFormat, format)
		if readErr != nil {
			return nil, fmt.Errorf("normalize room replay participant %q sent PCM: %w", participant.ID, readErr)
		}
		if len(pcm) == 0 {
			continue
		}
		segments := roomReplaySpeechSegments(replay.Timeline, participant.ID)
		if len(segments) == 0 {
			segments = []roomReplaySpeechSegment{{sequence: math.MaxInt64}}
		}
		frames := (len(pcm) + frameBytes - 1) / frameBytes
		cursor := 0
		lastScheduledFrame := -1
		for segmentIndex, segment := range segments {
			if cursor >= frames {
				break
			}
			startFrame := roomReplayFrameIndex(segment.startNanos, format.FrameDuration)
			limit := frames - cursor
			if segment.hasEnd {
				limit = roomReplaySegmentFrameCount(segment.startNanos, segment.endNanos, format.FrameDuration)
			} else if segmentIndex+1 < len(segments) {
				limit = roomReplaySegmentFrameCount(segment.startNanos, segments[segmentIndex+1].startNanos, format.FrameDuration)
			}
			if limit < 1 {
				limit = 1
			}
			if limit > frames-cursor {
				limit = frames - cursor
			}
			for frameOffset := 0; frameOffset < limit; frameOffset++ {
				frameIndex := startFrame + frameOffset
				contribution := roomReplayContribution{
					sourceID: participant.ID,
					sequence: segment.sequence,
					order:    order,
					pcm:      roomReplayPCMFrame(pcm, cursor, frameBytes),
				}
				order++
				contributionsByFrame[frameIndex] = append(contributionsByFrame[frameIndex], contribution)
				if frameIndex > maxFrame {
					maxFrame = frameIndex
				}
				if frameIndex > lastScheduledFrame {
					lastScheduledFrame = frameIndex
				}
				cursor++
			}
		}
		if cursor < frames {
			// If the final speech boundary was shorter than the recorded PCM,
			// keep the remaining bytes contiguous after that boundary. This is
			// deterministic and avoids silently dropping a provider delta.
			startFrame := roomReplayFrameIndex(segments[len(segments)-1].startNanos, format.FrameDuration)
			if lastScheduledFrame >= startFrame {
				startFrame = lastScheduledFrame + 1
			}
			for cursor < frames {
				contribution := roomReplayContribution{
					sourceID: participant.ID,
					sequence: segments[len(segments)-1].sequence,
					order:    order,
					pcm:      roomReplayPCMFrame(pcm, cursor, frameBytes),
				}
				order++
				contributionsByFrame[startFrame] = append(contributionsByFrame[startFrame], contribution)
				if startFrame > maxFrame {
					maxFrame = startFrame
				}
				startFrame++
				cursor++
			}
		}
	}

	totalFrames := expectedFrames
	if maxFrame+1 > totalFrames {
		totalFrames = maxFrame + 1
	}
	if totalFrames == 0 {
		return nil, fmt.Errorf("room replay contains inbound audio captures but no replayable sent PCM")
	}
	frames := make([]roomReplayScheduledFrame, totalFrames)
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
	return &roomReplaySchedule{frameDuration: format.FrameDuration, frameBytes: frameBytes, frames: frames, targetIDs: targetIDs}, nil
}

// run releases all source contributions for one logical frame before
// advancing any target mixer. Target acknowledgements make the next logical
// frame wait until the previous mixed frame has entered the provider session,
// giving strict replay a deterministic outbound order independent of host
// goroutine scheduling.
func (s *roomReplaySchedule) run(ctx context.Context, runtimes []*roomParticipantRuntime, coordinator *roomCoordinator, opts RoomRunOptions) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	byID := make(map[string]*roomParticipantRuntime, len(runtimes))
	for _, runtime := range runtimes {
		if runtime != nil && runtime.plan != nil {
			byID[runtime.plan.manifest.ID] = runtime
		}
	}
	for _, targetID := range s.targetIDs {
		target := byID[targetID]
		if target == nil || target.mixer == nil || target.replayFrameAcks == nil {
			return fmt.Errorf("room replay target %q is not scheduler-controlled", targetID)
		}
	}

	for frameIndex, frame := range s.frames {
		if coordinator != nil && coordinator.isStopping() {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, contribution := range frame.contributions {
			for _, targetID := range s.targetIDs {
				if targetID == contribution.sourceID {
					continue
				}
				target := byID[targetID]
				if target == nil || target.mixer == nil {
					return fmt.Errorf("room replay frame %d target %q is missing", frameIndex, targetID)
				}
				if coordinator != nil && !coordinator.isActive(targetID) {
					if coordinator.isStopping() {
						return nil
					}
					return fmt.Errorf("room replay target %q terminated before logical frame %d", targetID, frameIndex)
				}
				if err := target.mixer.WriteContext(ctx, contribution.sourceID, contribution.pcm); err != nil {
					if coordinator != nil && coordinator.isStopping() {
						return nil
					}
					return fmt.Errorf("release room replay frame %d from %q to %q: %w", frameIndex, contribution.sourceID, targetID, err)
				}
				if opts.onParticipantAudioFanned != nil {
					opts.onParticipantAudioFanned(contribution.sourceID, targetID, append([]byte(nil), contribution.pcm...))
				}
			}
		}
		for _, targetID := range s.targetIDs {
			target := byID[targetID]
			if err := target.mixer.Advance(ctx); err != nil {
				if coordinator != nil && coordinator.isStopping() {
					return nil
				}
				return fmt.Errorf("advance room replay mixer for %q at logical frame %d: %w", targetID, frameIndex, err)
			}
			select {
			case <-target.replayFrameAcks:
			case <-target.ctx.Done():
				if coordinator != nil && coordinator.isStopping() {
					return nil
				}
				return fmt.Errorf("room replay target %q stopped before accepting logical frame %d", targetID, frameIndex)
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	// The acknowledgement barrier above drains the mixer queues. Participant
	// completion is the final provider-side barrier: a valid capture can finish
	// its response/session-close events before the room is explicitly stopped.
	for _, runtime := range runtimes {
		if runtime == nil || runtime.participantDone == nil {
			continue
		}
		select {
		case <-runtime.participantDone:
		case <-ctx.Done():
			if coordinator != nil && coordinator.isStopping() {
				return nil
			}
			return ctx.Err()
		}
	}
	return nil
}

func roomReplaySpeechSegments(timeline []RoomReplayTimelineEvent, participantID string) []roomReplaySpeechSegment {
	segments := make([]roomReplaySpeechSegment, 0)
	open := make([]int, 0, 1)
	for _, event := range timeline {
		if event.ParticipantID != participantID {
			continue
		}
		eventType := normalizeRoomReplayEventType(event.Type)
		offsetNanos := event.OffsetNanos
		if offsetNanos == 0 && event.OffsetMS != 0 {
			offsetNanos = event.OffsetMS * int64(time.Millisecond)
		}
		switch eventType {
		case "speech_start", "audio_start", "response_audio_start", "output_audio_start", "speaking_start":
			segments = append(segments, roomReplaySpeechSegment{startNanos: offsetNanos, sequence: event.Sequence})
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

func normalizeRoomReplayEventType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, ".", "_")
	value = strings.ReplaceAll(value, "-", "_")
	return value
}

func roomReplayFrameIndex(offsetNanos int64, frameDuration time.Duration) int {
	if offsetNanos <= 0 || frameDuration <= 0 {
		return 0
	}
	return int(offsetNanos / int64(frameDuration))
}

func roomReplaySegmentFrameCount(startNanos, endNanos int64, frameDuration time.Duration) int {
	if endNanos <= startNanos || frameDuration <= 0 {
		return 1
	}
	frames := int(math.Ceil(float64(endNanos-startNanos) / float64(frameDuration)))
	if frames < 1 {
		return 1
	}
	return frames
}

func roomReplayPCMFrame(pcm []byte, frameIndex, frameBytes int) []byte {
	frame := make([]byte, frameBytes)
	start := frameIndex * frameBytes
	if start >= len(pcm) {
		return frame
	}
	copy(frame, pcm[start:])
	return frame
}

func normalizeRoomReplayPCM(pcm []byte, source RoomReplayPCMFormat, target room.PCM16Format) ([]byte, error) {
	sourceChannels := source.Channels
	if sourceChannels <= 0 {
		sourceChannels = 1
	}
	sampleWidth := source.SampleWidthBits
	if sampleWidth == 0 {
		sampleWidth = source.SampleWidthBit
	}
	if sampleWidth != 16 || source.SampleRate <= 0 || sourceChannels <= 0 {
		return nil, errors.New("room replay PCM format is not signed PCM16")
	}
	if target.SampleRate <= 0 || target.Channels <= 0 {
		return nil, errors.New("room replay target PCM format is invalid")
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
	sourceFrames := len(pcm) / (2 * sourceChannels)
	outputFrames := int(math.Ceil(float64(sourceFrames) * float64(target.SampleRate) / float64(source.SampleRate)))
	if outputFrames < 1 {
		outputFrames = 1
	}
	output := make([]byte, outputFrames*target.Channels*2)
	readSample := func(frame, channel int) int16 {
		index := (frame*sourceChannels + channel) * 2
		return int16(binary.LittleEndian.Uint16(pcm[index : index+2]))
	}
	for outputFrame := 0; outputFrame < outputFrames; outputFrame++ {
		position := float64(outputFrame) * float64(source.SampleRate) / float64(target.SampleRate)
		lower := int(position)
		if lower >= sourceFrames {
			lower = sourceFrames - 1
		}
		upper := lower + 1
		if upper >= sourceFrames {
			upper = sourceFrames - 1
		}
		fraction := position - float64(lower)
		for targetChannel := 0; targetChannel < target.Channels; targetChannel++ {
			value := float64(0)
			if sourceChannels == 1 {
				value = float64(readSample(lower, 0)) + (float64(readSample(upper, 0))-float64(readSample(lower, 0)))*fraction
			} else if target.Channels == 1 {
				var sumLower, sumUpper int64
				for sourceChannel := 0; sourceChannel < sourceChannels; sourceChannel++ {
					sumLower += int64(readSample(lower, sourceChannel))
					sumUpper += int64(readSample(upper, sourceChannel))
				}
				value = float64(sumLower)/float64(sourceChannels) + (float64(sumUpper)/float64(sourceChannels)-float64(sumLower)/float64(sourceChannels))*fraction
			} else {
				sourceChannel := targetChannel
				if sourceChannel >= sourceChannels {
					sourceChannel = sourceChannels - 1
				}
				value = float64(readSample(lower, sourceChannel)) + (float64(readSample(upper, sourceChannel))-float64(readSample(lower, sourceChannel)))*fraction
			}
			if value > 32767 {
				value = 32767
			} else if value < -32768 {
				value = -32768
			}
			outputIndex := (outputFrame*target.Channels + targetChannel) * 2
			binary.LittleEndian.PutUint16(output[outputIndex:outputIndex+2], uint16(int16(math.Round(value))))
		}
	}
	return output, nil
}
