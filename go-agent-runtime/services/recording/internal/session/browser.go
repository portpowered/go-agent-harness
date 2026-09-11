package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
)

const (
	browserEventsVersion = "webmcp.browser-events.v1"
	browserMaxEvents     = 4096
	browserMaxBytes      = 4 << 20
)

type browserEventType string

const (
	browserTargetAttached   browserEventType = "browser.chrome.target_attached"
	browserCatalogAdded     browserEventType = "browser.catalog.tool_added"
	browserCatalogRemoved   browserEventType = "browser.catalog.tool_removed"
	browserCatalogReady     browserEventType = "browser.catalog.ready"
	browserInvocationMade   browserEventType = "browser.invocation.created"
	browserInvocationSent   browserEventType = "browser.invocation.dispatched"
	browserInvocationDone   browserEventType = "browser.invocation.completed"
	browserInvocationError  browserEventType = "browser.invocation.error"
	browserInvocationCancel browserEventType = "browser.invocation.canceled"
	browserGeneration       browserEventType = "browser.page.generation_changed"
	browserTargetDetached   browserEventType = "browser.target.detached"
	browserTargetClosed     browserEventType = "browser.chrome.target_closed"
)

type browserEventInput struct {
	typeName      browserEventType
	browserID     string
	targetID      string
	generation    uint64
	generationSet bool
	payload       json.RawMessage
	at            time.Time
}

type browserWireEvent struct {
	Version       string
	Sequence      uint64
	MonotonicMS   uint64
	Type          browserEventType
	BrowserID     string
	TargetID      string
	Generation    *uint64
	Payload       json.RawMessage
	PayloadSHA256 string
	Redaction     browserRedaction
}

type browserRedaction struct {
	Mode  string   `json:"mode"`
	Rules []string `json:"rules,omitempty"`
}

func (e browserWireEvent) MarshalJSON() ([]byte, error) {
	type wire struct {
		Version       string           `json:"version"`
		Sequence      uint64           `json:"sequence"`
		MonotonicMS   uint64           `json:"monotonic_ms"`
		Type          browserEventType `json:"type"`
		BrowserID     string           `json:"browser_id,omitempty"`
		TargetID      string           `json:"target_id,omitempty"`
		Generation    *uint64          `json:"generation,omitempty"`
		Payload       json.RawMessage  `json:"payload,omitempty"`
		PayloadSHA256 string           `json:"payload_sha256,omitempty"`
		Redaction     browserRedaction `json:"redaction"`
	}
	return json.Marshal(wire{
		Version: e.Version, Sequence: e.Sequence, MonotonicMS: e.MonotonicMS,
		Type: e.Type, BrowserID: e.BrowserID, TargetID: e.TargetID,
		Generation: e.Generation, Payload: e.Payload, PayloadSHA256: e.PayloadSHA256,
		Redaction: e.Redaction,
	})
}

type browserRecorder struct {
	options     recording.BrowserOptions
	credentials []string
	base        time.Time

	mu        sync.Mutex
	events    []browserWireEvent
	bytes     int
	recordErr error
	cancel    context.CancelFunc
	done      chan struct{}
	started   bool
}

func newBrowserRecorder(options recording.BrowserOptions, credentials []string, base time.Time) *browserRecorder {
	if !options.IncludeArguments && !options.IncludeResults {
		// Explicit false values are meaningful; the defaults are only applied
		// when neither projection switch is intentionally selected.
	}
	return &browserRecorder{
		options: options, credentials: append([]string(nil), credentials...), base: base,
	}
}

func (r *browserRecorder) start() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return
	}
	r.started = true
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.done = make(chan struct{})
	source := r.options.Events
	if r.options.EventSource != nil {
		source = r.options.EventSource(ctx)
	}
	done := r.done
	r.mu.Unlock()
	if source == nil {
		close(done)
		return
	}
	go func() {
		defer close(done)
		for event := range source {
			r.record(event)
		}
	}()
}

func (r *browserRecorder) stop() {
	if r == nil {
		return
	}
	r.mu.Lock()
	cancel, done, started := r.cancel, r.done, r.started
	r.mu.Unlock()
	if !started {
		return
	}
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		r.mu.Lock()
		if r.recordErr == nil {
			r.recordErr = errors.New("browser recording observer did not stop")
		}
		r.mu.Unlock()
	}
}

func (r *browserRecorder) record(event recording.BrowserEvent) {
	inputs, err := browserEventInputs(event, r.options.IncludeArguments, r.options.IncludeResults)
	if err != nil {
		r.mu.Lock()
		if r.recordErr == nil {
			r.recordErr = fmt.Errorf("convert browser event %s: %w", event.Type, err)
		}
		r.mu.Unlock()
		return
	}
	for _, input := range inputs {
		r.mu.Lock()
		if r.recordErr != nil {
			r.mu.Unlock()
			return
		}
		if len(r.events) >= browserMaxEvents || r.bytes+len(input.payload) > browserMaxBytes {
			r.recordErr = errors.New("browser recording evidence budget exceeded")
			r.mu.Unlock()
			return
		}
		payload, redaction, redactErr := redactBrowserPayload(input.payload, r.options, r.credentials)
		if redactErr != nil {
			r.recordErr = redactErr
			r.mu.Unlock()
			return
		}
		input.payload = payload
		sequence := uint64(len(r.events) + 1)
		generation := (*uint64)(nil)
		if input.generationSet || browserNeedsGeneration(input.typeName) {
			value := input.generation
			generation = &value
		}
		monotonic := uint64(0)
		if !input.at.IsZero() && input.at.After(r.base) {
			monotonic = uint64(input.at.Sub(r.base) / time.Millisecond)
		}
		wire := browserWireEvent{
			Version: browserEventsVersion, Sequence: sequence, MonotonicMS: monotonic,
			Type: input.typeName, BrowserID: input.browserID, TargetID: input.targetID,
			Generation: generation, Payload: payload, Redaction: redaction,
		}
		encoded, marshalErr := json.Marshal(wire)
		if marshalErr != nil {
			r.recordErr = marshalErr
			r.mu.Unlock()
			return
		}
		r.bytes += len(encoded)
		r.events = append(r.events, wire)
		r.mu.Unlock()
	}
}

func (r *browserRecorder) artifact() (*transcript.BrowserArtifact, error) {
	if r == nil {
		return nil, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recordErr != nil {
		return nil, r.recordErr
	}
	if len(r.events) == 0 {
		return nil, nil
	}
	data := make([]byte, 0, r.bytes+len(r.events))
	for _, event := range r.events {
		encoded, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		data = append(data, encoded...)
		data = append(data, '\n')
	}
	if !utf8.Valid(data) {
		return nil, errors.New("browser artifact is not valid UTF-8")
	}
	policy := transcript.BrowserRedactionPolicy{
		URLQuery: r.options.RedactURLQuery, URLFragment: r.options.RedactURLFragment,
		ToolArguments:      append([]string(nil), r.options.ToolArguments...),
		ResultJSONPointers: append([]string(nil), r.options.ResultJSONPointers...),
		DigestTools:        append([]string(nil), r.options.DigestTools...),
	}
	return &transcript.BrowserArtifact{Format: browserEventsVersion, Path: transcript.BrowserArtifactDefaultPath, Data: data, Redaction: policy}, nil
}

func browserNeedsGeneration(kind browserEventType) bool {
	switch kind {
	case browserCatalogAdded, browserCatalogRemoved, browserCatalogReady, browserInvocationMade, browserInvocationSent, browserInvocationDone, browserInvocationError, browserInvocationCancel:
		return true
	default:
		return false
	}
}

func browserEventInputs(event recording.BrowserEvent, includeArguments, includeResults bool) ([]browserEventInput, error) {
	inputs := make([]browserEventInput, 0, 2)
	add := func(kind browserEventType, generation uint64, generationSet bool, payload any) error {
		if strings.TrimSpace(event.BrowserID) == "" || strings.TrimSpace(event.TargetID) == "" {
			return nil
		}
		raw, err := browserJSONValue(payload)
		if err != nil {
			return err
		}
		inputs = append(inputs, browserEventInput{typeName: kind, browserID: event.BrowserID, targetID: event.TargetID, generation: generation, generationSet: generationSet, payload: raw, at: event.At})
		return nil
	}
	toolNames := make([]string, 0, len(event.Tools))
	for _, tool := range event.Tools {
		if strings.TrimSpace(tool.Name) != "" {
			toolNames = append(toolNames, tool.Name)
		}
	}
	switch event.Type {
	case "target_attached":
		if err := add(browserTargetAttached, 0, false, map[string]any{"phase": "attached"}); err != nil {
			return nil, err
		}
	case "tools_added":
		count := len(toolNames)
		if event.ToolCountKnown {
			count = event.ToolCount
		}
		if err := add(browserCatalogAdded, event.Generation, true, map[string]any{"tools": toolNames, "tool_count": count}); err != nil {
			return nil, err
		}
	case "tools_removed":
		if err := add(browserCatalogRemoved, event.Generation, true, map[string]any{"tools": append([]string(nil), event.RemovedToolNames...)}); err != nil {
			return nil, err
		}
	case "catalog_ready":
		count := event.ToolCount
		if !event.ToolCountKnown {
			count = len(toolNames)
		}
		if err := add(browserCatalogReady, event.Generation, true, map[string]any{"tool_count": count, "schema_digest": browserSchemaDigest(event.Tools)}); err != nil {
			return nil, err
		}
	case "tool_invoked":
		if strings.TrimSpace(event.InvocationID) == "" {
			return inputs, nil
		}
		created := map[string]any{"invocation_id": event.InvocationID, "tool_name": event.ToolName}
		if event.FrameID != "" {
			created["frame_id"] = event.FrameID
		}
		if err := add(browserInvocationMade, event.Generation, true, created); err != nil {
			return nil, err
		}
		dispatched := map[string]any{"invocation_id": event.InvocationID}
		if includeArguments {
			dispatched["input"] = browserRawValue(event.Input)
		}
		if err := add(browserInvocationSent, event.Generation, true, dispatched); err != nil {
			return nil, err
		}
	case "tool_responded":
		if strings.TrimSpace(event.InvocationID) == "" {
			return inputs, nil
		}
		status, reason := strings.ToLower(strings.TrimSpace(event.Status)), strings.TrimSpace(event.Reason)
		if reason == "" {
			reason = status
		}
		if status == "canceled" || status == "cancelled" || status == "timed_out" || status == "timeout" || status == "timedout" {
			if err := add(browserInvocationCancel, event.Generation, true, map[string]any{"invocation_id": event.InvocationID, "source": "browser", "reason": reason}); err != nil {
				return nil, err
			}
		} else if event.ErrorCode != "" || status == "error" || status == "failed" {
			fields := map[string]any{"invocation_id": event.InvocationID, "code": browserErrorCode(event)}
			if includeResults && reason != "" {
				fields["message"] = reason
			}
			if includeResults && len(event.Output) > 0 {
				fields["error"] = browserRawValue(event.Output)
			}
			if err := add(browserInvocationError, event.Generation, true, fields); err != nil {
				return nil, err
			}
		} else {
			fields := map[string]any{"invocation_id": event.InvocationID, "status": event.Status}
			if fields["status"] == "" {
				fields["status"] = "completed"
			}
			if includeResults {
				fields["output"] = browserRawValue(event.Output)
			}
			if err := add(browserInvocationDone, event.Generation, true, fields); err != nil {
				return nil, err
			}
		}
	case "page_navigated", "frame_navigated":
		reason := strings.TrimSpace(event.Reason)
		if reason == "" {
			reason = "navigation"
		}
		if err := add(browserGeneration, 0, false, map[string]any{"previous_generation": event.PreviousGeneration, "current_generation": event.Generation, "reason": reason}); err != nil {
			return nil, err
		}
	case "target_detached", "browser_disconnected":
		if err := add(browserTargetDetached, 0, false, map[string]any{"reason": event.Reason}); err != nil {
			return nil, err
		}
	case "session_closed":
		if err := add(browserTargetClosed, 0, false, map[string]any{"reason": event.Reason}); err != nil {
			return nil, err
		}
	}
	return inputs, nil
}

func browserRawValue(raw json.RawMessage) any {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return json.RawMessage(append([]byte(nil), raw...))
}

func browserJSONValue(value any) (json.RawMessage, error) {
	if raw, ok := value.(json.RawMessage); ok {
		return compactJSON(raw)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return compactJSON(data)
}

func compactJSON(data []byte) (json.RawMessage, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return []byte("null"), nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("multiple JSON values")
	}
	return json.Marshal(value)
}

func browserSchemaDigest(tools []recording.BrowserTool) string {
	digest := sha256.New()
	for _, tool := range tools {
		digest.Write([]byte(tool.Name))
		digest.Write([]byte{0})
		digest.Write(bytes.TrimSpace(tool.InputSchema))
		digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func browserErrorCode(event recording.BrowserEvent) string {
	if code := strings.TrimSpace(event.ErrorCode); code != "" {
		return code
	}
	return "invocation_error"
}

func redactBrowserPayload(raw json.RawMessage, options recording.BrowserOptions, credentials []string) (json.RawMessage, browserRedaction, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, browserRedaction{}, err
	}
	changed := redactBrowserValue(&value, options, credentials)
	data, err := json.Marshal(value)
	if err != nil {
		return nil, browserRedaction{}, err
	}
	redaction := browserRedaction{Mode: "none", Rules: []string{"raw_cdp_disabled"}}
	if changed {
		redaction.Mode = "redacted"
		rules := make([]string, 0, 5)
		if options.RedactURLQuery {
			rules = append(rules, "url_query")
		}
		if options.RedactURLFragment {
			rules = append(rules, "url_fragment")
		}
		if len(options.ToolArguments) > 0 {
			rules = append(rules, "tool_arguments")
		}
		if len(options.ResultJSONPointers) > 0 {
			rules = append(rules, "result_json_pointers")
		}
		rules = append(rules, "raw_cdp_disabled")
		redaction.Rules = rules
	}
	return data, redaction, nil
}

func redactBrowserValue(value *any, options recording.BrowserOptions, credentials []string) bool {
	if value == nil || *value == nil {
		return false
	}
	changed := false
	switch current := (*value).(type) {
	case string:
		redacted := current
		for _, credential := range credentials {
			if strings.TrimSpace(credential) != "" {
				redacted = strings.ReplaceAll(redacted, credential, transcript.RecordingRedactionMarker)
			}
		}
		if options.RedactURLQuery || options.RedactURLFragment {
			if parsed, err := url.Parse(redacted); err == nil && isRedactableURL(parsed) {
				if options.RedactURLQuery {
					parsed.RawQuery = ""
					parsed.ForceQuery = false
				}
				if options.RedactURLFragment {
					parsed.Fragment = ""
					parsed.RawFragment = ""
				}
				redacted = parsed.String()
			}
		}
		if redacted != current {
			*value = redacted
			changed = true
		}
	case []any:
		for index := range current {
			child := any(current[index])
			if redactBrowserValue(&child, options, credentials) {
				changed = true
			}
			current[index] = child
		}
	case map[string]any:
		for key, child := range current {
			copyChild := any(child)
			if redactBrowserValue(&copyChild, options, credentials) {
				changed = true
			}
			current[key] = copyChild
		}
	}
	return changed
}

func isRedactableURL(value *url.URL) bool {
	switch strings.ToLower(value.Scheme) {
	case "http", "https", "ws", "wss", "ftp":
		return true
	}
	return false
}

func sortedStrings(values []string) []string {
	copyValues := append([]string(nil), values...)
	sort.Strings(copyValues)
	return copyValues
}
