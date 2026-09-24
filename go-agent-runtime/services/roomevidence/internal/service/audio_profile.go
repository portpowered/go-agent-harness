package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	roomanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/room"
	"math"
	"strconv"
	"strings"
	"time"
)

func parseRoomReplayToleranceProfile(manifest roomReplayJSONObject) (ToleranceProfile, error) {
	profile := DefaultRoomReplayToleranceProfile()
	raw, present := roomReplayProfileRawField(manifest, "tolerances", "tolerance_profile", "analysis_profile")
	if !present {
		return profile, nil
	}
	object, err := roomReplayObject(raw)
	if err != nil {
		return ToleranceProfile{}, fmt.Errorf("%w: profile must be a JSON object: %w", ErrRoomReplayToleranceProfile, err)
	}
	if err := applyRoomReplayProfileName(&profile, object); err != nil {
		return ToleranceProfile{}, err
	}
	if err := applyRoomReplayStreamProfile(&profile, object); err != nil {
		return ToleranceProfile{}, err
	}
	if err := applyRoomReplayRoomProfile(&profile, object); err != nil {
		return ToleranceProfile{}, err
	}

	if err := validateRoomReplayToleranceTightening(profile); err != nil {
		return ToleranceProfile{}, err
	}
	return profile, nil
}

func applyRoomReplayProfileName(profile *ToleranceProfile, object roomReplayJSONObject) error {
	name, present, err := firstRoomReplayStringField(object, nil, "name", "profile", "id")
	if err != nil && present {
		return fmt.Errorf("%w: name: %w", ErrRoomReplayToleranceProfile, err)
	}
	if present && strings.TrimSpace(name) != "" {
		profile.Name = strings.TrimSpace(name)
	}
	return nil
}

func applyRoomReplayStreamProfile(profile *ToleranceProfile, object roomReplayJSONObject) error {
	stream := &profile.StreamConfig
	checks := []func() error{
		func() error {
			return applyRoomReplayProfileDuration(object, "frame_duration", &stream.FrameDuration, "stream.frame_duration")
		},
		func() error {
			return applyRoomReplayProfileFloat(object, "silence_floor_dbfs", &stream.SilenceFloorDBFS, "stream.silence_floor_dbfs")
		},
		func() error {
			return applyRoomReplayProfileDuration(object, "max_natural_pause", &stream.MaxNaturalPause, "stream.max_natural_pause")
		},
		func() error {
			return applyRoomReplayProfileInt(object, "boundary_delta", &stream.BoundaryDelta, "stream.boundary_delta")
		},
		func() error {
			return applyRoomReplayProfileFloat(object, "boundary_quiet_dbfs", &stream.BoundaryQuietDBFS, "stream.boundary_quiet_dbfs")
		},
		func() error {
			return applyRoomReplayProfileInt(object, "clip_sample_threshold", &stream.ClipSampleThreshold, "stream.clip_sample_threshold")
		},
		func() error {
			return applyRoomReplayProfileInt(object, "edge_sample_threshold", &stream.EdgeSampleThreshold, "stream.edge_sample_threshold")
		},
		func() error {
			return applyRoomReplayProfileFloat(object, "final_frame_max_rms_dbfs", &stream.FinalFrameMaxRMSDBFS, "stream.final_frame_max_rms_dbfs")
		},
	}
	for _, check := range checks {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

func applyRoomReplayRoomProfile(profile *ToleranceProfile, object roomReplayJSONObject) error {
	roomConfig := &profile.RoomConfig
	roomConfig.StreamConfig = profile.StreamConfig
	if err := applyRoomReplayCorrelationWindow(roomConfig, object); err != nil {
		return err
	}
	checks := []func() error{
		func() error {
			return applyRoomReplayProfileFloat(object, "correlation_silence_floor_dbfs", &roomConfig.CorrelationSilenceFloorDBFS, "correlation_silence_floor_dbfs")
		},
		func() error {
			return applyRoomReplayProfileFloat(object, "min_peer_correlation", &roomConfig.MinPeerCorrelation, "min_peer_correlation")
		},
		func() error {
			return applyRoomReplayProfileFloat(object, "max_self_correlation", &roomConfig.MaxSelfCorrelation, "max_self_correlation")
		},
		func() error {
			return applyRoomReplayProfileFloat(object, "barge_in_speech_threshold_dbfs", &roomConfig.BargeInSpeechThresholdDBFS, "barge_in_speech_threshold_dbfs")
		},
		func() error {
			return applyRoomReplayProfileDuration(object, "max_barge_in_latency", &roomConfig.MaxBargeInLatency, "max_barge_in_latency")
		},
		func() error {
			return applyRoomReplayProfileFloat(object, "max_loudness_difference_db", &roomConfig.MaxLoudnessDifferenceDB, "max_loudness_difference_db")
		},
		func() error {
			return applyRoomReplayProfileDuration(object, "max_drift_absolute", &roomConfig.MaxDriftAbsolute, "max_drift_absolute")
		},
		func() error {
			return applyRoomReplayProfileFloat(object, "max_drift_fraction", &roomConfig.MaxDriftFraction, "max_drift_fraction")
		},
	}
	for _, check := range checks {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

func applyRoomReplayCorrelationWindow(roomConfig *roomanalysis.PCM16RoomAnalysisConfig, object roomReplayJSONObject) error {
	lagRaw, present := roomReplayProfileRawField(object, "correlation_lag_window", "routing_lag_window")
	if !present {
		return nil
	}
	lagObject, err := roomReplayObject(lagRaw)
	if err != nil {
		return fmt.Errorf("%w: correlation_lag_window must be an object: %w", ErrRoomReplayToleranceProfile, err)
	}
	if err := applyRoomReplayProfileDurationFromObject(lagObject, "min", &roomConfig.CorrelationLagWindow.Min, "correlation_lag_window.min"); err != nil {
		return err
	}
	return applyRoomReplayProfileDurationFromObject(lagObject, "max", &roomConfig.CorrelationLagWindow.Max, "correlation_lag_window.max")
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

func validateRoomReplayToleranceTightening(profile ToleranceProfile) error {
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

func validateRoomReplayDeltaStream(stream AudioStream, plan RoomReplayPlan, participantID string) error {
	if len(stream.Deltas) == 0 {
		return &DeltaReconstructionError{ParticipantID: participantID, StreamID: stream.StreamID, DeltaID: "missing", DeltaIndex: 0, ByteOffset: 0, ExpectedLength: len(stream.PCM), ActualLength: 0, ExpectedSampleCount: stream.SampleCount, ActualSampleCount: 0, Cause: ErrRoomReplayDeltaReconstruction}
	}
	previousOffset := time.Duration(-1)
	for _, delta := range stream.Deltas {
		if delta.HasOffset {
			if delta.Offset < 0 || delta.Offset > plan.EndedAt.Sub(plan.ClockBase) {
				return roomReplayAudioTimeline(fmt.Sprintf("participants[%s].deltas.line[%d].offset", participantID, delta.LineNumber), stream.DeltaArtifact.Path, "offset within declared room duration", delta.Offset.String())
			}
			if previousOffset >= 0 && delta.Offset < previousOffset {
				return roomReplayAudioTimeline(fmt.Sprintf("participants[%s].deltas.line[%d].offset", participantID, delta.LineNumber), stream.DeltaArtifact.Path, "monotonic delta timestamps", delta.Offset.String())
			}
			previousOffset = delta.Offset
		}
	}
	return validateRoomReplayAudioStreamTimeline(stream, plan, "participants["+participantID+"].wav")
}

func reconstructRoomReplayDeltaStream(stream AudioStream, participantID string) error {
	position := 0
	for index, delta := range stream.Deltas {
		if err := compareRoomReplayDelta(stream, participantID, index, delta, position); err != nil {
			return err
		}
		position += len(delta.PCM)
	}
	if position != len(stream.PCM) {
		deltaID := "missing"
		if len(stream.Deltas) > 0 {
			deltaID = "after-" + stream.Deltas[len(stream.Deltas)-1].ID
		}
		return &DeltaReconstructionError{
			ParticipantID: participantID, StreamID: stream.StreamID, DeltaID: deltaID, DeltaIndex: len(stream.Deltas), ByteOffset: position,
			ExpectedByte: int(stream.PCM[position]), ActualByte: -1, ExpectedLength: len(stream.PCM), ActualLength: position,
			ExpectedSampleCount: stream.SampleCount, ActualSampleCount: position / 2, Cause: ErrRoomReplayDeltaReconstruction,
		}
	}
	return nil
}

func compareRoomReplayDelta(stream AudioStream, participantID string, index int, delta AudioDelta, position int) error {
	if position < len(stream.PCM) {
		shared := len(delta.PCM)
		if remaining := len(stream.PCM) - position; shared > remaining {
			shared = remaining
		}
		for offset := 0; offset < shared; offset++ {
			if delta.PCM[offset] != stream.PCM[position+offset] {
				return &DeltaReconstructionError{
					ParticipantID: participantID, StreamID: stream.StreamID, DeltaID: delta.ID, DeltaIndex: index, ByteOffset: position + offset,
					ExpectedByte: int(stream.PCM[position+offset]), ActualByte: int(delta.PCM[offset]),
					ExpectedLength: len(stream.PCM), ActualLength: position + len(delta.PCM),
					ExpectedSampleCount: stream.SampleCount, ActualSampleCount: (position + len(delta.PCM)) / 2, Cause: ErrRoomReplayDeltaReconstruction,
				}
			}
		}
	}
	if position+len(delta.PCM) <= len(stream.PCM) {
		return nil
	}
	return &DeltaReconstructionError{
		ParticipantID: participantID, StreamID: stream.StreamID, DeltaID: delta.ID, DeltaIndex: index, ByteOffset: len(stream.PCM),
		ExpectedByte: -1, ActualByte: int(delta.PCM[len(stream.PCM)-position]), ExpectedLength: len(stream.PCM), ActualLength: position + len(delta.PCM),
		ExpectedSampleCount: stream.SampleCount, ActualSampleCount: (position + len(delta.PCM)) / 2, Cause: ErrRoomReplayDeltaReconstruction,
	}
}

func isRoomReplayAudioDeltaKind(kind string) bool {
	normalized := strings.ToLower(strings.NewReplacer(".", "", "_", "", "-", "", " ", "").Replace(strings.TrimSpace(kind)))
	return strings.Contains(normalized, "audio") && (strings.Contains(normalized, "delta") || strings.Contains(normalized, "chunk")) || normalized == "deltaaudio" || normalized == "pcmdelta"
}

func roomReplayFirstIntField(object roomReplayJSONObject, names ...string) (int64, string, bool, error) {
	for _, name := range names {
		value, present, err := roomReplayInt64Field(object, name)
		if present {
			return value, name, true, err
		}
	}
	return 0, "", false, nil
}

func roomReplayInt64Field(object roomReplayJSONObject, name string) (int64, bool, error) {
	raw, ok := object[name]
	if !ok {
		return 0, false, nil
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return 0, true, err
	}
	value, err := strconv.ParseInt(number.String(), 10, 64)
	return value, true, err
}

func roomReplayAudioOffset(object roomReplayJSONObject) (time.Duration, bool, error) {
	for _, name := range []string{"monotonic_offset_ms", "offset_ms", "timestamp_ms", "offset"} {
		if raw, ok := object[name]; ok {
			value, err := roomReplayDurationValue(raw, true)
			return value, true, err
		}
	}
	return 0, false, nil
}
