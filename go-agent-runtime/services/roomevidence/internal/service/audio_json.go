package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence/internal/admission"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence/internal/pathguard"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"io"
	"os"
	"strings"
	"time"
)

const (
	maxRoomReplayManifestBytes               = admission.MaxManifestBytes
	maxRoomReplayArtifactBytes               = 64 << 20
	roomReplayJSONLScannerInitialBufferBytes = 64 << 10
	roomReplayJSONLScannerMaxTokenBytes      = 4 << 20
	maxRedactedJSONDepth                     = 16
)

func newRoomReplayBundleError(kind roomReplayBundleErrorKind, field, artifact, expected, actual string, cause error) error {
	if kind == "" {
		kind = BundleMismatch
	}
	var replayCause error
	var classifications []error
	if kind == BundleIncomplete {
		replayCause = gateway.NewReplayIncompleteError(expected, actual, cause)
		classifications = []error{roomevidence.ErrRoomReplayBundleIncomplete, providers.ErrReplayIncomplete}
	} else {
		replayCause = gateway.NewReplayMismatchError(expected, actual, cause)
		classifications = []error{roomevidence.ErrInvalidRoomReplayBundle, providers.ErrReplayMismatch}
	}
	return &roomevidenceBundleError{
		Kind:     kind,
		Field:    field,
		Artifact: artifact,
		Expected: expected,
		Actual:   actual,
		Err:      &roomReplayBundleCause{cause: replayCause, classifications: classifications},
	}
}

type roomReplayBundleCause struct {
	cause           error
	classifications []error
}

func (e *roomReplayBundleCause) Error() string {
	if e == nil || e.cause == nil {
		return "<nil>"
	}
	return e.cause.Error()
}

func (e *roomReplayBundleCause) Unwrap() []error {
	if e == nil {
		return nil
	}
	errorsToUnwrap := make([]error, 0, len(e.classifications)+1)
	if e.cause != nil {
		errorsToUnwrap = append(errorsToUnwrap, e.cause)
	}
	return append(errorsToUnwrap, e.classifications...)
}

// roomReplayBundleErrorKind keeps the service implementation independent of
// the public type's string alias while retaining the exact public value.
type roomReplayBundleErrorKind = roomevidence.BundleErrorKind

type roomevidenceBundleError = roomevidence.BundleError

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

func readRoomReplayPath(root, path string, maxBytes int64, field string) (data []byte, retErr error) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(path) == "" {
		return nil, roomReplayAudioIncomplete(field, "", "readable bounded file", "missing path", ErrRoomReplayBundleIncomplete)
	}
	if err := pathguard.ValidateNoSymlink(root, path); err != nil {
		return nil, roomReplayAudioMismatch(field, "", "bundle-local non-symlink file", "invalid path", err)
	}
	if err := pathguard.ValidateRegularFile(path); err != nil {
		if os.IsNotExist(err) {
			return nil, roomReplayAudioIncomplete(field, "", "readable bounded file", "unavailable", err)
		}
		return nil, roomReplayAudioMismatch(field, "", "bundle-local regular file", "special file", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, roomReplayAudioIncomplete(field, "", "readable bounded file", "unavailable", err)
	}
	defer func() {
		if err := file.Close(); err != nil && retErr == nil {
			data = nil
			retErr = roomReplayAudioIncomplete(field, path, "closable bounded file", err.Error(), err)
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, roomReplayAudioIncomplete(field, "", "stat-able bounded file", "unavailable", err)
	}
	if info.Size() < 0 || info.Size() > maxBytes {
		return nil, roomReplayAudioMismatch(field, path, fmt.Sprintf("file no larger than %d bytes", maxBytes), fmt.Sprintf("%d bytes", info.Size()), nil)
	}
	data, err = io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, roomReplayAudioIncomplete(field, "", "readable bounded file", "unavailable", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, roomReplayAudioMismatch(field, path, fmt.Sprintf("file no larger than %d bytes", maxBytes), fmt.Sprintf("more than %d bytes", maxBytes), nil)
	}
	return data, nil
}

func readRoomReplayArtifact(root string, artifact RoomReplayArtifact, maxBytes int64, field string) ([]byte, error) {
	if artifact.Size < 0 || artifact.Size > maxBytes {
		return nil, roomReplayAudioMismatch(field, artifact.Path, fmt.Sprintf("artifact no larger than %d bytes", maxBytes), fmt.Sprintf("%d bytes", artifact.Size), nil)
	}
	data, err := readRoomReplayPath(root, artifact.AbsolutePath, maxBytes, field)
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

type streamMessageJSON struct {
	Type               string          `json:"type"`
	Role               string          `json:"role,omitempty"`
	ToolCallID         string          `json:"tool_call_id,omitempty"`
	ResponseID         string          `json:"response_id,omitempty"`
	Value              json.RawMessage `json:"value,omitempty"`
	GlobalIndex        int             `json:"global_index,omitempty"`
	ActorProvidedID    string          `json:"actor_provided_id,omitempty"`
	ActorProvidedIndex int             `json:"actor_provided_index,omitempty"`
	ActorStreamID      string          `json:"actor_stream_id,omitempty"`
	ActorID            string          `json:"actor_id,omitempty"`
	LoopPassID         int             `json:"loop_pass_id,omitempty"`
}

func marshalStreamMessage(message messages.StreamMessage) ([]byte, error) {
	var value json.RawMessage
	if message.Value != nil {
		encoded, err := json.Marshal(message.Value)
		if err != nil {
			return nil, fmt.Errorf("marshal stream value: %w", err)
		}
		value = encoded
	}
	return json.Marshal(streamMessageJSON{
		Type: string(message.Type), Role: string(message.Role), ToolCallID: message.ToolCallId,
		ResponseID: message.ResponseID, Value: value, GlobalIndex: message.GlobalIndex,
		ActorProvidedID: message.ActorProvidedID, ActorProvidedIndex: message.ActorProvidedIndex,
		ActorStreamID: message.ActorStreamID, ActorID: string(message.ActorID), LoopPassID: message.LoopPassID,
	})
}

func stampJSON(data []byte, clock clockState) ([]byte, error) {
	offset, unixMS := clock.now()
	return stampJSONFields(data, offset, unixMS)
}

func stampJSONAt(data []byte, clock clockState, at time.Time) ([]byte, error) {
	offset := at.Sub(clock.start)
	if offset < 0 {
		offset = 0
	}
	return stampJSONFields(data, offset, at.UTC().UnixMilli())
}

func stampJSONFields(data []byte, offset time.Duration, unixMS int64) ([]byte, error) {
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("decode JSONL record for wall-clock stamping: %w", err)
	}
	if value == nil {
		value = make(map[string]any, 2)
	}
	value["t_offset_ms"] = offsetMillis(offset)
	value["t_unix_ms"] = unixMS
	return json.Marshal(value)
}

func redactJSON(data []byte, secrets []string) []byte {
	if len(data) == 0 {
		return data
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return []byte(redactText(string(data), secrets))
	}
	value = redactValue(value, secrets)
	encoded, err := json.Marshal(value)
	if err != nil {
		return data
	}
	return encoded
}

func redactValue(value any, secrets []string) any {
	return redactValueAtDepth(value, secrets, 0)
}

func redactValueAtDepth(value any, secrets []string, depth int) any {
	if depth >= maxRedactedJSONDepth {
		return redactedMarker
	}
	switch typed := value.(type) {
	case string:
		return redactText(typed, secrets)
	case []any:
		for index := range typed {
			typed[index] = redactValueAtDepth(typed[index], secrets, depth+1)
		}
	case map[string]any:
		for key, nested := range typed {
			if sensitiveJSONKey(key) {
				typed[key] = redactedMarker
				continue
			}
			if text, ok := nested.(string); ok && jsonPayloadField(key) {
				typed[key] = redactJSONPayload(text, secrets, depth+1)
				continue
			}
			typed[key] = redactValueAtDepth(nested, secrets, depth+1)
		}
	}
	return value
}

func redactText(value string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, redactedMarker)
		}
	}
	return value
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), values...)
}

func cloneFields(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func cloneStatus(status *transcript.RecordingStatus) *transcript.RecordingStatus {
	if status == nil {
		return nil
	}
	clone := *status
	return &clone
}

func durationString(value time.Duration) string {
	if value <= 0 {
		return ""
	}
	return value.String()
}

func formatOffset(start time.Time, at time.Time) float64 {
	if at.Before(start) {
		return 0
	}
	return offsetMillis(at.Sub(start))
}
