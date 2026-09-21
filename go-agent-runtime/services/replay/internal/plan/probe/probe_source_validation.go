package probe

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

type sourceValidationError struct {
	file      string
	fieldPath string
	reason    string
}

func (e sourceValidationError) Error() string {
	if e.file != "" {
		return fmt.Sprintf("%s: %s: %s", e.file, e.fieldPath, e.reason)
	}
	return fmt.Sprintf("%s: %s", e.fieldPath, e.reason)
}

func validateCaptureSource(file string, capture gatewaytesting.SessionCapture) []sourceValidationError {
	var errs []sourceValidationError
	provenance := strings.TrimSpace(capture.Session.FixtureProvenance)
	switch provenance {
	case "":
		errs = append(errs, sourceValidationError{
			file: file, fieldPath: "session.fixture_provenance",
			reason: "must be present for replay probe source captures",
		})
	case gatewaytesting.SessionFixtureProvenanceSynthetic,
		gatewaytesting.SessionFixtureProvenanceProviderRecorded,
		gatewaytesting.SessionFixtureProvenanceSyntheticFailure:
	default:
		errs = append(errs, sourceValidationError{
			file: file, fieldPath: "session.fixture_provenance",
			reason: fmt.Sprintf("must be %q, %q, or %q",
				gatewaytesting.SessionFixtureProvenanceSynthetic,
				gatewaytesting.SessionFixtureProvenanceProviderRecorded,
				gatewaytesting.SessionFixtureProvenanceSyntheticFailure),
		})
	}

	for index, record := range capture.Records {
		if isProviderWireEventType(record.Type) && record.PayloadType == gatewaytesting.SessionPayloadTypeStreamMessage {
			errs = append(errs, sourceValidationError{
				file: file, fieldPath: fmt.Sprintf("records[%d].payload_type", index),
				reason: fmt.Sprintf("provider wire event %q must use %q", record.Type, gatewaytesting.SessionPayloadTypeWebSocketMessage),
			})
		}
		errs = append(errs, validateSourcePayload(file, index, record)...)
	}
	return errs
}

func validateSourcePayload(file string, index int, record gatewaytesting.CapturedSessionEvent) []sourceValidationError {
	payload := record.Payload
	if len(payload) == 0 {
		payload = record.Data
	}
	if len(payload) == 0 {
		return nil
	}

	var decoded any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return []sourceValidationError{{
			file: file, fieldPath: fmt.Sprintf("records[%d].payload", index),
			reason: fmt.Sprintf("must be valid JSON: %v", err),
		}}
	}

	var errs []sourceValidationError
	walkSourcePayload(decoded, fmt.Sprintf("records[%d].payload", index), func(path, key string, value any) {
		if sourceHasRawAudio(key, value) || sourceHasCredential(key) || sourceHasSensitiveString(value) {
			errs = append(errs, sourceValidationError{
				file: file, fieldPath: path,
				reason: "replay probe source contains raw audio or credential-like data",
			})
		}
	})
	return errs
}

func walkSourcePayload(value any, path string, visit func(path, key string, value any)) {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := typed[key]
			childPath := path + "." + key
			visit(childPath, key, child)
			walkSourcePayload(child, childPath, visit)
		}
	case []any:
		for index, child := range typed {
			walkSourcePayload(child, fmt.Sprintf("%s[%d]", path, index), visit)
		}
	}
}

func isProviderWireEventType(eventType string) bool {
	switch eventType {
	case string(models.SessionEventSessionUpdate), "conversation.item.create",
		string(models.SessionEventInputAudioBufferAppend),
		string(models.SessionEventInputAudioBufferCommit),
		string(models.SessionEventInputAudioBufferClear),
		string(models.SessionEventResponseCreate),
		string(models.SessionEventResponseCancel),
		string(models.SessionEventSessionCreated),
		string(models.SessionEventSessionUpdated),
		string(models.SessionEventInputAudioBufferSpeechStarted),
		string(models.SessionEventInputAudioBufferSpeechStopped),
		string(models.SessionEventResponseCreated),
		string(models.SessionEventResponseDone),
		string(models.SessionEventResponseOutputAudioDelta),
		string(models.SessionEventResponseOutputAudioDone),
		string(models.SessionEventResponseOutputAudioTranscriptDelta),
		string(models.SessionEventResponseOutputAudioTranscriptDone),
		string(models.SessionEventConversationItemInputAudioTranscriptionDelta),
		string(models.SessionEventConversationItemInputAudioTranscriptionCompleted),
		string(models.SessionEventResponseTextDelta),
		string(models.SessionEventResponseTextDone),
		"response.audio.delta", "response.audio.done",
		"response.audio_transcript.delta", "response.audio_transcript.done",
		"response.text.delta", "response.text.done",
		string(models.SessionEventResponseFunctionCallArgumentsDelta),
		string(models.SessionEventResponseFunctionCallArgumentsDone),
		string(models.SessionEventResponseOutputItemAdded),
		string(models.SessionEventError):
		return true
	default:
		return false
	}
}

func sourceHasRawAudio(key string, value any) bool {
	switch normalizeSourceFieldKey(key) {
	case "audiobytes", "inputaudio":
		return true
	case "audio":
		_, isString := value.(string)
		return isString
	default:
		return false
	}
}

func sourceHasCredential(key string) bool {
	normalized := normalizeSourceFieldKey(key)
	switch normalized {
	case "authorization", "apikey", "token", "password", "secret", "cookie", "setcookie":
		return true
	default:
		return strings.Contains(normalized, "apikey")
	}
}

func sourceHasSensitiveString(value any) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(text))
	return strings.HasPrefix(normalized, "bearer ") ||
		strings.HasPrefix(normalized, "sk-") ||
		strings.Contains(normalized, "raw_audio") ||
		strings.Contains(normalized, "unsanitized_audio")
}

func normalizeSourceFieldKey(key string) string {
	replacer := strings.NewReplacer("_", "", "-", "")
	return replacer.Replace(strings.ToLower(key))
}
