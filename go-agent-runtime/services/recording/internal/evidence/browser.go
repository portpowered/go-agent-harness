package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
)

const (
	browserEventsVersion = "webmcp.browser-events.v1"
	redactionMarker      = "REDACTED"
)

type browserEvidence struct {
	options     recording.BrowserRecordingOptions
	credentials []string
	maxBytes    int64
	maxItems    int64

	mu        sync.Mutex
	events    []browserEvent
	usedBytes int64
	usedItems int64
	next      uint64
	recordErr error
}

type browserEvent struct {
	Version     string           `json:"version"`
	Sequence    uint64           `json:"sequence"`
	MonotonicMS uint64           `json:"monotonic_ms"`
	Type        string           `json:"type"`
	BrowserID   string           `json:"browser_id,omitempty"`
	TargetID    string           `json:"target_id,omitempty"`
	Generation  *uint64          `json:"generation,omitempty"`
	Payload     json.RawMessage  `json:"payload,omitempty"`
	Redaction   browserRedaction `json:"redaction"`
}

type browserRedaction struct {
	Mode  string   `json:"mode"`
	Rules []string `json:"rules,omitempty"`
}

func newBrowserEvidence(options recording.BrowserRecordingOptions, credentials []string, limits recording.ResourceLimits) *browserEvidence {
	if !options.Enabled {
		return nil
	}
	maxBytes, maxItems := limits.SidecarBytes, limits.SidecarItems
	if maxBytes <= 0 {
		maxBytes = recording.DefaultSidecarBytes
	}
	if maxItems <= 0 {
		maxItems = recording.DefaultSidecarItems
	}
	return &browserEvidence{
		options:     options,
		credentials: append([]string(nil), credentials...),
		maxBytes:    maxBytes,
		maxItems:    maxItems,
		next:        1,
	}
}

func (b *browserEvidence) record(event recording.BrowserEvent) error {
	if b == nil {
		return nil
	}
	inputs, err := browserEventInputs(event, b.options)
	if err != nil {
		b.mu.Lock()
		if b.recordErr == nil {
			b.recordErr = err
		}
		b.mu.Unlock()
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.recordErr != nil {
		return nil
	}
	for _, input := range inputs {
		if input.BrowserID == "" || input.TargetID == "" {
			continue
		}
		payload, redaction, err := redactBrowserPayload(input.Payload, b.options, b.credentials, input.ToolName, input.Type)
		if err != nil {
			b.recordErr = err
			return nil
		}
		generation := (*uint64)(nil)
		if input.RequiresGeneration {
			value := input.Generation
			generation = &value
		}
		candidate := browserEvent{
			Version: browserEventsVersion, Sequence: b.next, Type: input.Type,
			BrowserID: input.BrowserID, TargetID: input.TargetID,
			Generation: generation, Payload: payload, Redaction: redaction,
		}
		encoded, err := json.Marshal(candidate)
		if err != nil {
			b.recordErr = fmt.Errorf("encode browser event: %w", err)
			return nil
		}
		encodedBytes := int64(len(encoded)) + 1
		if b.usedBytes > b.maxBytes-encodedBytes || b.usedItems >= b.maxItems {
			b.recordErr = fmt.Errorf("browser evidence budget exceeded: bytes=%d/%d items=%d/%d", b.usedBytes+encodedBytes, b.maxBytes, b.usedItems+1, b.maxItems)
			return nil
		}
		b.events = append(b.events, candidate)
		b.usedBytes += encodedBytes
		b.usedItems++
		b.next++
	}
	return nil
}

func (b *browserEvidence) artifact() (*transcript.BrowserArtifact, error) {
	if b == nil {
		return nil, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.recordErr != nil {
		return nil, b.recordErr
	}
	if len(b.events) == 0 {
		return nil, nil
	}
	var data bytes.Buffer
	for _, event := range b.events {
		encoded, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		data.Write(encoded)
		data.WriteByte('\n')
	}
	sum := sha256.Sum256(data.Bytes())
	return &transcript.BrowserArtifact{
		Format: browserEventsVersion, Path: transcript.BrowserArtifactDefaultPath,
		Data: data.Bytes(), SHA256: fmt.Sprintf("%x", sum),
		Redaction: transcript.BrowserRedactionPolicy{
			URLQuery: b.options.RedactURLQuery, URLFragment: b.options.RedactURLFragment,
		},
	}, nil
}

type browserEventInput struct {
	Type               string
	BrowserID          string
	TargetID           string
	Generation         uint64
	RequiresGeneration bool
	Payload            map[string]any
	ToolName           string
}

func browserEventInputs(event recording.BrowserEvent, options recording.BrowserRecordingOptions) ([]browserEventInput, error) {
	if strings.TrimSpace(event.BrowserID) == "" || strings.TrimSpace(event.TargetID) == "" {
		return nil, nil
	}
	add := func(eventType string, generation uint64, requiresGeneration bool, payload map[string]any) browserEventInput {
		return browserEventInput{Type: eventType, BrowserID: event.BrowserID, TargetID: event.TargetID, Generation: generation, RequiresGeneration: requiresGeneration, Payload: payload, ToolName: event.ToolName}
	}
	toolNames := append([]string(nil), event.ToolNames...)
	switch event.Type {
	case "target_attached":
		return []browserEventInput{add("browser.chrome.target_attached", 0, false, map[string]any{"phase": "attached"})}, nil
	case "tools_added":
		count := len(toolNames)
		if event.ToolCountKnown {
			count = event.ToolCount
		}
		return []browserEventInput{add("browser.catalog.tool_added", event.Generation, true, map[string]any{"tools": toolNames, "tool_count": count})}, nil
	case "tools_removed":
		return []browserEventInput{add("browser.catalog.tool_removed", event.Generation, true, map[string]any{"tools": append([]string(nil), event.RemovedToolNames...)})}, nil
	case "catalog_ready":
		count := event.ToolCount
		if !event.ToolCountKnown {
			count = len(toolNames)
		}
		return []browserEventInput{add("browser.catalog.ready", event.Generation, true, map[string]any{"tool_count": count, "schema_digest": browserSchemaDigest(toolNames)})}, nil
	case "tool_invoked":
		if strings.TrimSpace(event.InvocationID) == "" {
			return nil, nil
		}
		created := map[string]any{"invocation_id": event.InvocationID, "tool_name": event.ToolName}
		if event.FrameID != "" {
			created["frame_id"] = event.FrameID
		}
		dispatched := map[string]any{"invocation_id": event.InvocationID}
		if options.IncludeArguments {
			dispatched["input"] = browserRaw(event.Input)
		}
		return []browserEventInput{
			add("browser.invocation.created", event.Generation, true, created),
			add("browser.invocation.dispatched", event.Generation, true, dispatched),
		}, nil
	case "tool_responded":
		if strings.TrimSpace(event.InvocationID) == "" {
			return nil, nil
		}
		status := strings.ToLower(strings.TrimSpace(event.Status))
		reason := strings.TrimSpace(event.Reason)
		if reason == "" {
			reason = status
		}
		switch status {
		case "canceled", "cancelled", "timed_out", "timeout", "timedout":
			return []browserEventInput{add("browser.invocation.canceled", event.Generation, true, map[string]any{"invocation_id": event.InvocationID, "source": "browser", "reason": reason})}, nil
		case "error", "failed":
			fields := map[string]any{"invocation_id": event.InvocationID, "code": browserErrorCode(event)}
			if options.IncludeResults && reason != "" {
				fields["message"] = reason
			}
			if options.IncludeResults && len(event.Output) > 0 {
				fields["error"] = browserRaw(event.Output)
			}
			return []browserEventInput{add("browser.invocation.error", event.Generation, true, fields)}, nil
		default:
			statusValue := event.Status
			if statusValue == "" {
				statusValue = "completed"
			}
			fields := map[string]any{"invocation_id": event.InvocationID, "status": statusValue}
			if options.IncludeResults {
				fields["output"] = browserRaw(event.Output)
			}
			return []browserEventInput{add("browser.invocation.completed", event.Generation, true, fields)}, nil
		}
	case "page_navigated", "frame_navigated":
		reason := strings.TrimSpace(event.Reason)
		if reason == "" {
			reason = "navigation"
		}
		return []browserEventInput{add("browser.page.generation_changed", 0, false, map[string]any{"previous_generation": event.PreviousGeneration, "current_generation": event.Generation, "reason": reason})}, nil
	case "target_detached", "browser_disconnected":
		return []browserEventInput{add("browser.target.detached", 0, false, map[string]any{"reason": event.Reason})}, nil
	case "session_closed":
		return []browserEventInput{add("browser.chrome.target_closed", 0, false, map[string]any{"reason": event.Reason})}, nil
	default:
		return nil, nil
	}
}

func redactBrowserPayload(payload map[string]any, options recording.BrowserRecordingOptions, credentials []string, tool, eventType string) (json.RawMessage, browserRedaction, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, browserRedaction{}, err
	}
	redacted, changed, queryChanged, fragmentChanged, err := redactJSON(data, options, credentials)
	if err != nil {
		return nil, browserRedaction{}, err
	}
	// Argument/result omission is decided before this boundary; all remaining
	// page JSON still receives credential and URL redaction.
	_ = tool
	_ = eventType
	rules := []string{"raw_cdp_disabled"}
	if queryChanged {
		rules = append(rules, "url_query")
	}
	if fragmentChanged {
		rules = append(rules, "url_fragment")
	}
	mode := "none"
	if changed {
		mode = "redacted"
	}
	return redacted, browserRedaction{Mode: mode, Rules: rules}, nil
}

func redactJSON(raw []byte, options recording.BrowserRecordingOptions, credentials []string) ([]byte, bool, bool, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return nil, false, false, false, errors.New("browser payload must be valid JSON")
	}
	switch trimmed[0] {
	case '{':
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &fields); err != nil {
			return nil, false, false, false, err
		}
		out := make(map[string]json.RawMessage, len(fields))
		changed, queryChanged, fragmentChanged := false, false, false
		for key, value := range fields {
			child, childChanged, childQuery, childFragment, err := redactJSON(value, options, credentials)
			if err != nil {
				return nil, false, false, false, err
			}
			out[key] = child
			changed = changed || childChanged
			queryChanged = queryChanged || childQuery
			fragmentChanged = fragmentChanged || childFragment
		}
		encoded, err := json.Marshal(out)
		return encoded, changed, queryChanged, fragmentChanged, err
	case '[':
		var values []json.RawMessage
		if err := json.Unmarshal(trimmed, &values); err != nil {
			return nil, false, false, false, err
		}
		out := make([]json.RawMessage, len(values))
		changed, queryChanged, fragmentChanged := false, false, false
		for i, value := range values {
			child, childChanged, childQuery, childFragment, err := redactJSON(value, options, credentials)
			if err != nil {
				return nil, false, false, false, err
			}
			out[i] = child
			changed = changed || childChanged
			queryChanged = queryChanged || childQuery
			fragmentChanged = fragmentChanged || childFragment
		}
		encoded, err := json.Marshal(out)
		return encoded, changed, queryChanged, fragmentChanged, err
	case '"':
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return nil, false, false, false, err
		}
		redacted, changed, queryChanged, fragmentChanged := redactString(value, options, credentials)
		encoded, err := json.Marshal(redacted)
		return encoded, changed, queryChanged, fragmentChanged, err
	default:
		return append([]byte(nil), trimmed...), false, false, false, nil
	}
}

func redactString(value string, options recording.BrowserRecordingOptions, credentials []string) (string, bool, bool, bool) {
	redacted, queryChanged, fragmentChanged := value, false, false
	if parsed, ok := parseBrowserURL(value); ok {
		if options.RedactURLQuery && (parsed.RawQuery != "" || parsed.ForceQuery) {
			parsed.RawQuery, parsed.ForceQuery, queryChanged = "", false, true
		}
		if options.RedactURLFragment && (parsed.Fragment != "" || parsed.RawFragment != "") {
			parsed.Fragment, parsed.RawFragment, fragmentChanged = "", "", true
		}
		redacted = parsed.String()
	}
	for _, credential := range credentials {
		if strings.TrimSpace(credential) != "" {
			redacted = strings.ReplaceAll(redacted, credential, redactionMarker)
		}
	}
	return redacted, redacted != value || queryChanged || fragmentChanged, queryChanged, fragmentChanged
}

func parseBrowserURL(value string) (*url.URL, bool) {
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

func browserRaw(raw []byte) any {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return json.RawMessage(append([]byte(nil), raw...))
}

func browserSchemaDigest(toolNames []string) string {
	hash := sha256.New()
	for _, name := range toolNames {
		hash.Write([]byte(name))
		hash.Write([]byte{0})
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func browserErrorCode(event recording.BrowserEvent) string {
	if code := strings.TrimSpace(event.ErrorCode); code != "" {
		return code
	}
	return "invocation_error"
}
