// Package room analyzes relationships between independently recorded PCM16
// streams, including peer delivery, self-hearing, barge-in, loudness, and
// timeline drift.
package room

import (
	"errors"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/stream"
)

// Stream contracts are embedded in room inputs and reports. Aliasing the
// neutral stream types keeps room values assignable to the stream analyzer
// without retaining a second copy of their error or measurement shapes.
type (
	PCM16Input                     = stream.PCM16Input
	SpeechAnnotation               = stream.SpeechAnnotation
	PCM16AnalysisConfig            = stream.PCM16AnalysisConfig
	PCM16Analysis                  = stream.PCM16Analysis
	PropertyFailure                = stream.PropertyFailure
	PCM16LagWindow                 = stream.PCM16LagWindow
	PCM16CorrelationMeasurement    = stream.PCM16CorrelationMeasurement
	InvalidPCM16AnalysisInputError = stream.InvalidPCM16AnalysisInputError
)

const (
	PCM16AnalysisFrameDuration    = stream.PCM16AnalysisFrameDuration
	PCM16AnalysisSilenceFloorDBFS = stream.PCM16AnalysisSilenceFloorDBFS
)

var (
	ErrInvalidPCM16AnalysisInput = stream.ErrInvalidPCM16AnalysisInput
	ErrPCM16AnalysisFailed       = stream.ErrPCM16AnalysisFailed
)

// DefaultPCM16AnalysisConfig returns a fresh stream profile for callers that
// build a room profile incrementally.
func DefaultPCM16AnalysisConfig() PCM16AnalysisConfig { return stream.DefaultPCM16AnalysisConfig() }

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

// DefaultPCM16RoomAnalysisConfig returns a fresh profile for room assertions.
func DefaultPCM16RoomAnalysisConfig() PCM16RoomAnalysisConfig {
	return PCM16RoomAnalysisConfig{
		StreamConfig:                stream.DefaultPCM16AnalysisConfig(),
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
}

// DefaultRoomAnalysisConfig returns a copy of the default room profile.
func DefaultRoomAnalysisConfig() PCM16RoomAnalysisConfig { return DefaultPCM16RoomAnalysisConfig() }

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
