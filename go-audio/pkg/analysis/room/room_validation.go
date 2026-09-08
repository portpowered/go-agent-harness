package room

import (
	"fmt"
	"time"

	analysisstream "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/stream"
)

func normalizePCM16RoomAnalysisConfig(config PCM16RoomAnalysisConfig) (PCM16RoomAnalysisConfig, error) {
	streamConfig, err := analysisstream.NormalizePCM16AnalysisConfig(config.StreamConfig)
	if err != nil {
		return PCM16RoomAnalysisConfig{}, err
	}
	config.StreamConfig = streamConfig
	config = applyPCM16RoomAnalysisDefaults(config)
	if err := validatePCM16RoomAnalysisConfig(config); err != nil {
		return PCM16RoomAnalysisConfig{}, err
	}
	return config, nil
}

func normalizePCM16RoomInput(input PCM16RoomInput) (PCM16RoomInput, map[string]PCM16TimedStream, error) {
	normalized := clonePCM16RoomInput(input)
	streams, err := normalizePCM16RoomStreams(normalized.Streams)
	if err != nil {
		return PCM16RoomInput{}, nil, err
	}
	if err := normalizePCM16RoomAnnotations(&normalized, streams); err != nil {
		return PCM16RoomInput{}, nil, err
	}
	return normalized, streams, nil
}

func clonePCM16RoomInput(input PCM16RoomInput) PCM16RoomInput {
	input.Streams = append([]PCM16TimedStream(nil), input.Streams...)
	input.Overlaps = append([]PCM16OverlapInterval(nil), input.Overlaps...)
	input.BargeIns = append([]PCM16BargeInAnnotation(nil), input.BargeIns...)
	input.Loudness = append([]PCM16LoudnessInterval(nil), input.Loudness...)
	return input
}

func normalizePCM16RoomStreams(input []PCM16TimedStream) (map[string]PCM16TimedStream, error) {
	if len(input) == 0 {
		return nil, invalidPCM16RoomAnalysis("streams", "must not be empty")
	}
	streams := make(map[string]PCM16TimedStream, len(input))
	for index, timedStream := range input {
		if err := validatePCM16RoomStreamIdentity(timedStream, streams, index); err != nil {
			return nil, err
		}
		if err := validateTimedStream(timedStream, fmt.Sprintf("streams[%d]", index)); err != nil {
			return nil, err
		}
		streams[timedStream.StreamID] = timedStream
	}
	return streams, nil
}

func validatePCM16RoomStreamIdentity(timedStream PCM16TimedStream, streams map[string]PCM16TimedStream, index int) error {
	field := fmt.Sprintf("streams[%d]", index)
	if timedStream.StreamID == "" {
		return invalidPCM16RoomAnalysis(field+".stream_id", "must not be empty")
	}
	if timedStream.ParticipantID == "" {
		return invalidPCM16RoomAnalysis(field+".participant_id", "must not be empty")
	}
	if _, exists := streams[timedStream.StreamID]; exists {
		return invalidPCM16RoomAnalysis(field+".stream_id", fmt.Sprintf("duplicate stream identity %q", timedStream.StreamID))
	}
	return nil
}

func normalizePCM16RoomAnnotations(input *PCM16RoomInput, streams map[string]PCM16TimedStream) error {
	if err := normalizePCM16RoomOverlaps(input.Overlaps, streams); err != nil {
		return err
	}
	if err := normalizePCM16RoomBargeIns(input.BargeIns, streams); err != nil {
		return err
	}
	return normalizePCM16RoomLoudness(input.Loudness, streams)
}

func normalizePCM16RoomOverlaps(overlaps []PCM16OverlapInterval, streams map[string]PCM16TimedStream) error {
	for index := range overlaps {
		overlap := &overlaps[index]
		if overlap.ID == "" {
			overlap.ID = fmt.Sprintf("overlap-%d", index)
		}
		if err := validateOverlapInput(*overlap, streams, fmt.Sprintf("overlaps[%d]", index)); err != nil {
			return err
		}
	}
	return nil
}

func normalizePCM16RoomBargeIns(bargeIns []PCM16BargeInAnnotation, streams map[string]PCM16TimedStream) error {
	for index := range bargeIns {
		barge := &bargeIns[index]
		if barge.ID == "" {
			barge.ID = fmt.Sprintf("barge-in-%d", index)
		}
		if err := validateBargeInput(*barge, streams, fmt.Sprintf("barge_ins[%d]", index)); err != nil {
			return err
		}
	}
	return nil
}

func normalizePCM16RoomLoudness(intervals []PCM16LoudnessInterval, streams map[string]PCM16TimedStream) error {
	for index := range intervals {
		interval := &intervals[index]
		if interval.ID == "" {
			interval.ID = fmt.Sprintf("loudness-%d", index)
		}
		if err := validateLoudnessInput(*interval, streams, fmt.Sprintf("loudness[%d]", index)); err != nil {
			return err
		}
	}
	return nil
}

func validateTimedStream(stream PCM16TimedStream, field string) error {
	if stream.StreamID == "" {
		return invalidPCM16RoomAnalysis(field+".stream_id", "must not be empty")
	}
	if stream.ParticipantID == "" {
		return invalidPCM16RoomAnalysis(field+".participant_id", "must not be empty")
	}
	if stream.SampleRate <= 0 {
		return invalidPCM16RoomAnalysis(field+".sample_rate", "must be positive")
	}
	if len(stream.Samples) == 0 {
		return invalidPCM16RoomAnalysis(field+".samples", "must not be empty")
	}
	if stream.TimelineStart < 0 {
		return invalidPCM16RoomAnalysis(field+".timeline_start", "must not be negative")
	}
	if stream.TimelineEnd <= stream.TimelineStart {
		return invalidPCM16RoomAnalysis(field+".timeline_end", "must be after timeline_start")
	}
	return nil
}

func validateOverlapInput(overlap PCM16OverlapInterval, streams map[string]PCM16TimedStream, field string) error {
	if _, err := normalizeTimeInterval(overlap.PCM16TimeInterval, field); err != nil {
		return err
	}
	if overlap.A.ParticipantID == "" || overlap.B.ParticipantID == "" {
		return invalidPCM16RoomAnalysis(field, "both overlap participants must be identified")
	}
	if overlap.A.ParticipantID == overlap.B.ParticipantID {
		return invalidPCM16RoomAnalysis(field, "overlap participants must be distinct")
	}
	if err := validateEndpoint(overlap.A, streams, field+".a"); err != nil {
		return err
	}
	if err := validateEndpoint(overlap.B, streams, field+".b"); err != nil {
		return err
	}
	if overlap.A.SentStreamID == overlap.A.ReceivedStreamID || overlap.B.SentStreamID == overlap.B.ReceivedStreamID {
		return invalidPCM16RoomAnalysis(field, "sent and received evidence must have independent stream identities")
	}
	if streams[overlap.A.SentStreamID].SampleRate != streams[overlap.B.ReceivedStreamID].SampleRate || streams[overlap.B.SentStreamID].SampleRate != streams[overlap.A.ReceivedStreamID].SampleRate {
		return invalidPCM16RoomAnalysis(field+".sample_rate", "all overlap correlation pairs must use the same sample rate")
	}
	coverage := []struct {
		label    string
		streamID string
	}{
		{label: "a.sent_stream_id", streamID: overlap.A.SentStreamID},
		{label: "a.received_stream_id", streamID: overlap.A.ReceivedStreamID},
		{label: "b.sent_stream_id", streamID: overlap.B.SentStreamID},
		{label: "b.received_stream_id", streamID: overlap.B.ReceivedStreamID},
	}
	for _, entry := range coverage {
		if err := validateIntervalCoverage(streams[entry.streamID], overlap.PCM16TimeInterval, field+"."+entry.label); err != nil {
			return err
		}
	}
	return nil
}

func validateEndpoint(endpoint PCM16OverlapParticipant, streams map[string]PCM16TimedStream, field string) error {
	if endpoint.SentStreamID == "" || endpoint.ReceivedStreamID == "" {
		return invalidPCM16RoomAnalysis(field, "sent_stream_id and received_stream_id are required")
	}
	sent, exists := streams[endpoint.SentStreamID]
	if !exists {
		return invalidPCM16RoomAnalysis(field+".sent_stream_id", fmt.Sprintf("unknown stream %q", endpoint.SentStreamID))
	}
	received, exists := streams[endpoint.ReceivedStreamID]
	if !exists {
		return invalidPCM16RoomAnalysis(field+".received_stream_id", fmt.Sprintf("unknown stream %q", endpoint.ReceivedStreamID))
	}
	if sent.ParticipantID != endpoint.ParticipantID || received.ParticipantID != endpoint.ParticipantID {
		return invalidPCM16RoomAnalysis(field+".participant_id", fmt.Sprintf("sent and received streams must belong to participant %q", endpoint.ParticipantID))
	}
	return nil
}

func validateBargeInput(annotation PCM16BargeInAnnotation, streams map[string]PCM16TimedStream, field string) error {
	if _, err := normalizeTimeInterval(annotation.PCM16TimeInterval, field); err != nil {
		return err
	}
	if annotation.InterrupterStreamID == "" || annotation.InterruptedStreamID == "" {
		return invalidPCM16RoomAnalysis(field, "interrupter_stream_id and interrupted_stream_id are required")
	}
	if annotation.InterrupterStreamID == annotation.InterruptedStreamID {
		return invalidPCM16RoomAnalysis(field, "interrupter and interrupted streams must be distinct")
	}
	interrupter, exists := streams[annotation.InterrupterStreamID]
	if !exists {
		return invalidPCM16RoomAnalysis(field+".interrupter_stream_id", fmt.Sprintf("unknown stream %q", annotation.InterrupterStreamID))
	}
	interrupted, exists := streams[annotation.InterruptedStreamID]
	if !exists {
		return invalidPCM16RoomAnalysis(field+".interrupted_stream_id", fmt.Sprintf("unknown stream %q", annotation.InterruptedStreamID))
	}
	if err := validateIntervalCoverage(interrupter, annotation.PCM16TimeInterval, field+".interrupter_stream_id"); err != nil {
		return err
	}
	return validateIntervalCoverage(interrupted, annotation.PCM16TimeInterval, field+".interrupted_stream_id")
}

func validateLoudnessInput(interval PCM16LoudnessInterval, streams map[string]PCM16TimedStream, field string) error {
	if _, err := normalizeTimeInterval(interval.PCM16TimeInterval, field); err != nil {
		return err
	}
	if interval.LeftStreamID == "" || interval.RightStreamID == "" {
		return invalidPCM16RoomAnalysis(field, "left_stream_id and right_stream_id are required")
	}
	if interval.LeftStreamID == interval.RightStreamID {
		return invalidPCM16RoomAnalysis(field, "left and right streams must be distinct")
	}
	left, exists := streams[interval.LeftStreamID]
	if !exists {
		return invalidPCM16RoomAnalysis(field+".left_stream_id", fmt.Sprintf("unknown stream %q", interval.LeftStreamID))
	}
	right, exists := streams[interval.RightStreamID]
	if !exists {
		return invalidPCM16RoomAnalysis(field+".right_stream_id", fmt.Sprintf("unknown stream %q", interval.RightStreamID))
	}
	if err := validateIntervalCoverage(left, interval.PCM16TimeInterval, field+".left_stream_id"); err != nil {
		return err
	}
	return validateIntervalCoverage(right, interval.PCM16TimeInterval, field+".right_stream_id")
}

func validateIntervalCoverage(stream PCM16TimedStream, interval PCM16TimeInterval, field string) error {
	if interval.Start < stream.TimelineStart || interval.End > stream.TimelineEnd {
		return invalidPCM16RoomAnalysis(field, fmt.Sprintf("interval %q is outside stream timeline %s..%s", interval.ID, stream.TimelineStart, stream.TimelineEnd))
	}
	if _, _, err := sampleRangeForInterval(stream, interval); err != nil {
		return invalidPCM16RoomAnalysis(field, err.Error())
	}
	return nil
}

func normalizeTimeInterval(interval PCM16TimeInterval, field string) (PCM16TimeInterval, error) {
	if interval.ID == "" {
		interval.ID = field
	}
	if interval.Start < 0 {
		return PCM16TimeInterval{}, invalidPCM16RoomAnalysis(field+".start", "must not be negative")
	}
	if interval.End <= interval.Start {
		return PCM16TimeInterval{}, invalidPCM16RoomAnalysis(field+".end", "must be after start")
	}
	return interval, nil
}

func sampleRangeForInterval(stream PCM16TimedStream, interval PCM16TimeInterval) (int, int, error) {
	start, err := sampleIndexAt(stream, interval.Start)
	if err != nil {
		return 0, 0, err
	}
	end, err := sampleIndexAt(stream, interval.End)
	if err != nil {
		return 0, 0, err
	}
	if end <= start || start < 0 || end > len(stream.Samples) {
		return 0, 0, invalidPCM16RoomAnalysis("interval", fmt.Sprintf("sample range %d..%d is outside stream %q", start, end, stream.StreamID))
	}
	return start, end, nil
}

func sampleIndexAt(timed PCM16TimedStream, timestamp time.Duration) (int, error) {
	offset := timestamp - timed.TimelineStart
	if offset < 0 {
		return 0, invalidPCM16RoomAnalysis("timestamp", fmt.Sprintf("%s precedes stream %q timeline start %s", timestamp, timed.StreamID, timed.TimelineStart))
	}
	index, err := analysisstream.PCM16DurationToSamples(offset, timed.SampleRate)
	if err != nil {
		return 0, invalidPCM16RoomAnalysis("timestamp", err.Error())
	}
	if index < 0 || index > len(timed.Samples) {
		return 0, invalidPCM16RoomAnalysis("timestamp", fmt.Sprintf("%s maps to sample %d outside stream %q sample count %d", timestamp, index, timed.StreamID, len(timed.Samples)))
	}
	return index, nil
}

func signedDurationToSamples(duration time.Duration, sampleRate int) (int, error) {
	return analysisstream.PCM16SignedDurationToSamples(duration, sampleRate)
}

func samplesToSignedDuration(samples, sampleRate int) time.Duration {
	return analysisstream.PCM16SamplesToSignedDuration(samples, sampleRate)
}

func samplesToDuration(samples, sampleRate int) time.Duration {
	return analysisstream.PCM16SamplesToDuration(samples, sampleRate)
}

func isFinite(value float64) bool { return analysisstream.PCM16IsFinite(value) }

func analysisFailure(property, streamID, participantID string) PropertyFailure {
	return PropertyFailure{
		Property:      property,
		StreamID:      streamID,
		ParticipantID: participantID,
		StartSample:   -1,
		EndSample:     -1,
		SampleIndex:   -1,
		FrameIndex:    -1,
		BoundaryIndex: -1,
	}
}

func invalidPCM16Analysis(field, reason string) error {
	return &analysisstream.InvalidPCM16AnalysisInputError{Field: field, Reason: reason}
}

// forEachNormalizedPCM16CorrelationCandidate visits the same lag candidates
// used by NormalizedPCM16CrossCorrelation without allocating one result per
// lag. Streaming detectors use this shared primitive to ignore high
// correlations that only have a short overlap and to require a minimum
// evidence duration before classifying feedback.
