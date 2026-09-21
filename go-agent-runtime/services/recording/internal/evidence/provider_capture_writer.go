package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func writeProviderCaptureFromReader(path string, capture gatewaytesting.SessionCapture, reader providerCaptureRecordReader) (returnErr error) {
	if reader == nil {
		return errors.New("provider capture record reader is required")
	}
	if capture.Version == 0 {
		capture.Version = gatewaytesting.SessionCaptureVersion
	}
	if capture.Version != gatewaytesting.SessionCaptureVersion {
		return fmt.Errorf("provider capture schema version %d is unsupported", capture.Version)
	}
	temporary, err := newProviderCaptureTemporary(path)
	if err != nil {
		return err
	}
	removeTemporary := true
	temporaryClosed := false
	defer func() {
		var closeErr error
		if !temporaryClosed {
			closeErr = temporary.Close()
		}
		if removeTemporary {
			removeErr := os.Remove(temporary.Name())
			returnErr = errors.Join(returnErr, closeErr, removeErr)
			return
		}
		returnErr = errors.Join(returnErr, closeErr)
	}()

	digest := sha256.New()
	coverage := io.MultiWriter(temporary, digest)
	integrity, err := writeProviderCaptureCoverage(coverage, digest, capture, reader)
	if err != nil {
		return err
	}
	if err := writeProviderCaptureFooter(temporary, integrity, capture.EndsWithDisconnect); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync provider capture: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close provider capture: %w", err)
	}
	temporaryClosed = true
	if err := os.Rename(temporary.Name(), path); err != nil {
		return fmt.Errorf("publish provider capture stage: %w", err)
	}
	removeTemporary = false
	return nil
}

func writeProviderCaptureCoverage(writer io.Writer, digest hash.Hash, capture gatewaytesting.SessionCapture, reader providerCaptureRecordReader) (gatewaytesting.SessionCaptureIntegrity, error) {
	if err := writeProviderCaptureHeader(writer, capture); err != nil {
		return gatewaytesting.SessionCaptureIntegrity{}, err
	}
	if err := writeProviderCaptureRecords(writer, reader); err != nil {
		return gatewaytesting.SessionCaptureIntegrity{}, err
	}
	if _, err := io.WriteString(writer, "]"); err != nil {
		return gatewaytesting.SessionCaptureIntegrity{}, fmt.Errorf("write provider capture records close: %w", err)
	}
	return providerCaptureStreamIntegrity(digest, capture.EndsWithDisconnect)
}

func writeProviderCaptureRecords(writer io.Writer, reader providerCaptureRecordReader) error {
	previousSequence := 0
	index := 0
	for {
		event, ok, err := reader.Next()
		if err != nil {
			return fmt.Errorf("read provider capture spool: %w", err)
		}
		if !ok {
			return nil
		}
		if err := writeProviderCaptureRecord(writer, event, previousSequence, index); err != nil {
			return err
		}
		previousSequence = event.Sequence
		index++
	}
}

func writeProviderCaptureRecord(writer io.Writer, event gatewaytesting.CapturedSessionEvent, previousSequence, index int) error {
	if err := validateProviderCaptureEvent(event, previousSequence); err != nil {
		return err
	}
	if index > 0 {
		if _, err := io.WriteString(writer, ","); err != nil {
			return fmt.Errorf("write provider capture separator: %w", err)
		}
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode provider capture event %d: %w", event.Sequence, err)
	}
	if _, err := writer.Write(encoded); err != nil {
		return fmt.Errorf("write provider capture event %d: %w", event.Sequence, err)
	}
	return nil
}

func newProviderCaptureTemporary(path string) (*os.File, error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return nil, fmt.Errorf("create provider capture temporary file: %w", err)
	}
	if err := temporary.Chmod(evidenceFileMode); err != nil {
		return nil, errors.Join(fmt.Errorf("protect provider capture temporary file: %w", err), temporary.Close(), os.Remove(temporary.Name()))
	}
	return temporary, nil
}

func writeProviderCaptureHeader(writer io.Writer, capture gatewaytesting.SessionCapture) error {
	if _, err := io.WriteString(writer, `{"version":`); err != nil {
		return fmt.Errorf("write provider capture version: %w", err)
	}
	if err := writeProviderCaptureJSONValue(writer, capture.Version); err != nil {
		return err
	}
	if err := writeProviderCaptureJSONField(writer, `,"provider":`, capture.Provider); err != nil {
		return err
	}
	if err := writeProviderCaptureJSONField(writer, `,"session":`, capture.Session); err != nil {
		return err
	}
	if _, err := io.WriteString(writer, `,"records":[`); err != nil {
		return fmt.Errorf("write provider capture records field: %w", err)
	}
	return nil
}

func validateProviderCaptureEvent(event gatewaytesting.CapturedSessionEvent, previousSequence int) error {
	path := fmt.Sprintf("/records/sequence-%d", event.Sequence)
	switch {
	case event.Sequence <= 0:
		return fmt.Errorf("provider capture record at %s has a non-positive sequence", path)
	case event.Sequence <= previousSequence:
		return fmt.Errorf("provider capture record at %s is out of order", path)
	case event.Direction != gatewaytesting.DirectionClientToServer && event.Direction != gatewaytesting.DirectionServerToClient:
		return fmt.Errorf("provider capture record at %s has an invalid direction", path)
	case event.TimestampMs < 0:
		return fmt.Errorf("provider capture record at %s has a negative timestamp", path)
	case strings.TrimSpace(event.Type) == "":
		return fmt.Errorf("provider capture record at %s has no type", path)
	case event.PayloadType != gatewaytesting.SessionPayloadTypeStreamMessage && event.PayloadType != gatewaytesting.SessionPayloadTypeWebSocketMessage:
		return fmt.Errorf("provider capture record at %s has an invalid payload type", path)
	}
	payload := event.Payload
	if len(payload) == 0 {
		payload = event.Data
	}
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || !json.Valid(trimmed) || bytes.Equal(trimmed, []byte("null")) {
		return fmt.Errorf("provider capture record at %s has an invalid JSON payload", path)
	}
	return nil
}

func providerCaptureStreamIntegrity(digest hash.Hash, endsWithDisconnect bool) (gatewaytesting.SessionCaptureIntegrity, error) {
	if endsWithDisconnect {
		if _, err := io.WriteString(digest, `,"ends_with_disconnect":true`); err != nil {
			return gatewaytesting.SessionCaptureIntegrity{}, fmt.Errorf("write provider capture digest: %w", err)
		}
	}
	if _, err := io.WriteString(digest, "}"); err != nil {
		return gatewaytesting.SessionCaptureIntegrity{}, fmt.Errorf("write provider capture digest: %w", err)
	}
	return gatewaytesting.SessionCaptureIntegrity{
		Algorithm: gatewaytesting.SessionCaptureIntegrityAlgorithm,
		Coverage:  gatewaytesting.SessionCaptureIntegrityCoverage,
		Digest:    fmt.Sprintf("%x", digest.Sum(nil)),
	}, nil
}

func writeProviderCaptureFooter(writer io.Writer, integrity gatewaytesting.SessionCaptureIntegrity, endsWithDisconnect bool) error {
	if err := writeProviderCaptureJSONField(writer, `,"integrity":`, integrity); err != nil {
		return err
	}
	if endsWithDisconnect {
		if _, err := io.WriteString(writer, `,"ends_with_disconnect":true`); err != nil {
			return fmt.Errorf("write provider capture disconnect marker: %w", err)
		}
	}
	if _, err := io.WriteString(writer, "}"); err != nil {
		return fmt.Errorf("write provider capture close: %w", err)
	}
	return nil
}

type streamMessageEnvelope struct {
	Type               string          `json:"type"`
	Role               string          `json:"role,omitempty"`
	ToolCallId         string          `json:"tool_call_id,omitempty"`
	ResponseID         string          `json:"response_id,omitempty"`
	Value              json.RawMessage `json:"value,omitempty"`
	GlobalIndex        int             `json:"global_index,omitempty"`
	ActorProvidedID    string          `json:"actor_provided_id,omitempty"`
	ActorProvidedIndex int             `json:"actor_provided_index,omitempty"`
	ActorStreamID      string          `json:"actor_stream_id,omitempty"`
	ActorID            string          `json:"actor_id,omitempty"`
	LoopPassID         int             `json:"loop_pass_id,omitempty"`
}

func marshalEvidenceStreamMessage(message messages.StreamMessage) (json.RawMessage, error) {
	var value json.RawMessage
	if message.Value != nil {
		encoded, err := json.Marshal(message.Value)
		if err != nil {
			return nil, fmt.Errorf("marshal value: %w", err)
		}
		value = encoded
	}
	return json.Marshal(streamMessageEnvelope{
		Type: string(message.Type), Role: string(message.Role), ToolCallId: message.ToolCallId,
		ResponseID: message.ResponseID, Value: value, GlobalIndex: message.GlobalIndex,
		ActorProvidedID: message.ActorProvidedID, ActorProvidedIndex: message.ActorProvidedIndex,
		ActorStreamID: message.ActorStreamID, ActorID: string(message.ActorID), LoopPassID: message.LoopPassID,
	})
}

func unmarshalEvidenceStreamMessage(data json.RawMessage) (messages.StreamMessage, error) {
	var raw streamMessageEnvelope
	if err := json.Unmarshal(data, &raw); err != nil {
		return messages.StreamMessage{}, fmt.Errorf("unmarshal stream message envelope: %w", err)
	}
	message := messages.StreamMessage{
		Type: messages.StreamMessageType(raw.Type), Role: messages.Role(raw.Role),
		ToolCallId: raw.ToolCallId, ResponseID: raw.ResponseID, GlobalIndex: raw.GlobalIndex,
		ActorProvidedID: raw.ActorProvidedID, ActorProvidedIndex: raw.ActorProvidedIndex,
		ActorStreamID: raw.ActorStreamID, ActorID: messages.ParticipantID(raw.ActorID), LoopPassID: raw.LoopPassID,
	}
	if len(raw.Value) > 0 && string(raw.Value) != "null" {
		value, err := unmarshalEvidenceValue(message.Type, raw.Value)
		if err != nil {
			return messages.StreamMessage{}, err
		}
		message.Value = value
	}
	return message, nil
}

func unmarshalEvidenceValue(kind messages.StreamMessageType, data json.RawMessage) (messages.StreamMessageValue, error) {
	value := newEvidenceConversationValue(kind)
	if value == nil {
		value = newEvidenceLifecycleValue(kind)
	}
	if value == nil {
		value = newEvidenceArtifactValue(kind)
	}
	if value == nil {
		value = newEvidenceAudioValue(kind)
	}
	if value == nil {
		return nil, fmt.Errorf("unknown stream message type: %s", kind)
	}
	if err := json.Unmarshal(data, value); err != nil {
		return nil, fmt.Errorf("unmarshal value for type %s: %w", kind, err)
	}
	return value, nil
}

func newEvidenceConversationValue(kind messages.StreamMessageType) messages.StreamMessageValue {
	switch kind {
	case messages.StreamTypeMessageStart:
		return new(messages.MessageStartValue)
	case messages.StreamTypeMessageEnd:
		return new(messages.MessageEndValue)
	case messages.StreamTypeTextStart:
		return new(messages.TextStartValue)
	case messages.StreamTypeTextDelta:
		return new(messages.TextDeltaValue)
	case messages.StreamTypeTextEnd:
		return new(messages.TextEndValue)
	case messages.StreamTypeToolCallStart:
		return new(messages.ToolCallStartValue)
	case messages.StreamTypeToolCallDelta:
		return new(messages.ToolCallDeltaValue)
	case messages.StreamTypeToolCallEnd:
		return new(messages.ToolCallEndValue)
	case messages.StreamTypeReasoningStart:
		return new(messages.ReasoningStartValue)
	case messages.StreamTypeReasoningDelta:
		return new(messages.ReasoningDeltaValue)
	case messages.StreamTypeReasoningEnd:
		return new(messages.ReasoningEndValue)
	default:
		return nil
	}
}

func newEvidenceLifecycleValue(kind messages.StreamMessageType) messages.StreamMessageValue {
	switch kind {
	case messages.StreamTypePong:
		return new(messages.PongValue)
	case messages.StreamTypeSessionOpen:
		return new(messages.SessionOpenValue)
	case messages.StreamTypeSessionClose:
		return new(messages.SessionCloseValue)
	case messages.StreamTypeSessionCreated:
		return new(messages.SessionCreatedValue)
	case messages.StreamTypeSessionUpdated:
		return new(messages.SessionUpdatedValue)
	case messages.StreamTypeSessionUpdate:
		return new(messages.SessionUpdateValue)
	case messages.StreamTypeResponseCancel:
		return new(messages.ResponseCancelValue)
	case messages.StreamTypeResponseCreate:
		return new(messages.ResponseCreateValue)
	case messages.StreamTypeUsageInfo:
		return new(messages.UsageInfoValue)
	case messages.StreamTypeError:
		return new(messages.ErrorValue)
	case messages.StreamTypeRefusal:
		return new(messages.RefusalValue)
	case messages.StreamTypeLoopEnd:
		return new(messages.LoopEndValue)
	default:
		return nil
	}
}

func newEvidenceArtifactValue(kind messages.StreamMessageType) messages.StreamMessageValue {
	switch kind {
	case messages.StreamTypeImageStart:
		return new(messages.ImageStartValue)
	case messages.StreamTypeImageDelta:
		return new(messages.ImageDeltaValue)
	case messages.StreamTypeImageEnd:
		return new(messages.ImageEndValue)
	case messages.StreamTypeVideoStart:
		return new(messages.VideoStartValue)
	case messages.StreamTypeVideoDelta:
		return new(messages.VideoDeltaValue)
	case messages.StreamTypeVideoEnd:
		return new(messages.VideoEndValue)
	case messages.StreamTypeFileStart:
		return new(messages.FileStartValue)
	case messages.StreamTypeFileDelta:
		return new(messages.FileDeltaValue)
	case messages.StreamTypeFileEnd:
		return new(messages.FileEndValue)
	case messages.StreamTypeEmbeddingStart:
		return new(messages.EmbeddingStartValue)
	case messages.StreamTypeEmbeddingDelta:
		return new(messages.EmbeddingDeltaValue)
	case messages.StreamTypeEmbeddingEnd:
		return new(messages.EmbeddingEndValue)
	default:
		return nil
	}
}

func newEvidenceAudioValue(kind messages.StreamMessageType) messages.StreamMessageValue {
	switch kind {
	case messages.StreamTypeAudioStart:
		return new(messages.AudioStartValue)
	case messages.StreamTypeAudioDelta:
		return new(messages.AudioDeltaValue)
	case messages.StreamTypeAudioEnd:
		return new(messages.AudioEndValue)
	case messages.StreamTypeVADSpeechStarted:
		return new(messages.VADSpeechStartedValue)
	case messages.StreamTypeVADSpeechStopped:
		return new(messages.VADSpeechStoppedValue)
	case messages.StreamTypeTranscriptStart:
		return new(messages.TranscriptStartValue)
	case messages.StreamTypeTranscriptDelta:
		return new(messages.TranscriptDeltaValue)
	case messages.StreamTypeTranscriptEnd:
		return new(messages.TranscriptEndValue)
	case messages.StreamTypeInputItemAdded:
		return new(messages.InputItemAddedValue)
	default:
		return nil
	}
}
