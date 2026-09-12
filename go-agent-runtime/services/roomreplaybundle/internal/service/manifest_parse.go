package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	room "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

func parseRoomReplayManifest(data []byte) (roomReplayManifestDocument, error) {
	var object roomReplayJSONObject
	if err := json.Unmarshal(data, &object); err != nil {
		return roomReplayManifestDocument{}, newRoomReplayBundleError(RoomReplayBundleMismatch, "run-manifest.json", "", "valid JSON object", "invalid JSON", err)
	}
	if object == nil {
		return roomReplayManifestDocument{}, newRoomReplayBundleError(RoomReplayBundleMismatch, "run-manifest.json", "", "JSON object", "null", ErrInvalidRoomReplayBundle)
	}
	header, err := parseRoomReplayManifestHeader(object)
	if err != nil {
		return roomReplayManifestDocument{}, err
	}
	participantsRaw, present := roomReplayRawField(object, "participants")
	if !present {
		return roomReplayManifestDocument{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "participants", "", "participant map or array", "missing", ErrRoomReplayBundleIncomplete)
	}
	participants, err := parseRoomReplayParticipants(participantsRaw)
	if err != nil {
		return roomReplayManifestDocument{}, err
	}
	inventory, err := parseRoomReplayManifestInventory(object)
	if err != nil {
		return roomReplayManifestDocument{}, err
	}
	for index := range participants {
		if err := inferRoomReplayParticipantArtifacts(&participants[index], inventory); err != nil {
			return roomReplayManifestDocument{}, err
		}
	}
	roomArtifacts, err := parseRoomReplayArtifacts(object, inventory)
	if err != nil {
		return roomReplayManifestDocument{}, err
	}
	header.Participants, header.Inventory, header.RoomArtifacts = participants, inventory, roomArtifacts
	return header, nil
}

func parseRoomReplayManifestHeader(object roomReplayJSONObject) (roomReplayManifestDocument, error) {
	schema, err := parseRoomReplaySchema(object)
	if err != nil {
		return roomReplayManifestDocument{}, err
	}
	finalized, err := parseRoomReplayFinalized(object)
	if err != nil {
		return roomReplayManifestDocument{}, err
	}
	clockBase, err := parseRoomReplayClockBase(object)
	if err != nil {
		return roomReplayManifestDocument{}, err
	}
	startedAt, endedAt, err := parseRoomReplayTiming(object, clockBase)
	if err != nil {
		return roomReplayManifestDocument{}, err
	}
	pcm, err := parseRoomReplayPCMFormat(object)
	if err != nil {
		return roomReplayManifestDocument{}, err
	}
	return roomReplayManifestDocument{SchemaVersion: schema, Finalized: finalized, ClockBase: clockBase.UTC(), StartedAt: startedAt.UTC(), EndedAt: endedAt.UTC(), PCMFormat: pcm}, nil
}

func parseRoomReplaySchema(object roomReplayJSONObject) (int, error) {
	schema, present, err := roomReplayIntField(object, "schema_version")
	if err != nil {
		return 0, newRoomReplayBundleError(RoomReplayBundleMismatch, "schema_version", "", "integer", "invalid", err)
	}
	if !present {
		return 0, newRoomReplayBundleError(RoomReplayBundleIncomplete, "schema_version", "", "supported schema version", "missing", ErrRoomReplayBundleIncomplete)
	}
	return schema, nil
}

func parseRoomReplayFinalized(object roomReplayJSONObject) (bool, error) {
	finalized, present, err := roomReplayBoolField(object, "finalized")
	if err != nil {
		return false, newRoomReplayBundleError(RoomReplayBundleMismatch, "finalized", "", "boolean", "invalid", err)
	}
	if !present {
		status, ok, statusErr := roomReplayStringField(object, "status")
		if statusErr == nil && ok && strings.EqualFold(status, "finalized") {
			return true, nil
		}
	}
	if !present {
		return false, newRoomReplayBundleError(RoomReplayBundleIncomplete, "finalized", "", "true", "missing", ErrRoomReplayBundleIncomplete)
	}
	return finalized, nil
}

func parseRoomReplayClockBase(object roomReplayJSONObject) (time.Time, error) {
	clockBase, present, err := roomReplayTimeField(object, "clock_base")
	if err != nil || !present {
		clockBase, present, err = parseNestedRoomReplayClockBase(object)
	}
	if err != nil || !present || clockBase.IsZero() {
		return time.Time{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "clock_base", "", "UTC session start", "missing or invalid", errOrDefault(err, ErrRoomReplayBundleIncomplete))
	}
	return clockBase, nil
}

func parseNestedRoomReplayClockBase(object roomReplayJSONObject) (time.Time, bool, error) {
	timingRaw, ok := roomReplayRawField(object, "timing")
	if !ok {
		return time.Time{}, false, nil
	}
	timing, err := roomReplayObject(timingRaw)
	if err != nil {
		return time.Time{}, false, err
	}
	return roomReplayTimeField(timing, "clock_base")
}

func parseRoomReplayManifestInventory(object roomReplayJSONObject) ([]roomReplayArtifactRef, error) {
	inventory := make([]roomReplayArtifactRef, 0)
	for _, key := range []string{"artifacts", "artifact_integrity", "integrity", "files"} {
		raw, ok := roomReplayRawField(object, key)
		if !ok {
			continue
		}
		entries, err := parseRoomReplayArtifactInventory(raw, key)
		if err != nil {
			return nil, err
		}
		inventory = append(inventory, entries...)
	}
	return inventory, nil
}

func parseRoomReplayTiming(object roomReplayJSONObject, clockBase time.Time) (time.Time, time.Time, error) {
	container := object
	if raw, ok := roomReplayRawField(object, "timing"); ok {
		if nested, err := roomReplayObject(raw); err == nil {
			container = nested
		}
	}
	started, ok, err := firstRoomReplayTimeField(container, object, "started_at")
	if err != nil || !ok {
		return time.Time{}, time.Time{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "timing.started_at", "", "UTC start timestamp", "missing or invalid", errOrDefault(err, ErrRoomReplayBundleIncomplete))
	}
	ended, ok, err := firstRoomReplayTimeField(container, object, "ended_at")
	if err != nil || !ok {
		return time.Time{}, time.Time{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "timing.ended_at", "", "UTC end timestamp", "missing or invalid", errOrDefault(err, ErrRoomReplayBundleIncomplete))
	}
	if clockBase.Before(started) || clockBase.After(ended) {
		return time.Time{}, time.Time{}, newRoomReplayBundleError(RoomReplayBundleMismatch, "clock_base", "", "inside timing interval", clockBase.UTC().Format(time.RFC3339Nano), ErrInvalidRoomReplayBundle)
	}
	return started.UTC(), ended.UTC(), nil
}

func firstRoomReplayTimeField(primary, fallback roomReplayJSONObject, name string) (time.Time, bool, error) {
	if primary != nil {
		if value, present, err := roomReplayTimeField(primary, name); present {
			return value, true, err
		}
	}
	if fallback != nil {
		if value, present, err := roomReplayTimeField(fallback, name); present {
			return value, true, err
		}
	}
	return time.Time{}, false, errors.New("missing timestamp")
}

func parseRoomReplayPCMFormat(object roomReplayJSONObject) (RoomReplayPCMFormat, error) {
	format := RoomReplayPCMFormat{}
	container := object
	for _, key := range []string{"pcm_format", "pcm", "audio_format"} {
		if raw, ok := roomReplayRawField(object, key); ok {
			if nested, err := roomReplayObject(raw); err == nil {
				container = nested
				break
			}
		}
	}
	var err error
	format.SampleRate, _, err = firstRoomReplayIntField(container, object, "sample_rate_hz", "sample_rate", "sampleRate")
	if err != nil {
		return RoomReplayPCMFormat{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "pcm_format.sample_rate_hz", "", "positive sample rate", "invalid or missing", err)
	}
	format.Channels, _, err = firstRoomReplayIntField(container, object, "channels", "channel_count")
	if err != nil {
		return RoomReplayPCMFormat{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "pcm_format.channels", "", "positive channel count", "invalid or missing", err)
	}
	format.SampleWidthBits, _, err = firstRoomReplayIntField(container, object, "sample_width_bits", "sample_width", "bits_per_sample")
	if err != nil {
		return RoomReplayPCMFormat{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "pcm_format.sample_width_bits", "", "16", "invalid or missing", err)
	}
	format.SampleWidthBit = format.SampleWidthBits
	format.ByteOrder, _, err = firstRoomReplayStringField(container, object, "byte_order", "endianness")
	if err != nil {
		return RoomReplayPCMFormat{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "pcm_format.byte_order", "", "little", "invalid or missing", err)
	}
	format.Encoding, _, err = firstRoomReplayStringField(container, object, "encoding", "sample_encoding", "format")
	if err != nil {
		return RoomReplayPCMFormat{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "pcm_format.encoding", "", "signed_pcm16", "invalid or missing", err)
	}
	return format, nil
}

func validateRoomReplayPCMFormat(format RoomReplayPCMFormat) error {
	sampleWidth := format.SampleWidthBits
	if sampleWidth == 0 {
		sampleWidth = format.SampleWidthBit
	}
	if format.SampleRate <= 0 || format.Channels <= 0 || sampleWidth != 16 || !strings.EqualFold(format.ByteOrder, "little") {
		return newRoomReplayBundleError(RoomReplayBundleMismatch, "pcm_format", "", "signed little-endian PCM16 with positive rate/channels", fmt.Sprintf("rate=%d channels=%d width=%d byte_order=%q", format.SampleRate, format.Channels, sampleWidth, format.ByteOrder), ErrInvalidRoomReplayBundle)
	}
	encoding := strings.ToLower(strings.TrimSpace(format.Encoding))
	if encoding != "signed_pcm16" && encoding != "pcm_s16le" && encoding != "pcm16" && encoding != "signed 16-bit pcm" {
		return newRoomReplayBundleError(RoomReplayBundleMismatch, "pcm_format.encoding", "", "signed_pcm16", format.Encoding, ErrInvalidRoomReplayBundle)
	}
	return nil
}

func parseRoomReplayParticipants(raw json.RawMessage) ([]roomReplayParticipantRef, error) {
	if strings.HasPrefix(strings.TrimSpace(string(raw)), "[") {
		return parseRoomReplayParticipantArray(raw)
	}
	return parseRoomReplayParticipantMap(raw)
}

func parseRoomReplayParticipantArray(raw json.RawMessage) ([]roomReplayParticipantRef, error) {
	var values []roomReplayJSONObject
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, "participants", "", "array of participant objects", "invalid", err)
	}
	participants := make([]roomReplayParticipantRef, 0, len(values))
	for index, object := range values {
		participant, err := parseRoomReplayParticipant(object, fmt.Sprintf("participants[%d]", index), "")
		if err != nil {
			return nil, err
		}
		participants = append(participants, participant)
	}
	return participants, nil
}

func parseRoomReplayParticipantMap(raw json.RawMessage) ([]roomReplayParticipantRef, error) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, "participants", "", "map of participant objects", "invalid", err)
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	participants := make([]roomReplayParticipantRef, 0, len(keys))
	for _, key := range keys {
		object, err := roomReplayObject(values[key])
		if err != nil {
			return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, "participants["+key+"]", "", "participant object", "invalid", err)
		}
		participant, err := parseRoomReplayParticipant(object, "participants["+key+"]", key)
		if err != nil {
			return nil, err
		}
		participants = append(participants, participant)
	}
	return participants, nil
}

func parseRoomReplayParticipant(object roomReplayJSONObject, field, mapID string) (roomReplayParticipantRef, error) {
	id, err := parseRoomReplayParticipantID(object, field, mapID)
	if err != nil {
		return roomReplayParticipantRef{}, err
	}
	kindValue, _, err := roomReplayStringField(object, "kind")
	if err != nil {
		return roomReplayParticipantRef{}, newRoomReplayBundleError(RoomReplayBundleMismatch, field+".kind", id, "string", "invalid", err)
	}
	kind := room.ParticipantKindNormalizer{}.Normalize(room.ParticipantKind(kindValue))
	if kind != room.ParticipantKindAgent && kind != room.ParticipantKindHuman {
		return roomReplayParticipantRef{}, newRoomReplayBundleError(RoomReplayBundleMismatch, field+".kind", id, "agent or human", kindValue, ErrInvalidRoomReplayBundle)
	}
	provider, err := roomReplayParticipantString(object, field, id, "provider")
	if err != nil {
		return roomReplayParticipantRef{}, err
	}
	model, err := roomReplayParticipantString(object, field, id, "model")
	if err != nil {
		return roomReplayParticipantRef{}, err
	}
	voice, err := roomReplayParticipantString(object, field, id, "voice")
	if err != nil {
		return roomReplayParticipantRef{}, err
	}
	opening, err := roomReplayParticipantString(object, field, id, "opening_prompt")
	if err != nil {
		return roomReplayParticipantRef{}, err
	}
	system, err := roomReplayParticipantString(object, field, id, "system_prompt")
	if err != nil {
		return roomReplayParticipantRef{}, err
	}
	turnCount, err := parseRoomReplayTurnCount(object, field, id)
	if err != nil {
		return roomReplayParticipantRef{}, err
	}
	artifacts, err := parseRoomReplayParticipantArtifactFields(object, field)
	if err != nil {
		return roomReplayParticipantRef{}, err
	}
	return roomReplayParticipantRef{ID: id, Kind: kind, Provider: strings.TrimSpace(provider), Model: strings.TrimSpace(model), Voice: voice, OpeningPrompt: opening, SystemPrompt: system, RecordedTurnCount: turnCount, Artifacts: artifacts}, nil
}

func parseRoomReplayParticipantID(object roomReplayJSONObject, field, mapID string) (string, error) {
	id, present, err := roomReplayStringField(object, "id")
	if err != nil {
		return "", newRoomReplayBundleError(RoomReplayBundleMismatch, field+".id", "", "string", "invalid", err)
	}
	if !present {
		id = mapID
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return "", newRoomReplayBundleError(RoomReplayBundleIncomplete, field+".id", "", "non-empty participant ID", "missing", ErrRoomReplayBundleIncomplete)
	}
	if mapID != "" && id != mapID {
		return "", newRoomReplayBundleError(RoomReplayBundleMismatch, field+".id", id, mapID, id, ErrInvalidRoomReplayBundle)
	}
	return id, nil
}

func roomReplayParticipantString(object roomReplayJSONObject, field, id, name string) (string, error) {
	value, _, err := roomReplayStringField(object, name)
	if err != nil {
		return "", newRoomReplayBundleError(RoomReplayBundleMismatch, field+"."+name, id, "string", "invalid", err)
	}
	return value, nil
}

func parseRoomReplayTurnCount(object roomReplayJSONObject, field, id string) (int, error) {
	turnCount, present, err := firstRoomReplayIntField(object, nil, "completed_turns", "turns_completed", "turn_count")
	if present && err != nil {
		return 0, newRoomReplayBundleError(RoomReplayBundleMismatch, field+".completed_turns", id, "non-negative integer", "invalid", err)
	}
	if turnCount < 0 {
		return 0, newRoomReplayBundleError(RoomReplayBundleMismatch, field+".completed_turns", id, "non-negative integer", strconv.Itoa(turnCount), ErrInvalidRoomReplayBundle)
	}
	return turnCount, nil
}
