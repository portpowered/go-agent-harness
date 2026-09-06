// Package selfhearing classifies bounded local speaker-to-microphone feedback
// without opening devices or starting asynchronous work.
package selfhearing

import (
	"errors"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/stream"
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
	PCM16SelfHearingDefaultSilenceFloorDBFS = stream.PCM16AnalysisSilenceFloorDBFS
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

// DefaultPCM16SelfHearingConfig returns a fresh local feedback policy.
func DefaultPCM16SelfHearingConfig() PCM16SelfHearingConfig {
	return PCM16SelfHearingConfig{
		AnalysisWindow:           PCM16SelfHearingDefaultAnalysisWindow,
		MinimumEvidence:          PCM16SelfHearingDefaultMinimumEvidence,
		CorrelationLagWindow:     PCM16LagWindow{Min: PCM16SelfHearingDefaultLagMin, Max: PCM16SelfHearingDefaultLagMax},
		CorrelationThreshold:     PCM16SelfHearingDefaultCorrelationThreshold,
		SilenceFloorDBFS:         PCM16SelfHearingDefaultSilenceFloorDBFS,
		MaximumReleaseLatency:    PCM16SelfHearingDefaultMaximumReleaseLatency,
		PostPlaybackAcousticTail: PCM16SelfHearingDefaultAcousticTail,
	}
}

// DefaultSelfHearingConfig returns a copy of the default self-hearing policy.
func DefaultSelfHearingConfig() PCM16SelfHearingConfig { return DefaultPCM16SelfHearingConfig() }

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
