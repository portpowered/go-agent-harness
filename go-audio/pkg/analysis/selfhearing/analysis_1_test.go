package selfhearing_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	selfhearing "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/selfhearing"
	coreaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/contract"
)

func TestPCM16SelfHearingTopologyOnlyEnablesPairedLiveDevices(t *testing.T) {
	base := selfhearing.PCM16SelfHearingTopology{LiveMicrophone: true, LiveSpeaker: true}
	cases := []struct {
		name     string
		topology selfhearing.PCM16SelfHearingTopology
		want     selfhearing.PCM16SelfHearingPolicy
	}{
		{name: "paired live devices", topology: base, want: selfhearing.PCM16SelfHearingPolicyPairedDevice},
		{name: "no microphone", topology: selfhearing.PCM16SelfHearingTopology{LiveSpeaker: true}, want: selfhearing.PCM16SelfHearingPolicyBypass},
		{name: "no speaker", topology: selfhearing.PCM16SelfHearingTopology{LiveMicrophone: true}, want: selfhearing.PCM16SelfHearingPolicyBypass},
		{name: "file input", topology: selfhearing.PCM16SelfHearingTopology{LiveMicrophone: true, LiveSpeaker: true, FileInput: true}, want: selfhearing.PCM16SelfHearingPolicyBypass},
		{name: "file output", topology: selfhearing.PCM16SelfHearingTopology{LiveMicrophone: true, LiveSpeaker: true, FileOutput: true}, want: selfhearing.PCM16SelfHearingPolicyBypass},
		{name: "replay", topology: selfhearing.PCM16SelfHearingTopology{LiveMicrophone: true, LiveSpeaker: true, Replay: true}, want: selfhearing.PCM16SelfHearingPolicyBypass},
		{name: "room peer ingress", topology: selfhearing.PCM16SelfHearingTopology{LiveMicrophone: true, LiveSpeaker: true, RoomPeerIngress: true}, want: selfhearing.PCM16SelfHearingPolicyBypass},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := selfhearing.ResolvePCM16SelfHearingPolicy(test.topology); got != test.want {
				t.Fatalf("policy = %q, want %q", got, test.want)
			}
			detector, err := selfhearing.NewPCM16SelfHearingDetectorForTopology(test.topology, selfhearing.DefaultSelfHearingConfig())
			if test.want == selfhearing.PCM16SelfHearingPolicyBypass {
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
		mutate func(*selfhearing.PCM16SelfHearingConfig)
	}{
		{name: "non-positive analysis window", field: "analysis_window", mutate: func(config *selfhearing.PCM16SelfHearingConfig) { config.AnalysisWindow = -time.Nanosecond }},
		{name: "minimum evidence exceeds analysis window", field: "minimum_evidence", mutate: func(config *selfhearing.PCM16SelfHearingConfig) {
			config.AnalysisWindow = 100 * time.Millisecond
			config.MinimumEvidence = 101 * time.Millisecond
		}},
		{name: "inverted lag window", field: "correlation_lag_window", mutate: func(config *selfhearing.PCM16SelfHearingConfig) {
			config.CorrelationLagWindow = selfhearing.PCM16LagWindow{Min: time.Millisecond, Max: -time.Millisecond}
		}},
		{name: "negative correlation threshold", field: "correlation_threshold", mutate: func(config *selfhearing.PCM16SelfHearingConfig) { config.CorrelationThreshold = -0.01 }},
		{name: "correlation threshold above one", field: "correlation_threshold", mutate: func(config *selfhearing.PCM16SelfHearingConfig) { config.CorrelationThreshold = 1.01 }},
		{name: "non-finite correlation threshold", field: "correlation_threshold", mutate: func(config *selfhearing.PCM16SelfHearingConfig) { config.CorrelationThreshold = math.NaN() }},
		{name: "positive silence floor", field: "silence_floor_dbfs", mutate: func(config *selfhearing.PCM16SelfHearingConfig) { config.SilenceFloorDBFS = 0.01 }},
		{name: "non-finite silence floor", field: "silence_floor_dbfs", mutate: func(config *selfhearing.PCM16SelfHearingConfig) { config.SilenceFloorDBFS = math.Inf(1) }},
		{name: "non-positive release latency", field: "maximum_release_latency", mutate: func(config *selfhearing.PCM16SelfHearingConfig) { config.MaximumReleaseLatency = -time.Nanosecond }},
		{name: "non-positive acoustic tail", field: "post_playback_acoustic_tail", mutate: func(config *selfhearing.PCM16SelfHearingConfig) { config.PostPlaybackAcousticTail = -time.Nanosecond }},
		{name: "buffer duration overflow", field: "buffer_duration", mutate: func(config *selfhearing.PCM16SelfHearingConfig) {
			config.AnalysisWindow = time.Duration(math.MaxInt64)
			config.MinimumEvidence = time.Nanosecond
			config.CorrelationLagWindow = selfhearing.PCM16LagWindow{Max: time.Nanosecond}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := selfhearing.DefaultSelfHearingConfig()
			test.mutate(&config)
			detector, err := selfhearing.NewPCM16SelfHearingDetector(config)
			if detector != nil {
				t.Fatalf("detector = %v, want nil", detector)
			}
			if !errors.Is(err, selfhearing.ErrInvalidPCM16SelfHearingConfig) {
				t.Fatalf("error = %v, want ErrInvalidPCM16SelfHearingConfig", err)
			}
			var typed *selfhearing.InvalidPCM16SelfHearingConfigError
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
			detector := newSelfHearingDetector(t, selfhearing.DefaultSelfHearingConfig())
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
			if observation.EvidenceDuration < selfhearing.DefaultSelfHearingConfig().MinimumEvidence {
				t.Fatalf("evidence duration = %s, want at least %s", observation.EvidenceDuration, selfhearing.DefaultSelfHearingConfig().MinimumEvidence)
			}
		})
	}
}

func TestPCM16SelfHearingDetectsFarFieldDelayedRoomResponse(t *testing.T) {
	detector := newSelfHearingDetector(t, selfhearing.DefaultSelfHearingConfig())
	playback := testSignal(800, 117)
	capture := make([]int16, len(playback))
	for index := range playback {
		value := 0.52 * float64(playback[index])
		if index >= 3 {
			value += 0.21 * float64(playback[index-3])
		}
		if index >= 11 {
			value -= 0.09 * float64(playback[index-11])
		}
		capture[index] = int16(math.Round(value))
	}

	observation := feedPairedSignals(t, detector, playback, capture, 1000, 240)
	if !observation.Confirmed() {
		t.Fatalf("far-field observation = %+v, want confirmed self-hearing", observation)
	}
	if observation.Measurement.BestAbsoluteLag < 220*time.Millisecond || observation.Measurement.BestAbsoluteLag > 260*time.Millisecond {
		t.Fatalf("far-field absolute lag = %s, want near 240ms", observation.Measurement.BestAbsoluteLag)
	}
}

func TestPCM16SelfHearingDetectsContinuousClockedFarFieldDelay(t *testing.T) {
	detector := newSelfHearingDetector(t, selfhearing.DefaultSelfHearingConfig())
	playback := testSignal(800, 211)
	capture := append(make([]int16, 240), playback[:len(playback)-240]...)
	var observation selfhearing.PCM16SelfHearingObservation
	for start := 0; start < len(playback); start += 20 {
		if err := detector.ObservePlayback(selfhearing.PCM16TimedFrame{Samples: playback[start : start+20], SampleRate: 1000, Start: time.Duration(start) * time.Millisecond}); err != nil {
			t.Fatalf("ObservePlayback(%d): %v", start, err)
		}
		var err error
		observation, err = detector.ObserveCapture(selfhearing.PCM16TimedFrame{Samples: capture[start : start+20], SampleRate: 1000, Start: time.Duration(start) * time.Millisecond})
		if err != nil {
			t.Fatalf("ObserveCapture(%d): %v", start, err)
		}
	}
	if !observation.Confirmed() || observation.Measurement.BestAbsoluteLag < 220*time.Millisecond || observation.Measurement.BestAbsoluteLag > 260*time.Millisecond {
		t.Fatalf("continuous far-field observation = %+v, want confirmed near +240ms", observation)
	}
}

func TestPCM16SelfHearingDetectsFarFieldAfterSilentCaptureResets(t *testing.T) {
	detector := newSelfHearingDetector(t, selfhearing.DefaultSelfHearingConfig())
	const rate, frameSamples, delayedFrames = 16000, coreaudio.FrameSize, 8
	playback := testSignal(24*frameSamples, 313)
	var observation selfhearing.PCM16SelfHearingObservation
	for frameIndex := 0; frameIndex < 24; frameIndex++ {
		start := time.Duration(frameIndex*frameSamples) * time.Second / rate
		frame := playback[frameIndex*frameSamples : (frameIndex+1)*frameSamples]
		if err := detector.ObservePlayback(selfhearing.PCM16TimedFrame{Samples: frame, SampleRate: rate, Start: start}); err != nil {
			t.Fatalf("ObservePlayback(%d): %v", frameIndex, err)
		}
		capture := make([]int16, frameSamples)
		if frameIndex >= delayedFrames {
			copy(capture, playback[(frameIndex-delayedFrames)*frameSamples:(frameIndex-delayedFrames+1)*frameSamples])
		}
		var err error
		observation, err = detector.ObserveCapture(selfhearing.PCM16TimedFrame{Samples: capture, SampleRate: rate, Start: start})
		if err != nil {
			t.Fatalf("ObserveCapture(%d): %v", frameIndex, err)
		}
		if observation.Classification == selfhearing.PCM16SelfHearingNoEvidence {
			detector.ResetCapture()
		}
	}
	if !observation.Confirmed() {
		t.Fatalf("reset far-field observation = %+v, want confirmed", observation)
	}
}

func TestPCM16SelfHearingClassifiesIndependentSpeechAndSilence(t *testing.T) {
	t.Run("independent speech is non-feedback", func(t *testing.T) {
		detector := newSelfHearingDetector(t, selfhearing.DefaultSelfHearingConfig())
		observation := feedPairedSignals(t, detector, testSignal(160, 19), testSignal(160, 71), 1000, 30)
		if observation.Classification != selfhearing.PCM16SelfHearingNonFeedback {
			t.Fatalf("classification = %q, want non-feedback; observation = %+v", observation.Classification, observation)
		}
		if observation.Measurement.BestAbsoluteCorrelation >= selfhearing.DefaultSelfHearingConfig().CorrelationThreshold {
			t.Fatalf("absolute correlation = %f, want below threshold %f", observation.Measurement.BestAbsoluteCorrelation, selfhearing.DefaultSelfHearingConfig().CorrelationThreshold)
		}
	})

	t.Run("digital silence has no evidence", func(t *testing.T) {
		detector := newSelfHearingDetector(t, selfhearing.DefaultSelfHearingConfig())
		observation := feedPairedSignals(t, detector, make([]int16, 160), make([]int16, 160), 1000, 30)
		if observation.Classification != selfhearing.PCM16SelfHearingNoEvidence {
			t.Fatalf("classification = %q, want no-evidence; observation = %+v", observation.Classification, observation)
		}
	})

	t.Run("too-short evidence is insufficient", func(t *testing.T) {
		config := selfhearing.DefaultSelfHearingConfig()
		config.AnalysisWindow = 100 * time.Millisecond
		config.MinimumEvidence = 80 * time.Millisecond
		detector := newSelfHearingDetector(t, config)
		observation := feedPairedSignals(t, detector, testSignal(60, 23), testSignal(60, 23), 1000, 0)
		if observation.Classification != selfhearing.PCM16SelfHearingInsufficientEvidence {
			t.Fatalf("classification = %q, want insufficient-evidence; observation = %+v", observation.Classification, observation)
		}
	})
}

func TestPCM16SelfHearingUsesInclusiveThresholdBoundary(t *testing.T) {
	config := selfhearing.DefaultSelfHearingConfig()
	config.CorrelationThreshold = 1
	detector := newSelfHearingDetector(t, config)
	observation := feedPairedSignals(t, detector, testSignal(160, 29), testSignal(160, 29), 1000, 0)
	if !observation.Confirmed() {
		t.Fatalf("observation = %+v, want a perfect correlation at inclusive threshold 1", observation)
	}
}

func TestPCM16SelfHearingRejectsMismatchedRatesWithoutComparingBytes(t *testing.T) {
	detector := newSelfHearingDetector(t, selfhearing.DefaultSelfHearingConfig())
	for start := 0; start < 160; start += 20 {
		if err := detector.ObservePlayback(selfhearing.PCM16TimedFrame{
			Samples:    testSignal(20, 31+start),
			SampleRate: 1000,
			Start:      time.Duration(start) * time.Millisecond,
		}); err != nil {
			t.Fatalf("ObservePlayback(%d): %v", start, err)
		}
	}
	var observation selfhearing.PCM16SelfHearingObservation
	for start := 0; start < 320; start += 40 {
		var err error
		observation, err = detector.ObserveCapture(selfhearing.PCM16TimedFrame{
			Samples:    testSignal(40, 97+start),
			SampleRate: 2000,
			Start:      time.Duration(start/2) * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("ObserveCapture(%d): %v", start, err)
		}
	}
	if observation.Classification != selfhearing.PCM16SelfHearingRateMismatch {
		t.Fatalf("classification = %q, want rate-mismatch; observation = %+v", observation.Classification, observation)
	}
	if observation.Measurement.HasEvidence() {
		t.Fatalf("mismatched-rate observation carried correlation evidence: %+v", observation.Measurement)
	}
}

func TestPCM16SelfHearingCancellationCopyAndCloseReleaseState(t *testing.T) {
	detector := newSelfHearingDetector(t, selfhearing.DefaultSelfHearingConfig())
	frame := selfhearing.PCM16TimedFrame{Samples: testSignal(20, 41), SampleRate: 1000}
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
	if err := detector.ObservePlayback(selfhearing.PCM16TimedFrame{Samples: copyFrame, SampleRate: 1000, Start: 0}); err != nil {
		t.Fatalf("ObservePlayback(): %v", err)
	}
	copyFrame[0] = -32768
	for start := 20; start < len(playback); start += 20 {
		if err := detector.ObservePlayback(selfhearing.PCM16TimedFrame{
			Samples:    playback[start : start+20],
			SampleRate: 1000,
			Start:      time.Duration(start) * time.Millisecond,
		}); err != nil {
			t.Fatalf("ObservePlayback(%d): %v", start, err)
		}
	}
	var observation selfhearing.PCM16SelfHearingObservation
	for start := 0; start < len(playback); start += 20 {
		var err error
		observation, err = detector.ObserveCapture(selfhearing.PCM16TimedFrame{
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
	if err := detector.ObservePlayback(frame); !errors.Is(err, contract.ErrClosed) {
		t.Fatalf("ObservePlayback() after Close = %v, want ErrClosed", err)
	}
}

func TestPCM16SelfHearingRejectsBackwardsMediaPositions(t *testing.T) {
	detector := newSelfHearingDetector(t, selfhearing.DefaultSelfHearingConfig())
	frame := selfhearing.PCM16TimedFrame{Samples: testSignal(20, 53), SampleRate: 1000}
	if err := detector.ObservePlayback(frame); err != nil {
		t.Fatalf("first ObservePlayback(): %v", err)
	}
	frame.Start = 10 * time.Millisecond
	err := detector.ObservePlayback(frame)
	if !errors.Is(err, selfhearing.ErrInvalidPCM16SelfHearingFrame) {
		t.Fatalf("backwards ObservePlayback() = %v, want invalid-frame", err)
	}
}

func TestPCM16SelfHearingStorageRemainsBoundedForLargeFrames(t *testing.T) {
	config := selfhearing.DefaultSelfHearingConfig()
	config.AnalysisWindow = 120 * time.Millisecond
	config.CorrelationLagWindow = selfhearing.PCM16LagWindow{Min: -time.Millisecond, Max: time.Millisecond}
	detector := newSelfHearingDetector(t, config)
	large := testSignal(2000, 59)
	if err := detector.ObservePlayback(selfhearing.PCM16TimedFrame{Samples: large, SampleRate: 1000}); err != nil {
		t.Fatalf("large ObservePlayback(): %v", err)
	}
	if _, err := detector.ObserveCapture(selfhearing.PCM16TimedFrame{Samples: large, SampleRate: 1000}); err != nil {
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

func TestPCM16SelfHearingControllerAliasesMatchDetectorConstructors(t *testing.T) {
	config := selfhearing.DefaultSelfHearingConfig()
	controller, err := selfhearing.NewPCM16SelfHearingController(config)
	if err != nil || controller == nil {
		t.Fatalf("NewPCM16SelfHearingController() = (%v, %v), want a detector", controller, err)
	}
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("controller.Close(): %v", err)
		}
	})

	invalid := config
	invalid.AnalysisWindow = -time.Nanosecond
	if _, err := selfhearing.NewPCM16SelfHearingController(invalid); !errors.Is(err, selfhearing.ErrInvalidPCM16SelfHearingConfig) {
		t.Fatalf("NewPCM16SelfHearingController(invalid) error = %v, want ErrInvalidPCM16SelfHearingConfig", err)
	}

	paired := selfhearing.PCM16SelfHearingTopology{LiveMicrophone: true, LiveSpeaker: true}
	if !paired.EnablesPCM16SelfHearing() {
		t.Fatalf("paired topology EnablesPCM16SelfHearing() = false, want true")
	}
	pairedController, err := selfhearing.NewPCM16SelfHearingControllerForTopology(paired, config)
	if err != nil || pairedController == nil {
		t.Fatalf("NewPCM16SelfHearingControllerForTopology(paired) = (%v, %v), want a detector", pairedController, err)
	}
	t.Cleanup(func() {
		if err := pairedController.Close(); err != nil {
			t.Errorf("pairedController.Close(): %v", err)
		}
	})

	bypass := selfhearing.PCM16SelfHearingTopology{LiveMicrophone: true, LiveSpeaker: true, RoomPeerIngress: true}
	if bypass.EnablesPCM16SelfHearing() {
		t.Fatalf("bypass topology EnablesPCM16SelfHearing() = true, want false")
	}
	bypassController, err := selfhearing.NewPCM16SelfHearingControllerForTopology(bypass, config)
	if err != nil || bypassController != nil {
		t.Fatalf("NewPCM16SelfHearingControllerForTopology(bypass) = (%v, %v), want (nil, nil)", bypassController, err)
	}
}

func TestPCM16SelfHearingNilDetectorIsInert(t *testing.T) {
	var detector *selfhearing.PCM16SelfHearingDetector
	if got := detector.Config(); got != (selfhearing.PCM16SelfHearingConfig{}) {
		t.Fatalf("nil Config() = %+v, want zero value", got)
	}
	if got := detector.MaxBufferDuration(); got != 0 {
		t.Fatalf("nil MaxBufferDuration() = %s, want 0", got)
	}
	if got := detector.BufferStats(); got != (selfhearing.PCM16SelfHearingBufferStats{}) {
		t.Fatalf("nil BufferStats() = %+v, want zero value", got)
	}
	detector.ResetCapture() // must not panic on a nil receiver
	if err := detector.Close(); err != nil {
		t.Fatalf("nil Close() = %v, want nil", err)
	}
	frame := selfhearing.PCM16TimedFrame{Samples: testSignal(20, 63), SampleRate: 1000}
	if err := detector.ObservePlaybackContext(context.Background(), frame); !errors.Is(err, contract.ErrClosed) {
		t.Fatalf("nil ObservePlaybackContext() = %v, want ErrClosed", err)
	}
	if _, err := detector.ObserveCaptureContext(context.Background(), frame); !errors.Is(err, contract.ErrClosed) {
		t.Fatalf("nil ObserveCaptureContext() = %v, want ErrClosed", err)
	}
}

func TestPCM16SelfHearingConfigAndMaxBufferDurationReflectNormalizedPolicy(t *testing.T) {
	config := selfhearing.DefaultSelfHearingConfig()
	config.AnalysisWindow = 150 * time.Millisecond
	config.MinimumEvidence = 90 * time.Millisecond
	config.CorrelationLagWindow = selfhearing.PCM16LagWindow{Min: -40 * time.Millisecond, Max: 60 * time.Millisecond}
	detector := newSelfHearingDetector(t, config)

	got := detector.Config()
	if got.AnalysisWindow != config.AnalysisWindow || got.MinimumEvidence != config.MinimumEvidence || got.CorrelationLagWindow != config.CorrelationLagWindow {
		t.Fatalf("Config() = %+v, want the constructed policy %+v", got, config)
	}

	want := config.AnalysisWindow + 60*time.Millisecond // buffer bound covers the largest lag magnitude
	if gotBound := detector.MaxBufferDuration(); gotBound != want {
		t.Fatalf("MaxBufferDuration() = %s, want %s", gotBound, want)
	}
}

func TestPCM16SelfHearingResetCaptureDropsCaptureOnly(t *testing.T) {
	detector := newSelfHearingDetector(t, selfhearing.DefaultSelfHearingConfig())
	if err := detector.ObservePlayback(selfhearing.PCM16TimedFrame{Samples: testSignal(20, 71), SampleRate: 1000, Start: 0}); err != nil {
		t.Fatalf("ObservePlayback(): %v", err)
	}
	if _, err := detector.ObserveCapture(selfhearing.PCM16TimedFrame{Samples: testSignal(20, 73), SampleRate: 1000, Start: 0}); err != nil {
		t.Fatalf("ObserveCapture(): %v", err)
	}
	before := detector.BufferStats()
	if before.CaptureSamples == 0 || before.PlaybackSamples == 0 {
		t.Fatalf("seed observations did not populate both buffers: %+v", before)
	}

	detector.ResetCapture()

	after := detector.BufferStats()
	if after.CaptureSamples != 0 {
		t.Fatalf("capture samples after ResetCapture() = %d, want 0", after.CaptureSamples)
	}
	if after.PlaybackSamples != before.PlaybackSamples {
		t.Fatalf("playback samples after ResetCapture() = %d, want unchanged %d", after.PlaybackSamples, before.PlaybackSamples)
	}
}

func TestPCM16SelfHearingResetCaptureAfterCloseIsNoop(t *testing.T) {
	detector := newSelfHearingDetector(t, selfhearing.DefaultSelfHearingConfig())
	if _, err := detector.ObserveCapture(selfhearing.PCM16TimedFrame{Samples: testSignal(20, 67), SampleRate: 1000}); err != nil {
		t.Fatalf("seed ObserveCapture(): %v", err)
	}
	if err := detector.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	detector.ResetCapture() // must remain a no-op once closed

	if stats := detector.BufferStats(); stats.CaptureSamples != 0 {
		t.Fatalf("closed detector capture samples = %d, want 0", stats.CaptureSamples)
	}
}

func TestPCM16SelfHearingDiscontinuousPlaybackResetsCapture(t *testing.T) {
	detector := newSelfHearingDetector(t, selfhearing.DefaultSelfHearingConfig())
	if _, err := detector.ObserveCapture(selfhearing.PCM16TimedFrame{Samples: testSignal(20, 5), SampleRate: 1000, Start: 0}); err != nil {
		t.Fatalf("seed ObserveCapture(): %v", err)
	}
	if stats := detector.BufferStats(); stats.CaptureSamples == 0 {
		t.Fatalf("capture seed did not populate the buffer")
	}
	if err := detector.ObservePlayback(selfhearing.PCM16TimedFrame{Samples: testSignal(20, 7), SampleRate: 1000, Start: 0}); err != nil {
		t.Fatalf("first ObservePlayback(): %v", err)
	}
	// A large forward gap makes the playback stream discontinuous, which must
	// drop stale capture evidence gathered before the gap.
	if err := detector.ObservePlayback(selfhearing.PCM16TimedFrame{Samples: testSignal(20, 9), SampleRate: 1000, Start: time.Second}); err != nil {
		t.Fatalf("gapped ObservePlayback(): %v", err)
	}
	if stats := detector.BufferStats(); stats.CaptureSamples != 0 {
		t.Fatalf("capture samples after discontinuous playback = %d, want reset to 0", stats.CaptureSamples)
	}
}
