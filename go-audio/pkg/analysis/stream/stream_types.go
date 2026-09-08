// Package stream contains deterministic diagnostics for one PCM16 stream. It
// owns no devices, clocks, or provider state, so a runtime can use the report
// independently of the audio transport core.
package stream

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	pcm16FullScale                = 32768.0
	pcm16DBFSDecibels             = 20
	propertyFailureDetailCapacity = 6
)

const (
	// PCM16AnalysisFrameDuration is the stable analysis window used by the
	// audio regression suite. It is intentionally independent of the 30 ms
	// runtime VAD frame used by this package.
	PCM16AnalysisFrameDuration = 20 * time.Millisecond
	// PCM16AnalysisSilenceFloorDBFS is the inclusive level at or below which an
	// analysis frame is considered silent.
	PCM16AnalysisSilenceFloorDBFS = -50.0
	// PCM16AnalysisMaxNaturalPause is the longest quiet interval that can be
	// treated as a natural pause while expected speech is active.
	PCM16AnalysisMaxNaturalPause = 750 * time.Millisecond
	// PCM16AnalysisBoundaryDelta is the minimum adjacent-sample jump worth
	// inspecting at a recorded chunk boundary.
	PCM16AnalysisBoundaryDelta = 6000
	// PCM16AnalysisBoundaryQuietDBFS is the maximum level for both windows
	// around a boundary before a large jump is considered a click.
	PCM16AnalysisBoundaryQuietDBFS = -24.0
	// PCM16AnalysisClipSampleThreshold is the inclusive PCM16 sample level
	// counted as clipping or near-full-scale clipping.
	PCM16AnalysisClipSampleThreshold = 32700
	// PCM16AnalysisEdgeSampleThreshold is the largest permitted absolute first
	// or last sample for a clean turn edge.
	PCM16AnalysisEdgeSampleThreshold = 1000
	// PCM16AnalysisFinalFrameMaxRMSDBFS is the loudest permitted final analysis
	// window for a clean turn edge.
	PCM16AnalysisFinalFrameMaxRMSDBFS = -40.0
)

var (
	// ErrInvalidPCM16AnalysisInput identifies an input or configuration that
	// cannot be interpreted deterministically.
	ErrInvalidPCM16AnalysisInput = errors.New("invalid PCM16 analysis input")
	// ErrPCM16AnalysisFailed identifies a valid stream that violates one or
	// more configured audio properties.
	ErrPCM16AnalysisFailed = errors.New("PCM16 audio analysis failed")
)

// PCM16AnalysisConfig contains the explicit bounds used by AnalyzePCM16.
// Zero-valued fields select the corresponding default, which makes a zero
// config useful while still allowing fixture profiles to tighten one bound.
type PCM16AnalysisConfig struct {
	FrameDuration        time.Duration
	SilenceFloorDBFS     float64
	MaxNaturalPause      time.Duration
	BoundaryDelta        int
	BoundaryQuietDBFS    float64
	ClipSampleThreshold  int
	EdgeSampleThreshold  int
	FinalFrameMaxRMSDBFS float64
}

// DefaultPCM16AnalysisConfig returns a fresh copy of the suite-wide profile.
// Callers may tighten the returned value without changing another analysis.
func DefaultPCM16AnalysisConfig() PCM16AnalysisConfig {
	return PCM16AnalysisConfig{
		FrameDuration:        PCM16AnalysisFrameDuration,
		SilenceFloorDBFS:     PCM16AnalysisSilenceFloorDBFS,
		MaxNaturalPause:      PCM16AnalysisMaxNaturalPause,
		BoundaryDelta:        PCM16AnalysisBoundaryDelta,
		BoundaryQuietDBFS:    PCM16AnalysisBoundaryQuietDBFS,
		ClipSampleThreshold:  PCM16AnalysisClipSampleThreshold,
		EdgeSampleThreshold:  PCM16AnalysisEdgeSampleThreshold,
		FinalFrameMaxRMSDBFS: PCM16AnalysisFinalFrameMaxRMSDBFS,
	}
}

// DefaultAnalysisConfig returns a copy of the default analysis profile.
func DefaultAnalysisConfig() PCM16AnalysisConfig { return DefaultPCM16AnalysisConfig() }

// PCM16Input is the complete identity, timing, and annotation input for one
// mono PCM16 stream. Samples are never modified by AnalyzePCM16.
type PCM16Input struct {
	StreamID      string
	ParticipantID string
	SampleRate    int
	Samples       []int16

	// ExpectedSpeech identifies regions in which a long silent run is a
	// dropout rather than an expected pause. An annotation can use exact
	// sample endpoints or exact time endpoints; mixing both forms is invalid.
	ExpectedSpeech []SpeechAnnotation

	// ChunkBoundaries contains the sample index immediately after each
	// recorded chunk. A boundary at sample n compares samples n-1 and n.
	ChunkBoundaries []ChunkBoundary
}

// SpeechAnnotation marks expected speech. Endpoints are half-open. Use either
// StartSample/EndSample or Start/End, not both. Sample endpoints are useful
// for generated fixtures; time endpoints are useful for room timelines.
type SpeechAnnotation struct {
	Label string

	StartSample int
	EndSample   int

	Start time.Duration
	End   time.Duration
}

// SpeechInterval is a concise alias for SpeechAnnotation.
type SpeechInterval = SpeechAnnotation

// ExpectedSpeechAnnotation is an explicit alias for callers that prefer the
// annotation terminology used by fixture manifests.
type ExpectedSpeechAnnotation = SpeechAnnotation

// ChunkBoundary identifies a recorded PCM chunk boundary by sample index.
type ChunkBoundary struct {
	ID          string
	SampleIndex int
}

// PCM16SampleLocation points at one sample without retaining the input audio.
type PCM16SampleLocation struct {
	SampleIndex int
	FrameIndex  int
	Timestamp   time.Duration
	Value       int16
	AbsValue    int
}

// PCM16Frame is the measured result for one contiguous analysis window. The
// final frame is never padded; Partial is true when it has fewer samples than
// the configured window.
type PCM16Frame struct {
	Index       int
	StartSample int
	EndSample   int
	SampleCount int
	Timestamp   time.Duration
	Duration    time.Duration
	Partial     bool

	RMS            float64
	RMSDBFS        float64
	AbsolutePeak   int
	PeakDBFS       float64
	HeadroomDBFS   float64
	ClipCount      int
	ClippedSamples []PCM16SampleLocation
}

// SilentRun describes one contiguous run of silent analysis frames. Natural
// pauses are retained in SilentRuns; only runs marked Dropout are failures.
type SilentRun struct {
	StartSample int
	EndSample   int
	Start       time.Duration
	End         time.Duration
	Duration    time.Duration

	RMS     float64
	RMSDBFS float64

	ExpectedSpeechOverlap time.Duration
	InExpectedSpeech      bool
	NaturalPause          bool
	Dropout               bool
}

// BoundaryCheck records the evidence used for a chunk-boundary click check.
// A loud neighboring window suppresses the click failure and retains the
// large jump as an impulse candidate for later inspection.
type BoundaryCheck struct {
	ID          string
	SampleIndex int
	Timestamp   time.Duration

	PreviousSample int16
	NextSample     int16
	Delta          int

	PreviousWindowRMSDBFS float64
	NextWindowRMSDBFS     float64
	SuspiciousClick       bool
	ImpulseCandidate      bool
}

// EdgeMeasurement contains the clean-turn edge evidence.
type EdgeMeasurement struct {
	FirstSample      int16
	FirstAbsValue    int
	LastSample       int16
	LastAbsValue     int
	FinalStartSample int
	FinalEndSample   int
	FinalRMS         float64
	FinalRMSDBFS     float64
}

// PropertyFailure is a structured, user-actionable property failure. Indexes
// use -1 when a particular coordinate is not applicable. Measured and Bound
// use Unit to make numeric diagnostics unambiguous.
type PropertyFailure struct {
	Property         string
	StreamID         string
	ParticipantID    string
	SourceStreamID   string
	ReceivedStreamID string
	Direction        string
	Interval         string
	TurnID           string

	StartSample   int
	EndSample     int
	SampleIndex   int
	FrameIndex    int
	BoundaryID    string
	BoundaryIndex int
	Timestamp     time.Duration
	Lag           time.Duration

	Measured   float64
	Comparison string
	Bound      float64
	Unit       string
	Detail     string
}

// Error formats the complete diagnostic while keeping the structured fields
// available to callers through errors.As.
func (f PropertyFailure) Error() string {
	parts := []string{f.Property}
	parts = append(parts, f.identityDetails()...)
	parts = append(parts, f.locationDetails()...)
	parts = append(parts, f.measurementDetails()...)
	return strings.Join(parts, " ")
}

func (f PropertyFailure) identityDetails() []string {
	parts := make([]string, 0, propertyFailureDetailCapacity)
	if f.StreamID != "" {
		parts = append(parts, fmt.Sprintf("stream=%q", f.StreamID))
	}
	if f.ParticipantID != "" {
		parts = append(parts, fmt.Sprintf("participant=%q", f.ParticipantID))
	}
	if f.SourceStreamID != "" || f.ReceivedStreamID != "" {
		parts = append(parts, fmt.Sprintf("source=%q received=%q", f.SourceStreamID, f.ReceivedStreamID))
	}
	if f.Direction != "" {
		parts = append(parts, fmt.Sprintf("direction=%q", f.Direction))
	}
	if f.Interval != "" {
		parts = append(parts, fmt.Sprintf("interval=%q", f.Interval))
	}
	if f.TurnID != "" {
		parts = append(parts, fmt.Sprintf("turn=%q", f.TurnID))
	}
	return parts
}

func (f PropertyFailure) locationDetails() []string {
	parts := make([]string, 0, propertyFailureDetailCapacity)
	if f.StartSample >= 0 && f.EndSample >= 0 {
		parts = append(parts, fmt.Sprintf("samples=%d..%d", f.StartSample, f.EndSample))
	}
	if f.SampleIndex >= 0 {
		parts = append(parts, fmt.Sprintf("sample=%d", f.SampleIndex))
	}
	if f.FrameIndex >= 0 {
		parts = append(parts, fmt.Sprintf("frame=%d", f.FrameIndex))
	}
	if f.BoundaryIndex >= 0 {
		parts = append(parts, fmt.Sprintf("boundary=%d", f.BoundaryIndex))
	}
	if f.BoundaryID != "" {
		parts = append(parts, fmt.Sprintf("boundary-id=%q", f.BoundaryID))
	}
	if f.Lag != 0 {
		parts = append(parts, fmt.Sprintf("lag=%s", f.Lag))
	}
	return parts
}

func (f PropertyFailure) measurementDetails() []string {
	parts := []string{
		fmt.Sprintf("timestamp=%s", f.Timestamp),
		fmt.Sprintf("measured=%s %s", formatAnalysisNumber(f.Measured), f.Unit),
		fmt.Sprintf("%s bound=%s %s", f.Comparison, formatAnalysisNumber(f.Bound), f.Unit),
	}
	if f.Detail != "" {
		parts = append(parts, f.Detail)
	}
	return parts
}

// PCM16Analysis is the deterministic report for one stream.
type PCM16Analysis struct {
	StreamID      string
	ParticipantID string
	SampleRate    int
	FrameDuration time.Duration
	FrameSamples  int
	SampleCount   int
	Duration      time.Duration

	RMS          float64
	RMSDBFS      float64
	AbsolutePeak int
	PeakDBFS     float64
	HeadroomDBFS float64
	ClipCount    int

	Frames         []PCM16Frame
	ClippedSamples []PCM16SampleLocation
	SilentRuns     []SilentRun
	Dropouts       []SilentRun

	BoundaryChecks    []BoundaryCheck
	BoundaryClicks    []BoundaryCheck
	ImpulseCandidates []BoundaryCheck
	Edges             EdgeMeasurement
	Failures          []PropertyFailure
}

// Passed reports whether the valid stream satisfied every configured
// property. Invalid inputs are returned as errors before a report is built.
func (a PCM16Analysis) Passed() bool { return len(a.Failures) == 0 }

// FailuresCopy returns caller-owned failure storage.
func (a PCM16Analysis) FailuresCopy() []PropertyFailure {
	return append([]PropertyFailure(nil), a.Failures...)
}

// PCM16AssertionError wraps all property failures from one valid stream.
type PCM16AssertionError struct {
	StreamID string
	Failures []PropertyFailure
}

func (e *PCM16AssertionError) Error() string {
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
	return fmt.Sprintf("%s: %s", ErrPCM16AnalysisFailed, strings.Join(parts, "; "))
}

func (e *PCM16AssertionError) Unwrap() error { return ErrPCM16AnalysisFailed }

// FailuresCopy returns caller-owned failure storage.
func (e *PCM16AssertionError) FailuresCopy() []PropertyFailure {
	if e == nil {
		return nil
	}
	return append([]PropertyFailure(nil), e.Failures...)
}

// InvalidPCM16AnalysisInputError identifies one invalid input or profile
// field. It deliberately preserves the field name for fixture diagnostics.
type InvalidPCM16AnalysisInputError struct {
	Field  string
	Reason string
}

func (e *InvalidPCM16AnalysisInputError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Field == "" {
		return fmt.Sprintf("%s: %s", ErrInvalidPCM16AnalysisInput, e.Reason)
	}
	return fmt.Sprintf("%s: %s: %s", ErrInvalidPCM16AnalysisInput, e.Field, e.Reason)
}

func (e *InvalidPCM16AnalysisInputError) Unwrap() error { return ErrInvalidPCM16AnalysisInput }
