package services

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func resolveRoomReplayBundle(bundle string) (string, string, string, error) {
	raw := strings.TrimSpace(bundle)
	if raw == "" {
		return "", "", "", newRoomReplayBundleError(RoomReplayBundleIncomplete, "bundle", "", "directory or manifest path", "missing", ErrRoomReplayBundleIncomplete)
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", "", "", newRoomReplayBundleError(RoomReplayBundleMismatch, "bundle", "", "resolvable path", raw, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		kind := RoomReplayBundleMismatch
		if errors.Is(err, os.ErrNotExist) {
			kind = RoomReplayBundleIncomplete
		}
		return "", "", "", newRoomReplayBundleError(kind, "bundle", filepath.ToSlash(abs), "existing bundle", err.Error(), err)
	}

	var rootCandidate, manifestRelative string
	if info.IsDir() {
		rootCandidate = abs
		manifestRelative = RoomReplayBundleManifestPath
	} else {
		rootCandidate = filepath.Dir(abs)
		manifestRelative = filepath.Base(abs)
	}
	root, err := filepath.EvalSymlinks(rootCandidate)
	if err != nil {
		return "", "", "", newRoomReplayBundleError(RoomReplayBundleIncomplete, "bundle", filepath.ToSlash(rootCandidate), "resolvable bundle directory", err.Error(), err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", "", "", newRoomReplayBundleError(RoomReplayBundleMismatch, "bundle", rootCandidate, "absolute bundle directory", err.Error(), err)
	}
	manifestPath, normalized, pathErr := safeRoomReplayPath(root, manifestRelative)
	if pathErr != nil {
		return "", "", "", pathErr
	}
	return root, manifestPath, normalized, nil
}

func readRoomReplayManifest(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, errors.New("manifest is empty")
	}
	return data, nil
}

func safeRoomReplayPath(root, relative string) (string, string, error) {
	value := strings.TrimSpace(relative)
	if value == "" || strings.ContainsRune(value, '\x00') || strings.ContainsRune(value, '\\') {
		return "", "", newRoomReplayBundleError(RoomReplayBundleMismatch, "artifact.path", value, "bundle-relative slash path", "unsafe path", ErrInvalidRoomReplayBundle)
	}
	if filepath.IsAbs(value) || path.IsAbs(value) || filepath.VolumeName(value) != "" || strings.HasPrefix(value, "//") {
		return "", "", newRoomReplayBundleError(RoomReplayBundleMismatch, "artifact.path", value, "bundle-relative path", "absolute path", ErrInvalidRoomReplayBundle)
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if part == ".." {
			return "", "", newRoomReplayBundleError(RoomReplayBundleMismatch, "artifact.path", value, "path without traversal", ".. component", ErrInvalidRoomReplayBundle)
		}
	}
	normalized := path.Clean(value)
	if normalized == "." || normalized == ".." || strings.HasPrefix(normalized, "../") || strings.Contains(normalized, ":") {
		return "", "", newRoomReplayBundleError(RoomReplayBundleMismatch, "artifact.path", value, "non-empty bundle-relative path", normalized, ErrInvalidRoomReplayBundle)
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", "", newRoomReplayBundleError(RoomReplayBundleMismatch, "artifact.path", value, "bundle root", err.Error(), err)
	}
	joined := filepath.Join(root, filepath.FromSlash(normalized))
	rel, err := filepath.Rel(root, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", newRoomReplayBundleError(RoomReplayBundleMismatch, "artifact.path", value, "path confined to bundle", rel, ErrInvalidRoomReplayBundle)
	}
	if err := rejectRoomReplaySymlinkComponents(root, normalized); err != nil {
		return "", "", err
	}
	return joined, normalized, nil
}

func rejectRoomReplaySymlinkComponents(root, normalized string) error {
	current := root
	for _, component := range strings.Split(normalized, "/") {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return newRoomReplayBundleError(RoomReplayBundleMismatch, "artifact.path", normalized, "inspectable path components", err.Error(), err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return newRoomReplayBundleError(RoomReplayBundleMismatch, "artifact.path", normalized, "non-symlink artifact path", "symlink component", ErrInvalidRoomReplayBundle)
		}
	}
	return nil
}

func roomReplayPathKey(value string) string {
	return path.Clean(filepath.ToSlash(strings.TrimSpace(value)))
}

func validateRoomReplayArtifacts(root string, refs []roomReplayArtifactRef, metadata map[string]roomReplayArtifactRef) ([]RoomReplayArtifact, map[string]RoomReplayArtifact, error) {
	validated := make([]RoomReplayArtifact, 0, len(refs))
	byPath := make(map[string]RoomReplayArtifact, len(refs))
	for _, ref := range refs {
		absolute, normalized, err := safeRoomReplayPath(root, ref.Path)
		if err != nil {
			return nil, nil, err
		}
		if normalized == roomReplayPathKey(RoomReplayBundleManifestPath) {
			return nil, nil, newRoomReplayBundleError(RoomReplayBundleMismatch, ref.Field, normalized, "artifact distinct from manifest", "run-manifest.json", ErrInvalidRoomReplayBundle)
		}
		if existing, exists := byPath[normalized]; exists {
			return nil, nil, newRoomReplayBundleError(RoomReplayBundleMismatch, "artifact ownership", normalized, "one logical owner", existing.Owner+" and "+ref.Owner, ErrInvalidRoomReplayBundle)
		}
		declared, ok := metadata[normalized]
		if !ok {
			declared = ref
		}
		if declared.Size == nil || declared.SHA256 == "" {
			return nil, nil, newRoomReplayBundleError(RoomReplayBundleIncomplete, ref.Field, normalized, "declared size and sha256", "missing", ErrRoomReplayBundleIncomplete)
		}
		if len(declared.SHA256) != sha256.Size*2 {
			return nil, nil, newRoomReplayBundleError(RoomReplayBundleMismatch, ref.Field, normalized, "64-character sha256 hex digest", declared.SHA256, ErrInvalidRoomReplayBundle)
		}
		if _, err := hex.DecodeString(declared.SHA256); err != nil {
			return nil, nil, newRoomReplayBundleError(RoomReplayBundleMismatch, ref.Field, normalized, "sha256 hex digest", declared.SHA256, err)
		}
		info, statErr := os.Lstat(absolute)
		if statErr != nil {
			kind := RoomReplayBundleMismatch
			if errors.Is(statErr, os.ErrNotExist) {
				kind = RoomReplayBundleIncomplete
			}
			return nil, nil, newRoomReplayBundleError(kind, ref.Field, normalized, "declared artifact file", statErr.Error(), statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, nil, newRoomReplayBundleError(RoomReplayBundleMismatch, ref.Field, normalized, "regular file", info.Mode().String(), ErrInvalidRoomReplayBundle)
		}
		actualSize := info.Size()
		if actualSize != *declared.Size {
			kind := RoomReplayBundleMismatch
			if actualSize < *declared.Size {
				kind = RoomReplayBundleIncomplete
			}
			cause := error(ErrInvalidRoomReplayBundle)
			if kind == RoomReplayBundleIncomplete {
				cause = ErrRoomReplayBundleIncomplete
			}
			return nil, nil, newRoomReplayBundleError(kind, ref.Field, normalized, fmt.Sprintf("size %d", *declared.Size), fmt.Sprintf("size %d", actualSize), cause)
		}
		actualDigest, digestErr := roomReplayFileDigest(absolute)
		if digestErr != nil {
			return nil, nil, newRoomReplayBundleError(RoomReplayBundleMismatch, ref.Field, normalized, "readable artifact", digestErr.Error(), digestErr)
		}
		if actualDigest != declared.SHA256 {
			return nil, nil, newRoomReplayBundleError(RoomReplayBundleMismatch, ref.Field, normalized, declared.SHA256, actualDigest, ErrInvalidRoomReplayBundle)
		}
		artifact := RoomReplayArtifact{
			Name:         ref.Name,
			Role:         ref.Role,
			Owner:        ref.Owner,
			Path:         normalized,
			AbsolutePath: absolute,
			Size:         actualSize,
			SHA256:       actualDigest,
		}
		validated = append(validated, artifact)
		byPath[normalized] = artifact
	}
	return validated, byPath, nil
}

func validateRoomReplayInventory(root string, inventory []roomReplayArtifactRef, byPath map[string]RoomReplayArtifact) error {
	for _, entry := range inventory {
		_, normalized, err := safeRoomReplayPath(root, entry.Path)
		if err != nil {
			return err
		}
		if _, ok := byPath[normalized]; !ok {
			return newRoomReplayBundleError(RoomReplayBundleMismatch, entry.Field, normalized, "referenced artifact ownership", "orphan integrity entry", ErrInvalidRoomReplayBundle)
		}
	}
	return nil
}

func roomReplayFileDigest(filename string) (string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validateRoomReplayCaptures(plan *RoomReplayPlan) error {
	for index := range plan.Participants {
		participant := &plan.Participants[index]
		if participant.Kind == "human" {
			continue
		}
		if participant.Capture.AbsolutePath == "" {
			return newRoomReplayBundleError(RoomReplayBundleIncomplete, "participants["+participant.ID+"].capture", "", "provider capture", "missing", ErrRoomReplayBundleIncomplete)
		}
		capture, err := testing.LoadSessionCapture(participant.Capture.AbsolutePath)
		if err != nil {
			return newRoomReplayBundleError(RoomReplayBundleMismatch, "participants["+participant.ID+"].capture", participant.Capture.Path, "valid session capture", err.Error(), err)
		}
		if capture.Version != 0 && capture.Version != testing.SessionCaptureVersion {
			return newRoomReplayBundleError(RoomReplayBundleMismatch, "participants["+participant.ID+"].capture.version", participant.Capture.Path, fmt.Sprintf("%d", testing.SessionCaptureVersion), fmt.Sprintf("%d", capture.Version), ErrInvalidRoomReplayBundle)
		}
		if len(capture.Records) == 0 {
			return newRoomReplayBundleError(RoomReplayBundleIncomplete, "participants["+participant.ID+"].capture.records", participant.Capture.Path, "at least one provider event", "empty", ErrRoomReplayBundleIncomplete)
		}
		if capture.Provider.Name != "" && participant.Provider != "" && !strings.EqualFold(capture.Provider.Name, participant.Provider) {
			return newRoomReplayBundleError(RoomReplayBundleMismatch, "participants["+participant.ID+"].provider", participant.Capture.Path, participant.Provider, capture.Provider.Name, ErrInvalidRoomReplayBundle)
		}
		if capture.Provider.Model != "" && participant.Model != "" && capture.Provider.Model != participant.Model {
			return newRoomReplayBundleError(RoomReplayBundleMismatch, "participants["+participant.ID+"].model", participant.Capture.Path, participant.Model, capture.Provider.Model, ErrInvalidRoomReplayBundle)
		}
		if _, err := testing.NewReplayWebSocketDialerFromCapture(capture); err != nil {
			return newRoomReplayBundleError(RoomReplayBundleMismatch, "participants["+participant.ID+"].capture", participant.Capture.Path, "provider websocket payloads", err.Error(), err)
		}
	}
	return nil
}

func loadRoomReplayTimeline(artifact RoomReplayArtifact, participants map[string]struct{}, declared map[string]RoomReplayArtifact, clockBase, startedAt, endedAt time.Time) ([]RoomReplayTimelineEvent, error) {
	file, err := os.Open(artifact.AbsolutePath)
	if err != nil {
		return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, "room_timeline", artifact.Path, "readable timeline", err.Error(), err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	result := make([]RoomReplayTimelineEvent, 0)
	var previousOffset, previousSequence int64
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := append([]byte(nil), scanner.Bytes()...)
		if strings.TrimSpace(string(line)) == "" {
			continue
		}
		object, err := roomReplayObject(line)
		if err != nil {
			return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, fmt.Sprintf("room_timeline.line[%d]", lineNumber), artifact.Path, "JSON object", "invalid", err)
		}
		eventType, present, err := firstRoomReplayStringField(object, nil, "event_type", "type", "event")
		if err != nil || !present || strings.TrimSpace(eventType) == "" {
			return nil, newRoomReplayBundleError(RoomReplayBundleIncomplete, fmt.Sprintf("room_timeline.line[%d].type", lineNumber), artifact.Path, "non-empty event type", "missing or invalid", errOrDefault(err, ErrRoomReplayBundleIncomplete))
		}
		offset, offsetPresent, offsetErr := firstRoomReplayIntField(object, nil, "monotonic_offset_ms", "offset_ms", "offset")
		if offsetErr != nil || !offsetPresent || offset < 0 {
			return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, fmt.Sprintf("room_timeline.line[%d].monotonic_offset_ms", lineNumber), artifact.Path, "non-negative offset", "invalid or missing", errOrDefault(offsetErr, ErrInvalidRoomReplayBundle))
		}
		unixMS, unixPresent, unixErr := firstRoomReplayIntField(object, nil, "unix_ms", "timestamp_ms")
		if unixErr != nil || !unixPresent || unixMS < 0 {
			return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, fmt.Sprintf("room_timeline.line[%d].unix_ms", lineNumber), artifact.Path, "non-negative Unix milliseconds", "invalid or missing", errOrDefault(unixErr, ErrInvalidRoomReplayBundle))
		}
		sequence, sequencePresent, sequenceErr := firstRoomReplayIntField(object, nil, "sequence", "room_sequence")
		if sequenceErr != nil || !sequencePresent {
			sequence = len(result)
		}
		if sequence < 0 {
			return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, fmt.Sprintf("room_timeline.line[%d].sequence", lineNumber), artifact.Path, "non-negative sequence", fmt.Sprintf("%d", sequence), ErrInvalidRoomReplayBundle)
		}
		if len(result) > 0 && (int64(offset) < previousOffset || int64(offset) == previousOffset && int64(sequence) <= previousSequence) {
			return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, fmt.Sprintf("room_timeline.line[%d]", lineNumber), artifact.Path, "ordered by offset and increasing sequence", fmt.Sprintf("offset=%d sequence=%d after offset=%d sequence=%d", offset, sequence, previousOffset, previousSequence), ErrInvalidRoomReplayBundle)
		}
		expectedUnix := clockBase.UnixMilli() + int64(offset)
		if int64(unixMS) != expectedUnix {
			return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, fmt.Sprintf("room_timeline.line[%d].unix_ms", lineNumber), artifact.Path, fmt.Sprintf("%d", expectedUnix), fmt.Sprintf("%d", unixMS), ErrInvalidRoomReplayBundle)
		}
		spanMS := endedAt.Sub(clockBase).Milliseconds()
		if int64(offset) > spanMS {
			return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, fmt.Sprintf("room_timeline.line[%d].monotonic_offset_ms", lineNumber), artifact.Path, "offset inside room span", fmt.Sprintf("%d", offset), ErrInvalidRoomReplayBundle)
		}
		if clockBase.Add(time.Duration(offset) * time.Millisecond).Before(startedAt) {
			return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, fmt.Sprintf("room_timeline.line[%d].monotonic_offset_ms", lineNumber), artifact.Path, "offset inside room span", fmt.Sprintf("%d", offset), ErrInvalidRoomReplayBundle)
		}
		participantID := ""
		for _, field := range []string{"participant_id", "participant", "speaker_id", "source_participant_id", "target_participant_id"} {
			if value, ok, valueErr := roomReplayStringField(object, field); ok && valueErr == nil {
				if field == "participant_id" || field == "participant" || field == "speaker_id" {
					participantID = value
				}
				if _, known := participants[value]; !known {
					return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, fmt.Sprintf("room_timeline.line[%d].%s", lineNumber, field), artifact.Path, "declared participant ID", value, ErrInvalidRoomReplayBundle)
				}
			}
		}
		if err := validateRoomReplayTimelineArtifactReferences(object, declared); err != nil {
			return nil, fmt.Errorf("room timeline line %d: %w", lineNumber, err)
		}
		result = append(result, RoomReplayTimelineEvent{Sequence: int64(sequence), OffsetMS: int64(offset), UnixMS: int64(unixMS), Type: strings.TrimSpace(eventType), ParticipantID: participantID, Raw: append(json.RawMessage(nil), line...)})
		previousOffset, previousSequence = int64(offset), int64(sequence)
	}
	if err := scanner.Err(); err != nil {
		return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, "room_timeline", artifact.Path, "readable JSONL", err.Error(), err)
	}
	return result, nil
}

func validateRoomReplayTimelineArtifactReferences(object roomReplayJSONObject, declared map[string]RoomReplayArtifact) error {
	for key, raw := range object {
		normalizedKey := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), ".", "_"))
		isArtifactPath := normalizedKey == "artifact" || normalizedKey == "artifact_ref" || strings.HasSuffix(normalizedKey, "_artifact") || strings.HasSuffix(normalizedKey, "_artifact_ref") || normalizedKey == "audio_path" || normalizedKey == "capture_path"
		if !isArtifactPath {
			continue
		}
		value, ok := decodeRoomReplayString(raw)
		if !ok {
			continue
		}
		if filepath.IsAbs(value) || path.IsAbs(value) || strings.ContainsRune(value, '\\') || strings.ContainsRune(value, '\x00') {
			return newRoomReplayBundleError(RoomReplayBundleMismatch, "room_timeline.artifact", value, "declared bundle-relative artifact path", "unsafe", ErrInvalidRoomReplayBundle)
		}
		normalized := roomReplayPathKey(value)
		for _, component := range strings.Split(value, "/") {
			if component == ".." {
				return newRoomReplayBundleError(RoomReplayBundleMismatch, "room_timeline.artifact", value, "declared bundle-relative artifact path", "traversal", ErrInvalidRoomReplayBundle)
			}
		}
		if normalized == "." || normalized == ".." || strings.HasPrefix(normalized, "../") || strings.Contains(normalized, ":") {
			return newRoomReplayBundleError(RoomReplayBundleMismatch, "room_timeline.artifact", value, "declared bundle-relative artifact path", "traversal", ErrInvalidRoomReplayBundle)
		}
		if _, ok := declared[normalized]; !ok {
			return newRoomReplayBundleError(RoomReplayBundleMismatch, "room_timeline.artifact", value, "declared artifact path", "undeclared", ErrInvalidRoomReplayBundle)
		}
	}
	return nil
}
