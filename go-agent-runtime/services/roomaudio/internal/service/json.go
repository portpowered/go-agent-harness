package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomaudio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
)

const (
	maxRoomReplayManifestBytes               = 8 << 20
	maxRoomReplayArtifactBytes               = 64 << 20
	roomReplayJSONLScannerInitialBufferBytes = 64 << 10
	roomReplayJSONLScannerMaxTokenBytes      = 4 << 20
)

func newRoomReplayBundleError(kind roomReplayBundleErrorKind, field, artifact, expected, actual string, cause error) error {
	if kind == "" {
		kind = RoomReplayBundleMismatch
	}
	var replayCause error
	if kind == RoomReplayBundleIncomplete {
		replayCause = gateway.NewReplayIncompleteError(expected, actual, cause)
	} else {
		replayCause = gateway.NewReplayMismatchError(expected, actual, cause)
	}
	return &roomaudioBundleError{
		Kind:     kind,
		Field:    field,
		Artifact: artifact,
		Expected: expected,
		Actual:   actual,
		Err:      replayCause,
	}
}

// roomReplayBundleErrorKind keeps the service implementation independent of
// the public type's string alias while retaining the exact public value.
type roomReplayBundleErrorKind = roomaudio.RoomReplayBundleErrorKind

type roomaudioBundleError = roomaudio.RoomReplayBundleError

func roomReplayObject(raw json.RawMessage) (roomReplayJSONObject, error) {
	var object roomReplayJSONObject
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("expected JSON object")
	}
	return object, nil
}

func roomReplayRawField(object roomReplayJSONObject, names ...string) (json.RawMessage, bool) {
	for _, name := range names {
		if raw, ok := object[name]; ok {
			return raw, true
		}
	}
	return nil, false
}

func roomReplayStringField(object roomReplayJSONObject, names ...string) (string, bool, error) {
	raw, ok := roomReplayRawField(object, names...)
	if !ok {
		return "", false, nil
	}
	value, ok := decodeRoomReplayString(raw)
	if !ok {
		return "", true, errors.New("expected string")
	}
	return strings.TrimSpace(value), true, nil
}

func firstRoomReplayStringField(primary, fallback roomReplayJSONObject, names ...string) (string, bool, error) {
	if value, present, err := roomReplayStringField(primary, names...); present || err != nil {
		return value, present, err
	}
	return roomReplayStringField(fallback, names...)
}

func decodeRoomReplayString(raw json.RawMessage) (string, bool) {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func errOrDefault(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

func findRoomReplayArtifact(artifacts []RoomReplayArtifact, owner string) (RoomReplayArtifact, bool) {
	for _, artifact := range artifacts {
		if artifact.Owner == owner {
			return artifact, true
		}
	}
	return RoomReplayArtifact{}, false
}

func readRoomReplayPath(path string, maxBytes int64, field string) (data []byte, retErr error) {
	if strings.TrimSpace(path) == "" {
		return nil, roomReplayAudioIncomplete(field, "", "readable bounded file", "missing path", ErrRoomReplayBundleIncomplete)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, roomReplayAudioIncomplete(field, path, "readable bounded file", err.Error(), err)
	}
	defer func() {
		if err := file.Close(); err != nil && retErr == nil {
			data = nil
			retErr = roomReplayAudioIncomplete(field, path, "closable bounded file", err.Error(), err)
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, roomReplayAudioIncomplete(field, path, "stat-able bounded file", err.Error(), err)
	}
	if info.Size() < 0 || info.Size() > maxBytes {
		return nil, roomReplayAudioMismatch(field, path, fmt.Sprintf("file no larger than %d bytes", maxBytes), fmt.Sprintf("%d bytes", info.Size()), nil)
	}
	data, err = io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, roomReplayAudioIncomplete(field, path, "readable bounded file", err.Error(), err)
	}
	if int64(len(data)) > maxBytes {
		return nil, roomReplayAudioMismatch(field, path, fmt.Sprintf("file no larger than %d bytes", maxBytes), fmt.Sprintf("more than %d bytes", maxBytes), nil)
	}
	return data, nil
}

func readRoomReplayArtifact(artifact RoomReplayArtifact, maxBytes int64, field string) ([]byte, error) {
	if artifact.Size < 0 || artifact.Size > maxBytes {
		return nil, roomReplayAudioMismatch(field, artifact.Path, fmt.Sprintf("artifact no larger than %d bytes", maxBytes), fmt.Sprintf("%d bytes", artifact.Size), nil)
	}
	data, err := readRoomReplayPath(artifact.AbsolutePath, maxBytes, field)
	if err != nil {
		return nil, err
	}
	if artifact.Empty && len(data) != 0 {
		return nil, roomReplayAudioMismatch(field, artifact.Path, "empty artifact", fmt.Sprintf("%d bytes", len(data)), nil)
	}
	if artifact.Size > 0 && int64(len(data)) != artifact.Size {
		return nil, roomReplayAudioMismatch(field, artifact.Path, fmt.Sprintf("size %d", artifact.Size), fmt.Sprintf("size %d", len(data)), nil)
	}
	if strings.TrimSpace(artifact.SHA256) != "" {
		digest := sha256.Sum256(data)
		actual := hex.EncodeToString(digest[:])
		if !strings.EqualFold(strings.TrimSpace(artifact.SHA256), actual) {
			return nil, roomReplayAudioMismatch(field, artifact.Path, strings.TrimSpace(artifact.SHA256), actual, nil)
		}
	}
	return data, nil
}

func cloneRoomReplayArtifact(artifact RoomReplayArtifact) RoomReplayArtifact {
	return artifact
}

func cloneRoomReplayPlan(plan RoomReplayPlan) RoomReplayPlan {
	plan.Participants = append([]RoomReplayParticipant(nil), plan.Participants...)
	for index := range plan.Participants {
		plan.Participants[index].Capture = cloneRoomReplayArtifact(plan.Participants[index].Capture)
		plan.Participants[index].Artifacts = append([]RoomReplayArtifact(nil), plan.Participants[index].Artifacts...)
		for artifactIndex := range plan.Participants[index].Artifacts {
			plan.Participants[index].Artifacts[artifactIndex] = cloneRoomReplayArtifact(plan.Participants[index].Artifacts[artifactIndex])
		}
	}
	plan.Artifacts = append([]RoomReplayArtifact(nil), plan.Artifacts...)
	for index := range plan.Artifacts {
		plan.Artifacts[index] = cloneRoomReplayArtifact(plan.Artifacts[index])
	}
	plan.Timeline = append([]RoomReplayTimelineEvent(nil), plan.Timeline...)
	for index := range plan.Timeline {
		plan.Timeline[index].Raw = append(json.RawMessage(nil), plan.Timeline[index].Raw...)
	}
	return plan
}
