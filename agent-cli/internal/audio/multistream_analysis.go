package audio

import (
	"errors"
	"fmt"
	"math"
	"time"
)

const (
	// PCM16AnalysisDefaultCorrelationLagMin and Max bound the routing delay
	// searched by the room assertions when a profile does not provide one.
	PCM16AnalysisDefaultCorrelationLagMin = -100 * time.Millisecond
	PCM16AnalysisDefaultCorrelationLagMax = 100 * time.Millisecond
	// PCM16AnalysisDefaultPeerCorrelation is the minimum positive normalized
	// correlation expected for a delivered peer stream.
	PCM16AnalysisDefaultPeerCorrelation = 0.55
	// PCM16AnalysisDefaultSelfCorrelation is the maximum absolute correlation
	// permitted between a participant's sent and received streams.
	PCM16AnalysisDefaultSelfCorrelation = 0.30
	// PCM16AnalysisDefaultBargeInSpeechThresholdDBFS is the activity floor for
	// the 20 ms frames used to find interruption onset and stop.
	PCM16AnalysisDefaultBargeInSpeechThresholdDBFS = -40.0
	// PCM16AnalysisDefaultBargeInMaxLatency is the maximum allowed interval
	// between interruption onset and the interrupted stream's last active frame.
	PCM16AnalysisDefaultBargeInMaxLatency = 500 * time.Millisecond
	// PCM16AnalysisDefaultLoudnessDifferenceDB is the maximum active-speech
	// loudness difference between two participants.
	PCM16AnalysisDefaultLoudnessDifferenceDB = 6.0
	// PCM16AnalysisDefaultDrift is the absolute floor of the permitted timing
	// drift. Profiles also apply MaxDriftFraction to the stream duration.
	PCM16AnalysisDefaultDrift = 20 * time.Millisecond
	// PCM16AnalysisDefaultDriftFraction is 0.1 percent expressed as a ratio.
	PCM16AnalysisDefaultDriftFraction = 0.001
)

var (
	// ErrInvalidPCM16RoomAnalysisInput identifies a malformed timed-room
	// input, such as an unknown stream identity or an impossible interval.
	ErrInvalidPCM16RoomAnalysisInput = errors.New("invalid PCM16 room analysis input")
)

// PCM16TimedStream adds an explicit timeline to one independently identified
// PCM16 stream. TimelineStart and TimelineEnd are absolute recording times;
// the difference is intentionally retained separately from sample duration so
// drift can be measured rather than silently normalized away.
type PCM16TimedStream struct {
	PCM16Input
	TimelineStart time.Duration
	TimelineEnd   time.Duration
}

// PCM16TimeInterval is an absolute, half-open room interval.
type PCM16TimeInterval struct {
	ID    string
	Start time.Duration
	End   time.Duration
}

// PCM16LagWindow identifies the inclusive routing-lag range searched by a
// correlation measurement. Positive lag means the received stream is later
// than the source stream.
type PCM16LagWindow struct {
	Min time.Duration
	Max time.Duration
}

// PCM16OverlapParticipant identifies the sent and received evidence for one
// participant in a simultaneous-speech interval.
type PCM16OverlapParticipant struct {
	ParticipantID    string
	SentStreamID     string
	ReceivedStreamID string
}

// PCM16OverlapInterval identifies two independently recorded participants
// who are expected to speak at the same time.
type PCM16OverlapInterval struct {
	PCM16TimeInterval
	A PCM16OverlapParticipant
	B PCM16OverlapParticipant
}

// PCM16SimultaneousSpeechInterval is a descriptive alias for overlap inputs.
type PCM16SimultaneousSpeechInterval = PCM16OverlapInterval

// PCM16BargeInAnnotation identifies an interruption search window. The
// interrupter's first active frame and the interrupted stream's last active
// frame are both found inside this absolute interval.
type PCM16BargeInAnnotation struct {
	PCM16TimeInterval
	InterrupterStreamID string
	InterruptedStreamID string
}

// PCM16BargeIn is a concise alias for barge-in annotations.
type PCM16BargeIn = PCM16BargeInAnnotation

// PCM16LoudnessInterval identifies two active-speech streams to compare over
// an annotated interval. If a stream has ExpectedSpeech annotations, only
// their intersection with the interval contributes to its RMS.
type PCM16LoudnessInterval struct {
	PCM16TimeInterval
	LeftStreamID  string
	RightStreamID string
}

// PCM16RoomInput is the complete identity- and time-aware input for a room
// analysis. The slices are read-only to the analyzer.
type PCM16RoomInput struct {
	Streams  []PCM16TimedStream
	Overlaps []PCM16OverlapInterval
	BargeIns []PCM16BargeInAnnotation
	Loudness []PCM16LoudnessInterval
}

// PCM16RoomAnalysisConfig contains the suite-wide multi-stream bounds.
// StreamConfig is applied independently to every timed stream.
type PCM16RoomAnalysisConfig struct {
	StreamConfig PCM16AnalysisConfig

	CorrelationLagWindow        PCM16LagWindow
	CorrelationSilenceFloorDBFS float64
	MinPeerCorrelation          float64
	MaxSelfCorrelation          float64
	BargeInSpeechThresholdDBFS  float64
	MaxBargeInLatency           time.Duration
	MaxLoudnessDifferenceDB     float64
	MaxDriftAbsolute            time.Duration
	MaxDriftFraction            float64
}

// DefaultPCM16RoomAnalysisConfig is the default profile for room assertions.
var DefaultPCM16RoomAnalysisConfig = PCM16RoomAnalysisConfig{
	StreamConfig:                DefaultPCM16AnalysisConfig,
	CorrelationLagWindow:        PCM16LagWindow{Min: PCM16AnalysisDefaultCorrelationLagMin, Max: PCM16AnalysisDefaultCorrelationLagMax},
	CorrelationSilenceFloorDBFS: PCM16AnalysisSilenceFloorDBFS,
	MinPeerCorrelation:          PCM16AnalysisDefaultPeerCorrelation,
	MaxSelfCorrelation:          PCM16AnalysisDefaultSelfCorrelation,
	BargeInSpeechThresholdDBFS:  PCM16AnalysisDefaultBargeInSpeechThresholdDBFS,
	MaxBargeInLatency:           PCM16AnalysisDefaultBargeInMaxLatency,
	MaxLoudnessDifferenceDB:     PCM16AnalysisDefaultLoudnessDifferenceDB,
	MaxDriftAbsolute:            PCM16AnalysisDefaultDrift,
	MaxDriftFraction:            PCM16AnalysisDefaultDriftFraction,
}

// DefaultRoomAnalysisConfig returns a copy of the default room profile.
func DefaultRoomAnalysisConfig() PCM16RoomAnalysisConfig { return DefaultPCM16RoomAnalysisConfig }

// PCM16CorrelationMeasurement records signed and absolute best correlations
// over one explicit interval and lag window.
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

// PCM16PeerDeliveryMeasurement records one direction of overlap delivery.
type PCM16PeerDeliveryMeasurement struct {
	PCM16CorrelationMeasurement
	Direction string
	Passed    bool
}

// PCM16SelfHearingMeasurement records one participant's sent-to-received
// correlation. Self-hearing uses BestAbsoluteCorrelation by design.
type PCM16SelfHearingMeasurement struct {
	PCM16CorrelationMeasurement
	Direction string
	Passed    bool
}

// PCM16LoudnessMeasurement records active-speech RMS evidence for two
// streams.
type PCM16LoudnessMeasurement struct {
	IntervalID    string
	LeftStreamID  string
	RightStreamID string
	Start         time.Duration
	End           time.Duration

	LeftRMS      float64
	LeftRMSDBFS  float64
	LeftSamples  int
	RightRMS     float64
	RightRMSDBFS float64
	RightSamples int
	DifferenceDB float64
	Passed       bool
}

// PCM16DriftMeasurement compares exact sample duration with the declared
// timestamp span. Bound is populated by room analysis, where the configured
// max(absolute, fraction-of-duration) rule is available.
type PCM16DriftMeasurement struct {
	StreamID       string
	ParticipantID  string
	SampleDuration time.Duration
	TimestampSpan  time.Duration
	Drift          time.Duration
	Bound          time.Duration
	Passed         bool
}

// PCM16BargeInMeasurement contains the frame-level interruption evidence.
type PCM16BargeInMeasurement struct {
	ID                  string
	InterrupterStreamID string
	InterruptedStreamID string
	Start               time.Duration
	End                 time.Duration

	InterrupterOnsetFound bool
	InterrupterOnset      time.Duration
	InterrupterFrameIndex int
	InterruptedStopFound  bool
	InterruptedLastActive time.Duration
	InterruptedFrameIndex int
	Latency               time.Duration
	Passed                bool
}

// PCM16OverlapAnalysis contains both peer-delivery directions, both
// self-hearing checks, and the active-speech balance for one interval.
type PCM16OverlapAnalysis struct {
	Interval PCM16OverlapInterval
	Forward  PCM16PeerDeliveryMeasurement
	Reverse  PCM16PeerDeliveryMeasurement
	SelfA    PCM16SelfHearingMeasurement
	SelfB    PCM16SelfHearingMeasurement
	Loudness PCM16LoudnessMeasurement
}

// PCM16RoomAnalysis is the complete deterministic report for a room.
type PCM16RoomAnalysis struct {
	Streams        []PCM16Analysis
	Overlaps       []PCM16OverlapAnalysis
	PeerDeliveries []PCM16PeerDeliveryMeasurement
	SelfHearings   []PCM16SelfHearingMeasurement
	BargeIns       []PCM16BargeInMeasurement
	Loudness       []PCM16LoudnessMeasurement
	Drift          []PCM16DriftMeasurement
	Failures       []PropertyFailure
}

// Passed reports whether all valid room evidence satisfied every configured
// stream and multi-stream property.
func (a PCM16RoomAnalysis) Passed() bool { return len(a.Failures) == 0 }

// FailuresCopy returns caller-owned failure storage.
func (a PCM16RoomAnalysis) FailuresCopy() []PropertyFailure {
	return append([]PropertyFailure(nil), a.Failures...)
}

// PCM16RoomAssertionError wraps all valid-room property failures.
type PCM16RoomAssertionError struct {
	Failures []PropertyFailure
}

func (e *PCM16RoomAssertionError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if len(e.Failures) == 0 {
		return ErrPCM16AnalysisFailed.Error()
	}
	parts := make([]string, len(e.Failures))
	for i, failure := range e.Failures {
		parts[i] = failure.Error()
	}
	return fmt.Sprintf("%s: %s", ErrPCM16AnalysisFailed, joinPropertyFailures(parts))
}

func (e *PCM16RoomAssertionError) Unwrap() error { return ErrPCM16AnalysisFailed }

// FailuresCopy returns caller-owned failure storage.
func (e *PCM16RoomAssertionError) FailuresCopy() []PropertyFailure {
	if e == nil {
		return nil
	}
	return append([]PropertyFailure(nil), e.Failures...)
}

// NormalizedPCM16CrossCorrelation measures one explicitly identified source
// and received stream over an absolute interval. The lag window is searched
// at one-sample resolution and only sample pairs with non-silent evidence are
// included. It returns a measurement with zero evidence instead of treating
// absent audio as a malformed input; room assertions then report that as a
// delivery failure.
func NormalizedPCM16CrossCorrelation(source, received PCM16TimedStream, interval PCM16TimeInterval, lagWindow PCM16LagWindow, silenceFloorDBFS float64) (PCM16CorrelationMeasurement, error) {
	if err := validateTimedStream(source, "source"); err != nil {
		return PCM16CorrelationMeasurement{}, err
	}
	if err := validateTimedStream(received, "received"); err != nil {
		return PCM16CorrelationMeasurement{}, err
	}
	if source.StreamID == received.StreamID {
		return PCM16CorrelationMeasurement{}, invalidPCM16RoomAnalysis("source_stream_id", "source and received streams must be independently identified")
	}
	if source.SampleRate != received.SampleRate {
		return PCM16CorrelationMeasurement{}, invalidPCM16RoomAnalysis("sample_rate", fmt.Sprintf("source is %d Hz but received is %d Hz", source.SampleRate, received.SampleRate))
	}
	interval, err := normalizeTimeInterval(interval, "interval")
	if err != nil {
		return PCM16CorrelationMeasurement{}, err
	}
	if lagWindow.Min > lagWindow.Max {
		return PCM16CorrelationMeasurement{}, invalidPCM16RoomAnalysis("lag_window", "min must be at or before max")
	}
	if !isFinite(silenceFloorDBFS) || silenceFloorDBFS > 0 {
		return PCM16CorrelationMeasurement{}, invalidPCM16RoomAnalysis("silence_floor_dbfs", "must be finite and at or below 0")
	}
	if interval.Start < source.TimelineStart || interval.End > source.TimelineEnd {
		return PCM16CorrelationMeasurement{}, invalidPCM16RoomAnalysis("interval", "interval must be within the source timeline")
	}
	if _, _, err := sampleRangeForInterval(source, interval); err != nil {
		return PCM16CorrelationMeasurement{}, err
	}
	minLagSamples, err := signedDurationToSamples(lagWindow.Min, source.SampleRate)
	if err != nil {
		return PCM16CorrelationMeasurement{}, invalidPCM16Analysis("lag_window.min", err.Error())
	}
	maxLagSamples, err := signedDurationToSamples(lagWindow.Max, source.SampleRate)
	if err != nil {
		return PCM16CorrelationMeasurement{}, invalidPCM16Analysis("lag_window.max", err.Error())
	}
	if maxLagSamples < minLagSamples {
		return PCM16CorrelationMeasurement{}, invalidPCM16RoomAnalysis("lag_window", "sample range is inverted")
	}
	if int64(maxLagSamples)-int64(minLagSamples) > int64(math.MaxInt32) {
		return PCM16CorrelationMeasurement{}, invalidPCM16RoomAnalysis("lag_window", "contains too many sample offsets")
	}

	sourceStart, sourceEnd, err := sampleRangeForInterval(source, interval)
	if err != nil {
		return PCM16CorrelationMeasurement{}, err
	}
	sourceActivity, err := annotatedActivityMask(source, sourceStart, sourceEnd)
	if err != nil {
		return PCM16CorrelationMeasurement{}, err
	}
	receivedIntervalStart, err := signedDurationToSamples(interval.Start-received.TimelineStart, received.SampleRate)
	if err != nil {
		return PCM16CorrelationMeasurement{}, invalidPCM16Analysis("interval.start", err.Error())
	}
	threshold := pcm16AmplitudeForDBFS(silenceFloorDBFS)
	measurement := PCM16CorrelationMeasurement{
		SourceStreamID:        source.StreamID,
		SourceParticipantID:   source.ParticipantID,
		ReceivedStreamID:      received.StreamID,
		ReceivedParticipantID: received.ParticipantID,
		IntervalID:            interval.ID,
		Start:                 interval.Start,
		End:                   interval.End,
	}
	found := false
	absoluteFound := false
	for lagSamples := minLagSamples; lagSamples <= maxLagSamples; lagSamples++ {
		receivedStart := receivedIntervalStart + lagSamples
		coefficient, compared := normalizedCorrelationAtLag(source.Samples[sourceStart:sourceEnd], sourceActivity, received.Samples, receivedStart, threshold)
		if compared == 0 {
			continue
		}
		if !found || coefficient > measurement.BestCorrelation {
			measurement.BestCorrelation = coefficient
			measurement.BestLag = samplesToSignedDuration(lagSamples, source.SampleRate)
			measurement.ComparedSamples = compared
			found = true
		}
		if !absoluteFound || math.Abs(coefficient) > measurement.BestAbsoluteCorrelation {
			measurement.BestAbsoluteCorrelation = math.Abs(coefficient)
			measurement.BestAbsoluteLag = samplesToSignedDuration(lagSamples, source.SampleRate)
			absoluteFound = true
		}
	}
	return measurement, nil
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

// AnalyzePCM16Room measures all streams and evaluates the explicitly
// annotated room relationships. Valid-but-degraded evidence is represented in
// the returned report; malformed identities, timing, or format inputs return
// a typed error before relationship evaluation.
func AnalyzePCM16Room(input PCM16RoomInput, config PCM16RoomAnalysisConfig) (PCM16RoomAnalysis, error) {
	config, err := normalizePCM16RoomAnalysisConfig(config)
	if err != nil {
		return PCM16RoomAnalysis{}, err
	}
	normalized, streams, err := normalizePCM16RoomInput(input)
	if err != nil {
		return PCM16RoomAnalysis{}, err
	}

	result := PCM16RoomAnalysis{
		Streams:        make([]PCM16Analysis, 0, len(normalized.Streams)),
		Overlaps:       make([]PCM16OverlapAnalysis, 0, len(normalized.Overlaps)),
		PeerDeliveries: make([]PCM16PeerDeliveryMeasurement, 0, len(normalized.Overlaps)*2),
		SelfHearings:   make([]PCM16SelfHearingMeasurement, 0, len(normalized.Overlaps)*2),
		BargeIns:       make([]PCM16BargeInMeasurement, 0, len(normalized.BargeIns)),
		Loudness:       make([]PCM16LoudnessMeasurement, 0, len(normalized.Overlaps)+len(normalized.Loudness)),
		Drift:          make([]PCM16DriftMeasurement, 0, len(normalized.Streams)),
	}
	analyses := make(map[string]PCM16Analysis, len(normalized.Streams))
	for index, stream := range normalized.Streams {
		analysis, err := AnalyzePCM16(stream.PCM16Input, config.StreamConfig)
		if err != nil {
			return PCM16RoomAnalysis{}, fmt.Errorf("%w: streams[%d] %w", ErrInvalidPCM16RoomAnalysisInput, index, err)
		}
		result.Streams = append(result.Streams, analysis)
		analyses[stream.StreamID] = analysis
		result.Failures = append(result.Failures, analysis.Failures...)

		drift, err := MeasurePCM16Drift(stream)
		if err != nil {
			return PCM16RoomAnalysis{}, err
		}
		drift.Bound = configuredDriftBound(drift.SampleDuration, drift.TimestampSpan, config)
		drift.Passed = drift.Drift <= drift.Bound
		result.Drift = append(result.Drift, drift)
		if !drift.Passed {
			failure := roomFailure("timing-drift", stream, "")
			failure.Interval = "stream-timeline"
			failure.StartSample = 0
			failure.EndSample = len(stream.Samples)
			failure.Timestamp = stream.TimelineStart
			failure.Measured = durationMilliseconds(drift.Drift)
			failure.Comparison = ">"
			failure.Bound = durationMilliseconds(drift.Bound)
			failure.Unit = "milliseconds"
			failure.Detail = fmt.Sprintf("sample-duration=%s timestamp-span=%s", drift.SampleDuration, drift.TimestampSpan)
			result.Failures = append(result.Failures, failure)
		}
	}
	bargeAnalyses := analyses
	if len(normalized.BargeIns) > 0 {
		bargeAnalyses = make(map[string]PCM16Analysis, len(normalized.BargeIns)*2)
		bargeFrameConfig := config.StreamConfig
		bargeFrameConfig.FrameDuration = PCM16AnalysisFrameDuration
		for _, annotation := range normalized.BargeIns {
			for _, streamID := range []string{annotation.InterrupterStreamID, annotation.InterruptedStreamID} {
				if _, alreadyMeasured := bargeAnalyses[streamID]; alreadyMeasured {
					continue
				}
				stream := streams[streamID]
				analysis, err := AnalyzePCM16(stream.PCM16Input, bargeFrameConfig)
				if err != nil {
					return PCM16RoomAnalysis{}, fmt.Errorf("%w: barge-in stream %q: %w", ErrInvalidPCM16RoomAnalysisInput, streamID, err)
				}
				bargeAnalyses[streamID] = analysis
			}
		}
	}

	for _, overlap := range normalized.Overlaps {
		a := streams[overlap.A.SentStreamID]
		b := streams[overlap.B.SentStreamID]
		aReceived := streams[overlap.A.ReceivedStreamID]
		bReceived := streams[overlap.B.ReceivedStreamID]
		interval := overlap.PCM16TimeInterval
		forwardCorrelation, err := NormalizedPCM16CrossCorrelation(a, bReceived, interval, config.CorrelationLagWindow, config.CorrelationSilenceFloorDBFS)
		if err != nil {
			return PCM16RoomAnalysis{}, fmt.Errorf("%w: overlap %q forward correlation: %w", ErrInvalidPCM16RoomAnalysisInput, overlap.ID, err)
		}
		reverseCorrelation, err := NormalizedPCM16CrossCorrelation(b, aReceived, interval, config.CorrelationLagWindow, config.CorrelationSilenceFloorDBFS)
		if err != nil {
			return PCM16RoomAnalysis{}, fmt.Errorf("%w: overlap %q reverse correlation: %w", ErrInvalidPCM16RoomAnalysisInput, overlap.ID, err)
		}
		selfA, err := NormalizedPCM16CrossCorrelation(a, aReceived, interval, config.CorrelationLagWindow, config.CorrelationSilenceFloorDBFS)
		if err != nil {
			return PCM16RoomAnalysis{}, fmt.Errorf("%w: overlap %q self correlation for %q: %w", ErrInvalidPCM16RoomAnalysisInput, overlap.ID, overlap.A.ParticipantID, err)
		}
		selfB, err := NormalizedPCM16CrossCorrelation(b, bReceived, interval, config.CorrelationLagWindow, config.CorrelationSilenceFloorDBFS)
		if err != nil {
			return PCM16RoomAnalysis{}, fmt.Errorf("%w: overlap %q self correlation for %q: %w", ErrInvalidPCM16RoomAnalysisInput, overlap.ID, overlap.B.ParticipantID, err)
		}

		forward := PCM16PeerDeliveryMeasurement{
			PCM16CorrelationMeasurement: forwardCorrelation,
			Direction:                   fmt.Sprintf("%s->%s", overlap.A.ParticipantID, overlap.B.ParticipantID),
			Passed:                      forwardCorrelation.ComparedSamples > 0 && forwardCorrelation.BestCorrelation >= config.MinPeerCorrelation,
		}
		reverse := PCM16PeerDeliveryMeasurement{
			PCM16CorrelationMeasurement: reverseCorrelation,
			Direction:                   fmt.Sprintf("%s->%s", overlap.B.ParticipantID, overlap.A.ParticipantID),
			Passed:                      reverseCorrelation.ComparedSamples > 0 && reverseCorrelation.BestCorrelation >= config.MinPeerCorrelation,
		}
		selfAMeasurement := PCM16SelfHearingMeasurement{
			PCM16CorrelationMeasurement: selfA,
			Direction:                   fmt.Sprintf("%s->%s", overlap.A.ParticipantID, overlap.A.ParticipantID),
			Passed:                      selfA.BestAbsoluteCorrelation < config.MaxSelfCorrelation,
		}
		selfBMeasurement := PCM16SelfHearingMeasurement{
			PCM16CorrelationMeasurement: selfB,
			Direction:                   fmt.Sprintf("%s->%s", overlap.B.ParticipantID, overlap.B.ParticipantID),
			Passed:                      selfB.BestAbsoluteCorrelation < config.MaxSelfCorrelation,
		}
		loudness, err := MeasurePCM16Loudness(a, b, interval)
		if err != nil {
			return PCM16RoomAnalysis{}, fmt.Errorf("%w: overlap %q loudness: %w", ErrInvalidPCM16RoomAnalysisInput, overlap.ID, err)
		}
		loudness.Passed = loudness.LeftSamples > 0 && loudness.RightSamples > 0 && loudness.DifferenceDB <= config.MaxLoudnessDifferenceDB

		overlapResult := PCM16OverlapAnalysis{
			Interval: overlap,
			Forward:  forward,
			Reverse:  reverse,
			SelfA:    selfAMeasurement,
			SelfB:    selfBMeasurement,
			Loudness: loudness,
		}
		result.Overlaps = append(result.Overlaps, overlapResult)
		result.PeerDeliveries = append(result.PeerDeliveries, forward, reverse)
		result.SelfHearings = append(result.SelfHearings, selfAMeasurement, selfBMeasurement)
		result.Loudness = append(result.Loudness, loudness)
		appendCorrelationFailures(&result.Failures, forward, config.MinPeerCorrelation)
		appendCorrelationFailures(&result.Failures, reverse, config.MinPeerCorrelation)
		appendSelfHearingFailures(&result.Failures, selfAMeasurement, config.MaxSelfCorrelation)
		appendSelfHearingFailures(&result.Failures, selfBMeasurement, config.MaxSelfCorrelation)
		appendLoudnessFailure(&result.Failures, loudness, config.MaxLoudnessDifferenceDB)
	}

	for _, interval := range normalized.Loudness {
		left := streams[interval.LeftStreamID]
		right := streams[interval.RightStreamID]
		loudness, err := MeasurePCM16Loudness(left, right, interval.PCM16TimeInterval)
		if err != nil {
			return PCM16RoomAnalysis{}, fmt.Errorf("%w: loudness interval %q: %w", ErrInvalidPCM16RoomAnalysisInput, interval.ID, err)
		}
		loudness.Passed = loudness.LeftSamples > 0 && loudness.RightSamples > 0 && loudness.DifferenceDB <= config.MaxLoudnessDifferenceDB
		result.Loudness = append(result.Loudness, loudness)
		appendLoudnessFailure(&result.Failures, loudness, config.MaxLoudnessDifferenceDB)
	}

	for _, annotation := range normalized.BargeIns {
		interrupter := streams[annotation.InterrupterStreamID]
		interrupted := streams[annotation.InterruptedStreamID]
		measurement := measureBargeInFromAnalyses(interrupter, interrupted, annotation, bargeAnalyses[interrupter.StreamID], bargeAnalyses[interrupted.StreamID], config.BargeInSpeechThresholdDBFS, config.MaxBargeInLatency)
		result.BargeIns = append(result.BargeIns, measurement)
		appendBargeInFailures(&result.Failures, measurement, interrupter, interrupted, config.BargeInSpeechThresholdDBFS, config.MaxBargeInLatency)
	}

	return result, nil
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

func normalizePCM16RoomAnalysisConfig(config PCM16RoomAnalysisConfig) (PCM16RoomAnalysisConfig, error) {
	streamConfig, err := normalizePCM16AnalysisConfig(config.StreamConfig)
	if err != nil {
		return PCM16RoomAnalysisConfig{}, err
	}
	config.StreamConfig = streamConfig
	defaults := DefaultPCM16RoomAnalysisConfig
	if config.CorrelationLagWindow.Min == 0 && config.CorrelationLagWindow.Max == 0 {
		config.CorrelationLagWindow = defaults.CorrelationLagWindow
	}
	if config.CorrelationSilenceFloorDBFS == 0 {
		config.CorrelationSilenceFloorDBFS = defaults.CorrelationSilenceFloorDBFS
	}
	if config.MinPeerCorrelation == 0 {
		config.MinPeerCorrelation = defaults.MinPeerCorrelation
	}
	if config.MaxSelfCorrelation == 0 {
		config.MaxSelfCorrelation = defaults.MaxSelfCorrelation
	}
	if config.BargeInSpeechThresholdDBFS == 0 {
		config.BargeInSpeechThresholdDBFS = defaults.BargeInSpeechThresholdDBFS
	}
	if config.MaxBargeInLatency == 0 {
		config.MaxBargeInLatency = defaults.MaxBargeInLatency
	}
	if config.MaxLoudnessDifferenceDB == 0 {
		config.MaxLoudnessDifferenceDB = defaults.MaxLoudnessDifferenceDB
	}
	if config.MaxDriftAbsolute == 0 {
		config.MaxDriftAbsolute = defaults.MaxDriftAbsolute
	}
	if config.MaxDriftFraction == 0 {
		config.MaxDriftFraction = defaults.MaxDriftFraction
	}
	switch {
	case config.CorrelationLagWindow.Min > config.CorrelationLagWindow.Max:
		return PCM16RoomAnalysisConfig{}, invalidPCM16RoomAnalysis("correlation_lag_window", "min must be at or before max")
	case !isFinite(config.CorrelationSilenceFloorDBFS) || config.CorrelationSilenceFloorDBFS > 0:
		return PCM16RoomAnalysisConfig{}, invalidPCM16RoomAnalysis("correlation_silence_floor_dbfs", "must be finite and at or below 0")
	case !isFinite(config.MinPeerCorrelation) || config.MinPeerCorrelation < 0 || config.MinPeerCorrelation > 1:
		return PCM16RoomAnalysisConfig{}, invalidPCM16RoomAnalysis("min_peer_correlation", "must be between 0 and 1")
	case !isFinite(config.MaxSelfCorrelation) || config.MaxSelfCorrelation < 0 || config.MaxSelfCorrelation > 1:
		return PCM16RoomAnalysisConfig{}, invalidPCM16RoomAnalysis("max_self_correlation", "must be between 0 and 1")
	case !isFinite(config.BargeInSpeechThresholdDBFS) || config.BargeInSpeechThresholdDBFS > 0:
		return PCM16RoomAnalysisConfig{}, invalidPCM16RoomAnalysis("barge_in_speech_threshold_dbfs", "must be finite and at or below 0")
	case config.MaxBargeInLatency <= 0:
		return PCM16RoomAnalysisConfig{}, invalidPCM16RoomAnalysis("max_barge_in_latency", "must be positive")
	case !isFinite(config.MaxLoudnessDifferenceDB) || config.MaxLoudnessDifferenceDB < 0:
		return PCM16RoomAnalysisConfig{}, invalidPCM16RoomAnalysis("max_loudness_difference_db", "must be finite and non-negative")
	case config.MaxDriftAbsolute <= 0:
		return PCM16RoomAnalysisConfig{}, invalidPCM16RoomAnalysis("max_drift_absolute", "must be positive")
	case !isFinite(config.MaxDriftFraction) || config.MaxDriftFraction < 0:
		return PCM16RoomAnalysisConfig{}, invalidPCM16RoomAnalysis("max_drift_fraction", "must be finite and non-negative")
	}
	return config, nil
}

func normalizePCM16RoomInput(input PCM16RoomInput) (PCM16RoomInput, map[string]PCM16TimedStream, error) {
	if len(input.Streams) == 0 {
		return PCM16RoomInput{}, nil, invalidPCM16RoomAnalysis("streams", "must not be empty")
	}
	normalized := input
	normalized.Streams = append([]PCM16TimedStream(nil), input.Streams...)
	normalized.Overlaps = append([]PCM16OverlapInterval(nil), input.Overlaps...)
	normalized.BargeIns = append([]PCM16BargeInAnnotation(nil), input.BargeIns...)
	normalized.Loudness = append([]PCM16LoudnessInterval(nil), input.Loudness...)
	streams := make(map[string]PCM16TimedStream, len(input.Streams))
	for index, stream := range normalized.Streams {
		field := fmt.Sprintf("streams[%d]", index)
		if stream.StreamID == "" {
			return PCM16RoomInput{}, nil, invalidPCM16RoomAnalysis(field+".stream_id", "must not be empty")
		}
		if stream.ParticipantID == "" {
			return PCM16RoomInput{}, nil, invalidPCM16RoomAnalysis(field+".participant_id", "must not be empty")
		}
		if _, exists := streams[stream.StreamID]; exists {
			return PCM16RoomInput{}, nil, invalidPCM16RoomAnalysis(field+".stream_id", fmt.Sprintf("duplicate stream identity %q", stream.StreamID))
		}
		if err := validateTimedStream(stream, field); err != nil {
			return PCM16RoomInput{}, nil, err
		}
		streams[stream.StreamID] = stream
	}

	for index := range normalized.Overlaps {
		overlap := &normalized.Overlaps[index]
		if overlap.ID == "" {
			overlap.ID = fmt.Sprintf("overlap-%d", index)
		}
		if err := validateOverlapInput(*overlap, streams, fmt.Sprintf("overlaps[%d]", index)); err != nil {
			return PCM16RoomInput{}, nil, err
		}
	}
	for index := range normalized.BargeIns {
		barge := &normalized.BargeIns[index]
		if barge.ID == "" {
			barge.ID = fmt.Sprintf("barge-in-%d", index)
		}
		if err := validateBargeInput(*barge, streams, fmt.Sprintf("barge_ins[%d]", index)); err != nil {
			return PCM16RoomInput{}, nil, err
		}
	}
	for index := range normalized.Loudness {
		loudness := &normalized.Loudness[index]
		if loudness.ID == "" {
			loudness.ID = fmt.Sprintf("loudness-%d", index)
		}
		if err := validateLoudnessInput(*loudness, streams, fmt.Sprintf("loudness[%d]", index)); err != nil {
			return PCM16RoomInput{}, nil, err
		}
	}
	return normalized, streams, nil
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

func sampleIndexAt(stream PCM16TimedStream, timestamp time.Duration) (int, error) {
	offset := timestamp - stream.TimelineStart
	if offset < 0 {
		return 0, invalidPCM16RoomAnalysis("timestamp", fmt.Sprintf("%s precedes stream %q timeline start %s", timestamp, stream.StreamID, stream.TimelineStart))
	}
	index, err := durationToSamples(offset, stream.SampleRate)
	if err != nil {
		return 0, invalidPCM16RoomAnalysis("timestamp", err.Error())
	}
	if index < 0 || index > len(stream.Samples) {
		return 0, invalidPCM16RoomAnalysis("timestamp", fmt.Sprintf("%s maps to sample %d outside stream %q sample count %d", timestamp, index, stream.StreamID, len(stream.Samples)))
	}
	return index, nil
}

func signedDurationToSamples(duration time.Duration, sampleRate int) (int, error) {
	if duration >= 0 {
		return durationToSamples(duration, sampleRate)
	}
	positive, err := durationToSamples(-duration, sampleRate)
	if err != nil {
		return 0, err
	}
	return -positive, nil
}

func samplesToSignedDuration(samples, sampleRate int) time.Duration {
	if samples == 0 || sampleRate <= 0 {
		return 0
	}
	negative := samples < 0
	if negative {
		samples = -samples
	}
	duration := samplesToDuration(samples, sampleRate)
	if negative {
		return -duration
	}
	return duration
}

func normalizedCorrelationAtLag(source []int16, sourceActivity []bool, received []int16, receivedStart int, threshold float64) (float64, int) {
	var sumSource, sumReceived, sourceEnergy, receivedEnergy, cross float64
	compared := 0
	for offset, sourceSample := range source {
		if len(sourceActivity) == len(source) && !sourceActivity[offset] {
			continue
		}
		receivedIndex := receivedStart + offset
		if receivedIndex < 0 || receivedIndex >= len(received) {
			continue
		}
		receivedSample := received[receivedIndex]
		if float64(absoluteSample(sourceSample)) <= threshold && float64(absoluteSample(receivedSample)) <= threshold {
			continue
		}
		x := float64(sourceSample)
		y := float64(receivedSample)
		sumSource += x
		sumReceived += y
		sourceEnergy += x * x
		receivedEnergy += y * y
		cross += x * y
		compared++
	}
	if compared < 2 || sourceEnergy == 0 || receivedEnergy == 0 {
		return 0, compared
	}
	meanSource := sumSource / float64(compared)
	meanReceived := sumReceived / float64(compared)
	centeredSourceEnergy := sourceEnergy - float64(compared)*meanSource*meanSource
	centeredReceivedEnergy := receivedEnergy - float64(compared)*meanReceived*meanReceived
	centeredCross := cross - float64(compared)*meanSource*meanReceived
	if centeredSourceEnergy <= 0 || centeredReceivedEnergy <= 0 {
		// A constant PCM signal has no Pearson variance, but an exact constant
		// copy is still useful evidence for deterministic synthetic fixtures.
		coefficient := cross / math.Sqrt(sourceEnergy*receivedEnergy)
		if !isFinite(coefficient) {
			return 0, compared
		}
		return coefficient, compared
	}
	coefficient := centeredCross / math.Sqrt(centeredSourceEnergy*centeredReceivedEnergy)
	if !isFinite(coefficient) {
		return 0, compared
	}
	return coefficient, compared
}

func annotatedActivityMask(stream PCM16TimedStream, start, end int) ([]bool, error) {
	mask := make([]bool, end-start)
	annotations, err := normalizeSpeechAnnotations(stream.ExpectedSpeech, len(stream.Samples), stream.SampleRate)
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
		left := multiMaxInt(start, annotation.startSample)
		right := multiMinInt(end, annotation.endSample)
		for index := left; index < right; index++ {
			mask[index-start] = true
		}
	}
	return mask, nil
}

func pcm16AmplitudeForDBFS(level float64) float64 {
	if math.IsInf(level, -1) {
		return 0
	}
	return math.Pow(10, level/20.0) * 32768.0
}

func activeRMS(stream PCM16TimedStream, interval PCM16TimeInterval) (float64, int, error) {
	start, end, err := sampleRangeForInterval(stream, interval)
	if err != nil {
		return 0, 0, err
	}
	annotations, err := normalizeSpeechAnnotations(stream.ExpectedSpeech, len(stream.Samples), stream.SampleRate)
	if err != nil {
		return 0, 0, err
	}
	ranges := make([]sampleRange, 0, len(annotations))
	if len(annotations) == 0 {
		ranges = append(ranges, sampleRange{start: start, end: end})
	} else {
		for _, annotation := range annotations {
			left := multiMaxInt(start, annotation.startSample)
			right := multiMinInt(end, annotation.endSample)
			if right > left {
				ranges = append(ranges, sampleRange{start: left, end: right})
			}
		}
	}
	var energy float64
	count := 0
	for _, valueRange := range ranges {
		for _, sample := range stream.Samples[valueRange.start:valueRange.end] {
			value := float64(sample)
			energy += value * value
			count++
		}
	}
	if count == 0 {
		return 0, 0, nil
	}
	return math.Sqrt(energy / float64(count)), count, nil
}

type sampleRange struct {
	start int
	end   int
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
	interrupterAnalysis, err := AnalyzePCM16(interrupter.PCM16Input, DefaultPCM16AnalysisConfig)
	if err != nil {
		return PCM16BargeInMeasurement{}, err
	}
	interruptedAnalysis, err := AnalyzePCM16(interrupted.PCM16Input, DefaultPCM16AnalysisConfig)
	if err != nil {
		return PCM16BargeInMeasurement{}, err
	}
	return measureBargeInFromAnalyses(interrupter, interrupted, annotation, interrupterAnalysis, interruptedAnalysis, thresholdDBFS, PCM16AnalysisDefaultBargeInMaxLatency), nil
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
		failure.Unit = "milliseconds"
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
		failure.Unit = "milliseconds"
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

func multiMaxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func multiMinInt(left, right int) int {
	if left < right {
		return left
	}
	return right
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
