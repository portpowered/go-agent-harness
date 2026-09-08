package selfhearing

import (
	"time"

	analysisstream "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/stream"
)

// The self-hearing API keeps these neutral stream contracts available under
// its domain package while the stream owner remains the canonical definition.
type (
	PCM16LagWindow              = analysisstream.PCM16LagWindow
	PCM16TimedFrame             = analysisstream.PCM16TimedFrame
	PCM16MediaFrame             = analysisstream.PCM16MediaFrame
	PCM16CorrelationMeasurement = analysisstream.PCM16CorrelationMeasurement
)

func samplesToDuration(samples, sampleRate int) time.Duration {
	return analysisstream.PCM16SamplesToDuration(samples, sampleRate)
}

func samplesToSignedDuration(samples, sampleRate int) time.Duration {
	return analysisstream.PCM16SamplesToSignedDuration(samples, sampleRate)
}

func signedDurationToSamples(duration time.Duration, sampleRate int) (int, error) {
	return analysisstream.PCM16SignedDurationToSamples(duration, sampleRate)
}

func pcm16AmplitudeForDBFS(level float64) float64 {
	return analysisstream.PCM16AmplitudeForDBFS(level)
}

func normalizedCorrelationAtLag(source []int16, sourceActivity []bool, received []int16, receivedStart int, threshold float64) (float64, int) {
	return analysisstream.PCM16NormalizedCorrelationAtLag(source, sourceActivity, received, receivedStart, threshold)
}

func absoluteSample(sample int16) int {
	return analysisstream.PCM16AbsoluteSample(sample)
}

func isFinite(value float64) bool { return analysisstream.PCM16IsFinite(value) }
