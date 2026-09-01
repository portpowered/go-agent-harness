package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
)

const localFeedbackWarning = "Acoustic feedback detected: speaker audio is entering the microphone. Use headphones or route assistant audio to a non-speaker/file output."

var errNilLocalFeedbackPlaybackWrite = errors.New("local feedback playback write is nil")

type localFeedbackGateState string

const (
	localFeedbackGateIdle        localFeedbackGateState = "idle"
	localFeedbackGateAnalyzing   localFeedbackGateState = "analyzing"
	localFeedbackGateSuppressing localFeedbackGateState = "suppressing"
	localFeedbackGateDraining    localFeedbackGateState = "draining"
)

// rtcDeviceCaptureFilter is the narrow source-side seam used by the paired
// local-device policy. It returns owned frames that are ready for the
// provider-bound endpoint; an empty result means the raw frame was held or
// discarded.
type rtcDeviceCaptureFilter interface {
	FilterCapture(context.Context, []int16) ([][]int16, error)
	DiscardHeld()
}

// rtcDevicePlaybackObserver lets a policy observe exactly the PCM accepted by
// the local sink. The write callback runs under the observer's serialization
// boundary, which prevents a virtual or hardware loopback capture from being
// classified before the corresponding speaker frame is recorded.
type rtcDevicePlaybackObserver interface {
	WritePlayback(context.Context, []int16, func() error) error
}

type heldLocalCaptureFrame struct {
	samples []int16
	start   time.Duration
}

// localFeedbackGate is the stateful side-effect adapter around the pure audio
// self-hearing detector. It is shared by one source pump and one sink pump.
// Capture frames are copied while held and are either released in FIFO order
// or discarded; a terminal path always discards the held queue.
type localFeedbackGate struct {
	mu       sync.Mutex
	detector *audio.PCM16SelfHearingDetector
	// probe keeps the playback timeline but resets its capture window for each
	// frame after confirmation. The primary detector needs a minimum evidence
	// duration to avoid false positives; the probe lets an already-confirmed
	// gate discard a short final echo frame without holding unrelated speech.
	probe   *audio.PCM16SelfHearingDetector
	config  audio.PCM16SelfHearingConfig
	warning io.Writer

	state localFeedbackGateState

	playbackPosition time.Duration
	capturePosition  time.Duration
	lastPlaybackEnd  time.Duration
	suppressUntil    time.Duration
	playbackSeen     bool
	warningSent      bool
	closed           bool
	pending          []heldLocalCaptureFrame
}

func newLocalFeedbackGate(config audio.PCM16SelfHearingConfig, warning io.Writer) (*localFeedbackGate, error) {
	detector, err := audio.NewPCM16SelfHearingDetector(config)
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
	probeEvidence := pcm16DeviceDuration(audio.FrameSize) / 2
	if probeEvidence > probeConfig.AnalysisWindow {
		probeEvidence = probeConfig.AnalysisWindow
	}
	probeConfig.MinimumEvidence = probeEvidence
	// Once the full detector has confirmed a loop, the probe classifies one
	// device frame at a time. Use a lower floor for that short frame because
	// the acoustic path can reshape a frame even though the startup window was
	// strongly correlated; independent speech still has to clear the same
	// active-evidence and lag checks.
	if probeConfig.CorrelationThreshold > 0.20 {
		probeConfig.CorrelationThreshold = 0.20
	}
	probe, err := audio.NewPCM16SelfHearingDetector(probeConfig)
	if err != nil {
		_ = detector.Close()
		return nil, err
	}
	return &localFeedbackGate{
		detector: detector,
		probe:    probe,
		config:   detector.Config(),
		warning:  warning,
		state:    localFeedbackGateIdle,
	}, nil
}

// WritePlayback observes a speaker frame only after the underlying sink has
// accepted it. The gate lock covers the write and observation as one ordered
// boundary so a looped-back capture cannot overtake its playback evidence.
func (g *localFeedbackGate) WritePlayback(ctx context.Context, samples []int16, write func() error) error {
	if g == nil {
		if write == nil {
			return nil
		}
		return write()
	}
	if err := feedbackContextError(ctx); err != nil {
		return err
	}
	if write == nil {
		return errNilLocalFeedbackPlaybackWrite
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return audio.ErrClosed
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
		g.resetCaptureEvidenceLocked()
		g.state = localFeedbackGateIdle
	}
	if err := g.detector.ObservePlaybackContext(ctx, audio.PCM16TimedFrame{
		Samples:    samples,
		SampleRate: audio.SampleRate,
		Start:      g.playbackPosition,
	}); err != nil {
		return err
	}
	if err := g.probe.ObservePlaybackContext(ctx, audio.PCM16TimedFrame{
		Samples:    samples,
		SampleRate: audio.SampleRate,
		Start:      g.playbackPosition,
	}); err != nil {
		return err
	}

	g.playbackPosition += pcm16DeviceDuration(len(samples))
	g.lastPlaybackEnd = g.playbackPosition
	g.playbackSeen = true
	if g.state == localFeedbackGateSuppressing {
		g.suppressUntil = g.playbackTailEndLocked()
	}
	return nil
}

func (g *localFeedbackGate) captureNeedsReanchorLocked() bool {
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
	return g.capturePosition > addFeedbackDuration(g.playbackPosition, leadBound)
}

// FilterCapture observes raw microphone PCM before provider delivery. Frames
// that cannot yet be classified remain bounded by MaximumReleaseLatency;
// confirmed feedback is discarded and all other released frames preserve
// capture order.
func (g *localFeedbackGate) FilterCapture(ctx context.Context, samples []int16) ([][]int16, error) {
	if g == nil {
		return [][]int16{append([]int16(nil), samples...)}, nil
	}
	if err := feedbackContextError(ctx); err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		return nil, nil
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil, audio.ErrClosed
	}

	start := g.capturePosition
	end := start + pcm16DeviceDuration(len(samples))
	owned := append([]int16(nil), samples...)
	g.capturePosition = end
	if !g.playbackIsRelevantLocked(start) {
		// Capture outside the active playback/tail horizon is ordinary user
		// input. Do not retain it in the detector: otherwise a later speaker
		// frame could be compared with stale microphone audio from before
		// playback began (or after the acoustic tail ended).
		g.resetCaptureEvidenceLocked()
		g.state = localFeedbackGateIdle
		released := g.releaseAllLocked()
		return append(released, owned), nil
	}
	if g.state == localFeedbackGateSuppressing || g.state == localFeedbackGateDraining {
		// Once a full analysis window has confirmed feedback, classify each new
		// frame independently. This prevents a short final echo frame from being
		// released with an unrelated user frame while preserving headphone speech
		// that is not correlated with the current speaker PCM.
		g.probe.ResetCapture()
		probeObservation, probeErr := g.probe.ObserveCaptureContext(ctx, audio.PCM16TimedFrame{
			Samples:    owned,
			SampleRate: audio.SampleRate,
			Start:      start,
		})
		if probeErr != nil {
			return nil, probeErr
		}
		if probeObservation.Confirmed() {
			g.suppressUntil = g.playbackTailEndLocked()
			return nil, nil
		}
		g.resetCaptureEvidenceLocked()
		g.state = localFeedbackGateSuppressing
		return [][]int16{owned}, nil
	}
	observation, err := g.detector.ObserveCaptureContext(ctx, audio.PCM16TimedFrame{
		Samples:    owned,
		SampleRate: audio.SampleRate,
		Start:      start,
	})
	if err != nil {
		return nil, err
	}
	g.pending = append(g.pending, heldLocalCaptureFrame{samples: owned, start: start})

	if observation.Confirmed() {
		g.state = localFeedbackGateSuppressing
		g.suppressUntil = g.playbackTailEndLocked()
		g.pending = nil
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
	stableNonFeedback := observation.Classification == audio.PCM16SelfHearingNonFeedback && end >= g.config.AnalysisWindow
	if !g.playbackIsRelevantLocked(start) || observation.Classification == audio.PCM16SelfHearingNoEvidence || observation.Classification == audio.PCM16SelfHearingRateMismatch || stableNonFeedback {
		if g.state == localFeedbackGateSuppressing && g.playbackIsRelevantLocked(start) {
			g.state = localFeedbackGateDraining
		} else {
			g.state = localFeedbackGateIdle
		}
		released := g.releaseAllLocked()
		if observation.Classification != audio.PCM16SelfHearingNonFeedback {
			g.resetCaptureEvidenceLocked()
		}
		return released, nil
	}

	if g.state == localFeedbackGateIdle {
		g.state = localFeedbackGateAnalyzing
	}
	return g.releaseExpiredLocked(end), nil
}

// DiscardHeld applies the terminal policy for source cancellation, device
// loss, and provider termination. It deliberately does not close the shared
// gate because the sibling playback pump may still be unwinding.
func (g *localFeedbackGate) DiscardHeld() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.pending = nil
	g.mu.Unlock()
}

// Close releases detector-owned and gate-owned storage. It is intentionally
// called after both device pumps have been asked to stop so the playback write
// boundary cannot strand a source waiting on the gate mutex.
func (g *localFeedbackGate) Close() error {
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
	return errors.Join(g.detector.Close(), g.probe.Close())
}

func (g *localFeedbackGate) resetCaptureEvidenceLocked() {
	g.detector.ResetCapture()
	g.probe.ResetCapture()
}

func (g *localFeedbackGate) releaseAllLocked() [][]int16 {
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

func (g *localFeedbackGate) releaseExpiredLocked(currentEnd time.Duration) [][]int16 {
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

func (g *localFeedbackGate) playbackIsRelevantLocked(captureStart time.Duration) bool {
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

func (g *localFeedbackGate) playbackTailEndLocked() time.Duration {
	return addFeedbackDuration(g.lastPlaybackEnd, g.config.PostPlaybackAcousticTail)
}

func (g *localFeedbackGate) warnOnceLocked() {
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
		_, _ = fmt.Fprintln(writer, localFeedbackWarning)
	}()
}

func pcm16DeviceDuration(samples int) time.Duration {
	if samples <= 0 {
		return 0
	}
	return time.Duration(samples) * time.Second / audio.SampleRate
}

func addFeedbackDuration(start, duration time.Duration) time.Duration {
	if duration > 0 && start > time.Duration(1<<63-1)-duration {
		return time.Duration(1<<63 - 1)
	}
	return start + duration
}

func feedbackContextError(ctx context.Context) error {
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
