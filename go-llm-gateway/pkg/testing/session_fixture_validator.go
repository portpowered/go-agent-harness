package testing

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

const (
	// SessionFixtureProvenanceSynthetic marks fixtures built from fake test data.
	SessionFixtureProvenanceSynthetic = "synthetic"
	// SessionFixtureProvenanceProviderRecorded marks sanitized captures from a provider session.
	SessionFixtureProvenanceProviderRecorded = "provider_recorded"
	// SessionFixtureProvenanceSyntheticFailure marks fixtures that
	// intentionally encode a failure-shaped session (for example an
	// unexecutable provider tool call) replayed to prove diagnosability.
	// Failure fixtures may violate healthy-session shape expectations such as
	// matched tool-call/result pairs; hygiene rules still apply in full.
	SessionFixtureProvenanceSyntheticFailure = "synthetic_failure"
)

var providerWireEventTypes = map[string]struct{}{
	string(models.SessionEventSessionUpdate):                      {},
	"conversation.item.create":                                    {},
	string(models.SessionEventInputAudioBufferAppend):             {},
	string(models.SessionEventInputAudioBufferCommit):             {},
	string(models.SessionEventInputAudioBufferClear):              {},
	string(models.SessionEventResponseCreate):                     {},
	string(models.SessionEventResponseCancel):                     {},
	string(models.SessionEventSessionCreated):                     {},
	string(models.SessionEventSessionUpdated):                     {},
	string(models.SessionEventInputAudioBufferSpeechStarted):      {},
	string(models.SessionEventInputAudioBufferSpeechStopped):      {},
	string(models.SessionEventResponseCreated):                    {},
	string(models.SessionEventResponseDone):                       {},
	string(models.SessionEventResponseOutputAudioDelta):           {},
	string(models.SessionEventResponseOutputAudioDone):            {},
	string(models.SessionEventResponseOutputAudioTranscriptDelta): {},
	string(models.SessionEventResponseOutputAudioTranscriptDone):  {},
	string(models.SessionEventResponseTextDelta):                  {},
	string(models.SessionEventResponseTextDone):                   {},
	"response.audio.delta":                                        {},
	"response.audio.done":                                         {},
	"response.audio_transcript.delta":                             {},
	"response.audio_transcript.done":                              {},
	"response.text.delta":                                         {},
	"response.text.done":                                          {},
	string(models.SessionEventResponseFunctionCallArgumentsDelta): {},
	string(models.SessionEventResponseFunctionCallArgumentsDone):  {},
	string(models.SessionEventResponseOutputItemAdded):            {},
	string(models.SessionEventError):                              {},
}

// SessionFixtureValidationError describes one fixture hygiene violation.
type SessionFixtureValidationError struct {
	File      string
	FieldPath string
	Reason    string
}

// Error formats the validation error with stable file and field context.
func (e SessionFixtureValidationError) Error() string {
	if e.File != "" {
		return fmt.Sprintf("%s: %s: %s", e.File, e.FieldPath, e.Reason)
	}
	return fmt.Sprintf("%s: %s", e.FieldPath, e.Reason)
}

// ValidateSessionCaptureFile loads and validates a committed .session.json capture.
func ValidateSessionCaptureFile(path string) []SessionFixtureValidationError {
	capture, err := LoadSessionCapture(path)
	if err != nil {
		return []SessionFixtureValidationError{{
			File:      path,
			FieldPath: "$",
			Reason:    err.Error(),
		}}
	}
	return ValidateSessionCapture(path, capture)
}

// ValidateSessionCapture validates a decoded session capture against committed fixture hygiene rules.
func ValidateSessionCapture(file string, capture SessionCapture) []SessionFixtureValidationError {
	var errs []SessionFixtureValidationError

	provenance := strings.TrimSpace(capture.Session.FixtureProvenance)
	switch provenance {
	case "":
		errs = append(errs, SessionFixtureValidationError{
			File:      file,
			FieldPath: "session.fixture_provenance",
			Reason:    "must be present for committed session fixtures",
		})
	case SessionFixtureProvenanceSynthetic, SessionFixtureProvenanceProviderRecorded, SessionFixtureProvenanceSyntheticFailure:
	default:
		errs = append(errs, SessionFixtureValidationError{
			File:      file,
			FieldPath: "session.fixture_provenance",
			Reason:    fmt.Sprintf("must be %q, %q, or %q", SessionFixtureProvenanceSynthetic, SessionFixtureProvenanceProviderRecorded, SessionFixtureProvenanceSyntheticFailure),
		})
	}

	for i, record := range capture.Records {
		if isProviderWireEventType(record.Type) && record.PayloadType == SessionPayloadTypeStreamMessage {
			errs = append(errs, SessionFixtureValidationError{
				File:      file,
				FieldPath: fmt.Sprintf("records[%d].payload_type", i),
				Reason:    fmt.Sprintf("provider wire event %q must use %q", record.Type, SessionPayloadTypeWebSocketMessage),
			})
		}
		errs = append(errs, validateFixturePayloadHygiene(file, i, record)...)
	}

	return errs
}

func validateFixturePayloadHygiene(file string, recordIndex int, record CapturedSessionEvent) []SessionFixtureValidationError {
	payload := eventPayload(record)
	if len(payload) == 0 {
		return nil
	}

	var decoded any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return []SessionFixtureValidationError{{
			File:      file,
			FieldPath: fmt.Sprintf("records[%d].payload", recordIndex),
			Reason:    fmt.Sprintf("must be valid JSON: %v", err),
		}}
	}

	var errs []SessionFixtureValidationError
	walkFixturePayload(decoded, fmt.Sprintf("records[%d].payload", recordIndex), func(path, key string, value any) {
		if isRawAudioField(key, value) {
			errs = append(errs, SessionFixtureValidationError{
				File:      file,
				FieldPath: path,
				Reason:    "session fixtures must not contain unsanitized raw audio fields",
			})
		}
		if isCredentialLikeField(key) || isSensitiveFixtureString(value) {
			errs = append(errs, SessionFixtureValidationError{
				File:      file,
				FieldPath: path,
				Reason:    "session fixtures must not contain credential-like fields or values",
			})
		}
	})
	return errs
}

func walkFixturePayload(value any, path string, visitKey func(path, key string, value any)) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			childPath := path + "." + key
			visitKey(childPath, key, child)
			walkFixturePayload(child, childPath, visitKey)
		}
	case []any:
		for i, child := range typed {
			walkFixturePayload(child, fmt.Sprintf("%s[%d]", path, i), visitKey)
		}
	}
}

func isProviderWireEventType(eventType string) bool {
	_, ok := providerWireEventTypes[eventType]
	return ok
}

func isRawAudioField(key string, value any) bool {
	switch normalizeFixtureFieldKey(key) {
	case "audiobytes", "inputaudio":
		return true
	case "audio":
		_, isString := value.(string)
		return isString
	default:
		return false
	}
}

func isCredentialLikeField(key string) bool {
	switch normalized := normalizeFixtureFieldKey(key); normalized {
	case "authorization", "apikey", "token", "password", "secret", "cookie", "setcookie":
		return true
	default:
		return strings.Contains(normalized, "apikey")
	}
}

func isSensitiveFixtureString(value any) bool {
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

func normalizeFixtureFieldKey(key string) string {
	replacer := strings.NewReplacer("_", "", "-", "")
	return replacer.Replace(strings.ToLower(key))
}
