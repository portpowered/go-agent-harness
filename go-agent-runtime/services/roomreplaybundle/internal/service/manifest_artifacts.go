package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	roomReplayArtifactRoleWAV         = "wav"
	roomReplayArtifactRoleDiagnostics = "diagnostics"
	roomReplayArtifactRoleDeltas      = "deltas"
	roomReplayArtifactRoleSentPCM     = "sent_pcm"
	roomReplayArtifactRoleReceivedPCM = "received_pcm"
	roomReplayArtifactRoleEvents      = "events"
	roomReplayArtifactRoleCapture     = "capture"
)

func roomReplayRequiredParticipantArtifactRoles() []string {
	return []string{
		roomReplayArtifactRoleWAV,
		roomReplayArtifactRoleDiagnostics,
		roomReplayArtifactRoleDeltas,
		roomReplayArtifactRoleSentPCM,
		roomReplayArtifactRoleReceivedPCM,
		roomReplayArtifactRoleEvents,
	}
}

func parseRoomReplayParticipantArtifacts(raw json.RawMessage, field string) (map[string]roomReplayArtifactRef, error) {
	if strings.HasPrefix(strings.TrimSpace(string(raw)), "[") {
		return parseRoomReplayParticipantArtifactArray(raw, field)
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, field, "", "artifact map", "invalid", err)
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make(map[string]roomReplayArtifactRef, len(keys))
	for _, key := range keys {
		role := normalizeRoomReplayArtifactRole(key)
		if role == "" {
			role = key
		}
		artifact, err := parseRoomReplayArtifactRef(values[key], field+"."+key, role)
		if err != nil {
			return nil, err
		}
		if _, exists := result[role]; exists {
			return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, field, artifact.Path, "unique artifact role", role, ErrInvalidRoomReplayBundle)
		}
		result[role] = artifact
	}
	return result, nil
}

func parseRoomReplayParticipantArtifactArray(raw json.RawMessage, field string) (map[string]roomReplayArtifactRef, error) {
	var values []roomReplayJSONObject
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, field, "", "artifact array", "invalid", err)
	}
	result := make(map[string]roomReplayArtifactRef, len(values))
	for index, object := range values {
		role, _, _ := firstRoomReplayStringField(object, nil, "role", "name", "type", "kind")
		role = normalizeRoomReplayArtifactRole(role)
		if role == "" {
			return nil, newRoomReplayBundleError(RoomReplayBundleIncomplete, fmt.Sprintf("%s[%d].role", field, index), "", "artifact role", "missing", ErrRoomReplayBundleIncomplete)
		}
		artifact, err := parseRoomReplayArtifactRef(mustMarshal(object), fmt.Sprintf("%s[%d]", field, index), role)
		if err != nil {
			return nil, err
		}
		if _, exists := result[role]; exists {
			return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, field, artifact.Path, "unique artifact role", role, ErrInvalidRoomReplayBundle)
		}
		result[role] = artifact
	}
	return result, nil
}

func inferRoomReplayParticipantArtifacts(participant *roomReplayParticipantRef, inventory []roomReplayArtifactRef) error {
	for _, entry := range inventory {
		role := normalizeRoomReplayArtifactRole(entry.Name)
		if !isRoomReplayParticipantArtifactRole(role) {
			role = normalizeRoomReplayArtifactRole(entry.Path)
		}
		if role == "" || role == "timeline" || role == "mix" {
			continue
		}
		if !roomReplayInventoryNameBelongsToParticipant(entry.Name, participant.ID, role) && !roomReplayInventoryNameBelongsToParticipant(entry.Path, participant.ID, role) {
			continue
		}
		if _, exists := participant.Artifacts[role]; exists {
			continue
		}
		candidate := entry
		candidate.Role, candidate.Field = role, "artifacts."+entry.Name
		participant.Artifacts[role] = candidate
	}
	return nil
}

func roomReplayInventoryNameBelongsToParticipant(name, id, role string) bool {
	name, id = strings.ToLower(strings.TrimSpace(name)), strings.ToLower(strings.TrimSpace(id))
	if name == "" || id == "" || !roomReplayContainsParticipantID(name, id) {
		return false
	}
	for _, suffix := range []string{"." + role, "_" + role, "-" + role, "/" + role} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	for _, suffix := range roomReplayParticipantArtifactSuffixes(role) {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func roomReplayParticipantArtifactSuffixes(role string) []string {
	return map[string][]string{
		roomReplayArtifactRoleWAV:         {"/agent.wav", ".wav"},
		roomReplayArtifactRoleDiagnostics: {"/diagnostics.jsonl", ".diagnostics.jsonl"},
		roomReplayArtifactRoleDeltas:      {"/deltas.jsonl", ".deltas.jsonl"},
		roomReplayArtifactRoleSentPCM:     {"/sent.pcm", ".sent.pcm"},
		roomReplayArtifactRoleReceivedPCM: {"/received.pcm", ".received.pcm"},
		roomReplayArtifactRoleEvents:      {"/events.jsonl", ".events.jsonl"},
		roomReplayArtifactRoleCapture:     {".session.json", "/capture.json"},
	}[role]
}

func roomReplayContainsParticipantID(name, id string) bool {
	for start := 0; start < len(name); {
		index := strings.Index(name[start:], id)
		if index < 0 {
			return false
		}
		index += start
		after := index + len(id)
		if (index == 0 || !isRoomReplayIdentifierByte(name[index-1])) && (after == len(name) || !isRoomReplayIdentifierByte(name[after])) {
			return true
		}
		start = index + 1
	}
	return false
}

func isRoomReplayIdentifierByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

func isRoomReplayParticipantArtifactRole(role string) bool {
	switch role {
	case roomReplayArtifactRoleWAV, roomReplayArtifactRoleDiagnostics, roomReplayArtifactRoleDeltas, roomReplayArtifactRoleSentPCM, roomReplayArtifactRoleReceivedPCM, roomReplayArtifactRoleEvents, roomReplayArtifactRoleCapture:
		return true
	default:
		return false
	}
}

func parseRoomReplayArtifacts(object roomReplayJSONObject, inventory []roomReplayArtifactRef) ([]roomReplayArtifactRef, error) {
	result := make([]roomReplayArtifactRef, 0, 2)
	for _, spec := range []struct {
		role string
		keys []string
	}{
		{"timeline", []string{"room_timeline", "room-timeline", "timeline", "room_timeline_path", "timeline_path", "room_timeline_artifact"}},
		{"mix", []string{"room_mix", "room-mix", "mix", "room_mix_path", "mix_path", "room_mix_artifact"}},
	} {
		ref, found, err := parseRoomReplayRoomArtifact(object, inventory, spec.role, spec.keys)
		if err != nil {
			return nil, err
		}
		if found {
			result = append(result, ref)
		}
	}
	return result, nil
}

func parseRoomReplayRoomArtifact(object roomReplayJSONObject, inventory []roomReplayArtifactRef, role string, keys []string) (roomReplayArtifactRef, bool, error) {
	for _, key := range keys {
		if raw, ok := roomReplayRawField(object, key); ok {
			ref, err := parseRoomReplayArtifactRef(raw, key, role)
			return ref, true, err
		}
	}
	for _, entry := range inventory {
		if normalizeRoomReplayArtifactRole(entry.Name) == role || strings.Contains(normalizeRoomReplayArtifactRole(entry.Name), "room_"+role) {
			entry.Role, entry.Field = role, "artifacts."+entry.Name
			return entry, true, nil
		}
	}
	return roomReplayArtifactRef{}, false, nil
}

func parseRoomReplayArtifactRef(raw json.RawMessage, field, role string) (roomReplayArtifactRef, error) {
	artifact := roomReplayArtifactRef{Field: field, Role: normalizeRoomReplayArtifactRole(role), Name: role}
	if len(raw) == 0 || string(raw) == "null" {
		return artifact, newRoomReplayBundleError(RoomReplayBundleIncomplete, field, "", "artifact reference", "null", ErrRoomReplayBundleIncomplete)
	}
	if value, ok := decodeRoomReplayString(raw); ok {
		artifact.Path = strings.TrimSpace(value)
		return artifact, nil
	}
	object, err := roomReplayObject(raw)
	if err != nil {
		return artifact, newRoomReplayBundleError(RoomReplayBundleMismatch, field, "", "path string or artifact object", "invalid", err)
	}
	pathValue, present, err := firstRoomReplayStringField(object, nil, "path", "relative_path", "file", "filename")
	if err != nil || !present {
		return artifact, newRoomReplayBundleError(RoomReplayBundleIncomplete, field+".path", "", "bundle-relative path", "missing or invalid", errOrDefault(err, ErrRoomReplayBundleIncomplete))
	}
	artifact.Path = strings.TrimSpace(pathValue)
	if size, present, err := firstRoomReplayIntField(object, nil, "size", "bytes", "byte_size", "byte_count", "declared_size"); present {
		if err != nil || size < 0 {
			return artifact, newRoomReplayBundleError(RoomReplayBundleMismatch, field+".size", artifact.Path, "non-negative integer", "invalid", errOrDefault(err, ErrInvalidRoomReplayBundle))
		}
		artifact.Size = int64Pointer(size)
	}
	if digest, present, err := firstRoomReplayStringField(object, nil, "sha256", "sha_256", "sha256_digest", "digest"); present {
		if err != nil {
			return artifact, newRoomReplayBundleError(RoomReplayBundleMismatch, field+".sha256", artifact.Path, "sha256 hex digest", "invalid", err)
		}
		artifact.SHA256 = normalizeRoomReplayDigest(digest)
	}
	return artifact, nil
}

func findRoomReplayArtifact(artifacts []RoomReplayArtifact, owner string) (RoomReplayArtifact, bool) {
	for _, artifact := range artifacts {
		if artifact.Owner == owner {
			return artifact, true
		}
	}
	return RoomReplayArtifact{}, false
}

func artifactPathByRole(artifacts []RoomReplayArtifact, owner string) string {
	artifact, ok := findRoomReplayArtifact(artifacts, owner)
	if !ok {
		return ""
	}
	return artifact.AbsolutePath
}

func normalizeRoomReplayArtifactRole(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.NewReplacer("-", "_", ".", "_", " ", "_", "/", "_").Replace(normalized)
	if strings.Contains(normalized, "room_mix") || normalized == "mix" {
		return "mix"
	}
	if isRoomReplayWAVRole(normalized) {
		return roomReplayArtifactRoleWAV
	}
	if strings.Contains(normalized, "diagnostic") {
		return roomReplayArtifactRoleDiagnostics
	}
	if strings.Contains(normalized, "delta") {
		return roomReplayArtifactRoleDeltas
	}
	if isRoomReplayPCMRole(normalized, "sent") {
		return roomReplayArtifactRoleSentPCM
	}
	if isRoomReplayPCMRole(normalized, "received") {
		return roomReplayArtifactRoleReceivedPCM
	}
	if strings.Contains(normalized, "event") {
		return roomReplayArtifactRoleEvents
	}
	if strings.Contains(normalized, "capture") || strings.Contains(normalized, "replay") || strings.Contains(normalized, "session") {
		return roomReplayArtifactRoleCapture
	}
	if strings.Contains(normalized, "timeline") {
		return "timeline"
	}
	if strings.Contains(normalized, "mix") {
		return "mix"
	}
	return normalized
}

func isRoomReplayWAVRole(value string) bool {
	return value == "wav" || strings.Contains(value, "legacy_wav") || strings.HasSuffix(value, "_wav") || strings.HasSuffix(value, "_audio")
}

func isRoomReplayPCMRole(value, side string) bool {
	return strings.Contains(value, side) && (strings.Contains(value, "pcm") || value == side)
}
