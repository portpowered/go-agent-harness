package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func validateRoomReplayDeltaStream(stream RoomReplayAudioStream, plan RoomReplayPlan, participantID string) error {
	if len(stream.Deltas) == 0 {
		return &RoomReplayDeltaReconstructionError{ParticipantID: participantID, StreamID: stream.StreamID, DeltaID: "missing", DeltaIndex: 0, ByteOffset: 0, ExpectedLength: len(stream.PCM), ActualLength: 0, ExpectedSampleCount: stream.SampleCount, ActualSampleCount: 0, Cause: ErrRoomReplayDeltaReconstruction}
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

func reconstructRoomReplayDeltaStream(stream RoomReplayAudioStream, participantID string) error {
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
		return &RoomReplayDeltaReconstructionError{
			ParticipantID: participantID, StreamID: stream.StreamID, DeltaID: deltaID, DeltaIndex: len(stream.Deltas), ByteOffset: position,
			ExpectedByte: int(stream.PCM[position]), ActualByte: -1, ExpectedLength: len(stream.PCM), ActualLength: position,
			ExpectedSampleCount: stream.SampleCount, ActualSampleCount: position / 2, Cause: ErrRoomReplayDeltaReconstruction,
		}
	}
	return nil
}

func compareRoomReplayDelta(stream RoomReplayAudioStream, participantID string, index int, delta RoomReplayAudioDelta, position int) error {
	if position < len(stream.PCM) {
		shared := len(delta.PCM)
		if remaining := len(stream.PCM) - position; shared > remaining {
			shared = remaining
		}
		for offset := 0; offset < shared; offset++ {
			if delta.PCM[offset] != stream.PCM[position+offset] {
				return &RoomReplayDeltaReconstructionError{
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
	return &RoomReplayDeltaReconstructionError{
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
