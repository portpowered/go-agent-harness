package audio

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

const (
	// PCM16SelfHearingDefaultAnalysisWindow is the longest rolling signal
	// window retained for one detector decision. It also bounds the normal
	// release latency for a future selective gate.
	PCM16SelfHearingDefaultAnalysisWindow = 120 * time.Millisecond
	// PCM16SelfHearingDefaultMinimumEvidence is the amount of paired active
	// audio required before a correlation can be classified as feedback.
	PCM16SelfHearingDefaultMinimumEvidence = 80 * time.Millisecond
	// PCM16SelfHearingDefaultLagMin and Max cover the expected acoustic path
	// from a local speaker to a colocated microphone. Positive lag means the
	// microphone signal occurs after the speaker signal.
	PCM16SelfHearingDefaultLagMin = -100 * time.Millisecond
	PCM16SelfHearingDefaultLagMax = 100 * time.Millisecond
	// PCM16SelfHearingDefaultCorrelationThreshold requires strong normalized
	// absolute correlation. The inclusive comparison is intentional so a
	// measurement exactly on the configured boundary is confirmed.
	PCM16SelfHearingDefaultCorrelationThreshold = 0.80
	// PCM16SelfHearingDefaultSilenceFloorDBFS is shared with the room analysis
	// contract so digital silence and low-level noise do not become evidence.
	PCM16SelfHearingDefaultSilenceFloorDBFS = PCM16AnalysisSilenceFloorDBFS
	// PCM16SelfHearingDefaultMaximumReleaseLatency documents the bound a gate
	// may use when releasing a non-feedback capture window.
	PCM16SelfHearingDefaultMaximumReleaseLatency = 120 * time.Millisecond
	// PCM16SelfHearingDefaultAcousticTail is the post-playback tail reserved by
	// the selective gate for late speaker bleed. The detector itself remains a
	// pure classifier and does not discard audio.
	PCM16SelfHearingDefaultAcousticTail = 200 * time.Millisecond
)

var (
	// ErrInvalidPCM16SelfHearingConfig identifies an unusable detector profile.
	ErrInvalidPCM16SelfHearingConfig = errors.New("invalid PCM16 self-hearing configuration")
	// ErrInvalidPCM16SelfHearingFrame identifies malformed timed PCM supplied
	// to the streaming detector.
	ErrInvalidPCM16SelfHearingFrame = errors.New("invalid PCM16 self-hearing frame")
)

// PCM16SelfHearingConfig contains the deterministic signal policy used by a
// live local-device self-hearing detector. Durations are expressed in the
// shared monotonic media timeline, not wall-clock time.
type PCM16SelfHearingConfig struct {
	AnalysisWindow           time.Duration
	MinimumEvidence          time.Duration
	CorrelationLagWindow     PCM16LagWindow
	CorrelationThreshold     float64
	SilenceFloorDBFS         float64
	MaximumReleaseLatency    time.Duration
	PostPlaybackAcousticTail time.Duration
}

// DefaultPCM16SelfHearingConfig is the documented local feedback policy.
// Callers should copy it before tightening a bound for a deterministic test.
var DefaultPCM16SelfHearingConfig = PCM16SelfHearingConfig{
	AnalysisWindow:           PCM16SelfHearingDefaultAnalysisWindow,
	MinimumEvidence:          PCM16SelfHearingDefaultMinimumEvidence,
	CorrelationLagWindow:     PCM16LagWindow{Min: PCM16SelfHearingDefaultLagMin, Max: PCM16SelfHearingDefaultLagMax},
	CorrelationThreshold:     PCM16SelfHearingDefaultCorrelationThreshold,
	SilenceFloorDBFS:         PCM16SelfHearingDefaultSilenceFloorDBFS,
	MaximumReleaseLatency:    PCM16SelfHearingDefaultMaximumReleaseLatency,
	PostPlaybackAcousticTail: PCM16SelfHearingDefaultAcousticTail,
}

// DefaultSelfHearingConfig returns a copy of the default self-hearing policy.
func DefaultSelfHearingConfig() PCM16SelfHearingConfig { return DefaultPCM16SelfHearingConfig }

// InvalidPCM16SelfHearingConfigError identifies one invalid detector field.
type InvalidPCM16SelfHearingConfigError struct {
	Field  string
	Reason string
}

func (e *InvalidPCM16SelfHearingConfigError) Error() string {
	if e == nil {
		return ErrInvalidPCM16SelfHearingConfig.Error()
	}
	if e.Field == "" {
		return fmt.Sprintf("%s: %s", ErrInvalidPCM16SelfHearingConfig, e.Reason)
	}
	return fmt.Sprintf("%s: %s: %s", ErrInvalidPCM16SelfHearingConfig, e.Field, e.Reason)
}

func (e *InvalidPCM16SelfHearingConfigError) Unwrap() error { return ErrInvalidPCM16SelfHearingConfig }

// PCM16SelfHearingFrameError identifies the stream and invariant that made a
// timed input frame unusable.
type PCM16SelfHearingFrameError struct {
	Stream string
	Reason string
}

func (e *PCM16SelfHearingFrameError) Error() string {
	if e == nil {
		return ErrInvalidPCM16SelfHearingFrame.Error()
	}
	if e.Stream == "" {
		return fmt.Sprintf("%s: %s", ErrInvalidPCM16SelfHearingFrame, e.Reason)
	}
	return fmt.Sprintf("%s %s: %s", ErrInvalidPCM16SelfHearingFrame, e.Stream, e.Reason)
}

func (e *PCM16SelfHearingFrameError) Unwrap() error { return ErrInvalidPCM16SelfHearingFrame }

// PCM16TimedFrame is one owned-by-the-caller PCM16 observation with an
// explicit sample rate and monotonic media start position. The detector copies
// Samples before returning, so callers may reuse or mutate their input slice.
type PCM16TimedFrame struct {
	Samples    []int16
	SampleRate int
	Start      time.Duration
}

// PCM16MediaFrame is a descriptive alias for callers that think in terms of
// media rather than analysis timelines.
type PCM16MediaFrame = PCM16TimedFrame

// End returns the derived half-open end position. Invalid frames return the
// start position; observation methods validate the frame before using End.
func (f PCM16TimedFrame) End() time.Duration {
	return f.Start + samplesToDuration(len(f.Samples), f.SampleRate)
}

// PCM16SelfHearingClassification is the content-free result of one capture
// observation. Only Confirmed is a positive self-hearing classification.
type PCM16SelfHearingClassification string

const (
	// PCM16SelfHearingNoEvidence means there was no usable playback/capture
	// activity, normally because one side was silent or no playback was seen.
	PCM16SelfHearingNoEvidence PCM16SelfHearingClassification = "no-evidence"
	// PCM16SelfHearingRateMismatch means both streams are valid but their
	// explicit rates differ, so this detector refuses to compare their bytes.
	PCM16SelfHearingRateMismatch PCM16SelfHearingClassification = "rate-mismatch"
	// PCM16SelfHearingInsufficientEvidence means active paired samples exist,
	// but not for the configured minimum duration.
	PCM16SelfHearingInsufficientEvidence PCM16SelfHearingClassification = "insufficient-evidence"
	// PCM16SelfHearingNonFeedback means enough active evidence was measured but
	// its absolute correlation stayed below the configured threshold.
	PCM16SelfHearingNonFeedback PCM16SelfHearingClassification = "non-feedback"
	// PCM16SelfHearingConfirmed means the absolute correlation met the
	// inclusive threshold over the minimum evidence duration.
	PCM16SelfHearingConfirmed PCM16SelfHearingClassification = "confirmed-self-hearing"
)

// PCM16SelfHearingObservation is the result returned for one captured frame.
// It deliberately contains no audio samples or conversation metadata.
type PCM16SelfHearingObservation struct {
	Classification   PCM16SelfHearingClassification
	Measurement      PCM16CorrelationMeasurement
	EvidenceSamples  int
	EvidenceDuration time.Duration
}

// PCM16SelfHearingDecision is a descriptive alias for the streaming result.
type PCM16SelfHearingDecision = PCM16SelfHearingObservation

// Confirmed reports whether the observation meets the self-hearing policy.
func (o PCM16SelfHearingObservation) Confirmed() bool {
	return o.Classification == PCM16SelfHearingConfirmed
}

// PCM16SelfHearingTopology describes the media topology before side effects
// begin. Only one session with both live local directions and no alternate
// media or room peer ingress may enable the controller.
type PCM16SelfHearingTopology struct {
	LiveMicrophone  bool
	LiveSpeaker     bool
	FileInput       bool
	FileOutput      bool
	Replay          bool
	RoomPeerIngress bool
}

// PCM16SelfHearingPolicy is the immutable topology decision used by session
// composition. Bypass is explicit so callers do not infer policy from nil
// pumps after startup.
type PCM16SelfHearingPolicy string

const (
	PCM16SelfHearingPolicyBypass       PCM16SelfHearingPolicy = "bypass"
	PCM16SelfHearingPolicyPairedDevice PCM16SelfHearingPolicy = "paired-live-device"
)

// ResolvePCM16SelfHearingPolicy returns the only topology that can enable
// local feedback classification. File, replay, one-directional, no-audio,
// and room peer paths all bypass it.
func ResolvePCM16SelfHearingPolicy(topology PCM16SelfHearingTopology) PCM16SelfHearingPolicy {
	if !topology.LiveMicrophone || !topology.LiveSpeaker || topology.FileInput || topology.FileOutput || topology.Replay || topology.RoomPeerIngress {
		return PCM16SelfHearingPolicyBypass
	}
	return PCM16SelfHearingPolicyPairedDevice
}

// EnablesPCM16SelfHearing reports whether the topology admits a local
// feedback controller.
func (topology PCM16SelfHearingTopology) EnablesPCM16SelfHearing() bool {
	return ResolvePCM16SelfHearingPolicy(topology) == PCM16SelfHearingPolicyPairedDevice
}

// PCM16SelfHearingBufferStats exposes only bounded-storage counts. It never
// exposes the retained PCM itself.
type PCM16SelfHearingBufferStats struct {
	PlaybackSamples    int
	CaptureSamples     int
	MaxPlaybackSamples int
	MaxCaptureSamples  int
}

// PCM16SelfHearingDetector is a synchronous, bounded rolling classifier. It
// has no goroutine and owns no device, so cancellation/device-loss handling is
// explicit: the owner stops calling it and invokes Close, which drops both
// copied buffers immediately.
type PCM16SelfHearingDetector struct {
	mu                 sync.Mutex
	config             PCM16SelfHearingConfig
	maxBufferDuration  time.Duration
	maxPlaybackSamples int
	maxCaptureSamples  int

	playback selfHearingBuffer
	capture  selfHearingBuffer

	playbackLastEnd time.Duration
	captureLastEnd  time.Duration
	playbackSeen    bool
	captureSeen     bool
	closed          bool
}

// NewPCM16SelfHearingDetector validates and creates a detector. It does not
// open devices or start any asynchronous work.
func NewPCM16SelfHearingDetector(config PCM16SelfHearingConfig) (*PCM16SelfHearingDetector, error) {
	normalized, maxBufferDuration, err := normalizePCM16SelfHearingConfig(config)
	if err != nil {
		return nil, err
	}
	return &PCM16SelfHearingDetector{
		config:            normalized,
		maxBufferDuration: maxBufferDuration,
	}, nil
}

// NewPCM16SelfHearingController is a constructor-shaped alias for session
// owners that use controller terminology.
func NewPCM16SelfHearingController(config PCM16SelfHearingConfig) (*PCM16SelfHearingDetector, error) {
	return NewPCM16SelfHearingDetector(config)
}

// NewPCM16SelfHearingDetectorForTopology creates a detector only for the
// paired-live-device topology. Bypass paths return (nil, nil), making the
// absence of the policy observable without opening or wrapping media pumps.
func NewPCM16SelfHearingDetectorForTopology(topology PCM16SelfHearingTopology, config PCM16SelfHearingConfig) (*PCM16SelfHearingDetector, error) {
	if ResolvePCM16SelfHearingPolicy(topology) != PCM16SelfHearingPolicyPairedDevice {
		return nil, nil
	}
	return NewPCM16SelfHearingDetector(config)
}

// NewPCM16SelfHearingControllerForTopology is the controller-named alias of
// NewPCM16SelfHearingDetectorForTopology.
func NewPCM16SelfHearingControllerForTopology(topology PCM16SelfHearingTopology, config PCM16SelfHearingConfig) (*PCM16SelfHearingDetector, error) {
	return NewPCM16SelfHearingDetectorForTopology(topology, config)
}

// Config returns the immutable policy copy used by the detector.
func (d *PCM16SelfHearingDetector) Config() PCM16SelfHearingConfig {
	if d == nil {
		return PCM16SelfHearingConfig{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.config
}

// MaxBufferDuration returns the time bound used for each rolling stream.
func (d *PCM16SelfHearingDetector) MaxBufferDuration() time.Duration {
	if d == nil {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.maxBufferDuration
}

// BufferStats returns bounded, content-free storage observations.
func (d *PCM16SelfHearingDetector) BufferStats() PCM16SelfHearingBufferStats {
	if d == nil {
		return PCM16SelfHearingBufferStats{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return PCM16SelfHearingBufferStats{
		PlaybackSamples:    len(d.playback.samples),
		CaptureSamples:     len(d.capture.samples),
		MaxPlaybackSamples: d.maxPlaybackSamples,
		MaxCaptureSamples:  d.maxCaptureSamples,
	}
}

// ResetCapture drops retained microphone samples while preserving the
// capture timeline cursor. Runtime gates use it after a disposition so a new
// capture window cannot be classified from stale evidence that preceded the
// disposition.
func (d *PCM16SelfHearingDetector) ResetCapture() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	d.capture.reset()
}

// ObservePlayback records exactly the PCM accepted by the local speaker.
// Callers that need cancellation should use ObservePlaybackContext.
func (d *PCM16SelfHearingDetector) ObservePlayback(frame PCM16TimedFrame) error {
	return d.ObservePlaybackContext(context.Background(), frame)
}

// ObservePlaybackContext records a speaker-bound PCM frame after checking ctx.
// A cancelled call does not mutate detector state.
func (d *PCM16SelfHearingDetector) ObservePlaybackContext(ctx context.Context, frame PCM16TimedFrame) error {
	if err := selfHearingContextError(ctx); err != nil {
		return err
	}
	end, err := validatePCM16SelfHearingFrame(frame, "playback")
	if err != nil {
		return err
	}
	if d == nil {
		return ErrClosed
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return ErrClosed
	}
	if d.playbackSeen && frame.Start < d.playbackLastEnd {
		return invalidPCM16SelfHearingFrame("playback", "media position moved backwards")
	}
	d.playbackSeen = true
	d.playbackLastEnd = end
	maxSamples, err := ceilDurationSamples(d.maxBufferDuration, frame.SampleRate)
	if err != nil {
		return invalidPCM16SelfHearingFrame("playback", "buffer bound cannot be represented at sample rate")
	}
	d.maxPlaybackSamples = maxSamples
	discontinuous, err := d.playback.append(frame, end, maxSamples)
	if err != nil {
		return err
	}
	if discontinuous {
		// A gap or rate transition means the two rolling streams no longer form
		// one contiguous comparison window. Retain monotonic cursors but drop
		// stale evidence on the other side as well.
		d.capture.reset()
	}
	return nil
}

// ObserveCapture classifies one raw microphone frame before provider delivery.
// It copies the frame into bounded storage and returns a content-free result.
func (d *PCM16SelfHearingDetector) ObserveCapture(frame PCM16TimedFrame) (PCM16SelfHearingObservation, error) {
	return d.ObserveCaptureContext(context.Background(), frame)
}

// ObserveCaptureContext classifies one raw microphone frame after checking
// ctx. A cancelled call leaves existing evidence untouched.
func (d *PCM16SelfHearingDetector) ObserveCaptureContext(ctx context.Context, frame PCM16TimedFrame) (PCM16SelfHearingObservation, error) {
	if err := selfHearingContextError(ctx); err != nil {
		return PCM16SelfHearingObservation{}, err
	}
	end, err := validatePCM16SelfHearingFrame(frame, "capture")
	if err != nil {
		return PCM16SelfHearingObservation{}, err
	}
	if d == nil {
		return PCM16SelfHearingObservation{}, ErrClosed
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return PCM16SelfHearingObservation{}, ErrClosed
	}
	if d.captureSeen && frame.Start < d.captureLastEnd {
		return PCM16SelfHearingObservation{}, invalidPCM16SelfHearingFrame("capture", "media position moved backwards")
	}
	d.captureSeen = true
	d.captureLastEnd = end
	maxSamples, err := ceilDurationSamples(d.maxBufferDuration, frame.SampleRate)
	if err != nil {
		return PCM16SelfHearingObservation{}, invalidPCM16SelfHearingFrame("capture", "buffer bound cannot be represented at sample rate")
	}
	d.maxCaptureSamples = maxSamples
	discontinuous, err := d.capture.append(frame, end, maxSamples)
	if err != nil {
		return PCM16SelfHearingObservation{}, err
	}
	if discontinuous {
		d.playback.reset()
	}
	return d.classifyLocked(), nil
}

// Close releases detector-owned buffers. It is idempotent and never blocks on
// device or provider state; those owners retain their own shutdown contract.
func (d *PCM16SelfHearingDetector) Close() error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	d.playback.reset()
	d.capture.reset()
	return nil
}

func (d *PCM16SelfHearingDetector) classifyLocked() PCM16SelfHearingObservation {
	observation := PCM16SelfHearingObservation{Classification: PCM16SelfHearingNoEvidence}
	if len(d.playback.samples) == 0 || len(d.capture.samples) == 0 {
		return observation
	}
	if d.playback.rate != d.capture.rate {
		observation.Classification = PCM16SelfHearingRateMismatch
		return observation
	}

	rate := d.playback.rate
	lagMin := d.config.CorrelationLagWindow.Min
	sourceEnd := d.playback.end
	if candidateEnd := d.capture.end - lagMin; candidateEnd < sourceEnd {
		sourceEnd = candidateEnd
	}
	if sourceEnd <= d.playback.start {
		return observation
	}
	sourceStart := sourceEnd - d.config.AnalysisWindow
	if sourceStart < d.playback.start {
		sourceStart = d.playback.start
	}
	if sourceEnd <= sourceStart {
		return observation
	}

	base := d.playback.start
	if d.capture.start < base {
		base = d.capture.start
	}
	source := PCM16TimedStream{
		PCM16Input: PCM16Input{
			StreamID:      "assistant-playback",
			ParticipantID: "assistant",
			SampleRate:    rate,
			Samples:       d.playback.samples,
		},
		TimelineStart: d.playback.start - base,
		TimelineEnd:   d.playback.end - base,
	}
	received := PCM16TimedStream{
		PCM16Input: PCM16Input{
			StreamID:      "microphone-capture",
			ParticipantID: "local-microphone",
			SampleRate:    d.capture.rate,
			Samples:       d.capture.samples,
		},
		TimelineStart: d.capture.start - base,
		TimelineEnd:   d.capture.end - base,
	}
	interval := PCM16TimeInterval{
		ID:    "local-self-hearing-window",
		Start: sourceStart - base,
		End:   sourceEnd - base,
	}
	sourceStartIndex, sourceEndIndex, err := sampleRangeForInterval(source, interval)
	if err != nil {
		return PCM16SelfHearingObservation{Classification: PCM16SelfHearingInsufficientEvidence}
	}
	minLagSamples, err := signedDurationToSamples(d.config.CorrelationLagWindow.Min, rate)
	if err != nil {
		return PCM16SelfHearingObservation{Classification: PCM16SelfHearingInsufficientEvidence}
	}
	maxLagSamples, err := signedDurationToSamples(d.config.CorrelationLagWindow.Max, rate)
	if err != nil {
		return PCM16SelfHearingObservation{Classification: PCM16SelfHearingInsufficientEvidence}
	}
	if maxLagSamples < minLagSamples {
		return PCM16SelfHearingObservation{Classification: PCM16SelfHearingInsufficientEvidence}
	}
	receivedIntervalStart, err := signedDurationToSamples(interval.Start-received.TimelineStart, rate)
	if err != nil {
		return PCM16SelfHearingObservation{Classification: PCM16SelfHearingInsufficientEvidence}
	}
	threshold := pcm16AmplitudeForDBFS(d.config.SilenceFloorDBFS)
	minimumSamples, err := ceilDurationSamples(d.config.MinimumEvidence, rate)
	if err != nil {
		return PCM16SelfHearingObservation{Classification: PCM16SelfHearingInsufficientEvidence}
	}
	measurement := PCM16CorrelationMeasurement{
		SourceStreamID:        source.StreamID,
		SourceParticipantID:   source.ParticipantID,
		ReceivedStreamID:      received.StreamID,
		ReceivedParticipantID: received.ParticipantID,
		IntervalID:            interval.ID,
		Start:                 interval.Start,
		End:                   interval.End,
	}
	sourceWindow := source.Samples[sourceStartIndex:sourceEndIndex]
	// Lags whose geometric overlap is shorter than the minimum evidence can
	// never confirm self-hearing. Excluding them before the Pearson scan keeps
	// the live gate bounded in CPU as well as storage, while preserving the
	// detector's configured lag semantics for every eligible candidate.
	minimumEvidenceLag := minimumSamples - len(sourceWindow) - receivedIntervalStart
	maximumEvidenceLag := len(received.Samples) - minimumSamples - receivedIntervalStart
	if minimumEvidenceLag > maximumEvidenceLag {
		probeLag := -receivedIntervalStart
		if probeLag < minLagSamples {
			probeLag = minLagSamples
		}
		if probeLag > maxLagSamples {
			probeLag = maxLagSamples
		}
		evidenceSamples := pairedNonSilentSamples(sourceWindow, received.Samples, receivedIntervalStart+probeLag, threshold)
		observation.Measurement = measurement
		observation.EvidenceSamples = evidenceSamples
		observation.EvidenceDuration = samplesToDuration(evidenceSamples, rate)
		if evidenceSamples == 0 {
			observation.Classification = PCM16SelfHearingNoEvidence
		} else {
			observation.Classification = PCM16SelfHearingInsufficientEvidence
		}
		return observation
	}
	if minLagSamples < minimumEvidenceLag {
		minLagSamples = minimumEvidenceLag
	}
	if maxLagSamples > maximumEvidenceLag {
		maxLagSamples = maximumEvidenceLag
	}
	foundSigned := false
	foundAbsolute := false
	bestEvidenceSamples := 0
	anyEvidenceSamples := 0
	forEachNormalizedPCM16CorrelationCandidate(
		minLagSamples,
		maxLagSamples,
		func(lagSamples int, coefficient float64, compared int) {
			if compared == 0 {
				return
			}
			evidenceSamples := pairedNonSilentSamples(
				source.Samples[sourceStartIndex:sourceEndIndex],
				received.Samples,
				receivedIntervalStart+lagSamples,
				threshold,
			)
			if evidenceSamples > anyEvidenceSamples {
				anyEvidenceSamples = evidenceSamples
			}
			if evidenceSamples < minimumSamples {
				return
			}
			if !foundSigned || coefficient > measurement.BestCorrelation {
				measurement.BestCorrelation = coefficient
				measurement.BestLag = samplesToSignedDuration(lagSamples, rate)
				measurement.ComparedSamples = compared
				foundSigned = true
			}
			if !foundAbsolute || math.Abs(coefficient) > measurement.BestAbsoluteCorrelation {
				measurement.BestAbsoluteCorrelation = math.Abs(coefficient)
				measurement.BestAbsoluteLag = samplesToSignedDuration(lagSamples, rate)
				bestEvidenceSamples = evidenceSamples
				foundAbsolute = true
			}
		},
		func(lagSamples int) (float64, int) {
			return normalizedCorrelationAtLag(sourceWindow, nil, received.Samples, receivedIntervalStart+lagSamples, threshold)
		},
	)
	if anyEvidenceSamples == 0 {
		observation.Classification = PCM16SelfHearingNoEvidence
		return observation
	}
	if !foundAbsolute {
		observation.Classification = PCM16SelfHearingInsufficientEvidence
		observation.Measurement = measurement
		observation.EvidenceSamples = anyEvidenceSamples
		observation.EvidenceDuration = samplesToDuration(anyEvidenceSamples, rate)
		return observation
	}
	observation = PCM16SelfHearingObservation{
		Measurement:      measurement,
		EvidenceSamples:  bestEvidenceSamples,
		EvidenceDuration: samplesToDuration(bestEvidenceSamples, rate),
	}
	if bestEvidenceSamples < minimumSamples {
		observation.Classification = PCM16SelfHearingInsufficientEvidence
		return observation
	}
	if measurement.BestAbsoluteCorrelation < d.config.CorrelationThreshold {
		observation.Classification = PCM16SelfHearingNonFeedback
		return observation
	}
	observation.Classification = PCM16SelfHearingConfirmed
	return observation
}

func pairedNonSilentSamples(source, received []int16, receivedStart int, threshold float64) int {
	paired := 0
	for sourceIndex, sourceSample := range source {
		receivedIndex := receivedStart + sourceIndex
		if receivedIndex < 0 || receivedIndex >= len(received) {
			continue
		}
		if float64(absoluteSample(sourceSample)) <= threshold || float64(absoluteSample(received[receivedIndex])) <= threshold {
			continue
		}
		paired++
	}
	return paired
}

type selfHearingBuffer struct {
	rate    int
	start   time.Duration
	end     time.Duration
	samples []int16
	have    bool
}

func (b *selfHearingBuffer) reset() {
	b.rate = 0
	b.start = 0
	b.end = 0
	b.samples = nil
	b.have = false
}

func (b *selfHearingBuffer) append(frame PCM16TimedFrame, end time.Duration, maxSamples int) (bool, error) {
	if maxSamples <= 0 {
		return false, invalidPCM16SelfHearingFrame("buffer", "maximum sample count must be positive")
	}
	discontinuous := false
	if b.have && b.rate != frame.SampleRate {
		b.reset()
		discontinuous = true
	}
	if b.have && frame.Start < b.end {
		return discontinuous, invalidPCM16SelfHearingFrame("stream", "frame overlaps a previous frame")
	}
	if b.have && frame.Start > b.end {
		b.reset()
		discontinuous = true
	}
	if !b.have {
		b.rate = frame.SampleRate
		b.start = frame.Start
		b.end = frame.Start
		b.have = true
		initialCapacity := len(frame.Samples)
		if initialCapacity > maxSamples {
			initialCapacity = maxSamples
		}
		b.samples = make([]int16, 0, initialCapacity)
	}

	if len(frame.Samples) >= maxSamples {
		// Copy only the bounded tail. The caller retains ownership of the
		// potentially much larger input slice.
		b.samples = make([]int16, maxSamples)
		copy(b.samples, frame.Samples[len(frame.Samples)-maxSamples:])
		b.start = end - samplesToDuration(maxSamples, frame.SampleRate)
		b.end = end
		return discontinuous, nil
	}
	if len(b.samples)+len(frame.Samples) > maxSamples {
		drop := len(b.samples) + len(frame.Samples) - maxSamples
		copy(b.samples, b.samples[drop:])
		b.samples = b.samples[:len(b.samples)-drop]
		b.start += samplesToDuration(drop, b.rate)
	}
	b.samples = appendBoundedPCM16(b.samples, frame.Samples, maxSamples)
	b.end = end
	return discontinuous, nil
}

func appendBoundedPCM16(dst, src []int16, maxSamples int) []int16 {
	if len(src) == 0 {
		return dst
	}
	needed := len(dst) + len(src)
	if needed > maxSamples {
		return dst
	}
	capacity := cap(dst)
	if capacity > maxSamples {
		capacity = maxSamples
	}
	if capacity < needed {
		if capacity == 0 {
			capacity = 1
		}
		for capacity < needed && capacity < maxSamples {
			next := capacity * 2
			if next <= capacity {
				capacity = maxSamples
				break
			}
			capacity = next
		}
		if capacity > maxSamples {
			capacity = maxSamples
		}
		grown := make([]int16, len(dst), capacity)
		copy(grown, dst)
		dst = grown
	} else if cap(dst) > maxSamples {
		normalized := make([]int16, len(dst), maxSamples)
		copy(normalized, dst)
		dst = normalized
	}
	return append(dst, src...)
}

func normalizePCM16SelfHearingConfig(config PCM16SelfHearingConfig) (PCM16SelfHearingConfig, time.Duration, error) {
	defaults := DefaultPCM16SelfHearingConfig
	if config.AnalysisWindow == 0 {
		config.AnalysisWindow = defaults.AnalysisWindow
	}
	if config.MinimumEvidence == 0 {
		config.MinimumEvidence = defaults.MinimumEvidence
	}
	if config.CorrelationLagWindow.Min == 0 && config.CorrelationLagWindow.Max == 0 {
		config.CorrelationLagWindow = defaults.CorrelationLagWindow
	}
	if config.CorrelationThreshold == 0 {
		config.CorrelationThreshold = defaults.CorrelationThreshold
	}
	if config.SilenceFloorDBFS == 0 {
		config.SilenceFloorDBFS = defaults.SilenceFloorDBFS
	}
	if config.MaximumReleaseLatency == 0 {
		config.MaximumReleaseLatency = defaults.MaximumReleaseLatency
	}
	if config.PostPlaybackAcousticTail == 0 {
		config.PostPlaybackAcousticTail = defaults.PostPlaybackAcousticTail
	}
	if config.AnalysisWindow <= 0 {
		return PCM16SelfHearingConfig{}, 0, invalidPCM16SelfHearingConfig("analysis_window", "must be positive")
	}
	if config.MinimumEvidence <= 0 || config.MinimumEvidence > config.AnalysisWindow {
		return PCM16SelfHearingConfig{}, 0, invalidPCM16SelfHearingConfig("minimum_evidence", "must be positive and at or below analysis_window")
	}
	if config.CorrelationLagWindow.Min > config.CorrelationLagWindow.Max {
		return PCM16SelfHearingConfig{}, 0, invalidPCM16SelfHearingConfig("correlation_lag_window", "min must be at or before max")
	}
	if !isFinite(config.CorrelationThreshold) || config.CorrelationThreshold < 0 || config.CorrelationThreshold > 1 {
		return PCM16SelfHearingConfig{}, 0, invalidPCM16SelfHearingConfig("correlation_threshold", "must be between 0 and 1")
	}
	if !isFinite(config.SilenceFloorDBFS) || config.SilenceFloorDBFS > 0 {
		return PCM16SelfHearingConfig{}, 0, invalidPCM16SelfHearingConfig("silence_floor_dbfs", "must be finite and at or below 0")
	}
	if config.MaximumReleaseLatency <= 0 {
		return PCM16SelfHearingConfig{}, 0, invalidPCM16SelfHearingConfig("maximum_release_latency", "must be positive")
	}
	if config.PostPlaybackAcousticTail <= 0 {
		return PCM16SelfHearingConfig{}, 0, invalidPCM16SelfHearingConfig("post_playback_acoustic_tail", "must be positive")
	}
	lagMagnitude := config.CorrelationLagWindow.Min
	if lagMagnitude < 0 {
		if lagMagnitude == time.Duration(math.MinInt64) {
			return PCM16SelfHearingConfig{}, 0, invalidPCM16SelfHearingConfig("correlation_lag_window.min", "absolute value overflows")
		}
		lagMagnitude = -lagMagnitude
	}
	if max := config.CorrelationLagWindow.Max; max > lagMagnitude {
		lagMagnitude = max
	}
	maxBufferDuration, err := addSelfHearingDuration(config.AnalysisWindow, lagMagnitude)
	if err != nil {
		return PCM16SelfHearingConfig{}, 0, invalidPCM16SelfHearingConfig("buffer_duration", "overflows")
	}
	return config, maxBufferDuration, nil
}

func invalidPCM16SelfHearingConfig(field, reason string) error {
	return &InvalidPCM16SelfHearingConfigError{Field: field, Reason: reason}
}

func invalidPCM16SelfHearingFrame(stream, reason string) error {
	return &PCM16SelfHearingFrameError{Stream: stream, Reason: reason}
}

func validatePCM16SelfHearingFrame(frame PCM16TimedFrame, stream string) (time.Duration, error) {
	if frame.SampleRate <= 0 {
		return 0, invalidPCM16SelfHearingFrame(stream, "sample rate must be positive")
	}
	if len(frame.Samples) == 0 {
		return 0, invalidPCM16SelfHearingFrame(stream, "samples must not be empty")
	}
	if frame.Start < 0 {
		return 0, invalidPCM16SelfHearingFrame(stream, "media position must not be negative")
	}
	duration := samplesToDuration(len(frame.Samples), frame.SampleRate)
	if duration <= 0 || frame.Start > time.Duration(math.MaxInt64)-duration {
		return 0, invalidPCM16SelfHearingFrame(stream, "frame end overflows the media timeline")
	}
	return frame.Start + duration, nil
}

func selfHearingContextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func addSelfHearingDuration(left, right time.Duration) (time.Duration, error) {
	if right > 0 && left > time.Duration(math.MaxInt64)-right {
		return 0, errors.New("duration overflow")
	}
	return left + right, nil
}

func ceilDurationSamples(duration time.Duration, sampleRate int) (int, error) {
	if duration <= 0 || sampleRate <= 0 {
		return 0, errors.New("duration and sample rate must be positive")
	}
	nanoseconds := int64(duration)
	rate := int64(sampleRate)
	if nanoseconds > math.MaxInt64/rate {
		return 0, errors.New("sample conversion overflows")
	}
	product := nanoseconds * rate
	converted := product / int64(time.Second)
	if product%int64(time.Second) != 0 {
		converted++
	}
	maxInt := int64(^uint(0) >> 1)
	if converted <= 0 || converted > maxInt {
		return 0, errors.New("sample count overflows int")
	}
	return int(converted), nil
}
