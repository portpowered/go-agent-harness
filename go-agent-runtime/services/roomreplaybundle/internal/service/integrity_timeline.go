package service

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type roomReplayTimelineState struct {
	previousOffsetNanos int64
	previousSequence    int64
	hasPrevious         bool
}

func loadRoomReplayTimeline(artifact RoomReplayArtifact, participants map[string]struct{}, declared map[string]RoomReplayArtifact, clockBase, startedAt, endedAt time.Time) ([]RoomReplayTimelineEvent, error) {
	file, err := os.Open(artifact.AbsolutePath)
	if err != nil {
		return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, "room_timeline", artifact.Path, "readable timeline", err.Error(), err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), int(roomReplayMaxTimelineLineBytes))
	result := make([]RoomReplayTimelineEvent, 0)
	state := roomReplayTimelineState{}
	var totalBytes int64
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := append([]byte(nil), scanner.Bytes()...)
		totalBytes += int64(len(line)) + 1
		if err := validateRoomReplayTimelineBudget(totalBytes, int64(len(result))); err != nil {
			return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, "room_timeline", artifact.Path, err.Error(), "limit exceeded", ErrInvalidRoomReplayBundle)
		}
		if strings.TrimSpace(string(line)) == "" {
			continue
		}
		event, object, err := parseRoomReplayTimelineEvent(line, lineNumber, len(result), artifact.Path)
		if err != nil {
			return nil, err
		}
		if err := validateRoomReplayTimelineEvent(&event, object, state, participants, declared, clockBase, startedAt, endedAt, lineNumber, artifact.Path); err != nil {
			return nil, err
		}
		result = append(result, event)
		state.previousOffsetNanos, state.previousSequence, state.hasPrevious = event.OffsetNanos, event.Sequence, true
	}
	if err := scanner.Err(); err != nil {
		return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, "room_timeline", artifact.Path, fmt.Sprintf("JSONL lines at most %d bytes", roomReplayMaxTimelineLineBytes), err.Error(), err)
	}
	return result, nil
}

func validateRoomReplayTimelineBudget(totalBytes, records int64) error {
	if totalBytes > roomReplayMaxTimelineBytes {
		return fmt.Errorf("timeline size at most %d bytes", roomReplayMaxTimelineBytes)
	}
	if records >= roomReplayMaxTimelineRecords {
		return fmt.Errorf("at most %d records", roomReplayMaxTimelineRecords)
	}
	return nil
}

func parseRoomReplayTimelineEvent(line []byte, lineNumber, defaultSequence int, artifactPath string) (RoomReplayTimelineEvent, roomReplayJSONObject, error) {
	object, err := roomReplayObject(line)
	if err != nil {
		return RoomReplayTimelineEvent{}, nil, newRoomReplayBundleError(RoomReplayBundleMismatch, fmt.Sprintf("room_timeline.line[%d]", lineNumber), artifactPath, "JSON object", "invalid", err)
	}
	eventType, present, err := firstRoomReplayStringField(object, nil, "event_type", "type", "event")
	if err != nil || !present || strings.TrimSpace(eventType) == "" {
		return RoomReplayTimelineEvent{}, nil, newRoomReplayBundleError(RoomReplayBundleIncomplete, fmt.Sprintf("room_timeline.line[%d].type", lineNumber), artifactPath, "non-empty event type", "missing or invalid", errOrDefault(err, ErrRoomReplayBundleIncomplete))
	}
	offsetMS, offsetNanos, offsetPresent, offsetErr := firstRoomReplayTimelineOffset(object, "monotonic_offset_ms", "offset_ms", "offset", "t_offset_ms")
	if offsetErr != nil || !offsetPresent {
		return RoomReplayTimelineEvent{}, nil, newRoomReplayBundleError(RoomReplayBundleMismatch, fmt.Sprintf("room_timeline.line[%d].monotonic_offset_ms", lineNumber), artifactPath, "non-negative offset", "invalid or missing", errOrDefault(offsetErr, ErrInvalidRoomReplayBundle))
	}
	unixMS, unixPresent, unixErr := firstRoomReplayIntField(object, nil, "unix_ms", "timestamp_ms", "t_unix_ms")
	if unixErr != nil || !unixPresent || unixMS < 0 {
		return RoomReplayTimelineEvent{}, nil, newRoomReplayBundleError(RoomReplayBundleMismatch, fmt.Sprintf("room_timeline.line[%d].unix_ms", lineNumber), artifactPath, "non-negative Unix milliseconds", "invalid or missing", errOrDefault(unixErr, ErrInvalidRoomReplayBundle))
	}
	sequence, sequencePresent, sequenceErr := firstRoomReplayIntField(object, nil, "sequence", "room_sequence")
	if sequenceErr != nil || !sequencePresent {
		sequence = defaultSequence
	}
	if sequence < 0 {
		return RoomReplayTimelineEvent{}, nil, newRoomReplayBundleError(RoomReplayBundleMismatch, fmt.Sprintf("room_timeline.line[%d].sequence", lineNumber), artifactPath, "non-negative sequence", fmt.Sprintf("%d", sequence), ErrInvalidRoomReplayBundle)
	}
	return RoomReplayTimelineEvent{Sequence: int64(sequence), OffsetMS: offsetMS, OffsetNanos: offsetNanos, UnixMS: int64(unixMS), Type: strings.TrimSpace(eventType), Raw: append(json.RawMessage(nil), line...)}, object, nil
}

func validateRoomReplayTimelineEvent(event *RoomReplayTimelineEvent, object roomReplayJSONObject, state roomReplayTimelineState, participants map[string]struct{}, declared map[string]RoomReplayArtifact, clockBase, startedAt, endedAt time.Time, lineNumber int, artifactPath string) error {
	if state.hasPrevious && (event.OffsetNanos < state.previousOffsetNanos || event.OffsetNanos == state.previousOffsetNanos && event.Sequence <= state.previousSequence) {
		return newRoomReplayBundleError(RoomReplayBundleMismatch, fmt.Sprintf("room_timeline.line[%d]", lineNumber), artifactPath, "ordered by offset and increasing sequence", fmt.Sprintf("offset=%d sequence=%d after offset=%d sequence=%d", event.OffsetMS, event.Sequence, state.previousOffsetNanos/int64(time.Millisecond), state.previousSequence), ErrInvalidRoomReplayBundle)
	}
	expectedUnix := clockBase.Add(time.Duration(event.OffsetNanos)).UnixMilli()
	if event.UnixMS != expectedUnix {
		return newRoomReplayBundleError(RoomReplayBundleMismatch, fmt.Sprintf("room_timeline.line[%d].unix_ms", lineNumber), artifactPath, fmt.Sprintf("%d", expectedUnix), fmt.Sprintf("%d", event.UnixMS), ErrInvalidRoomReplayBundle)
	}
	spanNanos := endedAt.Sub(clockBase).Nanoseconds()
	if event.OffsetNanos > spanNanos || clockBase.Add(time.Duration(event.OffsetNanos)).Before(startedAt) {
		return newRoomReplayBundleError(RoomReplayBundleMismatch, fmt.Sprintf("room_timeline.line[%d].monotonic_offset_ms", lineNumber), artifactPath, "offset inside room span", fmt.Sprintf("%d", event.OffsetMS), ErrInvalidRoomReplayBundle)
	}
	participantID, err := roomReplayTimelineParticipant(object, participants, lineNumber, artifactPath)
	if err != nil {
		return err
	}
	event.ParticipantID = participantID
	if err := validateRoomReplayTimelineArtifactReferences(object, declared); err != nil {
		return fmt.Errorf("room timeline line %d: %w", lineNumber, err)
	}
	return nil
}

func roomReplayTimelineParticipant(object roomReplayJSONObject, participants map[string]struct{}, lineNumber int, artifactPath string) (string, error) {
	participantID := ""
	for _, field := range []string{"participant_id", "participant", "speaker_id", "source_participant_id", "target_participant_id"} {
		value, ok, valueErr := roomReplayStringField(object, field)
		if !ok || valueErr != nil {
			continue
		}
		if field == "participant_id" || field == "participant" || field == "speaker_id" {
			participantID = value
		}
		if _, known := participants[value]; !known {
			return "", newRoomReplayBundleError(RoomReplayBundleMismatch, fmt.Sprintf("room_timeline.line[%d].%s", lineNumber, field), artifactPath, "declared participant ID", value, ErrInvalidRoomReplayBundle)
		}
	}
	return participantID, nil
}

// firstRoomReplayTimelineOffset accepts integer millisecond fields and the
// fractional t_offset_ms emitted by room recording.
func firstRoomReplayTimelineOffset(object roomReplayJSONObject, names ...string) (int64, int64, bool, error) {
	for _, name := range names {
		raw, ok := roomReplayRawField(object, name)
		if !ok {
			continue
		}
		var number json.Number
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		if err := decoder.Decode(&number); err != nil {
			return 0, 0, true, err
		}
		value, err := number.Float64()
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			if err == nil {
				err = errors.New("offset must be a finite non-negative number")
			}
			return 0, 0, true, err
		}
		nanosFloat := value * float64(time.Millisecond)
		if nanosFloat > float64(math.MaxInt64) {
			return 0, 0, true, errors.New("offset is too large")
		}
		nanos := int64(math.Round(nanosFloat))
		if nanos < 0 {
			return 0, 0, true, errors.New("offset is too large")
		}
		return int64(math.Floor(value)), nanos, true, nil
	}
	return 0, 0, false, errors.New("missing offset")
}

func validateRoomReplayTimelineArtifactReferences(object roomReplayJSONObject, declared map[string]RoomReplayArtifact) error {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		raw := object[key]
		if err := validateRoomReplayTimelineArtifactReference(key, raw, declared); err != nil {
			return err
		}
	}
	return nil
}

func validateRoomReplayTimelineArtifactReference(key string, raw json.RawMessage, declared map[string]RoomReplayArtifact) error {
	normalizedKey := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), ".", "_"))
	if !isRoomReplayTimelineArtifactField(normalizedKey) {
		return nil
	}
	value, ok := decodeRoomReplayString(raw)
	if !ok {
		return nil
	}
	if filepath.IsAbs(value) || path.IsAbs(value) || strings.ContainsRune(value, '\\') || strings.ContainsRune(value, '\x00') || hasRoomReplayTraversal(value) {
		return newRoomReplayBundleError(RoomReplayBundleMismatch, "room_timeline.artifact", value, "declared bundle-relative artifact path", "unsafe", ErrInvalidRoomReplayBundle)
	}
	normalized := roomReplayPathKey(value)
	if normalized == "." || normalized == ".." || strings.HasPrefix(normalized, "../") || strings.Contains(normalized, ":") {
		return newRoomReplayBundleError(RoomReplayBundleMismatch, "room_timeline.artifact", value, "declared bundle-relative artifact path", "traversal", ErrInvalidRoomReplayBundle)
	}
	if _, ok := declared[normalized]; !ok {
		return newRoomReplayBundleError(RoomReplayBundleMismatch, "room_timeline.artifact", value, "declared artifact path", "undeclared", ErrInvalidRoomReplayBundle)
	}
	return nil
}

func isRoomReplayTimelineArtifactField(key string) bool {
	return key == "artifact" || key == "artifact_ref" || strings.HasSuffix(key, "_artifact") || strings.HasSuffix(key, "_artifact_ref") || key == "audio_path" || key == "capture_path"
}

func hasRoomReplayTraversal(value string) bool {
	for _, component := range strings.Split(value, "/") {
		if component == ".." {
			return true
		}
	}
	return false
}
