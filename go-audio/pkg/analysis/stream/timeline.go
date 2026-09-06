package stream

import (
	"errors"
	"math"
	"time"
)

// PCM16LagWindow identifies the inclusive routing-lag range searched by a
// correlation measurement. Positive lag means the received stream is later
// than the source stream.
type PCM16LagWindow struct {
	Min time.Duration
	Max time.Duration
}

// PCM16TimedFrame is one owned-by-the-caller PCM16 observation with an
// explicit sample rate and monotonic media start position. It is shared by
// streaming diagnostics that need to align bounded samples on a timeline.
type PCM16TimedFrame struct {
	Samples    []int16
	SampleRate int
	Start      time.Duration
}

// PCM16MediaFrame is a descriptive alias for callers that think in terms of
// media rather than analysis timelines.
type PCM16MediaFrame = PCM16TimedFrame

// End returns the derived half-open end position. Invalid frames return the
// start position; each consumer validates the frame before using End.
func (f PCM16TimedFrame) End() time.Duration {
	return f.Start + PCM16SamplesToDuration(len(f.Samples), f.SampleRate)
}

// PCM16CorrelationMeasurement records signed and absolute best correlations
// over one explicit interval and lag window. Room analysis and self-hearing
// both use this neutral measurement shape.
type PCM16CorrelationMeasurement struct {
	SourceStreamID        string
	SourceParticipantID   string
	ReceivedStreamID      string
	ReceivedParticipantID string
	IntervalID            string
	Start                 time.Duration
	End                   time.Duration

	BestCorrelation         float64
	BestLag                 time.Duration
	BestAbsoluteCorrelation float64
	BestAbsoluteLag         time.Duration
	ComparedSamples         int
}

// HasEvidence reports whether at least one non-silent sample pair was
// available in the interval.
func (m PCM16CorrelationMeasurement) HasEvidence() bool { return m.ComparedSamples > 0 }

// PCM16SamplesToDuration converts a sample count to the nearest media
// duration without using wall-clock state.
func PCM16SamplesToDuration(samples, sampleRate int) time.Duration {
	if samples <= 0 || sampleRate <= 0 {
		return 0
	}
	return time.Duration((int64(samples)*int64(time.Second) + int64(sampleRate)/2) / int64(sampleRate))
}

// PCM16DurationToSamples converts an exact media duration to a sample count.
// Fractional samples are rejected so timeline interval boundaries cannot be
// silently rounded differently by separate analysis owners.
func PCM16DurationToSamples(duration time.Duration, sampleRate int) (int, error) {
	if duration < 0 {
		return 0, errors.New("must not be negative")
	}
	nanoseconds := int64(duration)
	if sampleRate <= 0 || nanoseconds > math.MaxInt64/int64(sampleRate) {
		return 0, errors.New("sample conversion overflows")
	}
	product := nanoseconds * int64(sampleRate)
	if product%int64(time.Second) != 0 {
		return 0, errors.New("duration is not an exact whole number of samples")
	}
	converted := product / int64(time.Second)
	if converted > int64(math.MaxInt) {
		return 0, errors.New("sample index overflows int")
	}
	return int(converted), nil
}

// PCM16SignedDurationToSamples is the signed counterpart of
// PCM16DurationToSamples used for lag windows.
func PCM16SignedDurationToSamples(duration time.Duration, sampleRate int) (int, error) {
	if duration == time.Duration(math.MinInt64) {
		return 0, errors.New("duration magnitude overflows")
	}
	if duration >= 0 {
		return PCM16DurationToSamples(duration, sampleRate)
	}
	positive, err := PCM16DurationToSamples(-duration, sampleRate)
	if err != nil {
		return 0, err
	}
	return -positive, nil
}

// PCM16SamplesToSignedDuration converts a signed sample offset to a media
// duration. Unrepresentable offsets return zero, matching the invalid-input
// behavior of PCM16SamplesToDuration.
func PCM16SamplesToSignedDuration(samples, sampleRate int) time.Duration {
	if samples == 0 || sampleRate <= 0 {
		return 0
	}
	if samples < 0 {
		if samples == -samples {
			return 0
		}
		return -PCM16SamplesToDuration(-samples, sampleRate)
	}
	return PCM16SamplesToDuration(samples, sampleRate)
}

// PCM16AmplitudeForDBFS converts a dBFS floor to an absolute PCM16 level.
func PCM16AmplitudeForDBFS(level float64) float64 {
	if math.IsInf(level, -1) {
		return 0
	}
	return math.Pow(10, level/pcm16DBFSDecibels) * pcm16FullScale
}

// PCM16AbsoluteSample returns the magnitude of a signed PCM16 sample.
func PCM16AbsoluteSample(sample int16) int {
	value := int(sample)
	if value < 0 {
		return -value
	}
	return value
}

// PCM16IsFinite reports whether a diagnostic value can be compared
// deterministically.
func PCM16IsFinite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

// PCM16NormalizedCorrelationAtLag measures the normalized correlation at one
// sample offset. sourceActivity may mark source samples excluded from the
// comparison; a nil or differently sized mask includes every sample.
func PCM16NormalizedCorrelationAtLag(source []int16, sourceActivity []bool, received []int16, receivedStart int, threshold float64) (float64, int) {
	sums := collectPCM16CorrelationSums(source, sourceActivity, received, receivedStart, threshold)
	return pcm16CorrelationCoefficient(sums), sums.compared
}

type pcm16CorrelationSums struct {
	sumSource, sumReceived       float64
	sourceEnergy, receivedEnergy float64
	cross                        float64
	compared                     int
}

func collectPCM16CorrelationSums(source []int16, sourceActivity []bool, received []int16, receivedStart int, threshold float64) pcm16CorrelationSums {
	var sums pcm16CorrelationSums
	for offset, sourceSample := range source {
		if len(sourceActivity) == len(source) && !sourceActivity[offset] {
			continue
		}
		receivedIndex := receivedStart + offset
		if receivedIndex < 0 || receivedIndex >= len(received) {
			continue
		}
		receivedSample := received[receivedIndex]
		if float64(PCM16AbsoluteSample(sourceSample)) <= threshold && float64(PCM16AbsoluteSample(receivedSample)) <= threshold {
			continue
		}
		x := float64(sourceSample)
		y := float64(receivedSample)
		sums.sumSource += x
		sums.sumReceived += y
		sums.sourceEnergy += x * x
		sums.receivedEnergy += y * y
		sums.cross += x * y
		sums.compared++
	}
	return sums
}

func pcm16CorrelationCoefficient(sums pcm16CorrelationSums) float64 {
	if sums.compared < 2 || sums.sourceEnergy == 0 || sums.receivedEnergy == 0 {
		return 0
	}
	meanSource := sums.sumSource / float64(sums.compared)
	meanReceived := sums.sumReceived / float64(sums.compared)
	centeredSourceEnergy := sums.sourceEnergy - float64(sums.compared)*meanSource*meanSource
	centeredReceivedEnergy := sums.receivedEnergy - float64(sums.compared)*meanReceived*meanReceived
	centeredCross := sums.cross - float64(sums.compared)*meanSource*meanReceived
	if centeredSourceEnergy <= 0 || centeredReceivedEnergy <= 0 {
		coefficient := sums.cross / math.Sqrt(sums.sourceEnergy*sums.receivedEnergy)
		if !PCM16IsFinite(coefficient) {
			return 0
		}
		return coefficient
	}
	coefficient := centeredCross / math.Sqrt(centeredSourceEnergy*centeredReceivedEnergy)
	if !PCM16IsFinite(coefficient) {
		return 0
	}
	return coefficient
}

// PCM16ActivityMask returns a copy-safe activity mask for a source interval,
// honoring the source's expected-speech annotations.
func PCM16ActivityMask(input PCM16Input, start, end int) ([]bool, error) {
	if start < 0 || end < start || end > len(input.Samples) {
		return nil, &InvalidPCM16AnalysisInputError{Field: "activity_range", Reason: "range is outside the sample input"}
	}
	mask := make([]bool, end-start)
	annotations, err := normalizeSpeechAnnotations(input.ExpectedSpeech, len(input.Samples), input.SampleRate)
	if err != nil {
		return nil, err
	}
	if len(annotations) == 0 {
		for index := range mask {
			mask[index] = true
		}
		return mask, nil
	}
	for _, annotation := range annotations {
		left := start
		if annotation.startSample > left {
			left = annotation.startSample
		}
		right := end
		if annotation.endSample < right {
			right = annotation.endSample
		}
		for index := left; index < right; index++ {
			mask[index-start] = true
		}
	}
	return mask, nil
}
