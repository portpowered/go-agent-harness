package audio

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
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
	// The negative allowance absorbs capture/render callback skew. The positive
	// bound includes hardware buffering plus a far-field acoustic path. A live
	// MacBook capture measured 188 ms end-to-end, which the former +/-100 ms
	// window could never classify even though local 120 ms windows correlated
	// above 0.95.
	PCM16SelfHearingDefaultLagMin = -200 * time.Millisecond
	PCM16SelfHearingDefaultLagMax = 500 * time.Millisecond
	// PCM16SelfHearingDefaultCorrelationThreshold accounts for the gain and
	// frequency shaping introduced by a colocated laptop speaker/microphone
	// path while still requiring a materially correlated signal. The inclusive
	// comparison is intentional so a measurement exactly on the configured
	// boundary is confirmed.
	// Far-field microphones apply gain control, frequency shaping, and room
	// reflections. A modest reduction from the former 0.50 admits transformed
	// echo paths without making a wide lag search classify independent speech.
	// Confirmation still requires 80 ms of active paired evidence.
	PCM16SelfHearingDefaultCorrelationThreshold = 0.45
	// PCM16SelfHearingDefaultSilenceFloorDBFS is shared with the room analysis
	// contract so digital silence and low-level noise do not become evidence.
	PCM16SelfHearingDefaultSilenceFloorDBFS = PCM16AnalysisSilenceFloorDBFS
	// PCM16SelfHearingDefaultMaximumReleaseLatency documents the bound a gate
	// may use when releasing a non-feedback capture window.
	PCM16SelfHearingDefaultMaximumReleaseLatency = 120 * time.Millisecond
	// PCM16SelfHearingDefaultAcousticTail is the post-playback tail reserved by
	// the selective gate for late speaker bleed. The detector itself remains a
	// pure classifier and does not discard audio.
	PCM16SelfHearingDefaultAcousticTail = 500 * time.Millisecond
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
	mu                   sync.Mutex
	config               PCM16SelfHearingConfig
	correlationLagBounds PCM16LagWindow
	maxBufferDuration    time.Duration
	maxPlaybackSamples   int
	maxCaptureSamples    int

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
		config:               normalized,
		correlationLagBounds: normalized.CorrelationLagWindow,
		maxBufferDuration:    maxBufferDuration,
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

// RestrictCorrelationLagWindow narrows future classifications without
// discarding retained playback/capture evidence. A feedback gate uses this
// after its high-evidence primary detector has learned the physical acoustic
// lag, preventing a short post-confirmation probe from repeatedly searching
// thousands of unrelated lag candidates.
func (d *PCM16SelfHearingDetector) RestrictCorrelationLagWindow(window PCM16LagWindow) error {
	if d == nil {
		return ErrClosed
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return ErrClosed
	}
	if window.Min > window.Max {
		return invalidPCM16SelfHearingConfig("correlation_lag_window", "minimum must not exceed maximum")
	}
	current := d.config.CorrelationLagWindow
	if window.Min < current.Min || window.Max > current.Max {
		return invalidPCM16SelfHearingConfig("correlation_lag_window", "restriction must remain inside the current window")
	}
	d.config.CorrelationLagWindow = window
	return nil
}

// RetargetCorrelationLagWindow moves a previously narrowed search window
// while preserving retained playback/capture evidence. The replacement must
// remain inside the detector's original configured bounds. A multi-response
// feedback gate uses this when a new assistant response learns a different
// physical acoustic lag; intersecting with the prior response's narrowed
// window can otherwise invert the interval and terminate capture.
func (d *PCM16SelfHearingDetector) RetargetCorrelationLagWindow(window PCM16LagWindow) error {
	if d == nil {
		return ErrClosed
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return ErrClosed
	}
	if window.Min > window.Max {
		return invalidPCM16SelfHearingConfig("correlation_lag_window", "minimum must not exceed maximum")
	}
	if window.Min < d.correlationLagBounds.Min || window.Max > d.correlationLagBounds.Max {
		return invalidPCM16SelfHearingConfig("correlation_lag_window", "retarget must remain inside the original window")
	}
	d.config.CorrelationLagWindow = window
	return nil
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
	minimumSamples, err := ceilDurationSamples(d.config.MinimumEvidence, rate)
	if err != nil {
		return PCM16SelfHearingObservation{Classification: PCM16SelfHearingInsufficientEvidence}
	}
	insufficientActiveObservation := func() PCM16SelfHearingObservation {
		result := observation
		threshold := pcm16AmplitudeForDBFS(d.config.SilenceFloorDBFS)
		playbackActive, captureActive := 0, 0
		for _, sample := range d.playback.samples {
			if float64(absoluteSample(sample)) > threshold {
				playbackActive++
			}
		}
		for _, sample := range d.capture.samples {
			if float64(absoluteSample(sample)) > threshold {
				captureActive++
			}
		}
		if evidence := min(playbackActive, captureActive); evidence > 0 {
			result.Classification = PCM16SelfHearingInsufficientEvidence
			result.EvidenceSamples = evidence
			result.EvidenceDuration = samplesToDuration(evidence, rate)
		}
		return result
	}
	// Distinguish a short but potentially alignable active window from two
	// streams that cannot overlap anywhere inside the configured lag range.
	// The former is insufficient evidence; the latter is genuinely no evidence
	// and lets the gate release capture once an acoustic path has expired.
	overlapLagMin := d.capture.start - d.playback.end
	overlapLagMax := d.capture.end - d.playback.start
	if d.config.CorrelationLagWindow.Max <= overlapLagMin || d.config.CorrelationLagWindow.Min >= overlapLagMax {
		return observation
	}
	if d.playback.end-d.playback.start < d.config.MinimumEvidence || d.capture.end-d.capture.start < d.config.MinimumEvidence {
		return insufficientActiveObservation()
	}
	minLagDuration := d.config.CorrelationLagWindow.Min
	if eligible := d.capture.start + d.config.MinimumEvidence - d.playback.end; eligible > minLagDuration {
		minLagDuration = eligible
	}
	maxLagDuration := d.config.CorrelationLagWindow.Max
	if eligible := d.capture.end - d.config.MinimumEvidence - d.playback.start; eligible < maxLagDuration {
		maxLagDuration = eligible
	}
	if maxLagDuration < minLagDuration {
		return insufficientActiveObservation()
	}
	minLagSamples, err := signedDurationToSamples(minLagDuration, rate)
	if err != nil {
		return PCM16SelfHearingObservation{Classification: PCM16SelfHearingInsufficientEvidence}
	}
	maxLagSamples, err := signedDurationToSamples(maxLagDuration, rate)
	if err != nil || maxLagSamples < minLagSamples {
		return PCM16SelfHearingObservation{Classification: PCM16SelfHearingInsufficientEvidence}
	}
	threshold := pcm16AmplitudeForDBFS(d.config.SilenceFloorDBFS)
	base := d.playback.start
	if d.capture.start < base {
		base = d.capture.start
	}
	measurement := PCM16CorrelationMeasurement{
		SourceStreamID:        "assistant-playback",
		SourceParticipantID:   "assistant",
		ReceivedStreamID:      "microphone-capture",
		ReceivedParticipantID: "local-microphone",
		IntervalID:            "local-self-hearing-window",
	}

	type lagResult struct {
		coefficient float64
		compared    int
		evidence    int
		start       time.Duration
		end         time.Duration
	}
	results := make(map[int]lagResult, (maxLagSamples-minLagSamples)/streamingPCM16LagStride+1)
	measure := func(lagSamples int) lagResult {
		if result, ok := results[lagSamples]; ok {
			return result
		}
		lag := samplesToSignedDuration(lagSamples, rate)
		sourceStart := d.playback.start
		if candidate := d.capture.start - lag; candidate > sourceStart {
			sourceStart = candidate
		}
		sourceEnd := d.playback.end
		if candidate := d.capture.end - lag; candidate < sourceEnd {
			sourceEnd = candidate
		}
		if sourceEnd-sourceStart < d.config.MinimumEvidence {
			return lagResult{}
		}
		if sourceEnd-sourceStart > d.config.AnalysisWindow {
			sourceStart = sourceEnd - d.config.AnalysisWindow
		}
		playbackStart, startErr := signedDurationToSamples(sourceStart-d.playback.start, rate)
		playbackEnd, endErr := signedDurationToSamples(sourceEnd-d.playback.start, rate)
		captureStart, captureStartErr := signedDurationToSamples(sourceStart+lag-d.capture.start, rate)
		captureEnd, captureEndErr := signedDurationToSamples(sourceEnd+lag-d.capture.start, rate)
		if startErr != nil || endErr != nil || captureStartErr != nil || captureEndErr != nil ||
			playbackStart < 0 || playbackEnd > len(d.playback.samples) || captureStart < 0 || captureEnd > len(d.capture.samples) ||
			playbackEnd <= playbackStart || captureEnd <= captureStart {
			return lagResult{}
		}
		playback := d.playback.samples[playbackStart:playbackEnd]
		capture := d.capture.samples[captureStart:captureEnd]
		if len(playback) > len(capture) {
			playback = playback[:len(capture)]
		} else if len(capture) > len(playback) {
			capture = capture[:len(playback)]
		}
		coefficient, compared := normalizedCorrelationAtLag(playback, nil, capture, 0, threshold)
		result := lagResult{
			coefficient: coefficient,
			compared:    compared,
			evidence:    pairedNonSilentSamples(playback, capture, 0, threshold),
			start:       sourceStart - base,
			end:         sourceEnd - base,
		}
		results[lagSamples] = result
		return result
	}

	foundSigned := false
	foundAbsolute := false
	bestEvidenceSamples := 0
	anyEvidenceSamples := 0
	forEachStreamingPCM16CorrelationCandidate(
		minLagSamples,
		maxLagSamples,
		func(lagSamples int, coefficient float64, compared int) {
			result := measure(lagSamples)
			if result.evidence > anyEvidenceSamples {
				anyEvidenceSamples = result.evidence
			}
			if compared == 0 || result.evidence < minimumSamples {
				return
			}
			if !foundSigned || coefficient > measurement.BestCorrelation {
				measurement.BestCorrelation = coefficient
				measurement.BestLag = samplesToSignedDuration(lagSamples, rate)
				foundSigned = true
			}
			if !foundAbsolute || math.Abs(coefficient) > measurement.BestAbsoluteCorrelation {
				measurement.BestAbsoluteCorrelation = math.Abs(coefficient)
				measurement.BestAbsoluteLag = samplesToSignedDuration(lagSamples, rate)
				measurement.ComparedSamples = compared
				measurement.Start = result.start
				measurement.End = result.end
				bestEvidenceSamples = result.evidence
				foundAbsolute = true
			}
		},
		func(lagSamples int) (float64, int) {
			result := measure(lagSamples)
			return result.coefficient, result.compared
		},
	)
	if anyEvidenceSamples == 0 {
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
	if measurement.BestAbsoluteCorrelation < d.config.CorrelationThreshold {
		observation.Classification = PCM16SelfHearingNonFeedback
		return observation
	}
	observation.Classification = PCM16SelfHearingConfirmed
	return observation
}

// streamingPCM16CorrelationCandidate is a cheap first-pass lag estimate used
// by the synchronous device gate. The full room-analysis primitive still
// visits every sample lag; the live gate refines the strongest bounded set so
// detector work cannot starve the microphone pump while a response is being
// rendered.
type streamingPCM16CorrelationCandidate struct {
	lag         int
	coefficient float64
	compared    int
}

const (
	streamingPCM16LagStride       = 4
	streamingPCM16RefinementCount = 8
)

func forEachStreamingPCM16CorrelationCandidate(
	minLagSamples, maxLagSamples int,
	visit func(lagSamples int, coefficient float64, compared int),
	measure func(lagSamples int) (float64, int),
) {
	if visit == nil || measure == nil || maxLagSamples < minLagSamples {
		return
	}
	coarse := make([]streamingPCM16CorrelationCandidate, 0, (maxLagSamples-minLagSamples)/streamingPCM16LagStride+1)
	for lag := minLagSamples; ; lag += streamingPCM16LagStride {
		coefficient, compared := measure(lag)
		visit(lag, coefficient, compared)
		coarse = append(coarse, streamingPCM16CorrelationCandidate{lag: lag, coefficient: coefficient, compared: compared})
		if lag > maxLagSamples-streamingPCM16LagStride {
			if lag != maxLagSamples {
				coefficient, compared = measure(maxLagSamples)
				visit(maxLagSamples, coefficient, compared)
				coarse = append(coarse, streamingPCM16CorrelationCandidate{lag: maxLagSamples, coefficient: coefficient, compared: compared})
			}
			break
		}
	}
	sort.Slice(coarse, func(i, j int) bool {
		left, right := math.Abs(coarse[i].coefficient), math.Abs(coarse[j].coefficient)
		if left != right {
			return left > right
		}
		return coarse[i].compared > coarse[j].compared
	})
	seen := make(map[int]struct{}, streamingPCM16RefinementCount*streamingPCM16LagStride*2)
	for index := 0; index < len(coarse) && index < streamingPCM16RefinementCount; index++ {
		center := coarse[index].lag
		start, end := center-streamingPCM16LagStride, center+streamingPCM16LagStride
		if start < minLagSamples {
			start = minLagSamples
		}
		if end > maxLagSamples {
			end = maxLagSamples
		}
		for lag := start; lag <= end; lag++ {
			if _, ok := seen[lag]; ok {
				continue
			}
			seen[lag] = struct{}{}
			coefficient, compared := measure(lag)
			visit(lag, coefficient, compared)
		}
	}
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
