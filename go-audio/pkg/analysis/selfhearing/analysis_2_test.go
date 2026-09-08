package selfhearing_test

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	selfhearing "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/selfhearing"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/contract"
)

func TestPCM16SelfHearingDiscontinuousCaptureResetsPlayback(t *testing.T) {
	detector := newSelfHearingDetector(t, selfhearing.DefaultSelfHearingConfig())
	if err := detector.ObservePlayback(selfhearing.PCM16TimedFrame{Samples: testSignal(20, 11), SampleRate: 1000, Start: 0}); err != nil {
		t.Fatalf("seed ObservePlayback(): %v", err)
	}
	if stats := detector.BufferStats(); stats.PlaybackSamples == 0 {
		t.Fatalf("playback seed did not populate the buffer")
	}
	if _, err := detector.ObserveCapture(selfhearing.PCM16TimedFrame{Samples: testSignal(20, 13), SampleRate: 1000, Start: 0}); err != nil {
		t.Fatalf("first ObserveCapture(): %v", err)
	}
	// A large forward gap makes the capture stream discontinuous, which must
	// drop stale playback evidence gathered before the gap.
	if _, err := detector.ObserveCapture(selfhearing.PCM16TimedFrame{Samples: testSignal(20, 15), SampleRate: 1000, Start: time.Second}); err != nil {
		t.Fatalf("gapped ObserveCapture(): %v", err)
	}
	if stats := detector.BufferStats(); stats.PlaybackSamples != 0 {
		t.Fatalf("playback samples after discontinuous capture = %d, want reset to 0", stats.PlaybackSamples)
	}
}

func TestPCM16SelfHearingBufferGrowsThenTrimsIncrementally(t *testing.T) {
	config := selfhearing.DefaultSelfHearingConfig()
	config.AnalysisWindow = 120 * time.Millisecond
	config.CorrelationLagWindow = selfhearing.PCM16LagWindow{Min: -time.Millisecond, Max: time.Millisecond}
	detector := newSelfHearingDetector(t, config)
	for start := 0; start < 200; start += 20 {
		if err := detector.ObservePlayback(selfhearing.PCM16TimedFrame{
			Samples:    testSignal(20, 61+start),
			SampleRate: 1000,
			Start:      time.Duration(start) * time.Millisecond,
		}); err != nil {
			t.Fatalf("ObservePlayback(%d): %v", start, err)
		}
	}
	stats := detector.BufferStats()
	if stats.PlaybackSamples != stats.MaxPlaybackSamples {
		t.Fatalf("playback samples = %d, want exactly the bound %d after incremental trimming", stats.PlaybackSamples, stats.MaxPlaybackSamples)
	}
}

func TestPCM16SelfHearingRejectsMalformedFrames(t *testing.T) {
	detector := newSelfHearingDetector(t, selfhearing.DefaultSelfHearingConfig())
	cases := []struct {
		name  string
		frame selfhearing.PCM16TimedFrame
	}{
		{name: "non-positive sample rate", frame: selfhearing.PCM16TimedFrame{Samples: testSignal(20, 81), SampleRate: 0}},
		{name: "empty samples", frame: selfhearing.PCM16TimedFrame{Samples: nil, SampleRate: 1000}},
		{name: "negative media position", frame: selfhearing.PCM16TimedFrame{Samples: testSignal(20, 83), SampleRate: 1000, Start: -time.Millisecond}},
		{name: "frame end overflows the timeline", frame: selfhearing.PCM16TimedFrame{Samples: testSignal(20, 85), SampleRate: 1000, Start: time.Duration(math.MaxInt64) - time.Millisecond}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := detector.ObservePlayback(test.frame); !errors.Is(err, selfhearing.ErrInvalidPCM16SelfHearingFrame) {
				t.Fatalf("ObservePlayback(%+v) = %v, want invalid-frame", test.frame, err)
			}
		})
	}
}

func TestPCM16SelfHearingRejectsSampleRateThatOverflowsBufferConversion(t *testing.T) {
	config := selfhearing.DefaultSelfHearingConfig()
	config.AnalysisWindow = 5 * time.Second
	config.MinimumEvidence = time.Second
	detector := newSelfHearingDetector(t, config)
	frame := selfhearing.PCM16TimedFrame{Samples: testSignal(20, 79), SampleRate: math.MaxInt32}
	if err := detector.ObservePlayback(frame); !errors.Is(err, selfhearing.ErrInvalidPCM16SelfHearingFrame) {
		t.Fatalf("ObservePlayback() with overflowing rate = %v, want invalid-frame", err)
	}
}

func TestPCM16SelfHearingContextNilIsTreatedAsUncancelled(t *testing.T) {
	detector := newSelfHearingDetector(t, selfhearing.DefaultSelfHearingConfig())
	frame := selfhearing.PCM16TimedFrame{Samples: testSignal(20, 89), SampleRate: 1000}
	//lint:ignore SA1012 deliberately exercising the documented nil-context fast path in ObservePlaybackContext.
	if err := detector.ObservePlaybackContext(nil, frame); err != nil {
		t.Fatalf("ObservePlaybackContext(nil, frame) = %v, want nil", err)
	}
	//lint:ignore SA1012 deliberately exercising the documented nil-context fast path in ObserveCaptureContext.
	if _, err := detector.ObserveCaptureContext(nil, frame); err != nil {
		t.Fatalf("ObserveCaptureContext(nil, frame) = %v, want nil", err)
	}
}

func TestPCM16SelfHearingZeroConfigDefaultsToDocumentedPolicy(t *testing.T) {
	detector, err := selfhearing.NewPCM16SelfHearingDetector(selfhearing.PCM16SelfHearingConfig{})
	if err != nil {
		t.Fatalf("NewPCM16SelfHearingDetector(zero value) error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := detector.Close(); err != nil {
			t.Errorf("detector.Close(): %v", err)
		}
	})
	if got, want := detector.Config(), selfhearing.DefaultSelfHearingConfig(); got != want {
		t.Fatalf("Config() = %+v, want the documented default %+v", got, want)
	}
}

func TestPCM16SelfHearingLagRestrictionRetainsEvidenceAndRejectsWidening(t *testing.T) {
	detector := newSelfHearingDetector(t, selfhearing.DefaultSelfHearingConfig())
	if err := detector.ObservePlayback(selfhearing.PCM16TimedFrame{Samples: testSignal(100, 401), SampleRate: 1000, Start: 0}); err != nil {
		t.Fatal(err)
	}
	window := selfhearing.PCM16LagWindow{Min: 200 * time.Millisecond, Max: 280 * time.Millisecond}
	if err := detector.RestrictCorrelationLagWindow(window); err != nil {
		t.Fatalf("restrict lag: %v", err)
	}
	if detector.Config().CorrelationLagWindow != window || detector.BufferStats().PlaybackSamples != 100 {
		t.Fatalf("restricted detector config/stats = %+v/%+v", detector.Config(), detector.BufferStats())
	}
	if err := detector.RestrictCorrelationLagWindow(selfhearing.PCM16LagWindow{Min: 0, Max: time.Second}); !errors.Is(err, selfhearing.ErrInvalidPCM16SelfHearingConfig) {
		t.Fatalf("widen lag = %v, want invalid config", err)
	}
}

func TestPCM16SelfHearingLagRetargetMovesWithinOriginalBoundsAndRetainsEvidence(t *testing.T) {
	detector := newSelfHearingDetector(t, selfhearing.DefaultSelfHearingConfig())
	if err := detector.ObservePlayback(selfhearing.PCM16TimedFrame{Samples: testSignal(100, 409), SampleRate: 1000, Start: 0}); err != nil {
		t.Fatal(err)
	}
	first := selfhearing.PCM16LagWindow{Min: -30 * time.Millisecond, Max: 30 * time.Millisecond}
	if err := detector.RestrictCorrelationLagWindow(first); err != nil {
		t.Fatalf("restrict first lag: %v", err)
	}
	second := selfhearing.PCM16LagWindow{Min: 90 * time.Millisecond, Max: 150 * time.Millisecond}
	// This is the exact legacy clamp that terminated test14: the second
	// response's disjoint lag was intersected with the probe's already-narrowed
	// first-response window, turning 90ms..150ms into 90ms..30ms.
	legacySecond := second
	current := detector.Config().CorrelationLagWindow
	if legacySecond.Min < current.Min {
		legacySecond.Min = current.Min
	}
	if legacySecond.Max > current.Max {
		legacySecond.Max = current.Max
	}
	legacyErr := detector.RestrictCorrelationLagWindow(legacySecond)
	if !errors.Is(legacyErr, selfhearing.ErrInvalidPCM16SelfHearingConfig) || !strings.Contains(legacyErr.Error(), "correlation_lag_window: minimum must not exceed maximum") {
		t.Fatalf("legacy disjoint-lag clamp error = %v, want test14 terminal failure", legacyErr)
	}
	if err := detector.RetargetCorrelationLagWindow(second); err != nil {
		t.Fatalf("retarget disjoint lag inside original bounds: %v", err)
	}
	if detector.Config().CorrelationLagWindow != second || detector.BufferStats().PlaybackSamples != 100 {
		t.Fatalf("retargeted detector config/stats = %+v/%+v", detector.Config(), detector.BufferStats())
	}
	outside := selfhearing.PCM16LagWindow{Min: -time.Second, Max: 0}
	if err := detector.RetargetCorrelationLagWindow(outside); !errors.Is(err, selfhearing.ErrInvalidPCM16SelfHearingConfig) {
		t.Fatalf("retarget outside original bounds = %v, want invalid config", err)
	}
}

func TestPCM16SelfHearingErrorTypesFormatWithAndWithoutNilOrField(t *testing.T) {
	var nilConfigErr *selfhearing.InvalidPCM16SelfHearingConfigError
	if got, want := nilConfigErr.Error(), selfhearing.ErrInvalidPCM16SelfHearingConfig.Error(); got != want {
		t.Fatalf("nil InvalidPCM16SelfHearingConfigError.Error() = %q, want %q", got, want)
	}
	fieldless := &selfhearing.InvalidPCM16SelfHearingConfigError{Reason: "boom"}
	if got := fieldless.Error(); got == "" || got == nilConfigErr.Error() {
		t.Fatalf("fieldless InvalidPCM16SelfHearingConfigError.Error() = %q, want a reason-only message", got)
	}

	var nilFrameErr *selfhearing.PCM16SelfHearingFrameError
	if got, want := nilFrameErr.Error(), selfhearing.ErrInvalidPCM16SelfHearingFrame.Error(); got != want {
		t.Fatalf("nil PCM16SelfHearingFrameError.Error() = %q, want %q", got, want)
	}
	streamless := &selfhearing.PCM16SelfHearingFrameError{Reason: "boom"}
	if got := streamless.Error(); got == "" || got == nilFrameErr.Error() {
		t.Fatalf("streamless PCM16SelfHearingFrameError.Error() = %q, want a reason-only message", got)
	}
}

func TestPCM16TimedFrameEndAddsDurationToStart(t *testing.T) {
	frame := selfhearing.PCM16TimedFrame{Samples: testSignal(20, 93), SampleRate: 1000, Start: 5 * time.Millisecond}
	want := 25 * time.Millisecond // 20 samples @ 1kHz = 20ms, plus the 5ms start offset
	if got := frame.End(); got != want {
		t.Fatalf("End() = %s, want %s", got, want)
	}
}

func TestPCM16SelfHearingObserveCaptureContextGuardsMirrorPlayback(t *testing.T) {
	detector := newSelfHearingDetector(t, selfhearing.DefaultSelfHearingConfig())

	if _, err := detector.ObserveCapture(selfhearing.PCM16TimedFrame{SampleRate: 1000}); !errors.Is(err, selfhearing.ErrInvalidPCM16SelfHearingFrame) {
		t.Fatalf("ObserveCapture(malformed) = %v, want invalid-frame", err)
	}

	frame := selfhearing.PCM16TimedFrame{Samples: testSignal(20, 91), SampleRate: 1000}
	if _, err := detector.ObserveCapture(frame); err != nil {
		t.Fatalf("first ObserveCapture(): %v", err)
	}
	backwards := frame
	backwards.Start = 10 * time.Millisecond
	if _, err := detector.ObserveCapture(backwards); !errors.Is(err, selfhearing.ErrInvalidPCM16SelfHearingFrame) {
		t.Fatalf("backwards ObserveCapture() = %v, want invalid-frame", err)
	}

	if err := detector.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	if _, err := detector.ObserveCapture(frame); !errors.Is(err, contract.ErrClosed) {
		t.Fatalf("ObserveCapture() after Close = %v, want ErrClosed", err)
	}
}

func TestPCM16SelfHearingObserveCaptureContextRejectsOverflowingSampleRate(t *testing.T) {
	config := selfhearing.DefaultSelfHearingConfig()
	config.AnalysisWindow = 5 * time.Second
	config.MinimumEvidence = time.Second
	detector := newSelfHearingDetector(t, config)
	frame := selfhearing.PCM16TimedFrame{Samples: testSignal(20, 95), SampleRate: math.MaxInt32}
	if _, err := detector.ObserveCapture(frame); !errors.Is(err, selfhearing.ErrInvalidPCM16SelfHearingFrame) {
		t.Fatalf("ObserveCapture() with overflowing rate = %v, want invalid-frame", err)
	}
}

func TestPCM16SelfHearingClassifyLockedSkipsWhenCaptureWindowPrecedesPlayback(t *testing.T) {
	detector := newSelfHearingDetector(t, selfhearing.DefaultSelfHearingConfig())
	if _, err := detector.ObserveCapture(selfhearing.PCM16TimedFrame{Samples: testSignal(20, 97), SampleRate: 1000, Start: 0}); err != nil {
		t.Fatalf("first ObserveCapture(): %v", err)
	}
	if err := detector.ObservePlayback(selfhearing.PCM16TimedFrame{Samples: testSignal(20, 99), SampleRate: 1000, Start: 600 * time.Millisecond}); err != nil {
		t.Fatalf("ObservePlayback(): %v", err)
	}
	// The capture window is entirely stale relative to the playback horizon, so
	// classification must bail out before any correlation work.
	observation, err := detector.ObserveCapture(selfhearing.PCM16TimedFrame{Samples: testSignal(20, 101), SampleRate: 1000, Start: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("second ObserveCapture(): %v", err)
	}
	if observation.Classification != selfhearing.PCM16SelfHearingNoEvidence {
		t.Fatalf("classification = %q, want no-evidence when capture activity is entirely stale relative to playback; observation = %+v", observation.Classification, observation)
	}
}

func TestPCM16SelfHearingRoundsBufferBoundUpForNonExactSampleRates(t *testing.T) {
	detector := newSelfHearingDetector(t, selfhearing.DefaultSelfHearingConfig())
	if err := detector.ObservePlayback(selfhearing.PCM16TimedFrame{Samples: testSignal(20, 103), SampleRate: 333, Start: 0}); err != nil {
		t.Fatalf("ObservePlayback(): %v", err)
	}
	if stats := detector.BufferStats(); stats.MaxPlaybackSamples <= 0 {
		t.Fatalf("MaxPlaybackSamples = %d, want a positive rounded-up bound", stats.MaxPlaybackSamples)
	}
}

func newSelfHearingDetector(t *testing.T, config selfhearing.PCM16SelfHearingConfig) *selfhearing.PCM16SelfHearingDetector {
	t.Helper()
	detector, err := selfhearing.NewPCM16SelfHearingDetector(config)
	if err != nil {
		t.Fatalf("NewPCM16SelfHearingDetector(): %v", err)
	}
	t.Cleanup(func() {
		if err := detector.Close(); err != nil {
			t.Errorf("detector.Close(): %v", err)
		}
	})
	return detector
}

func feedPairedSignals(t *testing.T, detector *selfhearing.PCM16SelfHearingDetector, playback, capture []int16, sampleRate, lagSamples int) selfhearing.PCM16SelfHearingObservation {
	t.Helper()
	if len(playback)%20 != 0 || len(capture)%20 != 0 {
		t.Fatalf("test signals must be a multiple of 20 samples: playback=%d capture=%d", len(playback), len(capture))
	}
	for start := 0; start < len(playback); start += 20 {
		if err := detector.ObservePlayback(selfhearing.PCM16TimedFrame{
			Samples:    playback[start : start+20],
			SampleRate: sampleRate,
			Start:      time.Duration(start) * time.Millisecond,
		}); err != nil {
			t.Fatalf("ObservePlayback(%d): %v", start, err)
		}
	}
	var observation selfhearing.PCM16SelfHearingObservation
	for start := 0; start < len(capture); start += 20 {
		var err error
		observation, err = detector.ObserveCapture(selfhearing.PCM16TimedFrame{
			Samples:    capture[start : start+20],
			SampleRate: sampleRate,
			Start:      time.Duration(start+lagSamples) * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("ObserveCapture(%d): %v", start, err)
		}
	}
	return observation
}

func testSignal(length, seed int) []int16 {
	samples := make([]int16, length)
	state := uint32(seed) + 1
	for index := range samples {
		state = state*1664525 + 1013904223
		samples[index] = int16(6000 + ((state >> 16) % 15000))
	}
	return samples
}

func cloneSamples(samples []int16) []int16 { return append([]int16(nil), samples...) }

func invertSamples(samples []int16) []int16 {
	inverted := make([]int16, len(samples))
	for index, sample := range samples {
		inverted[index] = -sample
	}
	return inverted
}
