package room

import (
	"time"

	analysisstream "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/stream"
)

// AnalyzePCM16Room measures all streams and evaluates the explicitly
// annotated room relationships. Valid-but-degraded evidence is represented in
// the returned report; malformed identities, timing, or format inputs return
// a typed error before relationship evaluation.
func AnalyzePCM16Room(input PCM16RoomInput, config PCM16RoomAnalysisConfig) (PCM16RoomAnalysis, error) {
	state, err := newPCM16RoomAnalysisState(input, config)
	if err != nil {
		return PCM16RoomAnalysis{}, err
	}
	if err := state.measureStreams(); err != nil {
		return PCM16RoomAnalysis{}, err
	}
	if err := state.prepareBargeAnalyses(); err != nil {
		return PCM16RoomAnalysis{}, err
	}
	if err := state.measureOverlaps(); err != nil {
		return PCM16RoomAnalysis{}, err
	}
	if err := state.measureLoudness(); err != nil {
		return PCM16RoomAnalysis{}, err
	}
	if err := state.measureBargeIns(); err != nil {
		return PCM16RoomAnalysis{}, err
	}
	return state.result, nil
}

// AssertPCM16Room evaluates a room and returns a typed error for any valid
// evidence that violates its configured properties.
func AssertPCM16Room(input PCM16RoomInput, config PCM16RoomAnalysisConfig) error {
	analysis, err := AnalyzePCM16Room(input, config)
	if err != nil {
		return err
	}
	if analysis.Passed() {
		return nil
	}
	return &PCM16RoomAssertionError{Failures: analysis.FailuresCopy()}
}

// ValidatePCM16Room is an assertion-oriented alias for AssertPCM16Room.
func ValidatePCM16Room(input PCM16RoomInput, config PCM16RoomAnalysisConfig) error {
	return AssertPCM16Room(input, config)
}

func measureBargeInFromAnalyses(interrupter, interrupted PCM16TimedStream, annotation PCM16BargeInAnnotation, interrupterAnalysis, interruptedAnalysis PCM16Analysis, thresholdDBFS float64, maxLatency time.Duration) PCM16BargeInMeasurement {
	measurement := PCM16BargeInMeasurement{
		ID:                    annotation.ID,
		InterrupterStreamID:   annotation.InterrupterStreamID,
		InterruptedStreamID:   annotation.InterruptedStreamID,
		Start:                 annotation.Start,
		End:                   annotation.End,
		InterrupterFrameIndex: -1,
		InterruptedFrameIndex: -1,
	}
	for _, frame := range interrupterAnalysis.Frames {
		start := interrupter.TimelineStart + frame.Timestamp
		if start < annotation.Start || start >= annotation.End || frame.RMSDBFS <= thresholdDBFS {
			continue
		}
		measurement.InterrupterOnsetFound = true
		measurement.InterrupterOnset = start
		measurement.InterrupterFrameIndex = frame.Index
		break
	}
	if !measurement.InterrupterOnsetFound {
		return measurement
	}
	for _, frame := range interruptedAnalysis.Frames {
		start := interrupted.TimelineStart + frame.Timestamp
		end := start + frame.Duration
		if end <= measurement.InterrupterOnset || start >= annotation.End || frame.RMSDBFS <= thresholdDBFS {
			continue
		}
		measurement.InterruptedLastActive = end
		measurement.InterruptedFrameIndex = frame.Index
	}
	if measurement.InterruptedLastActive > measurement.InterrupterOnset && measurement.InterruptedLastActive < annotation.End {
		measurement.InterruptedStopFound = true
		measurement.Latency = measurement.InterruptedLastActive - measurement.InterrupterOnset
		measurement.Passed = measurement.Latency <= maxLatency
	}
	return measurement
}

// MeasurePCM16BargeIn is the standalone frame-based interruption primitive.
func MeasurePCM16BargeIn(interrupter, interrupted PCM16TimedStream, annotation PCM16BargeInAnnotation, thresholdDBFS float64) (PCM16BargeInMeasurement, error) {
	if err := validateTimedStream(interrupter, "interrupter"); err != nil {
		return PCM16BargeInMeasurement{}, err
	}
	if err := validateTimedStream(interrupted, "interrupted"); err != nil {
		return PCM16BargeInMeasurement{}, err
	}
	if interrupter.StreamID == interrupted.StreamID {
		return PCM16BargeInMeasurement{}, invalidPCM16RoomAnalysis("barge_in", "interrupter and interrupted streams must be distinct")
	}
	if thresholdDBFS == 0 {
		thresholdDBFS = PCM16AnalysisDefaultBargeInSpeechThresholdDBFS
	}
	if !isFinite(thresholdDBFS) || thresholdDBFS > 0 {
		return PCM16BargeInMeasurement{}, invalidPCM16RoomAnalysis("barge_in_speech_threshold_dbfs", "must be finite and at or below 0")
	}
	annotation, err := normalizeBargeAnnotation(annotation)
	if err != nil {
		return PCM16BargeInMeasurement{}, err
	}
	if err := validateIntervalCoverage(interrupter, annotation.PCM16TimeInterval, "barge_in.interrupter"); err != nil {
		return PCM16BargeInMeasurement{}, err
	}
	if err := validateIntervalCoverage(interrupted, annotation.PCM16TimeInterval, "barge_in.interrupted"); err != nil {
		return PCM16BargeInMeasurement{}, err
	}
	interrupterAnalysis, err := analysisstream.AnalyzePCM16(interrupter.PCM16Input, DefaultPCM16AnalysisConfig())
	if err != nil {
		return PCM16BargeInMeasurement{}, err
	}
	interruptedAnalysis, err := analysisstream.AnalyzePCM16(interrupted.PCM16Input, DefaultPCM16AnalysisConfig())
	if err != nil {
		return PCM16BargeInMeasurement{}, err
	}
	return measureBargeInFromAnalyses(interrupter, interrupted, annotation, interrupterAnalysis, interruptedAnalysis, thresholdDBFS, PCM16AnalysisDefaultBargeInMaxLatency), nil
}
