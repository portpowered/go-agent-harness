package selfhearing

import (
	"context"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/contract"
)

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
		return contract.ErrClosed
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return contract.ErrClosed
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
		return contract.ErrClosed
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return contract.ErrClosed
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
		return contract.ErrClosed
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return contract.ErrClosed
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
		return PCM16SelfHearingObservation{}, contract.ErrClosed
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return PCM16SelfHearingObservation{}, contract.ErrClosed
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
