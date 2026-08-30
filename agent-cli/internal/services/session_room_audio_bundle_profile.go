package services

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"encoding/json"
)

func parseRoomReplayToleranceProfile(manifest roomReplayJSONObject) (RoomReplayToleranceProfile, error) {
	profile := DefaultRoomReplayToleranceProfile()
	raw, present := roomReplayProfileRawField(manifest, "tolerances", "tolerance_profile", "analysis_profile")
	if !present {
		return profile, nil
	}
	object, err := roomReplayObject(raw)
	if err != nil {
		return RoomReplayToleranceProfile{}, fmt.Errorf("%w: profile must be a JSON object: %w", ErrRoomReplayToleranceProfile, err)
	}
	if name, present, nameErr := firstRoomReplayStringField(object, nil, "name", "profile", "id"); nameErr != nil && present {
		return RoomReplayToleranceProfile{}, fmt.Errorf("%w: name: %w", ErrRoomReplayToleranceProfile, nameErr)
	} else if present && strings.TrimSpace(name) != "" {
		profile.Name = strings.TrimSpace(name)
	}

	stream := &profile.StreamConfig
	roomConfig := &profile.RoomConfig
	roomConfig.StreamConfig = profile.StreamConfig
	if err := applyRoomReplayProfileDuration(object, "frame_duration", &stream.FrameDuration, "stream.frame_duration"); err != nil {
		return RoomReplayToleranceProfile{}, err
	}
	if err := applyRoomReplayProfileFloat(object, "silence_floor_dbfs", &stream.SilenceFloorDBFS, "stream.silence_floor_dbfs"); err != nil {
		return RoomReplayToleranceProfile{}, err
	}
	if err := applyRoomReplayProfileDuration(object, "max_natural_pause", &stream.MaxNaturalPause, "stream.max_natural_pause"); err != nil {
		return RoomReplayToleranceProfile{}, err
	}
	if err := applyRoomReplayProfileInt(object, "boundary_delta", &stream.BoundaryDelta, "stream.boundary_delta"); err != nil {
		return RoomReplayToleranceProfile{}, err
	}
	if err := applyRoomReplayProfileFloat(object, "boundary_quiet_dbfs", &stream.BoundaryQuietDBFS, "stream.boundary_quiet_dbfs"); err != nil {
		return RoomReplayToleranceProfile{}, err
	}
	if err := applyRoomReplayProfileInt(object, "clip_sample_threshold", &stream.ClipSampleThreshold, "stream.clip_sample_threshold"); err != nil {
		return RoomReplayToleranceProfile{}, err
	}
	if err := applyRoomReplayProfileInt(object, "edge_sample_threshold", &stream.EdgeSampleThreshold, "stream.edge_sample_threshold"); err != nil {
		return RoomReplayToleranceProfile{}, err
	}
	if err := applyRoomReplayProfileFloat(object, "final_frame_max_rms_dbfs", &stream.FinalFrameMaxRMSDBFS, "stream.final_frame_max_rms_dbfs"); err != nil {
		return RoomReplayToleranceProfile{}, err
	}

	lagRaw, lagPresent := roomReplayProfileRawField(object, "correlation_lag_window", "routing_lag_window")
	if lagPresent {
		lagObject, lagErr := roomReplayObject(lagRaw)
		if lagErr != nil {
			return RoomReplayToleranceProfile{}, fmt.Errorf("%w: correlation_lag_window must be an object: %w", ErrRoomReplayToleranceProfile, lagErr)
		}
		if err := applyRoomReplayProfileDurationFromObject(lagObject, "min", &roomConfig.CorrelationLagWindow.Min, "correlation_lag_window.min"); err != nil {
			return RoomReplayToleranceProfile{}, err
		}
		if err := applyRoomReplayProfileDurationFromObject(lagObject, "max", &roomConfig.CorrelationLagWindow.Max, "correlation_lag_window.max"); err != nil {
			return RoomReplayToleranceProfile{}, err
		}
	}
	if err := applyRoomReplayProfileFloat(object, "correlation_silence_floor_dbfs", &roomConfig.CorrelationSilenceFloorDBFS, "correlation_silence_floor_dbfs"); err != nil {
		return RoomReplayToleranceProfile{}, err
	}
	if err := applyRoomReplayProfileFloat(object, "min_peer_correlation", &roomConfig.MinPeerCorrelation, "min_peer_correlation"); err != nil {
		return RoomReplayToleranceProfile{}, err
	}
	if err := applyRoomReplayProfileFloat(object, "max_self_correlation", &roomConfig.MaxSelfCorrelation, "max_self_correlation"); err != nil {
		return RoomReplayToleranceProfile{}, err
	}
	if err := applyRoomReplayProfileFloat(object, "barge_in_speech_threshold_dbfs", &roomConfig.BargeInSpeechThresholdDBFS, "barge_in_speech_threshold_dbfs"); err != nil {
		return RoomReplayToleranceProfile{}, err
	}
	if err := applyRoomReplayProfileDuration(object, "max_barge_in_latency", &roomConfig.MaxBargeInLatency, "max_barge_in_latency"); err != nil {
		return RoomReplayToleranceProfile{}, err
	}
	if err := applyRoomReplayProfileFloat(object, "max_loudness_difference_db", &roomConfig.MaxLoudnessDifferenceDB, "max_loudness_difference_db"); err != nil {
		return RoomReplayToleranceProfile{}, err
	}
	if err := applyRoomReplayProfileDuration(object, "max_drift_absolute", &roomConfig.MaxDriftAbsolute, "max_drift_absolute"); err != nil {
		return RoomReplayToleranceProfile{}, err
	}
	if err := applyRoomReplayProfileFloat(object, "max_drift_fraction", &roomConfig.MaxDriftFraction, "max_drift_fraction"); err != nil {
		return RoomReplayToleranceProfile{}, err
	}
	roomConfig.StreamConfig = *stream

	if err := validateRoomReplayToleranceTightening(profile); err != nil {
		return RoomReplayToleranceProfile{}, err
	}
	return profile, nil
}

func roomReplayProfileRawField(object roomReplayJSONObject, names ...string) (json.RawMessage, bool) {
	if object == nil {
		return nil, false
	}
	for _, name := range names {
		if raw, ok := object[name]; ok {
			return raw, true
		}
	}
	for _, raw := range object {
		nested, err := roomReplayObject(raw)
		if err != nil {
			continue
		}
		if found, ok := roomReplayProfileRawField(nested, names...); ok {
			return found, true
		}
	}
	return nil, false
}

func applyRoomReplayProfileDuration(object roomReplayJSONObject, name string, destination *time.Duration, field string) error {
	raw, present := roomReplayProfileRawField(object, name)
	if !present {
		return nil
	}
	value, err := roomReplayDurationValue(raw, strings.HasSuffix(name, "_ms"))
	if err != nil {
		return fmt.Errorf("%w: field %q: %w", ErrRoomReplayToleranceProfile, field, err)
	}
	*destination = value
	return nil
}

func applyRoomReplayProfileDurationFromObject(object roomReplayJSONObject, name string, destination *time.Duration, field string) error {
	raw, present := object[name]
	if !present {
		return nil
	}
	value, err := roomReplayDurationValue(raw, false)
	if err != nil {
		return fmt.Errorf("%w: field %q: %w", ErrRoomReplayToleranceProfile, field, err)
	}
	*destination = value
	return nil
}

func applyRoomReplayProfileFloat(object roomReplayJSONObject, name string, destination *float64, field string) error {
	raw, present := roomReplayProfileRawField(object, name)
	if !present {
		return nil
	}
	var value float64
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&value); err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return fmt.Errorf("%w: field %q must be a finite number", ErrRoomReplayToleranceProfile, field)
	}
	*destination = value
	return nil
}

func applyRoomReplayProfileInt(object roomReplayJSONObject, name string, destination *int, field string) error {
	raw, present := roomReplayProfileRawField(object, name)
	if !present {
		return nil
	}
	value, err := roomReplayInt64Raw(raw)
	if err != nil || value < int64(-int(^uint(0)>>1)-1) || value > int64(^uint(0)>>1) {
		return fmt.Errorf("%w: field %q must be an integer", ErrRoomReplayToleranceProfile, field)
	}
	*destination = int(value)
	return nil
}

func roomReplayInt64Raw(raw json.RawMessage) (int64, error) {
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return 0, err
	}
	return strconv.ParseInt(number.String(), 10, 64)
}

func validateRoomReplayToleranceTightening(profile RoomReplayToleranceProfile) error {
	defaults := DefaultRoomReplayToleranceProfile()
	stream := profile.StreamConfig
	baseStream := defaults.StreamConfig
	checks := []struct {
		field  string
		looser bool
	}{
		{"frame_duration", stream.FrameDuration != baseStream.FrameDuration},
		{"silence_floor_dbfs", stream.SilenceFloorDBFS < baseStream.SilenceFloorDBFS},
		{"max_natural_pause", stream.MaxNaturalPause > baseStream.MaxNaturalPause},
		{"boundary_delta", stream.BoundaryDelta > baseStream.BoundaryDelta},
		{"boundary_quiet_dbfs", stream.BoundaryQuietDBFS < baseStream.BoundaryQuietDBFS},
		{"clip_sample_threshold", stream.ClipSampleThreshold > baseStream.ClipSampleThreshold},
		{"edge_sample_threshold", stream.EdgeSampleThreshold > baseStream.EdgeSampleThreshold},
		{"final_frame_max_rms_dbfs", stream.FinalFrameMaxRMSDBFS > baseStream.FinalFrameMaxRMSDBFS},
	}
	for _, check := range checks {
		if check.looser {
			return fmt.Errorf("%w: field %q would loosen suite default", ErrRoomReplayToleranceProfile, check.field)
		}
	}
	roomConfig := profile.RoomConfig
	baseRoom := defaults.RoomConfig
	if roomConfig.CorrelationLagWindow.Min < baseRoom.CorrelationLagWindow.Min || roomConfig.CorrelationLagWindow.Max > baseRoom.CorrelationLagWindow.Max || roomConfig.CorrelationLagWindow.Min > roomConfig.CorrelationLagWindow.Max {
		return fmt.Errorf("%w: correlation_lag_window would widen or invert suite window", ErrRoomReplayToleranceProfile)
	}
	roomChecks := []struct {
		field  string
		looser bool
	}{
		{"correlation_silence_floor_dbfs", roomConfig.CorrelationSilenceFloorDBFS < baseRoom.CorrelationSilenceFloorDBFS},
		{"min_peer_correlation", roomConfig.MinPeerCorrelation < baseRoom.MinPeerCorrelation},
		{"max_self_correlation", roomConfig.MaxSelfCorrelation > baseRoom.MaxSelfCorrelation},
		{"barge_in_speech_threshold_dbfs", roomConfig.BargeInSpeechThresholdDBFS < baseRoom.BargeInSpeechThresholdDBFS},
		{"max_barge_in_latency", roomConfig.MaxBargeInLatency > baseRoom.MaxBargeInLatency},
		{"max_loudness_difference_db", roomConfig.MaxLoudnessDifferenceDB > baseRoom.MaxLoudnessDifferenceDB},
		{"max_drift_absolute", roomConfig.MaxDriftAbsolute > baseRoom.MaxDriftAbsolute},
		{"max_drift_fraction", roomConfig.MaxDriftFraction > baseRoom.MaxDriftFraction},
	}
	for _, check := range roomChecks {
		if check.looser {
			return fmt.Errorf("%w: field %q would loosen suite default", ErrRoomReplayToleranceProfile, check.field)
		}
	}
	return nil
}
