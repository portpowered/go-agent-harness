package stream

import (
	"errors"
	"fmt"
	"math"
	"time"
)

type normalizedSpeechAnnotation struct {
	startSample int
	endSample   int
	label       string
}

func normalizeSpeechAnnotations(annotations []SpeechAnnotation, sampleCount, sampleRate int) ([]normalizedSpeechAnnotation, error) {
	normalized := make([]normalizedSpeechAnnotation, 0, len(annotations))
	previousEnd := 0
	for index, annotation := range annotations {
		item, err := normalizeSpeechAnnotation(annotation, index, sampleCount, sampleRate, previousEnd)
		if err != nil {
			return nil, err
		}
		normalized = append(normalized, item)
		previousEnd = item.endSample
	}
	return normalized, nil
}

func normalizeSpeechAnnotation(annotation SpeechAnnotation, index, sampleCount, sampleRate, previousEnd int) (normalizedSpeechAnnotation, error) {
	start, end, err := speechAnnotationRange(annotation, index, sampleRate)
	if err != nil {
		return normalizedSpeechAnnotation{}, err
	}
	field := fmt.Sprintf("expected_speech[%d]", index)
	if start < 0 || end <= start || end > sampleCount {
		return normalizedSpeechAnnotation{}, invalidPCM16Analysis(field, fmt.Sprintf("range %d..%d is outside sample count %d", start, end, sampleCount))
	}
	if index > 0 && start < previousEnd {
		return normalizedSpeechAnnotation{}, invalidPCM16Analysis(field, "annotations must be sorted and non-overlapping")
	}
	label := annotation.Label
	if label == "" {
		label = fmt.Sprintf("expected-speech-%d", index)
	}
	return normalizedSpeechAnnotation{startSample: start, endSample: end, label: label}, nil
}

func speechAnnotationRange(annotation SpeechAnnotation, index, sampleRate int) (int, int, error) {
	usesSamples := annotation.StartSample != 0 || annotation.EndSample != 0
	usesTime := annotation.Start != 0 || annotation.End != 0
	if usesSamples == usesTime {
		return 0, 0, invalidPCM16Analysis(fmt.Sprintf("expected_speech[%d]", index), "must specify exactly one complete sample or time range")
	}
	if !usesTime {
		return annotation.StartSample, annotation.EndSample, nil
	}
	start, err := durationToSamples(annotation.Start, sampleRate)
	if err != nil {
		return 0, 0, invalidPCM16Analysis(fmt.Sprintf("expected_speech[%d].start", index), err.Error())
	}
	end, err := durationToSamples(annotation.End, sampleRate)
	if err != nil {
		return 0, 0, invalidPCM16Analysis(fmt.Sprintf("expected_speech[%d].end", index), err.Error())
	}
	return start, end, nil
}

func validateChunkBoundaries(boundaries []ChunkBoundary, sampleCount, frameSamples int) ([]ChunkBoundary, error) {
	validated := make([]ChunkBoundary, len(boundaries))
	copy(validated, boundaries)
	previous := -1
	for index, boundary := range validated {
		if boundary.SampleIndex <= 0 || boundary.SampleIndex >= sampleCount {
			return nil, invalidPCM16Analysis(fmt.Sprintf("chunk_boundaries[%d].sample_index", index), fmt.Sprintf("must be between 1 and %d", sampleCount-1))
		}
		if boundary.SampleIndex <= previous {
			return nil, invalidPCM16Analysis(fmt.Sprintf("chunk_boundaries[%d].sample_index", index), "boundaries must be strictly increasing")
		}
		if boundary.SampleIndex < frameSamples || sampleCount-boundary.SampleIndex < frameSamples {
			return nil, invalidPCM16Analysis(fmt.Sprintf("chunk_boundaries[%d].sample_index", index), fmt.Sprintf("needs a complete %d-sample neighboring window on both sides", frameSamples))
		}
		if boundary.ID == "" {
			validated[index].ID = fmt.Sprintf("boundary-%d", index)
		}
		previous = boundary.SampleIndex
	}
	return validated, nil
}

func normalizePCM16AnalysisConfig(config PCM16AnalysisConfig) (PCM16AnalysisConfig, error) {
	config = applyPCM16AnalysisDefaults(config)
	if err := validatePCM16AnalysisConfig(config); err != nil {
		return PCM16AnalysisConfig{}, err
	}
	return config, nil
}

func applyPCM16AnalysisDefaults(config PCM16AnalysisConfig) PCM16AnalysisConfig {
	defaults := DefaultPCM16AnalysisConfig()
	if config.FrameDuration == 0 {
		config.FrameDuration = defaults.FrameDuration
	}
	if config.SilenceFloorDBFS == 0 {
		config.SilenceFloorDBFS = defaults.SilenceFloorDBFS
	}
	if config.MaxNaturalPause == 0 {
		config.MaxNaturalPause = defaults.MaxNaturalPause
	}
	if config.BoundaryDelta == 0 {
		config.BoundaryDelta = defaults.BoundaryDelta
	}
	if config.BoundaryQuietDBFS == 0 {
		config.BoundaryQuietDBFS = defaults.BoundaryQuietDBFS
	}
	if config.ClipSampleThreshold == 0 {
		config.ClipSampleThreshold = defaults.ClipSampleThreshold
	}
	if config.EdgeSampleThreshold == 0 {
		config.EdgeSampleThreshold = defaults.EdgeSampleThreshold
	}
	if config.FinalFrameMaxRMSDBFS == 0 {
		config.FinalFrameMaxRMSDBFS = defaults.FinalFrameMaxRMSDBFS
	}
	return config
}

func validatePCM16AnalysisConfig(config PCM16AnalysisConfig) error {
	switch {
	case config.FrameDuration <= 0:
		return invalidPCM16Analysis("frame_duration", "must be positive")
	case !isFinite(config.SilenceFloorDBFS) || config.SilenceFloorDBFS > 0:
		return invalidPCM16Analysis("silence_floor_dbfs", "must be finite and at or below 0")
	case config.MaxNaturalPause <= 0:
		return invalidPCM16Analysis("max_natural_pause", "must be positive")
	case config.BoundaryDelta <= 0:
		return invalidPCM16Analysis("boundary_delta", "must be positive")
	case !isFinite(config.BoundaryQuietDBFS) || config.BoundaryQuietDBFS >= 0:
		return invalidPCM16Analysis("boundary_quiet_dbfs", "must be finite and below 0")
	case config.ClipSampleThreshold <= 0 || config.ClipSampleThreshold > 32767:
		return invalidPCM16Analysis("clip_sample_threshold", "must be between 1 and 32767")
	case config.EdgeSampleThreshold < 0 || config.EdgeSampleThreshold > 32767:
		return invalidPCM16Analysis("edge_sample_threshold", "must be between 0 and 32767")
	case !isFinite(config.FinalFrameMaxRMSDBFS) || config.FinalFrameMaxRMSDBFS > 0:
		return invalidPCM16Analysis("final_frame_max_rms_dbfs", "must be finite and at or below 0")
	}
	return nil
}

// NormalizePCM16AnalysisConfig validates and fills the stream analysis
// profile defaults. Room and self-hearing owners use this at their boundary
// instead of reaching into stream implementation helpers.
func NormalizePCM16AnalysisConfig(config PCM16AnalysisConfig) (PCM16AnalysisConfig, error) {
	return normalizePCM16AnalysisConfig(config)
}

func analysisFrameSamples(sampleRate int, frameDuration time.Duration) (int, error) {
	nanoseconds := int64(frameDuration)
	if sampleRate <= 0 || nanoseconds <= 0 {
		return 0, invalidPCM16Analysis("frame_duration", "does not produce a positive sample count")
	}
	if nanoseconds > math.MaxInt64/int64(sampleRate) {
		return 0, invalidPCM16Analysis("frame_duration", "sample count overflows")
	}
	product := nanoseconds * int64(sampleRate)
	if product%int64(time.Second) != 0 {
		return 0, invalidPCM16Analysis("frame_duration", fmt.Sprintf("%s is not an exact whole number of samples at %d Hz", frameDuration, sampleRate))
	}
	frameSamples := int(product / int64(time.Second))
	if frameSamples <= 0 {
		return 0, invalidPCM16Analysis("frame_duration", "does not produce a positive sample count")
	}
	return frameSamples, nil
}

func durationToSamples(duration time.Duration, sampleRate int) (int, error) {
	if duration < 0 {
		return 0, errors.New("must not be negative")
	}
	nanoseconds := int64(duration)
	if sampleRate <= 0 || nanoseconds > math.MaxInt64/int64(sampleRate) {
		return 0, errors.New("sample conversion overflows")
	}
	product := nanoseconds * int64(sampleRate)
	if product%int64(time.Second) != 0 {
		return 0, fmt.Errorf("%s is not an exact whole number of samples at %d Hz", duration, sampleRate)
	}
	converted := product / int64(time.Second)
	if converted > int64(math.MaxInt) {
		return 0, errors.New("sample index overflows int")
	}
	return int(converted), nil
}

func expectedSpeechOverlap(start, end int, annotations []normalizedSpeechAnnotation) (int, string) {
	overlap := 0
	label := ""
	for _, annotation := range annotations {
		if annotation.endSample <= start {
			continue
		}
		if annotation.startSample >= end {
			break
		}
		left := start
		if annotation.startSample > left {
			left = annotation.startSample
		}
		right := end
		if annotation.endSample < right {
			right = annotation.endSample
		}
		if right > left {
			overlap += right - left
			if label == "" {
				label = annotation.label
			}
		}
	}
	return overlap, label
}

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
	return &InvalidPCM16AnalysisInputError{Field: field, Reason: reason}
}

func boundaryLabel(boundary ChunkBoundary) string {
	if boundary.ID != "" {
		return boundary.ID
	}
	return fmt.Sprintf("sample-%d", boundary.SampleIndex)
}

func samplesToDuration(samples, sampleRate int) time.Duration {
	if samples <= 0 || sampleRate <= 0 {
		return 0
	}
	return time.Duration((int64(samples)*int64(time.Second) + int64(sampleRate)/2) / int64(sampleRate))
}

func absoluteSample(sample int16) int {
	value := int(sample)
	if value < 0 {
		return -value
	}
	return value
}

func absoluteSampleDifference(left, right int16) int {
	difference := int(left) - int(right)
	if difference < 0 {
		return -difference
	}
	return difference
}

func absolutePeak(samples []int16) int {
	peak := 0
	for _, sample := range samples {
		if value := absoluteSample(sample); value > peak {
			peak = value
		}
	}
	return peak
}

func dbfs(rms float64) float64 {
	if rms <= 0 {
		return math.Inf(-1)
	}
	return pcm16DBFSDecibels * math.Log10(rms/pcm16FullScale)
}

func headroomDBFS(peak int) float64 {
	if peak <= 0 {
		return math.Inf(1)
	}
	return pcm16DBFSDecibels * math.Log10(pcm16FullScale/float64(peak))
}

func isFinite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func formatAnalysisNumber(value float64) string {
	switch {
	case math.IsInf(value, 1):
		return "+Inf"
	case math.IsInf(value, -1):
		return "-Inf"
	default:
		return fmt.Sprintf("%.3f", value)
	}
}
