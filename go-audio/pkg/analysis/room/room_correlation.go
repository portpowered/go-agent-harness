package room

import (
	"fmt"
	"math"

	analysisstream "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/stream"
)

type preparedPCM16Correlation struct {
	source         PCM16TimedStream
	received       PCM16TimedStream
	interval       PCM16TimeInterval
	minLagSamples  int
	maxLagSamples  int
	sourceStart    int
	sourceEnd      int
	receivedStart  int
	sourceActivity []bool
	threshold      float64
}

func preparePCM16Correlation(source, received PCM16TimedStream, interval PCM16TimeInterval, lagWindow PCM16LagWindow, silenceFloorDBFS float64) (preparedPCM16Correlation, error) {
	if err := validateCorrelationStreams(source, received); err != nil {
		return preparedPCM16Correlation{}, err
	}
	interval, err := normalizeTimeInterval(interval, "interval")
	if err != nil {
		return preparedPCM16Correlation{}, err
	}
	minLagSamples, maxLagSamples, err := normalizeCorrelationLagWindow(lagWindow, source.SampleRate)
	if err != nil {
		return preparedPCM16Correlation{}, err
	}
	if !isFinite(silenceFloorDBFS) || silenceFloorDBFS > 0 {
		return preparedPCM16Correlation{}, invalidPCM16RoomAnalysis("silence_floor_dbfs", "must be finite and at or below 0")
	}
	if interval.Start < source.TimelineStart || interval.End > source.TimelineEnd {
		return preparedPCM16Correlation{}, invalidPCM16RoomAnalysis("interval", "interval must be within the source timeline")
	}
	sourceStart, sourceEnd, err := sampleRangeForInterval(source, interval)
	if err != nil {
		return preparedPCM16Correlation{}, err
	}
	sourceActivity, err := annotatedActivityMask(source, sourceStart, sourceEnd)
	if err != nil {
		return preparedPCM16Correlation{}, err
	}
	receivedStart, err := signedDurationToSamples(interval.Start-received.TimelineStart, received.SampleRate)
	if err != nil {
		return preparedPCM16Correlation{}, invalidPCM16Analysis("interval.start", err.Error())
	}
	return preparedPCM16Correlation{
		source:         source,
		received:       received,
		interval:       interval,
		minLagSamples:  minLagSamples,
		maxLagSamples:  maxLagSamples,
		sourceStart:    sourceStart,
		sourceEnd:      sourceEnd,
		receivedStart:  receivedStart,
		sourceActivity: sourceActivity,
		threshold:      pcm16AmplitudeForDBFS(silenceFloorDBFS),
	}, nil
}

func validateCorrelationStreams(source, received PCM16TimedStream) error {
	if err := validateTimedStream(source, "source"); err != nil {
		return err
	}
	if err := validateTimedStream(received, "received"); err != nil {
		return err
	}
	if source.StreamID == received.StreamID {
		return invalidPCM16RoomAnalysis("source_stream_id", "source and received streams must be independently identified")
	}
	if source.SampleRate != received.SampleRate {
		return invalidPCM16RoomAnalysis("sample_rate", fmt.Sprintf("source is %d Hz but received is %d Hz", source.SampleRate, received.SampleRate))
	}
	return nil
}

func normalizeCorrelationLagWindow(lagWindow PCM16LagWindow, sampleRate int) (int, int, error) {
	if lagWindow.Min > lagWindow.Max {
		return 0, 0, invalidPCM16RoomAnalysis("lag_window", "min must be at or before max")
	}
	minLagSamples, err := signedDurationToSamples(lagWindow.Min, sampleRate)
	if err != nil {
		return 0, 0, invalidPCM16Analysis("lag_window.min", err.Error())
	}
	maxLagSamples, err := signedDurationToSamples(lagWindow.Max, sampleRate)
	if err != nil {
		return 0, 0, invalidPCM16Analysis("lag_window.max", err.Error())
	}
	if maxLagSamples < minLagSamples {
		return 0, 0, invalidPCM16RoomAnalysis("lag_window", "sample range is inverted")
	}
	if int64(maxLagSamples)-int64(minLagSamples) > int64(math.MaxInt32) {
		return 0, 0, invalidPCM16RoomAnalysis("lag_window", "contains too many sample offsets")
	}
	return minLagSamples, maxLagSamples, nil
}

func scanPCM16Correlation(prepared preparedPCM16Correlation) PCM16CorrelationMeasurement {
	measurement := PCM16CorrelationMeasurement{
		SourceStreamID:        prepared.source.StreamID,
		SourceParticipantID:   prepared.source.ParticipantID,
		ReceivedStreamID:      prepared.received.StreamID,
		ReceivedParticipantID: prepared.received.ParticipantID,
		IntervalID:            prepared.interval.ID,
		Start:                 prepared.interval.Start,
		End:                   prepared.interval.End,
	}
	found, absoluteFound := false, false
	forEachNormalizedPCM16CorrelationCandidate(
		prepared.minLagSamples,
		prepared.maxLagSamples,
		func(lagSamples int, coefficient float64, compared int) {
			found, absoluteFound = updatePCM16CorrelationMeasurement(&measurement, found, absoluteFound, lagSamples, coefficient, compared, prepared.source.SampleRate)
		},
		func(lagSamples int) (float64, int) {
			return normalizedCorrelationAtLag(prepared.source.Samples[prepared.sourceStart:prepared.sourceEnd], prepared.sourceActivity, prepared.received.Samples, prepared.receivedStart+lagSamples, prepared.threshold)
		},
	)
	return measurement
}

func updatePCM16CorrelationMeasurement(measurement *PCM16CorrelationMeasurement, found, absoluteFound bool, lagSamples int, coefficient float64, compared, sampleRate int) (bool, bool) {
	if compared == 0 {
		return found, absoluteFound
	}
	if !found || coefficient > measurement.BestCorrelation {
		measurement.BestCorrelation = coefficient
		measurement.BestLag = samplesToSignedDuration(lagSamples, sampleRate)
		measurement.ComparedSamples = compared
		found = true
	}
	if !absoluteFound || math.Abs(coefficient) > measurement.BestAbsoluteCorrelation {
		measurement.BestAbsoluteCorrelation = math.Abs(coefficient)
		measurement.BestAbsoluteLag = samplesToSignedDuration(lagSamples, sampleRate)
		absoluteFound = true
	}
	return found, absoluteFound
}

func forEachNormalizedPCM16CorrelationCandidate(
	minLagSamples, maxLagSamples int,
	visit func(lagSamples int, coefficient float64, compared int),
	measure func(lagSamples int) (float64, int),
) {
	if visit == nil || measure == nil || maxLagSamples < minLagSamples {
		return
	}
	for lagSamples := minLagSamples; ; lagSamples++ {
		coefficient, compared := measure(lagSamples)
		visit(lagSamples, coefficient, compared)
		if lagSamples == maxLagSamples {
			return
		}
	}
}

func normalizedCorrelationAtLag(source []int16, sourceActivity []bool, received []int16, receivedStart int, threshold float64) (float64, int) {
	return analysisstream.PCM16NormalizedCorrelationAtLag(source, sourceActivity, received, receivedStart, threshold)
}

func annotatedActivityMask(timed PCM16TimedStream, start, end int) ([]bool, error) {
	return analysisstream.PCM16ActivityMask(timed.PCM16Input, start, end)
}

func pcm16AmplitudeForDBFS(level float64) float64 {
	return analysisstream.PCM16AmplitudeForDBFS(level)
}

func activeRMS(stream PCM16TimedStream, interval PCM16TimeInterval) (float64, int, error) {
	start, end, err := sampleRangeForInterval(stream, interval)
	if err != nil {
		return 0, 0, err
	}
	activity, err := analysisstream.PCM16ActivityMask(stream.PCM16Input, start, end)
	if err != nil {
		return 0, 0, err
	}
	var energy float64
	count := 0
	for index, active := range activity {
		if !active {
			continue
		}
		value := float64(stream.Samples[start+index])
		energy += value * value
		count++
	}
	if count == 0 {
		return 0, 0, nil
	}
	return math.Sqrt(energy / float64(count)), count, nil
}
