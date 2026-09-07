package room

import (
	"errors"
	"fmt"
	"math"
	"time"
)

const (
	pcm16FullScale           = 32768.0
	pcm16DBFSDecibels        = 20
	durationUnitMilliseconds = "milliseconds"
)

func NormalizedPCM16CrossCorrelation(source, received PCM16TimedStream, interval PCM16TimeInterval, lagWindow PCM16LagWindow, silenceFloorDBFS float64) (PCM16CorrelationMeasurement, error) {
	prepared, err := preparePCM16Correlation(source, received, interval, lagWindow, silenceFloorDBFS)
	if err != nil {
		return PCM16CorrelationMeasurement{}, err
	}
	return scanPCM16Correlation(prepared), nil
}

// MeasurePCM16Correlation is a concise alias for the explicit correlation
// primitive.
func MeasurePCM16Correlation(source, received PCM16TimedStream, interval PCM16TimeInterval, lagWindow PCM16LagWindow, silenceFloorDBFS float64) (PCM16CorrelationMeasurement, error) {
	return NormalizedPCM16CrossCorrelation(source, received, interval, lagWindow, silenceFloorDBFS)
}

// MeasurePCM16Drift measures sample duration versus the declared timeline
// span for one stream. It does not apply a bound; room analysis does that
// using the configured max(absolute, fractional) rule.
func MeasurePCM16Drift(stream PCM16TimedStream) (PCM16DriftMeasurement, error) {
	if err := validateTimedStream(stream, "stream"); err != nil {
		return PCM16DriftMeasurement{}, err
	}
	sampleDuration := samplesToDuration(len(stream.Samples), stream.SampleRate)
	timestampSpan := stream.TimelineEnd - stream.TimelineStart
	drift := absoluteDurationDifference(sampleDuration, timestampSpan)
	return PCM16DriftMeasurement{
		StreamID:       stream.StreamID,
		ParticipantID:  stream.ParticipantID,
		SampleDuration: sampleDuration,
		TimestampSpan:  timestampSpan,
		Drift:          drift,
	}, nil
}

// MeasurePCM16Loudness measures active-speech RMS for two streams over an
// explicit interval. ExpectedSpeech annotations narrow each stream to its
// annotated active regions; absent annotations mean the whole interval is
// considered active evidence.
func MeasurePCM16Loudness(left, right PCM16TimedStream, interval PCM16TimeInterval) (PCM16LoudnessMeasurement, error) {
	if err := validateTimedStream(left, "left"); err != nil {
		return PCM16LoudnessMeasurement{}, err
	}
	if err := validateTimedStream(right, "right"); err != nil {
		return PCM16LoudnessMeasurement{}, err
	}
	if left.StreamID == right.StreamID {
		return PCM16LoudnessMeasurement{}, invalidPCM16RoomAnalysis("loudness", "left and right streams must be independently identified")
	}
	interval, err := normalizeTimeInterval(interval, "interval")
	if err != nil {
		return PCM16LoudnessMeasurement{}, err
	}
	leftRMS, leftCount, err := activeRMS(left, interval)
	if err != nil {
		return PCM16LoudnessMeasurement{}, err
	}
	rightRMS, rightCount, err := activeRMS(right, interval)
	if err != nil {
		return PCM16LoudnessMeasurement{}, err
	}
	leftDBFS := dbfs(leftRMS)
	rightDBFS := dbfs(rightRMS)
	difference := math.Abs(leftDBFS - rightDBFS)
	if math.IsInf(leftDBFS, 0) && math.IsInf(rightDBFS, 0) && leftDBFS == rightDBFS {
		difference = 0
	}
	return PCM16LoudnessMeasurement{
		IntervalID:    interval.ID,
		LeftStreamID:  left.StreamID,
		RightStreamID: right.StreamID,
		Start:         interval.Start,
		End:           interval.End,
		LeftRMS:       leftRMS,
		LeftRMSDBFS:   leftDBFS,
		LeftSamples:   leftCount,
		RightRMS:      rightRMS,
		RightRMSDBFS:  rightDBFS,
		RightSamples:  rightCount,
		DifferenceDB:  difference,
	}, nil
}

func normalizeBargeAnnotation(annotation PCM16BargeInAnnotation) (PCM16BargeInAnnotation, error) {
	interval, err := normalizeTimeInterval(annotation.PCM16TimeInterval, "barge_in")
	if err != nil {
		return PCM16BargeInAnnotation{}, err
	}
	annotation.PCM16TimeInterval = interval
	return annotation, nil
}

func appendCorrelationFailures(failures *[]PropertyFailure, measurement PCM16PeerDeliveryMeasurement, bound float64) {
	if measurement.Passed {
		return
	}
	failure := analysisFailure("overlap-delivery", measurement.ReceivedStreamID, measurement.ReceivedParticipantID)
	failure.SourceStreamID = measurement.SourceStreamID
	failure.ReceivedStreamID = measurement.ReceivedStreamID
	failure.Direction = measurement.Direction
	failure.Interval = measurement.IntervalID
	failure.Timestamp = measurement.Start
	failure.Lag = measurement.BestLag
	failure.Measured = measurement.BestCorrelation
	failure.Comparison = "<"
	failure.Bound = bound
	failure.Unit = "normalized correlation"
	failure.Detail = fmt.Sprintf("best-lag=%s compared-samples=%d", measurement.BestLag, measurement.ComparedSamples)
	if !measurement.HasEvidence() {
		failure.Detail += "; no non-silent source/received sample pairs"
	}
	*failures = append(*failures, failure)
}

func appendSelfHearingFailures(failures *[]PropertyFailure, measurement PCM16SelfHearingMeasurement, bound float64) {
	if measurement.Passed {
		return
	}
	failure := analysisFailure("self-hearing", measurement.ReceivedStreamID, measurement.ReceivedParticipantID)
	failure.SourceStreamID = measurement.SourceStreamID
	failure.ReceivedStreamID = measurement.ReceivedStreamID
	failure.Direction = measurement.Direction
	failure.Interval = measurement.IntervalID
	failure.Timestamp = measurement.Start
	failure.Lag = measurement.BestAbsoluteLag
	failure.Measured = measurement.BestAbsoluteCorrelation
	failure.Comparison = ">="
	failure.Bound = bound
	failure.Unit = "absolute normalized correlation"
	failure.Detail = fmt.Sprintf("signed-best-lag=%s absolute-best-lag=%s compared-samples=%d", measurement.BestLag, measurement.BestAbsoluteLag, measurement.ComparedSamples)
	*failures = append(*failures, failure)
}

func appendLoudnessFailure(failures *[]PropertyFailure, measurement PCM16LoudnessMeasurement, bound float64) {
	if measurement.Passed {
		return
	}
	failure := analysisFailure("inter-speaker-loudness", measurement.RightStreamID, "")
	failure.SourceStreamID = measurement.LeftStreamID
	failure.ReceivedStreamID = measurement.RightStreamID
	failure.Direction = fmt.Sprintf("%s-vs-%s", measurement.LeftStreamID, measurement.RightStreamID)
	failure.Interval = measurement.IntervalID
	failure.Timestamp = measurement.Start
	failure.Measured = measurement.DifferenceDB
	failure.Comparison = ">"
	failure.Bound = bound
	failure.Unit = "dB"
	failure.Detail = fmt.Sprintf("left=%.3f dBFS right=%.3f dBFS samples=%d/%d", measurement.LeftRMSDBFS, measurement.RightRMSDBFS, measurement.LeftSamples, measurement.RightSamples)
	*failures = append(*failures, failure)
}

func appendBargeInFailures(failures *[]PropertyFailure, measurement PCM16BargeInMeasurement, interrupter, interrupted PCM16TimedStream, thresholdDBFS float64, maxLatency time.Duration) {
	if !measurement.InterrupterOnsetFound {
		failure := analysisFailure("barge-in-onset", interrupter.StreamID, interrupter.ParticipantID)
		failure.SourceStreamID = interrupter.StreamID
		failure.ReceivedStreamID = interrupted.StreamID
		failure.Direction = fmt.Sprintf("%s->%s", interrupter.ParticipantID, interrupted.ParticipantID)
		failure.Interval = measurement.ID
		failure.Timestamp = measurement.Start
		failure.Measured = math.Inf(-1)
		failure.Comparison = ">"
		failure.Bound = thresholdDBFS
		failure.Unit = "dBFS"
		failure.Detail = "no interrupter 20 ms frame exceeded the activity threshold"
		*failures = append(*failures, failure)
		return
	}
	if !measurement.InterruptedStopFound {
		failure := analysisFailure("barge-in-stop", interrupted.StreamID, interrupted.ParticipantID)
		failure.SourceStreamID = interrupter.StreamID
		failure.ReceivedStreamID = interrupted.StreamID
		failure.Direction = fmt.Sprintf("%s->%s", interrupter.ParticipantID, interrupted.ParticipantID)
		failure.Interval = measurement.ID
		failure.Timestamp = measurement.InterrupterOnset
		failure.Measured = math.Inf(1)
		failure.Comparison = ">"
		failure.Bound = durationMilliseconds(maxLatency)
		failure.Unit = durationUnitMilliseconds
		failure.Detail = fmt.Sprintf("no interrupted-stream stop was observed before interval end %s; last-active=%s", measurement.End, measurement.InterruptedLastActive)
		*failures = append(*failures, failure)
		return
	}
	if measurement.Latency > maxLatency {
		failure := analysisFailure("barge-in-latency", interrupted.StreamID, interrupted.ParticipantID)
		failure.SourceStreamID = interrupter.StreamID
		failure.ReceivedStreamID = interrupted.StreamID
		failure.Direction = fmt.Sprintf("%s->%s", interrupter.ParticipantID, interrupted.ParticipantID)
		failure.Interval = measurement.ID
		failure.Timestamp = measurement.InterrupterOnset
		failure.Measured = durationMilliseconds(measurement.Latency)
		failure.Comparison = ">"
		failure.Bound = durationMilliseconds(maxLatency)
		failure.Unit = durationUnitMilliseconds
		failure.Detail = fmt.Sprintf("interrupted frame=%d stopped at %s", measurement.InterruptedFrameIndex, measurement.InterruptedLastActive)
		*failures = append(*failures, failure)
	}
}

func roomFailure(property string, stream PCM16TimedStream, direction string) PropertyFailure {
	failure := analysisFailure(property, stream.StreamID, stream.ParticipantID)
	failure.Direction = direction
	return failure
}

func configuredDriftBound(sampleDuration, timestampSpan time.Duration, config PCM16RoomAnalysisConfig) time.Duration {
	duration := sampleDuration
	if timestampSpan > duration {
		duration = timestampSpan
	}
	fractional := time.Duration(math.Ceil(float64(duration) * config.MaxDriftFraction))
	if fractional > config.MaxDriftAbsolute {
		return fractional
	}
	return config.MaxDriftAbsolute
}

func absoluteDurationDifference(left, right time.Duration) time.Duration {
	if left >= right {
		return left - right
	}
	return right - left
}

func durationMilliseconds(duration time.Duration) float64 {
	return float64(duration) / float64(time.Millisecond)
}

func dbfs(rms float64) float64 {
	if rms <= 0 {
		return math.Inf(-1)
	}
	return pcm16DBFSDecibels * math.Log10(rms/pcm16FullScale)
}

func invalidPCM16RoomAnalysis(field, reason string) error {
	return &InvalidPCM16RoomAnalysisInputError{Field: field, Reason: reason}
}

// InvalidPCM16RoomAnalysisInputError identifies one invalid room input field.
type InvalidPCM16RoomAnalysisInputError struct {
	Field  string
	Reason string
}

func (e *InvalidPCM16RoomAnalysisInputError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s: %s: %s", ErrInvalidPCM16RoomAnalysisInput, e.Field, e.Reason)
}

func (e *InvalidPCM16RoomAnalysisInputError) Unwrap() error {
	return errors.Join(ErrInvalidPCM16RoomAnalysisInput, ErrInvalidPCM16AnalysisInput)
}

func joinPropertyFailures(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	joined := parts[0]
	for _, part := range parts[1:] {
		joined += "; " + part
	}
	return joined
}
