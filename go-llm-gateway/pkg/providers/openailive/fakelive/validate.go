package fakelive

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
)

// schema is an allowlist of object keys. A nil schema accepts any value; a
// non-nil schema checks the keys of an object, or of every object in an
// array, and accepts scalars.
type schema map[string]schema

// sessionSchema is the documented session object of session.start (spec
// section 1.5), independent of the openailive structs so a struct field the
// spec does not list is caught.
func sessionSchema() schema {
	responses := schema{
		"model": nil, "instructions": nil, "max_output_tokens": nil, "parallel_tool_calls": nil,
		"reasoning":    {"effort": nil, "summary": nil},
		"service_tier": nil,
		"text":         {"verbosity": nil},
		"tool_choice":  {"type": nil, "name": nil, "server_label": nil},
		"tools":        {"type": nil, "name": nil, "description": nil, "parameters": nil, "strict": nil},
	}
	return schema{
		"model":        nil,
		"instructions": nil,
		"audio": {
			"format": {"type": nil, "rate": nil},
			"output": {"voice": {"id": nil}},
		},
		"client": {"data_channel": {
			"allowed_client_events": nil,
			"allowed_server_events": {"type": nil, "response_event": nil},
		}},
		"delegation": {"type": nil, "responses": responses},
		"input": {
			"type": nil, "id": nil, "role": nil, "status": nil,
			"content": {"type": nil, "text": nil},
		},
		"store": nil,
	}
}

// UnknownSessionKey returns the dotted path ("session.turn_detection") of
// the first key in a session.start session object that the GPT-Live spec
// does not document, or "" when every key is documented. The real server
// rejects such a key with unknown_parameter.
func UnknownSessionKey(session json.RawMessage) string {
	return unknownKey("session", session, sessionSchema())
}

func unknownKey(path string, raw json.RawMessage, allowed schema) string {
	if allowed == nil {
		return ""
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ""
	}
	switch trimmed[0] {
	case '{':
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &fields); err != nil {
			return path
		}
		for _, key := range sortedKeys(fields) {
			child, known := allowed[key]
			if !known {
				return path + "." + key
			}
			if unknown := unknownKey(path+"."+key, fields[key], child); unknown != "" {
				return unknown
			}
		}
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return path
		}
		for _, item := range items {
			if unknown := unknownKey(path, item, allowed); unknown != "" {
				return unknown
			}
		}
	}
	return ""
}

type startFailure struct {
	code    string
	message string
	param   string
}

func validateStart(payload []byte, start openailive.SessionStart) *startFailure {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return &startFailure{code: CodeMalformedEvent, message: err.Error()}
	}
	for _, key := range sortedKeys(envelope) {
		if key != "type" && key != "event_id" && key != "session" {
			return unknownParameter(key)
		}
	}
	session, ok := envelope["session"]
	if !ok {
		return &startFailure{code: CodeMissingRequiredParameter, message: "session is required", param: "session"}
	}
	if unknown := UnknownSessionKey(session); unknown != "" {
		return unknownParameter(unknown)
	}
	return validateSessionValues(start.Session)
}

func validateSessionValues(cfg openailive.SessionConfig) *startFailure {
	if strings.TrimSpace(cfg.Model) == "" {
		return &startFailure{code: CodeMissingRequiredParameter, message: "session.model is required", param: "session.model"}
	}
	if cfg.Model != openailive.Model1 {
		return &startFailure{code: CodeInvalidValue, message: fmt.Sprintf("model %q is not a GPT-Live model", cfg.Model), param: "session.model"}
	}
	if cfg.Audio != nil && cfg.Audio.Format != nil && !validFormat(*cfg.Audio.Format) {
		format := *cfg.Audio.Format
		return &startFailure{code: CodeInvalidValue, message: fmt.Sprintf("audio format %s at %d Hz is not supported", format.Type, format.Rate), param: "session.audio.format"}
	}
	if cfg.Delegation == nil {
		return nil
	}
	switch cfg.Delegation.Type {
	case openailive.DelegationClient:
		return nil
	case openailive.DelegationResponses:
		if cfg.Delegation.Responses == nil || cfg.Delegation.Responses.Model == "" {
			return &startFailure{code: CodeMissingRequiredParameter, message: "responses delegation requires a model", param: "session.delegation.responses.model"}
		}
		return nil
	default:
		return &startFailure{code: CodeInvalidValue, message: fmt.Sprintf("delegation type %q is not supported", cfg.Delegation.Type), param: "session.delegation.type"}
	}
}

func validFormat(format openailive.AudioFormat) bool {
	switch format.Type {
	case openailive.AudioTypePCM:
		return format.Rate == openailive.RatePCM24k || format.Rate == openailive.RatePCM16k
	case openailive.AudioTypePCMU, openailive.AudioTypePCMA:
		return format.Rate == openailive.RateG711
	default:
		return false
	}
}

func unknownParameter(param string) *startFailure {
	return &startFailure{
		code:    openailive.CodeUnknownParameter,
		message: fmt.Sprintf("Unknown parameter: '%s'.", param),
		param:   param,
	}
}

// resolveSession fills the server defaults into an accepted session: PCM
// 24 kHz, voice marin, client delegation and an empty input history.
func resolveSession(cfg openailive.SessionConfig) openailive.SessionResource {
	resolved := cfg
	audio := openailive.SessionAudio{}
	if cfg.Audio != nil {
		audio = *cfg.Audio
	}
	if audio.Format == nil {
		audio.Format = &openailive.AudioFormat{Type: openailive.AudioTypePCM, Rate: openailive.RatePCM24k}
	}
	if audio.Output == nil || audio.Output.Voice == (openailive.Voice{}) {
		audio.Output = &openailive.AudioOutput{Voice: openailive.Voice{Name: DefaultVoice}}
	}
	resolved.Audio = &audio
	if resolved.Delegation == nil {
		resolved.Delegation = &openailive.Delegation{Type: openailive.DelegationClient}
	}
	if resolved.Input == nil {
		resolved.Input = []openailive.InitialItem{}
	}
	return openailive.SessionResource{
		ID:            DefaultSessionID,
		Status:        openailive.SessionStatusActive,
		ExpiresAt:     DefaultExpiresAt,
		SessionConfig: resolved,
	}
}

func sortedKeys(fields map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
