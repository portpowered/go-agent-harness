package audio

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
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

// DefaultPCM16AnalysisConfig is the suite-wide default profile. Callers may
// copy it and tighten individual bounds for a verified fixture.
var DefaultPCM16AnalysisConfig = PCM16AnalysisConfig{
	FrameDuration:        PCM16AnalysisFrameDuration,
	SilenceFloorDBFS:     PCM16AnalysisSilenceFloorDBFS,
	MaxNaturalPause:      PCM16AnalysisMaxNaturalPause,
	BoundaryDelta:        PCM16AnalysisBoundaryDelta,
	BoundaryQuietDBFS:    PCM16AnalysisBoundaryQuietDBFS,
	ClipSampleThreshold:  PCM16AnalysisClipSampleThreshold,
	EdgeSampleThreshold:  PCM16AnalysisEdgeSampleThreshold,
	FinalFrameMaxRMSDBFS: PCM16AnalysisFinalFrameMaxRMSDBFS,
}

// DefaultAnalysisConfig returns a copy of the default analysis profile.
func DefaultAnalysisConfig() PCM16AnalysisConfig { return DefaultPCM16AnalysisConfig }

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
	parts = append(parts,
		fmt.Sprintf("timestamp=%s", f.Timestamp),
		fmt.Sprintf("measured=%s %s", formatAnalysisNumber(f.Measured), f.Unit),
		fmt.Sprintf("%s bound=%s %s", f.Comparison, formatAnalysisNumber(f.Bound), f.Unit),
	)
	if f.Detail != "" {
		parts = append(parts, f.Detail)
	}
	return strings.Join(parts, " ")
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

// AnalyzePCM16 measures and evaluates one explicit mono PCM16 stream. It is
// side-effect-free: the input sample slice and all annotation slices remain
// untouched, and the returned report owns its slices.
func AnalyzePCM16(input PCM16Input, config PCM16AnalysisConfig) (PCM16Analysis, error) {
	config, err := normalizePCM16AnalysisConfig(config)
	if err != nil {
		return PCM16Analysis{}, err
	}
	streamID := input.StreamID
	if streamID == "" {
		streamID = "stream"
	}
	if input.SampleRate <= 0 {
		return PCM16Analysis{}, invalidPCM16Analysis("sample_rate", "must be positive")
	}
	if len(input.Samples) == 0 {
		return PCM16Analysis{}, invalidPCM16Analysis("samples", "must not be empty")
	}
	frameSamples, err := analysisFrameSamples(input.SampleRate, config.FrameDuration)
	if err != nil {
		return PCM16Analysis{}, err
	}
	annotations, err := normalizeSpeechAnnotations(input.ExpectedSpeech, len(input.Samples), input.SampleRate)
	if err != nil {
		return PCM16Analysis{}, err
	}
	boundaries, err := validateChunkBoundaries(input.ChunkBoundaries, len(input.Samples), frameSamples)
	if err != nil {
		return PCM16Analysis{}, err
	}

	samples := input.Samples
	analysis := PCM16Analysis{
		StreamID:      streamID,
		ParticipantID: input.ParticipantID,
		SampleRate:    input.SampleRate,
		FrameDuration: config.FrameDuration,
		FrameSamples:  frameSamples,
		SampleCount:   len(samples),
		Duration:      samplesToDuration(len(samples), input.SampleRate),
		RMS:           rmsEnergy(samples),
	}
	analysis.RMSDBFS = dbfs(analysis.RMS)
	analysis.AbsolutePeak = absolutePeak(samples)
	analysis.PeakDBFS = dbfs(float64(analysis.AbsolutePeak))
	analysis.HeadroomDBFS = headroomDBFS(analysis.AbsolutePeak)

	frameCount := (len(samples) + frameSamples - 1) / frameSamples
	analysis.Frames = make([]PCM16Frame, 0, frameCount)
	for frameIndex, start := 0, 0; start < len(samples); frameIndex, start = frameIndex+1, start+frameSamples {
		end := start + frameSamples
		if end > len(samples) {
			end = len(samples)
		}
		frame := makePCM16Frame(samples[start:end], start, end, frameIndex, input.SampleRate, frameSamples, config.ClipSampleThreshold)
		analysis.ClipCount += frame.ClipCount
		analysis.ClippedSamples = append(analysis.ClippedSamples, frame.ClippedSamples...)
		analysis.Frames = append(analysis.Frames, frame)
	}
	if analysis.ClipCount > 0 {
		failure := analysisFailure("clipping", streamID, input.ParticipantID)
		failure.Interval = fmt.Sprintf("frame-%d", analysis.ClippedSamples[0].FrameIndex)
		failure.StartSample = analysis.ClippedSamples[0].SampleIndex
		failure.EndSample = failure.StartSample + 1
		failure.SampleIndex = failure.StartSample
		failure.FrameIndex = analysis.ClippedSamples[0].FrameIndex
		failure.Timestamp = analysis.ClippedSamples[0].Timestamp
		failure.Measured = float64(analysis.ClippedSamples[0].AbsValue)
		failure.Comparison = ">="
		failure.Bound = float64(config.ClipSampleThreshold)
		failure.Unit = "absolute PCM16 sample"
		failure.Detail = fmt.Sprintf("%d sample(s) reached |sample| >= %d", analysis.ClipCount, config.ClipSampleThreshold)
		analysis.Failures = append(analysis.Failures, failure)
	}

	analysis.SilentRuns = analyzeSilentRuns(samples, analysis.Frames, annotations, input.SampleRate, config, streamID, input.ParticipantID, &analysis.Failures)
	analysis.Dropouts = make([]SilentRun, 0)
	for _, run := range analysis.SilentRuns {
		if run.Dropout {
			analysis.Dropouts = append(analysis.Dropouts, run)
		}
	}

	analysis.BoundaryChecks = make([]BoundaryCheck, 0, len(boundaries))
	for boundaryIndex, boundary := range boundaries {
		check := makeBoundaryCheck(samples, boundary, input.SampleRate, frameSamples, config)
		analysis.BoundaryChecks = append(analysis.BoundaryChecks, check)
		if check.SuspiciousClick {
			analysis.BoundaryClicks = append(analysis.BoundaryClicks, check)
			failure := analysisFailure("quiet-boundary-click", streamID, input.ParticipantID)
			failure.Interval = boundaryLabel(boundary)
			failure.SampleIndex = boundary.SampleIndex
			failure.FrameIndex = boundary.SampleIndex / frameSamples
			failure.BoundaryID = boundary.ID
			failure.BoundaryIndex = boundaryIndex
			failure.Timestamp = check.Timestamp
			failure.Measured = float64(check.Delta)
			failure.Comparison = ">"
			failure.Bound = float64(config.BoundaryDelta)
			failure.Unit = "PCM16 sample delta"
			failure.Detail = fmt.Sprintf("neighbor windows %.2f/%.2f dBFS are both quieter than %.2f dBFS", check.PreviousWindowRMSDBFS, check.NextWindowRMSDBFS, config.BoundaryQuietDBFS)
			analysis.Failures = append(analysis.Failures, failure)
		} else if check.ImpulseCandidate {
			analysis.ImpulseCandidates = append(analysis.ImpulseCandidates, check)
		}
	}

	analysis.Edges = makeEdgeMeasurement(samples, frameSamples)
	if analysis.Edges.FirstAbsValue > config.EdgeSampleThreshold {
		failure := analysisFailure("leading-click", streamID, input.ParticipantID)
		failure.Interval = "turn-start"
		failure.SampleIndex = 0
		failure.FrameIndex = 0
		failure.Timestamp = 0
		failure.Measured = float64(analysis.Edges.FirstAbsValue)
		failure.Comparison = ">"
		failure.Bound = float64(config.EdgeSampleThreshold)
		failure.Unit = "absolute PCM16 sample"
		failure.Detail = "leading turn edge exceeds the clean-edge bound"
		analysis.Failures = append(analysis.Failures, failure)
	}
	lastSampleIndex := len(samples) - 1
	if analysis.Edges.LastAbsValue > config.EdgeSampleThreshold {
		failure := analysisFailure("trailing-click", streamID, input.ParticipantID)
		failure.Interval = "turn-end"
		failure.SampleIndex = lastSampleIndex
		failure.FrameIndex = lastSampleIndex / frameSamples
		failure.Timestamp = samplesToDuration(lastSampleIndex, input.SampleRate)
		failure.Measured = float64(analysis.Edges.LastAbsValue)
		failure.Comparison = ">"
		failure.Bound = float64(config.EdgeSampleThreshold)
		failure.Unit = "absolute PCM16 sample"
		failure.Detail = "trailing turn edge exceeds the clean-edge bound"
		analysis.Failures = append(analysis.Failures, failure)
	}
	if analysis.Edges.FinalRMSDBFS > config.FinalFrameMaxRMSDBFS {
		failure := analysisFailure("probable-truncation-pop", streamID, input.ParticipantID)
		failure.Interval = "final-20ms"
		failure.StartSample = analysis.Edges.FinalStartSample
		failure.EndSample = analysis.Edges.FinalEndSample
		failure.SampleIndex = analysis.Edges.FinalStartSample
		failure.FrameIndex = analysis.Edges.FinalStartSample / frameSamples
		failure.Timestamp = samplesToDuration(analysis.Edges.FinalStartSample, input.SampleRate)
		failure.Measured = analysis.Edges.FinalRMSDBFS
		failure.Comparison = ">"
		failure.Bound = config.FinalFrameMaxRMSDBFS
		failure.Unit = "dBFS"
		failure.Detail = "final 20 ms window is loud enough to suggest a truncated turn"
		analysis.Failures = append(analysis.Failures, failure)
	}

	return analysis, nil
}

// Analyze is a concise alias for AnalyzePCM16.
func Analyze(input PCM16Input, config PCM16AnalysisConfig) (PCM16Analysis, error) {
	return AnalyzePCM16(input, config)
}

// AssertPCM16 evaluates a stream and returns a typed error for any measured
// property violation. The report-oriented AnalyzePCM16 function remains
// available when callers need all measurements and failures.
func AssertPCM16(input PCM16Input, config PCM16AnalysisConfig) error {
	analysis, err := AnalyzePCM16(input, config)
	if err != nil {
		return err
	}
	if analysis.Passed() {
		return nil
	}
	return &PCM16AssertionError{StreamID: analysis.StreamID, Failures: analysis.FailuresCopy()}
}

// ValidatePCM16 is an assertion-oriented alias for AssertPCM16.
func ValidatePCM16(input PCM16Input, config PCM16AnalysisConfig) error {
	return AssertPCM16(input, config)
}

func makePCM16Frame(samples []int16, start, end, index, sampleRate, frameSamples, clipThreshold int) PCM16Frame {
	frame := PCM16Frame{
		Index:       index,
		StartSample: start,
		EndSample:   end,
		SampleCount: end - start,
		Timestamp:   samplesToDuration(start, sampleRate),
		Duration:    samplesToDuration(end-start, sampleRate),
		Partial:     end-start < frameSamples,
		RMS:         rmsEnergy(samples),
	}
	frame.RMSDBFS = dbfs(frame.RMS)
	frame.AbsolutePeak = absolutePeak(samples)
	frame.PeakDBFS = dbfs(float64(frame.AbsolutePeak))
	frame.HeadroomDBFS = headroomDBFS(frame.AbsolutePeak)
	for offset, sample := range samples {
		absValue := absoluteSample(sample)
		if absValue < clipThreshold {
			continue
		}
		location := PCM16SampleLocation{
			SampleIndex: start + offset,
			FrameIndex:  index,
			Timestamp:   samplesToDuration(start+offset, sampleRate),
			Value:       sample,
			AbsValue:    absValue,
		}
		frame.ClippedSamples = append(frame.ClippedSamples, location)
	}
	frame.ClipCount = len(frame.ClippedSamples)
	return frame
}

func analyzeSilentRuns(samples []int16, frames []PCM16Frame, annotations []normalizedSpeechAnnotation, sampleRate int, config PCM16AnalysisConfig, streamID, participantID string, failures *[]PropertyFailure) []SilentRun {
	runs := make([]SilentRun, 0)
	for frameIndex := 0; frameIndex < len(frames); {
		if frames[frameIndex].RMSDBFS > config.SilenceFloorDBFS {
			frameIndex++
			continue
		}
		startFrame := frameIndex
		for frameIndex+1 < len(frames) && frames[frameIndex+1].RMSDBFS <= config.SilenceFloorDBFS {
			frameIndex++
		}
		startSample := frames[startFrame].StartSample
		endSample := frames[frameIndex].EndSample
		overlapSamples, label := expectedSpeechOverlap(startSample, endSample, annotations)
		run := SilentRun{
			StartSample:           startSample,
			EndSample:             endSample,
			Start:                 samplesToDuration(startSample, sampleRate),
			End:                   samplesToDuration(endSample, sampleRate),
			Duration:              samplesToDuration(endSample-startSample, sampleRate),
			RMS:                   rmsEnergy(samples[startSample:endSample]),
			ExpectedSpeechOverlap: samplesToDuration(overlapSamples, sampleRate),
			InExpectedSpeech:      overlapSamples > 0,
			NaturalPause:          samplesToDuration(overlapSamples, sampleRate) <= config.MaxNaturalPause,
			Dropout:               samplesToDuration(overlapSamples, sampleRate) > config.MaxNaturalPause,
		}
		run.RMSDBFS = dbfs(run.RMS)
		runs = append(runs, run)
		if run.Dropout {
			failure := analysisFailure("dropout", streamID, participantID)
			failure.Interval = label
			failure.StartSample = startSample
			failure.EndSample = endSample
			failure.SampleIndex = startSample
			failure.FrameIndex = startFrame
			failure.Timestamp = run.Start
			failure.Measured = float64(run.ExpectedSpeechOverlap) / float64(time.Millisecond)
			failure.Comparison = ">"
			failure.Bound = float64(config.MaxNaturalPause) / float64(time.Millisecond)
			failure.Unit = "milliseconds of expected-speech silence"
			failure.Detail = fmt.Sprintf("silent run is %.2f ms overall at %.2f dBFS; expected-speech overlap is %.2f ms", float64(run.Duration)/float64(time.Millisecond), run.RMSDBFS, float64(run.ExpectedSpeechOverlap)/float64(time.Millisecond))
			*failures = append(*failures, failure)
		}
		frameIndex++
	}
	return runs
}

func makeBoundaryCheck(samples []int16, boundary ChunkBoundary, sampleRate, frameSamples int, config PCM16AnalysisConfig) BoundaryCheck {
	previousWindow := samples[boundary.SampleIndex-frameSamples : boundary.SampleIndex]
	nextWindow := samples[boundary.SampleIndex : boundary.SampleIndex+frameSamples]
	previous := samples[boundary.SampleIndex-1]
	next := samples[boundary.SampleIndex]
	delta := absoluteSampleDifference(previous, next)
	previousRMSDBFS := dbfs(rmsEnergy(previousWindow))
	nextRMSDBFS := dbfs(rmsEnergy(nextWindow))
	quiet := previousRMSDBFS < config.BoundaryQuietDBFS && nextRMSDBFS < config.BoundaryQuietDBFS
	largeJump := delta > config.BoundaryDelta
	return BoundaryCheck{
		ID:                    boundary.ID,
		SampleIndex:           boundary.SampleIndex,
		Timestamp:             samplesToDuration(boundary.SampleIndex, sampleRate),
		PreviousSample:        previous,
		NextSample:            next,
		Delta:                 delta,
		PreviousWindowRMSDBFS: previousRMSDBFS,
		NextWindowRMSDBFS:     nextRMSDBFS,
		SuspiciousClick:       largeJump && quiet,
		ImpulseCandidate:      largeJump && !quiet,
	}
}

func makeEdgeMeasurement(samples []int16, frameSamples int) EdgeMeasurement {
	finalStart := len(samples) - frameSamples
	if finalStart < 0 {
		finalStart = 0
	}
	finalEnd := len(samples)
	finalSamples := samples[finalStart:finalEnd]
	return EdgeMeasurement{
		FirstSample:      samples[0],
		FirstAbsValue:    absoluteSample(samples[0]),
		LastSample:       samples[len(samples)-1],
		LastAbsValue:     absoluteSample(samples[len(samples)-1]),
		FinalStartSample: finalStart,
		FinalEndSample:   finalEnd,
		FinalRMS:         rmsEnergy(finalSamples),
		FinalRMSDBFS:     dbfs(rmsEnergy(finalSamples)),
	}
}

type normalizedSpeechAnnotation struct {
	startSample int
	endSample   int
	label       string
}

func normalizeSpeechAnnotations(annotations []SpeechAnnotation, sampleCount, sampleRate int) ([]normalizedSpeechAnnotation, error) {
	normalized := make([]normalizedSpeechAnnotation, 0, len(annotations))
	previousEnd := 0
	for index, annotation := range annotations {
		usesSamples := annotation.StartSample != 0 || annotation.EndSample != 0
		usesTime := annotation.Start != 0 || annotation.End != 0
		if usesSamples == usesTime {
			return nil, invalidPCM16Analysis(fmt.Sprintf("expected_speech[%d]", index), "must specify exactly one complete sample or time range")
		}
		start, end := annotation.StartSample, annotation.EndSample
		if usesTime {
			var err error
			start, err = durationToSamples(annotation.Start, sampleRate)
			if err != nil {
				return nil, invalidPCM16Analysis(fmt.Sprintf("expected_speech[%d].start", index), err.Error())
			}
			end, err = durationToSamples(annotation.End, sampleRate)
			if err != nil {
				return nil, invalidPCM16Analysis(fmt.Sprintf("expected_speech[%d].end", index), err.Error())
			}
		}
		if start < 0 || end <= start || end > sampleCount {
			return nil, invalidPCM16Analysis(fmt.Sprintf("expected_speech[%d]", index), fmt.Sprintf("range %d..%d is outside sample count %d", start, end, sampleCount))
		}
		if index > 0 && start < previousEnd {
			return nil, invalidPCM16Analysis(fmt.Sprintf("expected_speech[%d]", index), "annotations must be sorted and non-overlapping")
		}
		label := annotation.Label
		if label == "" {
			label = fmt.Sprintf("expected-speech-%d", index)
		}
		normalized = append(normalized, normalizedSpeechAnnotation{startSample: start, endSample: end, label: label})
		previousEnd = end
	}
	return normalized, nil
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
	defaults := DefaultPCM16AnalysisConfig
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
	switch {
	case config.FrameDuration <= 0:
		return PCM16AnalysisConfig{}, invalidPCM16Analysis("frame_duration", "must be positive")
	case !isFinite(config.SilenceFloorDBFS) || config.SilenceFloorDBFS > 0:
		return PCM16AnalysisConfig{}, invalidPCM16Analysis("silence_floor_dbfs", "must be finite and at or below 0")
	case config.MaxNaturalPause <= 0:
		return PCM16AnalysisConfig{}, invalidPCM16Analysis("max_natural_pause", "must be positive")
	case config.BoundaryDelta <= 0:
		return PCM16AnalysisConfig{}, invalidPCM16Analysis("boundary_delta", "must be positive")
	case !isFinite(config.BoundaryQuietDBFS) || config.BoundaryQuietDBFS >= 0:
		return PCM16AnalysisConfig{}, invalidPCM16Analysis("boundary_quiet_dbfs", "must be finite and below 0")
	case config.ClipSampleThreshold <= 0 || config.ClipSampleThreshold > 32767:
		return PCM16AnalysisConfig{}, invalidPCM16Analysis("clip_sample_threshold", "must be between 1 and 32767")
	case config.EdgeSampleThreshold < 0 || config.EdgeSampleThreshold > 32767:
		return PCM16AnalysisConfig{}, invalidPCM16Analysis("edge_sample_threshold", "must be between 0 and 32767")
	case !isFinite(config.FinalFrameMaxRMSDBFS) || config.FinalFrameMaxRMSDBFS > 0:
		return PCM16AnalysisConfig{}, invalidPCM16Analysis("final_frame_max_rms_dbfs", "must be finite and at or below 0")
	}
	return config, nil
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
	return 20 * math.Log10(rms/32768.0)
}

func headroomDBFS(peak int) float64 {
	if peak <= 0 {
		return math.Inf(1)
	}
	return 20 * math.Log10(32768.0/float64(peak))
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
