package audio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/selfhearing"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/contract"
)

const pcm16FeedbackWarning = "Acoustic feedback detected: speaker audio is entering the microphone. Use headphones or route assistant audio to a non-speaker/file output."

var errNilPCM16FeedbackPlaybackWrite = errors.New("local feedback playback write is nil")

const pcm16FeedbackIndependentCorrelation = 0.15

type pcm16FeedbackGateState string

const (
	pcm16FeedbackGateIdle        pcm16FeedbackGateState = "idle"
	pcm16FeedbackGateAnalyzing   pcm16FeedbackGateState = "analyzing"
	pcm16FeedbackGateSuppressing pcm16FeedbackGateState = "suppressing"
	pcm16FeedbackGateDraining    pcm16FeedbackGateState = "draining"
)

type heldPCM16CaptureFrame struct {
	samples []int16
	start   time.Duration
}

// PCM16FeedbackGate is the stateful side-effect adapter around the pure audio
// self-hearing detector. It is shared by one source pump and one sink pump.
// Capture frames are copied while held and are either released in FIFO order
// or discarded; a terminal path always discards the held queue.
type PCM16FeedbackGate struct {
	mu       sync.Mutex
	detector *selfhearing.PCM16SelfHearingDetector
	// probe keeps the playback timeline but resets its capture window for each
	// frame after confirmation. The primary detector needs a minimum evidence
	// duration to avoid false positives; the probe lets an already-confirmed
	// gate discard a short final echo frame without holding unrelated speech.
	probe   *selfhearing.PCM16SelfHearingDetector
	config  selfhearing.PCM16SelfHearingConfig
	warning io.Writer

	// playbackRate and captureRate are the true negotiated device rates, not
	// the provider's requested rate. A capture device that falls back to an
	// alternate supported rate (see openRTCDeviceSourceAtRate) still hands raw,
	// pre-resample PCM to FilterCapture, so declaring the requested rate here
	// would silently misstate every duration and lag computed from it. If the
	// two ever diverge, the detector's own rate-mismatch classification is the
	// safety net: it refuses to compare bytes across an undeclared rate change
	// rather than produce a meaningless correlation.
	playbackRate int
	captureRate  int

	state pcm16FeedbackGateState

	playbackPosition time.Duration
	capturePosition  time.Duration
	lastPlaybackEnd  time.Duration
	suppressUntil    time.Duration
	playbackSeen     bool
	warningSent      bool
	closed           bool
	pending          []heldPCM16CaptureFrame

	// probeIndependentEvidence accumulates the duration of consecutive
	// PCM16SelfHearingNonFeedback probe classifications while suppressing or
	// draining. A single ~1-frame probe window is short enough that ordinary
	// acoustic-path noise (reverb tail, a word/pause boundary, AGC artifacts)
	// can push one frame's correlation below threshold even though it is
	// still the same ongoing echo; requiring config.AnalysisWindow of
	// sustained independent classification before releasing anything mirrors
	// the confidence bar the primary detector already applies to confirming
	// feedback in the first place. Any other classification resets it to
	// zero and discards the frames held while it accumulated.
	probeIndependentEvidence time.Duration
	confirmedLag             time.Duration
	// startupAmbiguousEvidence marks a sub-MinimumEvidence active burst seen
	// while local playback is relevant. Such a burst cannot yet be classified
	// as feedback or independent speech. Releasing it merely because the normal
	// latency hold expired caused the short AUVoiceIO startup echo in test42 to
	// reach server VAD and cancel the assistant. It remains held until sustained
	// non-feedback proves real speech, confirmed correlation proves echo, or a
	// return to no evidence lets the gate discard the meaningless short burst.
	startupAmbiguousEvidence bool
}

// NewPCM16FeedbackGate constructs the gate for one paired local-device
// session. playbackRate and captureRate are the true negotiated device rates
// (see the PCM16FeedbackGate field comments); a non-positive value falls back
// to the shared compatibility default so a caller that has not resolved a
// concrete rate keeps prior behavior.
func NewPCM16FeedbackGate(config selfhearing.PCM16SelfHearingConfig, warning io.Writer, playbackRate, captureRate int) (*PCM16FeedbackGate, error) {
	if playbackRate <= 0 {
		playbackRate = SampleRate
	}
	if captureRate <= 0 {
		captureRate = SampleRate
	}
	detector, err := selfhearing.NewPCM16SelfHearingDetector(config)
	if err != nil {
		return nil, err
	}
	probeConfig := detector.Config()
	// Require a substantial fraction of a device frame for the
	// post-confirmation probe. A one-sample overlap at a playback boundary can
	// otherwise produce a perfect Pearson coefficient for unrelated speech and
	// suppress the first headphone/user frame after playback ends. Half a frame
	// tolerates the silence-floor samples that are excluded from otherwise
	// contentful deterministic frames without accepting a short boundary sliver.
	probeEvidence := pcm16DeviceDurationAtRate(FrameSize, captureRate) / 2
	if probeEvidence > probeConfig.AnalysisWindow {
		probeEvidence = probeConfig.AnalysisWindow
	}
	probeConfig.MinimumEvidence = probeEvidence
	// Once the full detector has confirmed a loop, the probe classifies one
	// device frame at a time. Use a lower floor for that short frame because
	// the acoustic path can reshape a frame even though the startup window was
	// strongly correlated; independent speech still has to clear the same
	// active-evidence and lag checks.
	// The primary detector's threshold remains appropriate after the probe is
	// narrowed to its learned acoustic lag. Lowering it here would classify
	// moderately similar synthetic/user speech as echo in a one-frame window.
	probe, err := selfhearing.NewPCM16SelfHearingDetector(probeConfig)
	if err != nil {
		_ = detector.Close()
		return nil, err
	}
	return &PCM16FeedbackGate{
		detector:     detector,
		probe:        probe,
		config:       detector.Config(),
		warning:      warning,
		playbackRate: playbackRate,
		captureRate:  captureRate,
		state:        pcm16FeedbackGateIdle,
	}, nil
}

// WritePlayback observes a speaker frame only after the underlying sink has
// accepted it. The gate lock covers the write and observation as one ordered
// boundary so a looped-back capture cannot overtake its playback evidence.
func (g *PCM16FeedbackGate) WritePlayback(ctx context.Context, samples []int16, write func() error) error {
	if g == nil {
		if write == nil {
			return nil
		}
		return write()
	}
	if err := pcm16FeedbackContextError(ctx); err != nil {
		return err
	}
	if write == nil {
		return errNilPCM16FeedbackPlaybackWrite
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return contract.ErrClosed
	}
	if err := write(); err != nil {
		return err
	}
	if g.captureNeedsReanchorLocked() {
		// Capture starts before the first assistant response in a normal live
		// session, so its local media cursor can be seconds ahead of the first
		// playback cursor. Re-anchor the comparison window at the first accepted
		// speaker frame; otherwise playbackIsRelevantLocked would bypass the
		// actual echo as stale pre-playback capture. A long idle gap between
		// responses needs the same treatment, while a modest capture lead remains
		// within the configured correlation window.
		g.playbackPosition = g.capturePosition
		g.lastPlaybackEnd = g.playbackPosition
		g.pending = nil
		g.startupAmbiguousEvidence = false
		g.resetCaptureEvidenceLocked()
		g.state = pcm16FeedbackGateIdle
	}
	if err := g.detector.ObservePlaybackContext(ctx, selfhearing.PCM16TimedFrame{
		Samples:    samples,
		SampleRate: g.playbackRate,
		Start:      g.playbackPosition,
	}); err != nil {
		return err
	}
	if err := g.probe.ObservePlaybackContext(ctx, selfhearing.PCM16TimedFrame{
		Samples:    samples,
		SampleRate: g.playbackRate,
		Start:      g.playbackPosition,
	}); err != nil {
		return err
	}

	g.playbackPosition += pcm16DeviceDurationAtRate(len(samples), g.playbackRate)
	g.lastPlaybackEnd = g.playbackPosition
	g.playbackSeen = true
	if g.state == pcm16FeedbackGateSuppressing {
		g.suppressUntil = g.playbackTailEndLocked()
	}
	return nil
}

func (g *PCM16FeedbackGate) captureNeedsReanchorLocked() bool {
	if !g.playbackSeen {
		return true
	}
	leadBound := g.config.AnalysisWindow
	if lag := g.config.CorrelationLagWindow.Min; lag < 0 && -lag > leadBound {
		leadBound = -lag
	}
	if lag := g.config.CorrelationLagWindow.Max; lag > leadBound {
		leadBound = lag
	}
	return g.capturePosition > addPCM16FeedbackDuration(g.playbackPosition, leadBound)
}

// FilterCapture observes raw microphone PCM before provider delivery. Frames
// that cannot yet be classified remain bounded by MaximumReleaseLatency;
// confirmed feedback is discarded and all other released frames preserve
// capture order.
func (g *PCM16FeedbackGate) FilterCapture(ctx context.Context, samples []int16) ([][]int16, error) {
	if g == nil {
		return [][]int16{append([]int16(nil), samples...)}, nil
	}
	if err := pcm16FeedbackContextError(ctx); err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		return nil, nil
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil, contract.ErrClosed
	}

	start := g.capturePosition
	end := start + pcm16DeviceDurationAtRate(len(samples), g.captureRate)
	owned := append([]int16(nil), samples...)
	g.capturePosition = end
	if !g.playbackIsRelevantLocked(start) {
		// Capture outside the active playback/tail horizon is ordinary user
		// input. Do not retain it in the detector: otherwise a later speaker
		// frame could be compared with stale microphone audio from before
		// playback began (or after the acoustic tail ended).
		g.resetCaptureEvidenceLocked()
		g.state = pcm16FeedbackGateIdle
		var released [][]int16
		if g.startupAmbiguousEvidence {
			g.pending = nil
		} else {
			released = g.releaseAllLocked()
		}
		g.startupAmbiguousEvidence = false
		return append(released, owned), nil
	}
	if g.state == pcm16FeedbackGateSuppressing || g.state == pcm16FeedbackGateDraining {
		return g.classifySuppressedCaptureLocked(ctx, owned, start, end)
	}
	observation, err := g.detector.ObserveCaptureContext(ctx, selfhearing.PCM16TimedFrame{
		Samples:    owned,
		SampleRate: g.captureRate,
		Start:      start,
	})
	if err != nil {
		return nil, err
	}
	g.pending = append(g.pending, heldPCM16CaptureFrame{samples: owned, start: start})

	if observation.Confirmed() {
		lag := observation.Measurement.BestAbsoluteLag
		g.confirmedLag = lag
		tolerance := pcm16DeviceDurationAtRate(FrameSize, g.captureRate)
		probeWindow := selfhearing.PCM16LagWindow{Min: lag - tolerance, Max: lag + tolerance}
		// Each assistant response may have a different device/callback lag. Clamp
		// to the immutable session policy, not the probe's prior response window:
		// disjoint successive windows would otherwise clamp into Min > Max.
		configuredWindow := g.config.CorrelationLagWindow
		if probeWindow.Min < configuredWindow.Min {
			probeWindow.Min = configuredWindow.Min
		}
		if probeWindow.Max > configuredWindow.Max {
			probeWindow.Max = configuredWindow.Max
		}
		if err := g.probe.RetargetCorrelationLagWindow(probeWindow); err != nil {
			return nil, err
		}
		g.state = pcm16FeedbackGateSuppressing
		g.suppressUntil = g.playbackTailEndLocked()
		g.pending = nil
		g.startupAmbiguousEvidence = false
		g.resetCaptureEvidenceLocked()
		g.warnOnceLocked()
		return nil, nil
	}

	// A rolling detector can find enough samples for a below-threshold
	// candidate before the full startup window has overlapped both streams. In
	// that short interval, retain the capture rather than releasing a genuine
	// loop on the basis of a truncated alignment. The default analysis window
	// is also the documented maximum release latency, so this does not extend
	// the user-visible bound.
	stableNonFeedback := observation.Classification == selfhearing.PCM16SelfHearingNonFeedback && end >= g.config.AnalysisWindow
	if observation.Classification == selfhearing.PCM16SelfHearingNoEvidence && g.startupAmbiguousEvidence && g.playbackIsRelevantLocked(start) {
		g.pending = nil
		g.startupAmbiguousEvidence = false
		g.resetCaptureEvidenceLocked()
		g.state = pcm16FeedbackGateIdle
		return nil, nil
	}
	if !g.playbackIsRelevantLocked(start) || observation.Classification == selfhearing.PCM16SelfHearingNoEvidence || observation.Classification == selfhearing.PCM16SelfHearingRateMismatch || stableNonFeedback {
		if g.state == pcm16FeedbackGateSuppressing && g.playbackIsRelevantLocked(start) {
			g.state = pcm16FeedbackGateDraining
		} else {
			g.state = pcm16FeedbackGateIdle
		}
		released := g.releaseAllLocked()
		g.startupAmbiguousEvidence = false
		if observation.Classification != selfhearing.PCM16SelfHearingNonFeedback {
			g.resetCaptureEvidenceLocked()
		}
		return released, nil
	}

	if g.state == pcm16FeedbackGateIdle {
		g.state = pcm16FeedbackGateAnalyzing
	}
	if observation.Classification == selfhearing.PCM16SelfHearingInsufficientEvidence && observation.EvidenceSamples > 0 {
		g.startupAmbiguousEvidence = true
	}
	if g.startupAmbiguousEvidence {
		return nil, nil
	}
	return g.releaseExpiredLocked(end), nil
}

// classifySuppressedCaptureLocked re-classifies one already-confirmed-loop
// capture frame against the probe detector. It is called while the gate is
// suppressing or draining, i.e. after the primary detector has already
// confirmed feedback at least once.
//
// A single ~1-frame probe window is short enough that ordinary acoustic-path
// noise (a word/pause boundary in the assistant's own speech, reverb
// smearing, AGC/noise-suppression artifacts) routinely produces a
// non-Confirmed classification for a frame that is still part of the same
// ongoing echo. Releasing on any non-Confirmed result (the original
// behavior) leaked exactly these ambiguous frames straight to the provider,
// which is what let the assistant hear fragments of itself throughout a
// response after its one-time warning had already fired.
//
// Only a PCM16SelfHearingNonFeedback classification (real paired evidence
// that is specifically uncorrelated) is treated as possible independent
// speech, and even then only after it has held for config.AnalysisWindow
// in a row -- the same confidence bar the primary detector requires before
// confirming feedback in the first place. PCM16SelfHearingNoEvidence means
// the probe found nothing to compare (typically because the acoustic tail
// has genuinely run out of playback to correlate against); it is safe to
// release immediately and also flushes any partially accumulated evidence.
// Every other classification (Confirmed, insufficient evidence, or a rate
// mismatch) keeps the frame suppressed and discards whatever independent
// evidence had been accumulating, because it is still consistent with
// ongoing echo.
func (g *PCM16FeedbackGate) classifySuppressedCaptureLocked(ctx context.Context, owned []int16, start, end time.Duration) ([][]int16, error) {
	g.probe.ResetCapture()
	probeObservation, probeErr := g.probe.ObserveCaptureContext(ctx, selfhearing.PCM16TimedFrame{
		Samples:    owned,
		SampleRate: g.captureRate,
		Start:      start,
	})
	if probeErr != nil {
		return nil, probeErr
	}

	switch probeObservation.Classification {
	case selfhearing.PCM16SelfHearingNonFeedback:
		// A one-frame room response can dip below the primary confirmation
		// threshold at pauses while remaining moderately correlated. Only a
		// clearly decorrelated frame contributes to an independent-speech streak;
		// the sustained-duration requirement below remains the second guard.
		if probeObservation.Measurement.BestAbsoluteCorrelation > pcm16FeedbackIndependentCorrelation {
			g.pending = nil
			g.probeIndependentEvidence = 0
			g.suppressUntil = g.playbackTailEndLocked()
			g.state = pcm16FeedbackGateSuppressing
			return nil, nil
		}
		g.pending = append(g.pending, heldPCM16CaptureFrame{samples: owned, start: start})
		g.probeIndependentEvidence += end - start
		if g.probeIndependentEvidence < g.config.AnalysisWindow {
			return nil, nil
		}
		g.state = pcm16FeedbackGateDraining
		g.probeIndependentEvidence = 0
		return g.releaseAllLocked(), nil
	case selfhearing.PCM16SelfHearingNoEvidence:
		g.probeIndependentEvidence = 0
		// Silence/no-evidence inside the learned acoustic alignment is a common
		// TTS pause, not proof of independent speech. Drop it while its source
		// interval still overlaps accepted playback; outside that interval the
		// acoustic path has expired and held independent frames can be released.
		sourceStart := start - g.confirmedLag
		sourceEnd := end - g.confirmedLag
		if sourceEnd > 0 && sourceStart < g.lastPlaybackEnd {
			g.pending = nil
			g.state = pcm16FeedbackGateSuppressing
			return nil, nil
		}
		g.state = pcm16FeedbackGateDraining
		return append(g.releaseAllLocked(), owned), nil
	case selfhearing.PCM16SelfHearingInsufficientEvidence:
		// Preserve a boundary fragment until subsequent frames disambiguate it.
		// A following confirmed echo clears it; a sustained non-feedback streak
		// releases it in order, avoiding loss of the first barge-in frame.
		g.pending = append(g.pending, heldPCM16CaptureFrame{samples: owned, start: start})
		g.probeIndependentEvidence = 0
		g.suppressUntil = g.playbackTailEndLocked()
		g.state = pcm16FeedbackGateSuppressing
		return nil, nil
	default:
		// Confirmed feedback or a rate mismatch remains consistent with echo.
		// Extend the acoustic tail and drop ambiguous held frames.
		g.pending = nil
		g.probeIndependentEvidence = 0
		g.suppressUntil = g.playbackTailEndLocked()
		g.state = pcm16FeedbackGateSuppressing
		return nil, nil
	}
}

// DiscardHeld applies the terminal policy for source cancellation, device
// loss, and provider termination. It deliberately does not close the shared
// gate because the sibling playback pump may still be unwinding.
func (g *PCM16FeedbackGate) DiscardHeld() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.pending = nil
	g.probeIndependentEvidence = 0
	g.startupAmbiguousEvidence = false
	g.mu.Unlock()
}

// Close releases detector-owned and gate-owned storage. It is intentionally
// called after both device pumps have been asked to stop so the playback write
// boundary cannot strand a source waiting on the gate mutex.
func (g *PCM16FeedbackGate) Close() error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil
	}
	g.closed = true
	g.pending = nil
	g.startupAmbiguousEvidence = false
	return errors.Join(g.detector.Close(), g.probe.Close())
}

func (g *PCM16FeedbackGate) resetCaptureEvidenceLocked() {
	g.detector.ResetCapture()
	g.probe.ResetCapture()
}

func (g *PCM16FeedbackGate) releaseAllLocked() [][]int16 {
	if len(g.pending) == 0 {
		return nil
	}
	released := make([][]int16, len(g.pending))
	for index := range g.pending {
		released[index] = g.pending[index].samples
		g.pending[index].samples = nil
	}
	g.pending = nil
	return released
}

func (g *PCM16FeedbackGate) releaseExpiredLocked(currentEnd time.Duration) [][]int16 {
	latency := g.config.MaximumReleaseLatency
	if latency <= 0 || len(g.pending) == 0 {
		return nil
	}
	count := 0
	for count < len(g.pending) && currentEnd-g.pending[count].start >= latency {
		count++
	}
	if count == 0 {
		return nil
	}
	released := make([][]int16, count)
	for index := 0; index < count; index++ {
		released[index] = g.pending[index].samples
		g.pending[index].samples = nil
	}
	copy(g.pending, g.pending[count:])
	g.pending = g.pending[:len(g.pending)-count]
	if len(g.pending) == 0 {
		g.pending = nil
	}
	return released
}

func (g *PCM16FeedbackGate) playbackIsRelevantLocked(captureStart time.Duration) bool {
	if !g.playbackSeen {
		return false
	}
	// Capture can be ahead of the sink by one bounded correlation lag. The
	// acoustic tail then covers late speaker bleed after the last accepted
	// playback frame.
	horizon := g.suppressUntil
	if horizon < g.playbackTailEndLocked() {
		horizon = g.playbackTailEndLocked()
	}
	if maxLag := g.config.CorrelationLagWindow.Max; maxLag > 0 {
		horizon += maxLag
	}
	return captureStart < horizon
}

func (g *PCM16FeedbackGate) playbackTailEndLocked() time.Duration {
	return addPCM16FeedbackDuration(g.lastPlaybackEnd, g.config.PostPlaybackAcousticTail)
}

// FeedbackConfirmed reports whether this gate has ever classified captured
// audio as confirmed acoustic feedback. It is monotonic and never resets for
// the lifetime of a gate: warningSent is
// itself set exactly once, exactly when feedback is first confirmed (see
// warnOnceLocked), guarded by the same mutex as every other gate field.
func (g *PCM16FeedbackGate) FeedbackConfirmed() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.warningSent
}

// State reports the gate's current policy state. It is intended for
// diagnostics and tests; media filtering remains available through
// FilterCapture and WritePlayback.
func (g *PCM16FeedbackGate) State() string {
	if g == nil {
		return string(pcm16FeedbackGateIdle)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return string(g.state)
}

// PlaybackPosition reports the gate's accepted playback cursor.
func (g *PCM16FeedbackGate) PlaybackPosition() time.Duration {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.playbackPosition
}

// CapturePosition reports the gate's observed capture cursor.
func (g *PCM16FeedbackGate) CapturePosition() time.Duration {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.capturePosition
}

// ConfirmedLag reports the most recently learned acoustic lag.
func (g *PCM16FeedbackGate) ConfirmedLag() time.Duration {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.confirmedLag
}

func (g *PCM16FeedbackGate) warnOnceLocked() {
	if g.warningSent {
		return
	}
	g.warningSent = true
	if g.warning == nil {
		return
	}
	writer := g.warning
	// Warning I/O is deliberately detached from both media pumps. A terminal
	// writer supplied by an embedding may block or fail; neither condition can
	// hold the gate or affect provider delivery.
	go func() {
		_, _ = fmt.Fprintln(writer, pcm16FeedbackWarning)
	}()
}
