package audio_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	audio "github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
)

func TestPCM16SelfHearingTopologyOnlyEnablesPairedLiveDevices(t *testing.T) {
	base := audio.PCM16SelfHearingTopology{LiveMicrophone: true, LiveSpeaker: true}
	cases := []struct {
		name     string
		topology audio.PCM16SelfHearingTopology
		want     audio.PCM16SelfHearingPolicy
	}{
		{name: "paired live devices", topology: base, want: audio.PCM16SelfHearingPolicyPairedDevice},
		{name: "no microphone", topology: audio.PCM16SelfHearingTopology{LiveSpeaker: true}, want: audio.PCM16SelfHearingPolicyBypass},
		{name: "no speaker", topology: audio.PCM16SelfHearingTopology{LiveMicrophone: true}, want: audio.PCM16SelfHearingPolicyBypass},
		{name: "file input", topology: audio.PCM16SelfHearingTopology{LiveMicrophone: true, LiveSpeaker: true, FileInput: true}, want: audio.PCM16SelfHearingPolicyBypass},
		{name: "file output", topology: audio.PCM16SelfHearingTopology{LiveMicrophone: true, LiveSpeaker: true, FileOutput: true}, want: audio.PCM16SelfHearingPolicyBypass},
		{name: "replay", topology: audio.PCM16SelfHearingTopology{LiveMicrophone: true, LiveSpeaker: true, Replay: true}, want: audio.PCM16SelfHearingPolicyBypass},
		{name: "room peer ingress", topology: audio.PCM16SelfHearingTopology{LiveMicrophone: true, LiveSpeaker: true, RoomPeerIngress: true}, want: audio.PCM16SelfHearingPolicyBypass},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := audio.ResolvePCM16SelfHearingPolicy(test.topology); got != test.want {
				t.Fatalf("policy = %q, want %q", got, test.want)
			}
			detector, err := audio.NewPCM16SelfHearingDetectorForTopology(test.topology, audio.DefaultSelfHearingConfig())
			if test.want == audio.PCM16SelfHearingPolicyBypass {
				if err != nil || detector != nil {
					t.Fatalf("bypass constructor = (%v, %v), want (nil, nil)", detector, err)
				}
				return
			}
			if err != nil || detector == nil {
				t.Fatalf("paired constructor = (%v, %v), want a detector", detector, err)
			}
		})
	}
}

func TestPCM16SelfHearingRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		field  string
		mutate func(*audio.PCM16SelfHearingConfig)
	}{
		{name: "non-positive analysis window", field: "analysis_window", mutate: func(config *audio.PCM16SelfHearingConfig) { config.AnalysisWindow = -time.Nanosecond }},
		{name: "minimum evidence exceeds analysis window", field: "minimum_evidence", mutate: func(config *audio.PCM16SelfHearingConfig) {
			config.AnalysisWindow = 100 * time.Millisecond
			config.MinimumEvidence = 101 * time.Millisecond
		}},
		{name: "inverted lag window", field: "correlation_lag_window", mutate: func(config *audio.PCM16SelfHearingConfig) {
			config.CorrelationLagWindow = audio.PCM16LagWindow{Min: time.Millisecond, Max: -time.Millisecond}
		}},
		{name: "negative correlation threshold", field: "correlation_threshold", mutate: func(config *audio.PCM16SelfHearingConfig) { config.CorrelationThreshold = -0.01 }},
		{name: "correlation threshold above one", field: "correlation_threshold", mutate: func(config *audio.PCM16SelfHearingConfig) { config.CorrelationThreshold = 1.01 }},
		{name: "non-finite correlation threshold", field: "correlation_threshold", mutate: func(config *audio.PCM16SelfHearingConfig) { config.CorrelationThreshold = math.NaN() }},
		{name: "positive silence floor", field: "silence_floor_dbfs", mutate: func(config *audio.PCM16SelfHearingConfig) { config.SilenceFloorDBFS = 0.01 }},
		{name: "non-finite silence floor", field: "silence_floor_dbfs", mutate: func(config *audio.PCM16SelfHearingConfig) { config.SilenceFloorDBFS = math.Inf(1) }},
		{name: "non-positive release latency", field: "maximum_release_latency", mutate: func(config *audio.PCM16SelfHearingConfig) { config.MaximumReleaseLatency = -time.Nanosecond }},
		{name: "non-positive acoustic tail", field: "post_playback_acoustic_tail", mutate: func(config *audio.PCM16SelfHearingConfig) { config.PostPlaybackAcousticTail = -time.Nanosecond }},
		{name: "buffer duration overflow", field: "buffer_duration", mutate: func(config *audio.PCM16SelfHearingConfig) {
			config.AnalysisWindow = time.Duration(math.MaxInt64)
			config.MinimumEvidence = time.Nanosecond
			config.CorrelationLagWindow = audio.PCM16LagWindow{Max: time.Nanosecond}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := audio.DefaultSelfHearingConfig()
			test.mutate(&config)
			detector, err := audio.NewPCM16SelfHearingDetector(config)
			if detector != nil {
				t.Fatalf("detector = %v, want nil", detector)
			}
			if !errors.Is(err, audio.ErrInvalidPCM16SelfHearingConfig) {
				t.Fatalf("error = %v, want ErrInvalidPCM16SelfHearingConfig", err)
			}
			var typed *audio.InvalidPCM16SelfHearingConfigError
			if !errors.As(err, &typed) {
				t.Fatalf("error = %v, want InvalidPCM16SelfHearingConfigError", err)
			}
			if typed.Field != test.field {
				t.Fatalf("error field = %q, want %q", typed.Field, test.field)
			}
		})
	}
}

func TestPCM16SelfHearingDetectsLaggedAndInvertedPlayback(t *testing.T) {
	for _, test := range []struct {
		name      string
		transform func([]int16) []int16
	}{
		{name: "lagged copy", transform: cloneSamples},
		{name: "lagged inverted copy", transform: invertSamples},
	} {
		t.Run(test.name, func(t *testing.T) {
			detector := newSelfHearingDetector(t, audio.DefaultSelfHearingConfig())
			playback := testSignal(160, 17)
			capture := test.transform(playback)
			observation := feedPairedSignals(t, detector, playback, capture, 1000, 30)
			if !observation.Confirmed() {
				t.Fatalf("observation = %+v, want confirmed self-hearing", observation)
			}
			if observation.Measurement.BestAbsoluteCorrelation < 0.99 {
				t.Fatalf("absolute correlation = %f, want at least 0.99", observation.Measurement.BestAbsoluteCorrelation)
			}
			if observation.Measurement.BestAbsoluteLag != 30*time.Millisecond {
				t.Fatalf("absolute lag = %s, want 30ms", observation.Measurement.BestAbsoluteLag)
			}
			if observation.EvidenceDuration < audio.DefaultSelfHearingConfig().MinimumEvidence {
				t.Fatalf("evidence duration = %s, want at least %s", observation.EvidenceDuration, audio.DefaultSelfHearingConfig().MinimumEvidence)
			}
		})
	}
}

func TestPCM16SelfHearingClassifiesIndependentSpeechAndSilence(t *testing.T) {
	t.Run("independent speech is non-feedback", func(t *testing.T) {
		detector := newSelfHearingDetector(t, audio.DefaultSelfHearingConfig())
		observation := feedPairedSignals(t, detector, testSignal(160, 19), testSignal(160, 71), 1000, 30)
		if observation.Classification != audio.PCM16SelfHearingNonFeedback {
			t.Fatalf("classification = %q, want non-feedback; observation = %+v", observation.Classification, observation)
		}
		if observation.Measurement.BestAbsoluteCorrelation >= audio.DefaultSelfHearingConfig().CorrelationThreshold {
			t.Fatalf("absolute correlation = %f, want below threshold %f", observation.Measurement.BestAbsoluteCorrelation, audio.DefaultSelfHearingConfig().CorrelationThreshold)
		}
	})

	t.Run("digital silence has no evidence", func(t *testing.T) {
		detector := newSelfHearingDetector(t, audio.DefaultSelfHearingConfig())
		observation := feedPairedSignals(t, detector, make([]int16, 160), make([]int16, 160), 1000, 30)
		if observation.Classification != audio.PCM16SelfHearingNoEvidence {
			t.Fatalf("classification = %q, want no-evidence; observation = %+v", observation.Classification, observation)
		}
	})

	t.Run("too-short evidence is insufficient", func(t *testing.T) {
		config := audio.DefaultSelfHearingConfig()
		config.AnalysisWindow = 100 * time.Millisecond
		config.MinimumEvidence = 80 * time.Millisecond
		detector := newSelfHearingDetector(t, config)
		observation := feedPairedSignals(t, detector, testSignal(60, 23), testSignal(60, 23), 1000, 0)
		if observation.Classification != audio.PCM16SelfHearingInsufficientEvidence {
			t.Fatalf("classification = %q, want insufficient-evidence; observation = %+v", observation.Classification, observation)
		}
	})
}

func TestPCM16SelfHearingUsesInclusiveThresholdBoundary(t *testing.T) {
	config := audio.DefaultSelfHearingConfig()
	config.CorrelationThreshold = 1
	detector := newSelfHearingDetector(t, config)
	observation := feedPairedSignals(t, detector, testSignal(160, 29), testSignal(160, 29), 1000, 0)
	if !observation.Confirmed() {
		t.Fatalf("observation = %+v, want a perfect correlation at inclusive threshold 1", observation)
	}
}

func TestPCM16SelfHearingRejectsMismatchedRatesWithoutComparingBytes(t *testing.T) {
	detector := newSelfHearingDetector(t, audio.DefaultSelfHearingConfig())
	for start := 0; start < 160; start += 20 {
		if err := detector.ObservePlayback(audio.PCM16TimedFrame{
			Samples:    testSignal(20, 31+start),
			SampleRate: 1000,
			Start:      time.Duration(start) * time.Millisecond,
		}); err != nil {
			t.Fatalf("ObservePlayback(%d): %v", start, err)
		}
	}
	var observation audio.PCM16SelfHearingObservation
	for start := 0; start < 320; start += 40 {
		var err error
		observation, err = detector.ObserveCapture(audio.PCM16TimedFrame{
			Samples:    testSignal(40, 97+start),
			SampleRate: 2000,
			Start:      time.Duration(start/2) * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("ObserveCapture(%d): %v", start, err)
		}
	}
	if observation.Classification != audio.PCM16SelfHearingRateMismatch {
		t.Fatalf("classification = %q, want rate-mismatch; observation = %+v", observation.Classification, observation)
	}
	if observation.Measurement.HasEvidence() {
		t.Fatalf("mismatched-rate observation carried correlation evidence: %+v", observation.Measurement)
	}
}

func TestPCM16SelfHearingCancellationCopyAndCloseReleaseState(t *testing.T) {
	detector := newSelfHearingDetector(t, audio.DefaultSelfHearingConfig())
	frame := audio.PCM16TimedFrame{Samples: testSignal(20, 41), SampleRate: 1000}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := detector.BufferStats()
	if err := detector.ObservePlaybackContext(ctx, frame); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ObservePlaybackContext() = %v, want context.Canceled", err)
	}
	if got := detector.BufferStats(); got.PlaybackSamples != before.PlaybackSamples || got.CaptureSamples != before.CaptureSamples {
		t.Fatalf("cancelled observation changed buffers: before=%+v after=%+v", before, got)
	}
	if _, err := detector.ObserveCaptureContext(ctx, frame); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ObserveCaptureContext() = %v, want context.Canceled", err)
	}
	if got := detector.BufferStats(); got.PlaybackSamples != before.PlaybackSamples || got.CaptureSamples != before.CaptureSamples {
		t.Fatalf("cancelled capture observation changed buffers: before=%+v after=%+v", before, got)
	}

	playback := testSignal(160, 43)
	copyFrame := append([]int16(nil), playback[:20]...)
	if err := detector.ObservePlayback(audio.PCM16TimedFrame{Samples: copyFrame, SampleRate: 1000, Start: 0}); err != nil {
		t.Fatalf("ObservePlayback(): %v", err)
	}
	copyFrame[0] = -32768
	for start := 20; start < len(playback); start += 20 {
		if err := detector.ObservePlayback(audio.PCM16TimedFrame{
			Samples:    playback[start : start+20],
			SampleRate: 1000,
			Start:      time.Duration(start) * time.Millisecond,
		}); err != nil {
			t.Fatalf("ObservePlayback(%d): %v", start, err)
		}
	}
	var observation audio.PCM16SelfHearingObservation
	for start := 0; start < len(playback); start += 20 {
		var err error
		observation, err = detector.ObserveCapture(audio.PCM16TimedFrame{
			Samples:    playback[start : start+20],
			SampleRate: 1000,
			Start:      time.Duration(start) * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("ObserveCapture(%d): %v", start, err)
		}
	}
	if !observation.Confirmed() {
		t.Fatalf("copied playback observation = %+v, want confirmed self-hearing", observation)
	}

	if err := detector.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	if err := detector.Close(); err != nil {
		t.Fatalf("second Close(): %v", err)
	}
	stats := detector.BufferStats()
	if stats.PlaybackSamples != 0 || stats.CaptureSamples != 0 {
		t.Fatalf("closed detector buffers = %+v, want empty", stats)
	}
	if err := detector.ObservePlayback(frame); !errors.Is(err, audio.ErrClosed) {
		t.Fatalf("ObservePlayback() after Close = %v, want ErrClosed", err)
	}
}

func TestPCM16SelfHearingRejectsBackwardsMediaPositions(t *testing.T) {
	detector := newSelfHearingDetector(t, audio.DefaultSelfHearingConfig())
	frame := audio.PCM16TimedFrame{Samples: testSignal(20, 53), SampleRate: 1000}
	if err := detector.ObservePlayback(frame); err != nil {
		t.Fatalf("first ObservePlayback(): %v", err)
	}
	frame.Start = 10 * time.Millisecond
	err := detector.ObservePlayback(frame)
	if !errors.Is(err, audio.ErrInvalidPCM16SelfHearingFrame) {
		t.Fatalf("backwards ObservePlayback() = %v, want invalid-frame", err)
	}
}

func TestPCM16SelfHearingStorageRemainsBoundedForLargeFrames(t *testing.T) {
	config := audio.DefaultSelfHearingConfig()
	config.AnalysisWindow = 120 * time.Millisecond
	config.CorrelationLagWindow = audio.PCM16LagWindow{Min: -time.Millisecond, Max: time.Millisecond}
	detector := newSelfHearingDetector(t, config)
	large := testSignal(2000, 59)
	if err := detector.ObservePlayback(audio.PCM16TimedFrame{Samples: large, SampleRate: 1000}); err != nil {
		t.Fatalf("large ObservePlayback(): %v", err)
	}
	if _, err := detector.ObserveCapture(audio.PCM16TimedFrame{Samples: large, SampleRate: 1000}); err != nil {
		t.Fatalf("large ObserveCapture(): %v", err)
	}
	stats := detector.BufferStats()
	if stats.MaxPlaybackSamples != 121 || stats.MaxCaptureSamples != 121 {
		t.Fatalf("buffer bounds = %+v, want 121 samples per stream", stats)
	}
	if stats.PlaybackSamples > stats.MaxPlaybackSamples || stats.CaptureSamples > stats.MaxCaptureSamples {
		t.Fatalf("buffered samples exceed bounds: %+v", stats)
	}
}

func newSelfHearingDetector(t *testing.T, config audio.PCM16SelfHearingConfig) *audio.PCM16SelfHearingDetector {
	t.Helper()
	detector, err := audio.NewPCM16SelfHearingDetector(config)
	if err != nil {
		t.Fatalf("NewPCM16SelfHearingDetector(): %v", err)
	}
	t.Cleanup(func() { _ = detector.Close() })
	return detector
}

func feedPairedSignals(t *testing.T, detector *audio.PCM16SelfHearingDetector, playback, capture []int16, sampleRate, lagSamples int) audio.PCM16SelfHearingObservation {
	t.Helper()
	if len(playback)%20 != 0 || len(capture)%20 != 0 {
		t.Fatalf("test signals must be a multiple of 20 samples: playback=%d capture=%d", len(playback), len(capture))
	}
	for start := 0; start < len(playback); start += 20 {
		if err := detector.ObservePlayback(audio.PCM16TimedFrame{
			Samples:    playback[start : start+20],
			SampleRate: sampleRate,
			Start:      time.Duration(start) * time.Millisecond,
		}); err != nil {
			t.Fatalf("ObservePlayback(%d): %v", start, err)
		}
	}
	var observation audio.PCM16SelfHearingObservation
	for start := 0; start < len(capture); start += 20 {
		var err error
		observation, err = detector.ObserveCapture(audio.PCM16TimedFrame{
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
