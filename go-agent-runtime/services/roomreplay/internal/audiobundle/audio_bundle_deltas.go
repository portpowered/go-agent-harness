package audiobundle

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

// roomReplayDeltaDecoder validates the delta JSONL lines of one participant
// stream, tracking identity uniqueness and timestamp monotonicity across lines.
type roomReplayDeltaDecoder struct {
	artifact          RoomReplayArtifact
	participantID     string
	streamID          string
	roomDuration      time.Duration
	deltas            []RoomReplayAudioDelta
	seenDeltaIDs      map[string]int
	previousOffset    time.Duration
	hasPreviousOffset bool
}

func loadRoomReplayAudioDeltas(artifact RoomReplayArtifact, participantID, streamID string, plan RoomReplayPlan) ([]RoomReplayAudioDelta, error) {
	data, err := os.ReadFile(artifact.AbsolutePath)
	if err != nil {
		return nil, roomReplayAudioIncomplete("participants["+participantID+"].deltas", artifact.Path, "readable delta JSONL", err.Error(), err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, roomReplayJSONLInitialBufferBytes), roomReplayJSONLMaxTokenBytes)
	decoder := roomReplayDeltaDecoder{
		artifact:      artifact,
		participantID: participantID,
		streamID:      streamID,
		roomDuration:  plan.EndedAt.Sub(plan.ClockBase),
		deltas:        make([]RoomReplayAudioDelta, 0),
		seenDeltaIDs:  make(map[string]int),
	}
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := decoder.decodeLine(line, lineNumber); err != nil {
			return nil, err
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, roomReplayAudioMismatch("participants["+participantID+"].deltas", artifact.Path, "readable delta JSONL", err.Error(), err)
	}
	if len(decoder.deltas) == 0 {
		return nil, roomReplayAudioIncomplete("participants["+participantID+"].deltas", artifact.Path, "at least one audio delta", "none", ErrRoomReplayBundleIncomplete)
	}
	return decoder.deltas, nil
}

func (d *roomReplayDeltaDecoder) field(lineNumber int, suffix string) string {
	return fmt.Sprintf("participants[%s].deltas.line[%d]%s", d.participantID, lineNumber, suffix)
}

func (d *roomReplayDeltaDecoder) decodeLine(line []byte, lineNumber int) error {
	object, err := roomReplayObject(line)
	if err != nil {
		return roomReplayAudioMismatch(d.field(lineNumber, ""), d.artifact.Path, "JSON object", "invalid", err)
	}
	kind, _, kindErr := firstRoomReplayStringField(object, nil, "type", "event_type", "kind")
	if kindErr != nil {
		return roomReplayAudioMismatch(d.field(lineNumber, ".type"), d.artifact.Path, "string event type", "invalid", kindErr)
	}
	payload, found, err := d.payload(object, kind, lineNumber)
	if err != nil || !found {
		return err
	}
	sequence, hasSequence, err := d.sequence(object, lineNumber)
	if err != nil {
		return err
	}
	offset, hasOffset, err := d.offset(object, lineNumber)
	if err != nil {
		return err
	}
	if err := d.validateIdentity(object, lineNumber); err != nil {
		return err
	}
	id, err := d.deltaID(object, lineNumber)
	if err != nil {
		return err
	}
	turnID, _ := optionalRoomReplayStringField(object, nil, "turn_id", "turn", "response_id")
	d.deltas = append(d.deltas, RoomReplayAudioDelta{ID: id, Sequence: sequence, HasSequence: hasSequence, Offset: offset, HasOffset: hasOffset, TurnID: strings.TrimSpace(turnID), LineNumber: lineNumber, PCM: append([]byte(nil), payload...)})
	return nil
}

// payload decodes the line's PCM16 audio. A line without audio is skipped
// (found is false) unless its kind declares an audio delta.
func (d *roomReplayDeltaDecoder) payload(object roomReplayJSONObject, kind string, lineNumber int) ([]byte, bool, error) {
	payload, found, err := roomReplayAudioPayload(object, kind)
	if err != nil {
		return nil, false, roomReplayAudioMismatch(d.field(lineNumber, ""), d.artifact.Path, "base64 PCM16 audio payload", "invalid", err)
	}
	if !found {
		if isRoomReplayAudioDeltaKind(kind) {
			return nil, false, roomReplayAudioIncomplete(d.field(lineNumber, ".content"), d.artifact.Path, "audio payload", "missing", ErrRoomReplayBundleIncomplete)
		}
		return nil, false, nil
	}
	if len(payload) == 0 {
		return nil, false, roomReplayAudioMismatch(d.field(lineNumber, ".content"), d.artifact.Path, "non-empty even PCM16 byte payload", strconv.Itoa(len(payload)), nil)
	}
	if err := codec.ValidatePCM16(payload, codec.MaxPCM16Bytes); err != nil {
		return nil, false, roomReplayAudioMismatch(d.field(lineNumber, ".content"), d.artifact.Path, "non-empty even PCM16 byte payload", strconv.Itoa(len(payload)), err)
	}
	return payload, true, nil
}

func (d *roomReplayDeltaDecoder) sequence(object roomReplayJSONObject, lineNumber int) (int64, bool, error) {
	sequence, _, hasSequence, err := roomReplayFirstIntField(object, "sequence", "delta_index", "chunk_index", "index", "global_index", "actor_provided_index")
	if err != nil {
		return 0, false, roomReplayAudioMismatch(d.field(lineNumber, ".sequence"), d.artifact.Path, "integer sequence", "invalid", err)
	}
	if hasSequence && sequence < 0 {
		return 0, false, roomReplayAudioMismatch(d.field(lineNumber, ".sequence"), d.artifact.Path, "non-negative sequence", strconv.FormatInt(sequence, 10), nil)
	}
	return sequence, hasSequence, nil
}

func (d *roomReplayDeltaDecoder) offset(object roomReplayJSONObject, lineNumber int) (time.Duration, bool, error) {
	offset, hasOffset, err := roomReplayAudioOffset(object)
	if err != nil {
		return 0, false, roomReplayAudioMismatch(d.field(lineNumber, ".offset"), d.artifact.Path, "non-negative millisecond offset", "invalid", err)
	}
	if !hasOffset {
		return offset, false, nil
	}
	if offset < 0 || offset > d.roomDuration {
		return 0, false, roomReplayAudioTimeline(d.field(lineNumber, ".offset"), d.artifact.Path, "offset within declared room duration", offset.String())
	}
	if d.hasPreviousOffset && offset < d.previousOffset {
		return 0, false, roomReplayAudioTimeline(d.field(lineNumber, ".offset"), d.artifact.Path, "monotonic delta timestamps", offset.String())
	}
	d.previousOffset, d.hasPreviousOffset = offset, true
	return offset, true, nil
}

// validateIdentity rejects lines that declare a stream or participant other
// than the one whose delta artifact is being decoded.
func (d *roomReplayDeltaDecoder) validateIdentity(object roomReplayJSONObject, lineNumber int) error {
	declaredStreamID, streamPresent, streamErr := firstRoomReplayStringField(object, nil, "stream_id", "audio_stream_id", "stream_identity")
	if streamErr != nil && streamPresent {
		return roomReplayAudioMismatch(d.field(lineNumber, ".stream_id"), d.artifact.Path, "string stream identity", "invalid", streamErr)
	}
	if streamPresent {
		declaredStreamID = strings.TrimSpace(declaredStreamID)
		if declaredStreamID == "" || declaredStreamID != d.streamID {
			return roomReplayAudioMismatch(d.field(lineNumber, ".stream_id"), d.artifact.Path, d.streamID, declaredStreamID, nil)
		}
	}
	declaredParticipantID, participantPresent, participantErr := firstRoomReplayStringField(object, nil, "participant_id")
	if participantErr != nil && participantPresent {
		return roomReplayAudioMismatch(d.field(lineNumber, ".participant_id"), d.artifact.Path, "string participant identity", "invalid", participantErr)
	}
	if participantPresent && strings.TrimSpace(declaredParticipantID) != d.participantID {
		return roomReplayAudioMismatch(d.field(lineNumber, ".participant_id"), d.artifact.Path, d.participantID, strings.TrimSpace(declaredParticipantID), nil)
	}
	return nil
}

func (d *roomReplayDeltaDecoder) deltaID(object roomReplayJSONObject, lineNumber int) (string, error) {
	id, _, err := firstRoomReplayStringField(object, nil, "delta_id", "chunk_id", "id")
	if err != nil {
		return "", roomReplayAudioMismatch(d.field(lineNumber, ".id"), d.artifact.Path, "string delta identity", "invalid", err)
	}
	if strings.TrimSpace(id) == "" {
		id = fmt.Sprintf("delta-%d", len(d.deltas))
	}
	id = strings.TrimSpace(id)
	if previousLine, exists := d.seenDeltaIDs[id]; exists {
		return "", roomReplayAudioMismatch(d.field(lineNumber, ".id"), d.artifact.Path, fmt.Sprintf("unique delta identity (first seen on line %d)", previousLine), id, nil)
	}
	d.seenDeltaIDs[id] = lineNumber
	return id, nil
}

func roomReplayAudioPayload(object roomReplayJSONObject, kind string) ([]byte, bool, error) {
	audioKind := isRoomReplayAudioDeltaKind(kind)
	if payload, found, err := firstRoomReplayAudioRaw(object, "pcm_base64", "audio_base64", "delta_base64", "pcm", "audio"); found || err != nil {
		return payload, true, err
	}
	if audioKind || strings.TrimSpace(kind) == "" {
		if payload, found, err := firstRoomReplayAudioRaw(object, "delta", "data"); found || err != nil {
			return payload, true, err
		}
	}
	if raw, ok := object["content"]; ok && audioKind {
		return decodeRoomReplayAudioRaw(raw)
	}
	return nestedRoomReplayAudioPayload(object, kind)
}

// firstRoomReplayAudioRaw decodes the first listed key that yields audio or a
// decode error.
func firstRoomReplayAudioRaw(object roomReplayJSONObject, keys ...string) ([]byte, bool, error) {
	for _, key := range keys {
		raw, ok := object[key]
		if !ok {
			continue
		}
		if payload, found, err := decodeRoomReplayAudioRaw(raw); found || err != nil {
			return payload, found, err
		}
	}
	return nil, false, nil
}

// nestedRoomReplayAudioPayload searches wrapper objects, letting a nested
// type field override the enclosing event kind.
func nestedRoomReplayAudioPayload(object roomReplayJSONObject, kind string) ([]byte, bool, error) {
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
		if value, present := optionalRoomReplayStringField(nested, nil, "type", "event_type", "kind"); present {
			nestedKind = value
		}
		if payload, found, payloadErr := roomReplayAudioPayload(nested, nestedKind); found || payloadErr != nil {
			return payload, found, payloadErr
		}
	}
	return nil, false, nil
}
