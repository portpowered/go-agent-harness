package service

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

type roomReplayDeltaParseState struct {
	deltas            []RoomReplayAudioDelta
	seenDeltaIDs      map[string]int
	previousOffset    time.Duration
	hasPreviousOffset bool
}

func loadRoomReplayAudioDeltas(artifact RoomReplayArtifact, participantID, streamID string, plan RoomReplayPlan) ([]RoomReplayAudioDelta, error) {
	data, err := readRoomReplayArtifact(artifact, maxRoomReplayArtifactBytes, "participants["+participantID+"].deltas")
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	state := roomReplayDeltaParseState{deltas: make([]RoomReplayAudioDelta, 0), seenDeltaIDs: make(map[string]int)}
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		delta, found, lineErr := parseRoomReplayAudioDeltaLine(line, lineNumber, artifact, participantID, streamID, plan, &state)
		if lineErr != nil {
			return nil, lineErr
		}
		if found {
			state.deltas = append(state.deltas, delta)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, roomReplayAudioMismatch("participants["+participantID+"].deltas", artifact.Path, "readable delta JSONL", err.Error(), err)
	}
	if len(state.deltas) == 0 {
		return nil, roomReplayAudioIncomplete("participants["+participantID+"].deltas", artifact.Path, "at least one audio delta", "none", ErrRoomReplayBundleIncomplete)
	}
	return state.deltas, nil
}

func parseRoomReplayAudioDeltaLine(line []byte, lineNumber int, artifact RoomReplayArtifact, participantID, streamID string, plan RoomReplayPlan, state *roomReplayDeltaParseState) (RoomReplayAudioDelta, bool, error) {
	object, _, payload, found, err := parseRoomReplayDeltaPayload(line, lineNumber, artifact, participantID)
	if err != nil || !found {
		return RoomReplayAudioDelta{}, found, err
	}
	sequence, hasSequence, err := parseRoomReplayDeltaSequence(object, lineNumber, artifact, participantID)
	if err != nil {
		return RoomReplayAudioDelta{}, false, err
	}
	offset, hasOffset, err := parseRoomReplayDeltaOffset(object, lineNumber, artifact, participantID, plan, state)
	if err != nil {
		return RoomReplayAudioDelta{}, false, err
	}
	if err := validateRoomReplayDeltaOwners(object, lineNumber, artifact, participantID, streamID); err != nil {
		return RoomReplayAudioDelta{}, false, err
	}
	id, err := parseRoomReplayDeltaIdentity(object, lineNumber, artifact, participantID, state)
	if err != nil {
		return RoomReplayAudioDelta{}, false, err
	}
	turnID, _, _ := firstRoomReplayStringField(object, nil, "turn_id", "turn", "response_id")
	return RoomReplayAudioDelta{ID: id, Sequence: sequence, HasSequence: hasSequence, Offset: offset, HasOffset: hasOffset, TurnID: strings.TrimSpace(turnID), LineNumber: lineNumber, PCM: append([]byte(nil), payload...)}, true, nil
}

func parseRoomReplayDeltaPayload(line []byte, lineNumber int, artifact RoomReplayArtifact, participantID string) (roomReplayJSONObject, string, []byte, bool, error) {
	object, err := roomReplayObject(line)
	if err != nil {
		return nil, "", nil, false, roomReplayAudioMismatch(roomReplayDeltaLineField(participantID, lineNumber, ""), artifact.Path, "JSON object", "invalid", err)
	}
	kind, _, kindErr := firstRoomReplayStringField(object, nil, "type", "event_type", "kind")
	if kindErr != nil {
		return nil, "", nil, false, roomReplayAudioMismatch(roomReplayDeltaLineField(participantID, lineNumber, "type"), artifact.Path, "string event type", "invalid", kindErr)
	}
	payload, found, payloadErr := roomReplayAudioPayload(object, kind)
	if payloadErr != nil {
		return nil, "", nil, false, roomReplayAudioMismatch(roomReplayDeltaLineField(participantID, lineNumber, ""), artifact.Path, "base64 PCM16 audio payload", "invalid", payloadErr)
	}
	if !found {
		if isRoomReplayAudioDeltaKind(kind) {
			return nil, "", nil, false, roomReplayAudioIncomplete(roomReplayDeltaLineField(participantID, lineNumber, "content"), artifact.Path, "audio payload", "missing", ErrRoomReplayBundleIncomplete)
		}
		return object, kind, nil, false, nil
	}
	if len(payload) == 0 {
		return nil, "", nil, false, roomReplayAudioMismatch(roomReplayDeltaLineField(participantID, lineNumber, "content"), artifact.Path, "non-empty even PCM16 byte payload", strconv.Itoa(len(payload)), nil)
	}
	if err := codec.ValidatePCM16(payload, codec.MaxPCM16Bytes); err != nil {
		return nil, "", nil, false, roomReplayAudioMismatch(roomReplayDeltaLineField(participantID, lineNumber, "content"), artifact.Path, "non-empty even PCM16 byte payload", strconv.Itoa(len(payload)), err)
	}
	return object, kind, payload, true, nil
}

func parseRoomReplayDeltaSequence(object roomReplayJSONObject, lineNumber int, artifact RoomReplayArtifact, participantID string) (int64, bool, error) {
	sequence, _, hasSequence, sequenceErr := roomReplayFirstIntField(object, "sequence", "delta_index", "chunk_index", "index", "global_index", "actor_provided_index")
	if sequenceErr != nil {
		return 0, false, roomReplayAudioMismatch(roomReplayDeltaLineField(participantID, lineNumber, "sequence"), artifact.Path, "integer sequence", "invalid", sequenceErr)
	}
	if hasSequence && sequence < 0 {
		return 0, false, roomReplayAudioMismatch(roomReplayDeltaLineField(participantID, lineNumber, "sequence"), artifact.Path, "non-negative sequence", strconv.FormatInt(sequence, 10), nil)
	}
	return sequence, hasSequence, nil
}

func parseRoomReplayDeltaOffset(object roomReplayJSONObject, lineNumber int, artifact RoomReplayArtifact, participantID string, plan RoomReplayPlan, state *roomReplayDeltaParseState) (time.Duration, bool, error) {
	offset, hasOffset, offsetErr := roomReplayAudioOffset(object)
	if offsetErr != nil {
		return 0, false, roomReplayAudioMismatch(roomReplayDeltaLineField(participantID, lineNumber, "offset"), artifact.Path, "non-negative millisecond offset", "invalid", offsetErr)
	}
	if !hasOffset {
		return offset, false, nil
	}
	if offset < 0 || offset > plan.EndedAt.Sub(plan.ClockBase) {
		return 0, false, roomReplayAudioTimeline(roomReplayDeltaLineField(participantID, lineNumber, "offset"), artifact.Path, "offset within declared room duration", offset.String())
	}
	if state.hasPreviousOffset && offset < state.previousOffset {
		return 0, false, roomReplayAudioTimeline(roomReplayDeltaLineField(participantID, lineNumber, "offset"), artifact.Path, "monotonic delta timestamps", offset.String())
	}
	state.previousOffset, state.hasPreviousOffset = offset, true
	return offset, true, nil
}

func validateRoomReplayDeltaOwners(object roomReplayJSONObject, lineNumber int, artifact RoomReplayArtifact, participantID, streamID string) error {
	declaredStreamID, streamPresent, streamErr := firstRoomReplayStringField(object, nil, "stream_id", "audio_stream_id", "stream_identity")
	if streamErr != nil && streamPresent {
		return roomReplayAudioMismatch(roomReplayDeltaLineField(participantID, lineNumber, "stream_id"), artifact.Path, "string stream identity", "invalid", streamErr)
	}
	if streamPresent {
		declaredStreamID = strings.TrimSpace(declaredStreamID)
		if declaredStreamID == "" || declaredStreamID != streamID {
			return roomReplayAudioMismatch(roomReplayDeltaLineField(participantID, lineNumber, "stream_id"), artifact.Path, streamID, declaredStreamID, nil)
		}
	}
	declaredParticipantID, participantPresent, participantErr := firstRoomReplayStringField(object, nil, "participant_id")
	if participantErr != nil && participantPresent {
		return roomReplayAudioMismatch(roomReplayDeltaLineField(participantID, lineNumber, "participant_id"), artifact.Path, "string participant identity", "invalid", participantErr)
	}
	if participantPresent && strings.TrimSpace(declaredParticipantID) != participantID {
		return roomReplayAudioMismatch(roomReplayDeltaLineField(participantID, lineNumber, "participant_id"), artifact.Path, participantID, strings.TrimSpace(declaredParticipantID), nil)
	}
	return nil
}

func parseRoomReplayDeltaIdentity(object roomReplayJSONObject, lineNumber int, artifact RoomReplayArtifact, participantID string, state *roomReplayDeltaParseState) (string, error) {
	id, _, idErr := firstRoomReplayStringField(object, nil, "delta_id", "chunk_id", "id")
	if idErr != nil {
		return "", roomReplayAudioMismatch(roomReplayDeltaLineField(participantID, lineNumber, "id"), artifact.Path, "string delta identity", "invalid", idErr)
	}
	if strings.TrimSpace(id) == "" {
		id = fmt.Sprintf("delta-%d", len(state.deltas))
	}
	id = strings.TrimSpace(id)
	if previousLine, exists := state.seenDeltaIDs[id]; exists {
		return "", roomReplayAudioMismatch(roomReplayDeltaLineField(participantID, lineNumber, "id"), artifact.Path, fmt.Sprintf("unique delta identity (first seen on line %d)", previousLine), id, nil)
	}
	state.seenDeltaIDs[id] = lineNumber
	return id, nil
}

func roomReplayDeltaLineField(participantID string, lineNumber int, field string) string {
	base := fmt.Sprintf("participants[%s].deltas.line[%d]", participantID, lineNumber)
	if field == "" {
		return base
	}
	return base + "." + field
}

func roomReplayAudioPayload(object roomReplayJSONObject, kind string) ([]byte, bool, error) {
	payload, found, err := roomReplayAudioPayloadAliases(object, "pcm_base64", "audio_base64", "delta_base64", "pcm", "audio")
	if found || err != nil {
		return payload, true, err
	}
	audioKind := isRoomReplayAudioDeltaKind(kind)
	if audioKind || strings.TrimSpace(kind) == "" {
		payload, found, err = roomReplayAudioPayloadAliases(object, "delta", "data")
		if found || err != nil {
			return payload, true, err
		}
	}
	if audioKind {
		if raw, ok := object["content"]; ok {
			return decodeRoomReplayAudioRaw(raw)
		}
	}
	return roomReplayNestedAudioPayload(object, kind)
}

func roomReplayAudioPayloadAliases(object roomReplayJSONObject, keys ...string) ([]byte, bool, error) {
	for _, key := range keys {
		raw, ok := object[key]
		if !ok {
			continue
		}
		payload, found, err := decodeRoomReplayAudioRaw(raw)
		if found || err != nil {
			return payload, true, err
		}
	}
	return nil, false, nil
}

func roomReplayNestedAudioPayload(object roomReplayJSONObject, kind string) ([]byte, bool, error) {
	for _, key := range []string{"value", "payload", "audio_delta"} {
		raw, ok := object[key]
		if !ok {
			continue
		}
		nested, err := roomReplayObject(raw)
		if err != nil {
			continue
		}
		nestedKind := kind
		if value, present, _ := firstRoomReplayStringField(nested, nil, "type", "event_type", "kind"); present {
			nestedKind = value
		}
		payload, found, payloadErr := roomReplayAudioPayload(nested, nestedKind)
		if found || payloadErr != nil {
			return payload, found, payloadErr
		}
	}
	return nil, false, nil
}

func decodeRoomReplayAudioRaw(raw json.RawMessage) ([]byte, bool, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false, nil
	}
	if payload, found, err := decodeRoomReplayAudioString(raw); found || err != nil {
		return payload, found, err
	}
	if payload, found, err := decodeRoomReplayAudioByteArray(raw); found || err != nil {
		return payload, found, err
	}
	if payload, found, err := decodeRoomReplayAudioObject(raw); found || err != nil {
		return payload, found, err
	}
	return nil, true, errors.New("audio payload must be base64 string or byte array")
}

func decodeRoomReplayAudioString(raw json.RawMessage) ([]byte, bool, error) {
	var encoded string
	if json.Unmarshal(raw, &encoded) != nil {
		return nil, false, nil
	}
	if encoded == "" {
		return []byte{}, true, nil
	}
	decoded, err := codec.DecodeLegacyBase64(encoded)
	return decoded, true, err
}

func decodeRoomReplayAudioByteArray(raw json.RawMessage) ([]byte, bool, error) {
	var numbers []int
	if json.Unmarshal(raw, &numbers) != nil {
		return nil, false, nil
	}
	payload := make([]byte, len(numbers))
	for index, value := range numbers {
		if value < 0 || value > 255 {
			return nil, true, fmt.Errorf("audio byte %d is outside 0..255", value)
		}
		payload[index] = byte(value)
	}
	return payload, true, nil
}

func decodeRoomReplayAudioObject(raw json.RawMessage) ([]byte, bool, error) {
	object, err := roomReplayObject(raw)
	if err != nil {
		return nil, false, nil
	}
	for _, key := range []string{"base64", "data", "content", "pcm", "delta"} {
		if nested, ok := object[key]; ok {
			return decodeRoomReplayAudioRaw(nested)
		}
	}
	return nil, false, nil
}
