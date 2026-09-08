package selfhearing

import (
	"context"
	"errors"
	"math"
	"time"
)

func normalizePCM16SelfHearingConfig(config PCM16SelfHearingConfig) (PCM16SelfHearingConfig, time.Duration, error) {
	config = applyPCM16SelfHearingDefaults(config)
	if err := validatePCM16SelfHearingConfig(config); err != nil {
		return PCM16SelfHearingConfig{}, 0, err
	}
	maxBufferDuration, err := pcm16SelfHearingBufferDuration(config)
	if err != nil {
		return PCM16SelfHearingConfig{}, 0, err
	}
	return config, maxBufferDuration, nil
}

func applyPCM16SelfHearingDefaults(config PCM16SelfHearingConfig) PCM16SelfHearingConfig {
	defaults := DefaultPCM16SelfHearingConfig()
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
	return config
}

func validatePCM16SelfHearingConfig(config PCM16SelfHearingConfig) error {
	switch {
	case config.AnalysisWindow <= 0:
		return invalidPCM16SelfHearingConfig("analysis_window", "must be positive")
	case config.MinimumEvidence <= 0 || config.MinimumEvidence > config.AnalysisWindow:
		return invalidPCM16SelfHearingConfig("minimum_evidence", "must be positive and at or below analysis_window")
	case config.CorrelationLagWindow.Min > config.CorrelationLagWindow.Max:
		return invalidPCM16SelfHearingConfig("correlation_lag_window", "min must be at or before max")
	case !isFinite(config.CorrelationThreshold) || config.CorrelationThreshold < 0 || config.CorrelationThreshold > 1:
		return invalidPCM16SelfHearingConfig("correlation_threshold", "must be between 0 and 1")
	case !isFinite(config.SilenceFloorDBFS) || config.SilenceFloorDBFS > 0:
		return invalidPCM16SelfHearingConfig("silence_floor_dbfs", "must be finite and at or below 0")
	case config.MaximumReleaseLatency <= 0:
		return invalidPCM16SelfHearingConfig("maximum_release_latency", "must be positive")
	case config.PostPlaybackAcousticTail <= 0:
		return invalidPCM16SelfHearingConfig("post_playback_acoustic_tail", "must be positive")
	}
	return nil
}

func pcm16SelfHearingBufferDuration(config PCM16SelfHearingConfig) (time.Duration, error) {
	lagMagnitude := config.CorrelationLagWindow.Min
	if lagMagnitude < 0 {
		if lagMagnitude == time.Duration(math.MinInt64) {
			return 0, invalidPCM16SelfHearingConfig("correlation_lag_window.min", "absolute value overflows")
		}
		lagMagnitude = -lagMagnitude
	}
	if max := config.CorrelationLagWindow.Max; max > lagMagnitude {
		lagMagnitude = max
	}
	maxBufferDuration, err := addSelfHearingDuration(config.AnalysisWindow, lagMagnitude)
	if err != nil {
		return 0, invalidPCM16SelfHearingConfig("buffer_duration", "overflows")
	}
	return maxBufferDuration, nil
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
