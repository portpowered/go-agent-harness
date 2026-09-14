package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation"
)

const (
	recordingBrowserEventsVersion = "webmcp.browser-events.v1"
	recordingRedactionMarker      = "REDACTED"
)

type recordingArtifactEvent struct {
	Version       string                  `json:"version"`
	Sequence      uint64                  `json:"sequence"`
	MonotonicMS   uint64                  `json:"monotonic_ms"`
	Type          string                  `json:"type"`
	BrowserID     string                  `json:"browser_id,omitempty"`
	TargetID      string                  `json:"target_id,omitempty"`
	Generation    *uint64                 `json:"generation,omitempty"`
	Payload       json.RawMessage         `json:"payload,omitempty"`
	PayloadSHA256 string                  `json:"payload_sha256,omitempty"`
	Redaction     recordingRedactionState `json:"redaction"`
}

type recordingRedactionState struct {
	Mode  string   `json:"mode"`
	Rules []string `json:"rules,omitempty"`
}

func buildRecordingArtifact(events []browserconversation.BrowserEvent, request browserconversation.RecordingRequest) (*transcript.BrowserArtifact, error) {
	if len(events) == 0 {
		return nil, nil
	}
	data, err := encodeRecordingEvents(events, request)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	return &transcript.BrowserArtifact{
		Format: recordingBrowserEventsVersion,
		Path:   transcript.BrowserArtifactDefaultPath,
		Data:   data,
		Redaction: transcript.BrowserRedactionPolicy{
			URLQuery: request.RedactURLQuery, URLFragment: request.RedactURLFragment, RawCDP: false,
		},
	}, nil
}

func encodeRecordingEvents(events []browserconversation.BrowserEvent, request browserconversation.RecordingRequest) ([]byte, error) {
	var data bytes.Buffer
	sequence := uint64(1)
	for _, event := range events {
		inputs, err := recordingArtifactEvents(event, request)
		if err != nil {
			return nil, err
		}
		for _, input := range inputs {
			input.Sequence = sequence
			sequence++
			encoded, err := json.Marshal(input)
			if err != nil {
				return nil, fmt.Errorf("encode browser event: %w", err)
			}
			if _, err := data.Write(encoded); err != nil {
				return nil, err
			}
			if err := data.WriteByte('\n'); err != nil {
				return nil, err
			}
		}
	}
	return data.Bytes(), nil
}

func recordingArtifactEvents(event browserconversation.BrowserEvent, request browserconversation.RecordingRequest) ([]recordingArtifactEvent, error) {
	if strings.TrimSpace(event.BrowserID) == "" || strings.TrimSpace(event.TargetID) == "" {
		return nil, nil
	}
	switch event.Type {
	case "target_attached":
		return oneRecordingArtifactEvent(event, request, "browser.chrome.target_attached", nil, map[string]any{"phase": "attached"})
	case "tools_added":
		return recordingToolsAdded(event, request)
	case "tools_removed":
		return oneRecordingArtifactEvent(event, request, "browser.catalog.tool_removed", recordingGeneration(event), map[string]any{"tools": append([]string(nil), event.RemovedToolNames...)})
	case "catalog_ready":
		return recordingCatalogReady(event, request)
	case "tool_invoked":
		return recordingInvocationCreated(event, request)
	case "tool_responded":
		return recordingInvocationResponse(event, request)
	case "page_navigated", "frame_navigated":
		reason := strings.TrimSpace(event.Reason)
		if reason == "" {
			reason = "navigation"
		}
		return oneRecordingArtifactEvent(event, request, "browser.page.generation_changed", nil, map[string]any{"previous_generation": event.PreviousGeneration, "current_generation": event.Generation, "reason": reason})
	case "target_detached", "browser_disconnected":
		return oneRecordingArtifactEvent(event, request, "browser.target.detached", nil, map[string]any{"reason": event.Reason})
	case "session_closed":
		return oneRecordingArtifactEvent(event, request, "browser.chrome.target_closed", nil, map[string]any{"reason": event.Reason})
	default:
		return nil, nil
	}
}

func oneRecordingArtifactEvent(event browserconversation.BrowserEvent, request browserconversation.RecordingRequest, eventType string, generation *uint64, payload map[string]any) ([]recordingArtifactEvent, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	redacted, changed, rules, err := redactRecordingJSON(raw, request)
	if err != nil {
		return nil, err
	}
	if !changed {
		rules = []string{"raw_cdp_disabled"}
	}
	mode := "none"
	if changed {
		mode = "redacted"
	}
	return []recordingArtifactEvent{{Version: recordingBrowserEventsVersion, Type: eventType, BrowserID: event.BrowserID, TargetID: event.TargetID, Generation: generation, Payload: redacted, Redaction: recordingRedactionState{Mode: mode, Rules: rules}}}, nil
}

func recordingGeneration(event browserconversation.BrowserEvent) *uint64 {
	generation := event.Generation
	return &generation
}

func recordingToolsAdded(event browserconversation.BrowserEvent, request browserconversation.RecordingRequest) ([]recordingArtifactEvent, error) {
	count := len(event.Tools)
	if event.ToolCountKnown {
		count = event.ToolCount
	}
	return oneRecordingArtifactEvent(event, request, "browser.catalog.tool_added", recordingGeneration(event), map[string]any{"tools": recordingToolNames(event), "tool_count": count})
}

func recordingCatalogReady(event browserconversation.BrowserEvent, request browserconversation.RecordingRequest) ([]recordingArtifactEvent, error) {
	count := event.ToolCount
	if !event.ToolCountKnown {
		count = len(event.Tools)
	}
	return oneRecordingArtifactEvent(event, request, "browser.catalog.ready", recordingGeneration(event), map[string]any{"tool_count": count, "schema_digest": recordingSchemaDigest(event.Tools)})
}

func recordingInvocationCreated(event browserconversation.BrowserEvent, request browserconversation.RecordingRequest) ([]recordingArtifactEvent, error) {
	if strings.TrimSpace(event.InvocationID) == "" {
		return nil, nil
	}
	created := map[string]any{"invocation_id": event.InvocationID, "tool_name": event.ToolName}
	if event.FrameID != "" {
		created["frame_id"] = event.FrameID
	}
	first, err := oneRecordingArtifactEvent(event, request, "browser.invocation.created", recordingGeneration(event), created)
	if err != nil {
		return nil, err
	}
	dispatched := map[string]any{"invocation_id": event.InvocationID}
	if request.IncludeArguments {
		dispatched["input"] = rawRecordingValue(event.Input)
	}
	second, err := oneRecordingArtifactEvent(event, request, "browser.invocation.dispatched", recordingGeneration(event), dispatched)
	if err != nil {
		return nil, err
	}
	return append(first, second...), nil
}

func recordingInvocationResponse(event browserconversation.BrowserEvent, request browserconversation.RecordingRequest) ([]recordingArtifactEvent, error) {
	if strings.TrimSpace(event.InvocationID) == "" {
		return nil, nil
	}
	status, reason := strings.ToLower(strings.TrimSpace(event.Status)), strings.TrimSpace(event.Reason)
	if recordingCanceledStatus(status) {
		if reason == "" {
			reason = status
		}
		return oneRecordingArtifactEvent(event, request, "browser.invocation.canceled", recordingGeneration(event), map[string]any{"invocation_id": event.InvocationID, "source": "browser", "reason": reason})
	}
	if event.ErrorCode != "" || status == "error" || status == "failed" {
		payload := map[string]any{"invocation_id": event.InvocationID, "code": recordingErrorCode(event)}
		if request.IncludeResults && reason != "" {
			payload["message"] = reason
		}
		if request.IncludeResults && len(event.Output) > 0 {
			payload["error"] = rawRecordingValue(event.Output)
		}
		return oneRecordingArtifactEvent(event, request, "browser.invocation.error", recordingGeneration(event), payload)
	}
	if status == "" {
		status = "completed"
	}
	payload := map[string]any{"invocation_id": event.InvocationID, "status": status}
	if request.IncludeResults {
		payload["output"] = rawRecordingValue(event.Output)
	}
	return oneRecordingArtifactEvent(event, request, "browser.invocation.completed", recordingGeneration(event), payload)
}

func recordingCanceledStatus(status string) bool {
	switch status {
	case "canceled", "cancelled", "timed_out", "timeout", "timedout":
		return true
	default:
		return false
	}
}

func rawRecordingValue(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func recordingSchemaDigest(tools []browserconversation.BrowserToolDescriptor) string {
	digest := sha256.New()
	for _, tool := range tools {
		digest.Write([]byte(tool.Name))
		digest.Write([]byte{0})
		digest.Write(bytes.TrimSpace(tool.InputSchema))
		digest.Write([]byte{0})
	}
	return fmt.Sprintf("%x", digest.Sum(nil))
}

func recordingErrorCode(event browserconversation.BrowserEvent) string {
	if value := strings.TrimSpace(event.ErrorCode); value != "" {
		return value
	}
	return "invocation_error"
}

func redactRecordingJSON(raw json.RawMessage, request browserconversation.RecordingRequest) (json.RawMessage, bool, []string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, false, nil, nil
	}
	if !json.Valid(raw) {
		return nil, false, nil, errors.New("browser event payload is invalid JSON")
	}
	switch raw[0] {
	case '{':
		return redactRecordingObject(raw, request)
	case '[':
		return redactRecordingArray(raw, request)
	case '"':
		return redactRecordingJSONString(raw, request)
	default:
		return append(json.RawMessage(nil), raw...), false, nil, nil
	}
}

func redactRecordingObject(raw json.RawMessage, request browserconversation.RecordingRequest) (json.RawMessage, bool, []string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, false, nil, err
	}
	result := make(map[string]json.RawMessage, len(fields))
	changed, rules := false, map[string]bool{}
	for key, value := range fields {
		if isRawCDPRecordingField(key) {
			return nil, false, nil, errors.New("raw CDP field is not allowed in browser evidence")
		}
		redacted, childChanged, childRules, err := redactRecordingJSON(value, request)
		if err != nil {
			return nil, false, nil, err
		}
		result[key], changed = redacted, changed || childChanged
		for _, rule := range childRules {
			rules[rule] = true
		}
	}
	encoded, err := json.Marshal(result)
	return encoded, changed, orderedRecordingRules(rules), err
}

func redactRecordingArray(raw json.RawMessage, request browserconversation.RecordingRequest) (json.RawMessage, bool, []string, error) {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, false, nil, err
	}
	changed, rules := false, map[string]bool{}
	for index, value := range values {
		redacted, childChanged, childRules, err := redactRecordingJSON(value, request)
		if err != nil {
			return nil, false, nil, err
		}
		values[index], changed = redacted, changed || childChanged
		for _, rule := range childRules {
			rules[rule] = true
		}
	}
	encoded, err := json.Marshal(values)
	return encoded, changed, orderedRecordingRules(rules), err
}

func redactRecordingJSONString(raw json.RawMessage, request browserconversation.RecordingRequest) (json.RawMessage, bool, []string, error) {
	value, err := strconv.Unquote(string(raw))
	if err != nil {
		return nil, false, nil, err
	}
	redacted, changed, queryChanged, fragmentChanged := redactRecordingString(value, request)
	encoded, err := json.Marshal(redacted)
	rules := map[string]bool{"url_query": queryChanged, "url_fragment": fragmentChanged}
	return encoded, changed, orderedRecordingRules(rules), err
}

func redactRecordingString(value string, request browserconversation.RecordingRequest) (string, bool, bool, bool) {
	if parsed, ok := recordingURL(value); ok {
		return redactRecordingURL(value, parsed, request)
	}
	redacted := redactRecordingCredentials(value, request.Credentials)
	return redacted, redacted != value, false, false
}

func redactRecordingURL(original string, parsed *url.URL, request browserconversation.RecordingRequest) (string, bool, bool, bool) {
	queryChanged, fragmentChanged := false, false
	if request.RedactURLQuery && (parsed.RawQuery != "" || parsed.ForceQuery) {
		parsed.RawQuery, parsed.ForceQuery, queryChanged = "", false, true
	}
	if request.RedactURLFragment && (parsed.Fragment != "" || parsed.RawFragment != "") {
		parsed.Fragment, parsed.RawFragment, fragmentChanged = "", "", true
	}
	if parsed.User != nil {
		if _, hasPassword := parsed.User.Password(); hasPassword {
			parsed.User = url.UserPassword(parsed.User.Username(), recordingRedactionMarker)
			queryChanged = true
		}
	}
	redacted := redactRecordingCredentials(parsed.String(), request.Credentials)
	return redacted, redacted != original, queryChanged, fragmentChanged
}

func redactRecordingCredentials(value string, credentials []string) string {
	for _, credential := range credentials {
		if strings.TrimSpace(credential) != "" {
			value = strings.ReplaceAll(value, credential, recordingRedactionMarker)
		}
	}
	if recordingCredentialMarker(value) {
		return recordingRedactionMarker
	}
	return value
}

func recordingURL(value string) (*url.URL, bool) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "ws", "wss", "ftp":
		return parsed, true
	default:
		return nil, false
	}
}
func recordingCredentialMarker(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"authorization:", "bearer ", "api_key", "api-key", "access_token", "refresh_token", "client_secret", "password", "-----begin ", "sk-"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isRawCDPRecordingField(name string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(name, "-", "_"), " ", "_"))
	switch normalized {
	case "raw_cdp", "raw_cdp_frame", "raw_cdp_frames", "cdp_frame", "cdp_frames":
		return true
	default:
		return false
	}
}

func orderedRecordingRules(rules map[string]bool) []string {
	ordered := make([]string, 0, len(rules)+1)
	for _, rule := range []string{"url_query", "url_fragment", "raw_cdp_disabled"} {
		if rules[rule] || rule == "raw_cdp_disabled" {
			ordered = append(ordered, rule)
		}
	}
	return ordered
}
