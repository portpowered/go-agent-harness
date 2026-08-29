// Package testkit contains deterministic, browser-independent WebMCP test
// fixtures and evidence helpers.
//
// The event types in this package deliberately contain semantic observations,
// not CDP transport frames. Page-owned JSON is retained as json.RawMessage so
// recording cannot reinterpret application values or lose large integers.
package testkit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

const (
	// BrowserEventsVersion is the only event-stream version understood by the
	// testkit. Unknown versions are rejected instead of being guessed.
	BrowserEventsVersion = "webmcp.browser-events.v1"

	// The redaction rule names are part of the frozen event contract. Keep the
	// values as untyped constants so callers can use them in []string literals.
	RedactionRuleURLQuery           = "url_query"
	RedactionRuleURLFragment        = "url_fragment"
	RedactionRuleToolArguments      = "tool_arguments"
	RedactionRuleResultJSONPointers = "result_json_pointers"
	RedactionRuleRawCDPDisabled     = "raw_cdp_disabled"
)

var (
	// ErrInvalidBrowserEvent identifies a malformed event or event stream.
	ErrInvalidBrowserEvent = errors.New("webmcp testkit: invalid browser event")
	// ErrRecorderWrite identifies a failure to append a canonical event line.
	ErrRecorderWrite = errors.New("webmcp testkit: browser event write failed")
	// ErrRecorderClock identifies a clock that moved backwards.
	ErrRecorderClock = errors.New("webmcp testkit: monotonic clock moved backwards")
	// ErrIDSourceUnavailable identifies an attempt to allocate an ID without an
	// injected ID source.
	ErrIDSourceUnavailable = errors.New("webmcp testkit: deterministic ID source unavailable")
)

// EventType is the semantic browser observation name.
type EventType string

const (
	EventBrowserDiscoveryStarted      EventType = "browser.discovery.started"
	EventBrowserDiscoveryCompleted    EventType = "browser.discovery.completed"
	EventBrowserEndpointVersion       EventType = "browser.endpoint.version"
	EventBrowserTargetsSnapshot       EventType = "browser.targets.snapshot"
	EventBrowserTargetSelected        EventType = "browser.target.selected"
	EventBrowserChromeTargetAttached  EventType = "browser.chrome.target_attached"
	EventBrowserWebMCPEnabled         EventType = "browser.webmcp.enabled"
	EventBrowserCatalogToolAdded      EventType = "browser.catalog.tool_added"
	EventBrowserCatalogToolRemoved    EventType = "browser.catalog.tool_removed"
	EventBrowserCatalogReady          EventType = "browser.catalog.ready"
	EventBrowserInvocationCreated     EventType = "browser.invocation.created"
	EventBrowserInvocationApproval    EventType = "browser.invocation.approval"
	EventBrowserInvocationDispatched  EventType = "browser.invocation.dispatched"
	EventBrowserInvocationCompleted   EventType = "browser.invocation.completed"
	EventBrowserInvocationError       EventType = "browser.invocation.error"
	EventBrowserInvocationCancel      EventType = "browser.invocation.cancel_requested"
	EventBrowserInvocationCanceled    EventType = "browser.invocation.canceled"
	EventBrowserPageGenerationChanged EventType = "browser.page.generation_changed"
	EventBrowserTargetDetached        EventType = "browser.target.detached"
	EventBrowserChromeTargetClosed    EventType = "browser.chrome.target_closed"
)

// Short aliases make event construction readable while retaining the full
// C0 names in the canonical JSON.
const (
	BrowserDiscoveryStarted      = EventBrowserDiscoveryStarted
	BrowserDiscoveryCompleted    = EventBrowserDiscoveryCompleted
	BrowserEndpointVersion       = EventBrowserEndpointVersion
	BrowserTargetsSnapshot       = EventBrowserTargetsSnapshot
	BrowserTargetSelected        = EventBrowserTargetSelected
	BrowserChromeTargetAttached  = EventBrowserChromeTargetAttached
	BrowserWebMCPEnabled         = EventBrowserWebMCPEnabled
	BrowserCatalogToolAdded      = EventBrowserCatalogToolAdded
	BrowserCatalogToolRemoved    = EventBrowserCatalogToolRemoved
	BrowserCatalogReady          = EventBrowserCatalogReady
	BrowserInvocationCreated     = EventBrowserInvocationCreated
	BrowserInvocationApproval    = EventBrowserInvocationApproval
	BrowserInvocationDispatched  = EventBrowserInvocationDispatched
	BrowserInvocationCompleted   = EventBrowserInvocationCompleted
	BrowserInvocationError       = EventBrowserInvocationError
	BrowserInvocationCancel      = EventBrowserInvocationCancel
	BrowserInvocationCanceled    = EventBrowserInvocationCanceled
	BrowserPageGenerationChanged = EventBrowserPageGenerationChanged
	BrowserTargetDetached        = EventBrowserTargetDetached
	BrowserChromeTargetClosed    = EventBrowserChromeTargetClosed
)

// RedactionMode describes how an event payload was retained.
type RedactionMode string

const (
	RedactionNone     RedactionMode = "none"
	RedactionRedacted RedactionMode = "redacted"
	RedactionDigest   RedactionMode = "digest"
)

// RedactionMetadata records the rules applied before an event was serialized.
// Rules are normalized to the frozen declaration order when emitted.
type RedactionMetadata struct {
	Mode  RedactionMode `json:"mode"`
	Rules []string      `json:"rules,omitempty"`
}

// Redaction is a concise alias for RedactionMetadata.
type Redaction = RedactionMetadata

// Validate checks the event-level redaction metadata.
func (r RedactionMetadata) Validate() error {
	if r.Mode != RedactionNone && r.Mode != RedactionRedacted && r.Mode != RedactionDigest {
		return fmt.Errorf("redaction.mode must be one of %q, %q, or %q", RedactionNone, RedactionRedacted, RedactionDigest)
	}
	seen := make(map[string]struct{}, len(r.Rules))
	for _, rule := range r.Rules {
		if !isRedactionRule(rule) {
			return fmt.Errorf("redaction.rules contains unknown rule %q", rule)
		}
		if _, ok := seen[rule]; ok {
			return fmt.Errorf("redaction.rules contains duplicate rule %q", rule)
		}
		seen[rule] = struct{}{}
	}
	return nil
}

func (r RedactionMetadata) normalized() RedactionMetadata {
	if len(r.Rules) == 0 {
		return RedactionMetadata{Mode: r.Mode}
	}
	seen := make(map[string]struct{}, len(r.Rules))
	rules := make([]string, 0, len(r.Rules))
	for _, rule := range redactionRuleOrder {
		for _, supplied := range r.Rules {
			if supplied == rule {
				if _, alreadyAdded := seen[supplied]; alreadyAdded {
					continue
				}
				rules = append(rules, supplied)
				seen[supplied] = struct{}{}
			}
		}
	}
	return RedactionMetadata{Mode: r.Mode, Rules: rules}
}

func (r RedactionMetadata) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	type wire struct {
		Mode  RedactionMode `json:"mode"`
		Rules []string      `json:"rules,omitempty"`
	}
	normalized := r.normalized()
	return json.Marshal(wire(normalized))
}

func (r *RedactionMetadata) UnmarshalJSON(data []byte) error {
	if r == nil {
		return errors.New("cannot unmarshal redaction into nil receiver")
	}
	fields, err := decodeJSONObject(data)
	if err != nil {
		return err
	}
	if err := rejectUnknownFields(fields, map[string]struct{}{"mode": {}, "rules": {}}); err != nil {
		return err
	}
	modeRaw, ok := fields["mode"]
	if !ok {
		return errors.New("redaction.mode is required")
	}
	mode, err := parseString(modeRaw)
	if err != nil {
		return fmt.Errorf("redaction.mode: %w", err)
	}
	result := RedactionMetadata{Mode: RedactionMode(mode)}
	if rulesRaw, ok := fields["rules"]; ok {
		var rules []string
		if err := json.Unmarshal(rulesRaw, &rules); err != nil {
			return fmt.Errorf("redaction.rules: %w", err)
		}
		if rules == nil {
			return errors.New("redaction.rules must be an array")
		}
		result.Rules = rules
	}
	if err := result.Validate(); err != nil {
		return err
	}
	*r = result.normalized()
	return nil
}

var redactionRuleOrder = []string{
	RedactionRuleURLQuery,
	RedactionRuleURLFragment,
	RedactionRuleToolArguments,
	RedactionRuleResultJSONPointers,
	RedactionRuleRawCDPDisabled,
}

func isRedactionRule(rule string) bool {
	for _, allowed := range redactionRuleOrder {
		if rule == allowed {
			return true
		}
	}
	return false
}

// Event is one canonical semantic browser event. Payload is retained as raw
// JSON; use JSONValue or json.RawMessage rather than map[string]any when large
// integer tokens must remain exact.
type Event struct {
	Version       string            `json:"version"`
	Sequence      uint64            `json:"sequence"`
	MonotonicMS   uint64            `json:"monotonic_ms"`
	Type          EventType         `json:"type"`
	BrowserID     string            `json:"browser_id,omitempty"`
	TargetID      string            `json:"target_id,omitempty"`
	Generation    uint64            `json:"generation,omitempty"`
	Payload       json.RawMessage   `json:"payload,omitempty"`
	PayloadSHA256 string            `json:"payload_sha256,omitempty"`
	Redaction     RedactionMetadata `json:"redaction"`

	generationSet bool
}

// EventInput contains the event-specific values supplied to Recorder.Record.
// Version, sequence, and monotonic_ms are assigned by the recorder.
type EventInput struct {
	Type          EventType
	BrowserID     string
	TargetID      string
	Generation    uint64
	Payload       json.RawMessage
	PayloadSHA256 string
	Redaction     RedactionMetadata
}

// NewEventInput marshals a page-owned value without decoding it through a
// floating-point interface. A json.RawMessage value is compacted and retained
// as-is apart from insignificant whitespace.
func NewEventInput(eventType EventType, payload any) (EventInput, error) {
	raw, err := JSONValue(payload)
	if err != nil {
		return EventInput{}, err
	}
	return EventInput{Type: eventType, Payload: raw}, nil
}

// JSONValue converts a value to a validated JSON token for a payload. Callers
// that already have JSON should pass json.RawMessage to avoid a decode/re-encode
// cycle.
func JSONValue(value any) (json.RawMessage, error) {
	if raw, ok := value.(json.RawMessage); ok {
		return normalizeJSON(raw)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return normalizeJSON(data)
}

// MustJSONValue is the test-friendly form of JSONValue.
func MustJSONValue(value any) json.RawMessage {
	raw, err := JSONValue(value)
	if err != nil {
		panic(err)
	}
	return raw
}

// Validate checks one event independently of stream ordering.
func (e Event) Validate() error {
	return validateEvent(e)
}

// MarshalJSON enforces the canonical field order and omits context fields that
// do not belong to the event type. It also emits generation zero when that
// context is required, which ordinary omitempty tags cannot express.
func (e Event) MarshalJSON() ([]byte, error) {
	if err := validateEvent(e); err != nil {
		return nil, err
	}
	var payload json.RawMessage
	if e.Payload != nil {
		var err error
		payload, err = normalizeJSON(e.Payload)
		if err != nil {
			return nil, fmt.Errorf("payload: %w", err)
		}
	}
	var generation *uint64
	if definitionFor(e.Type).requiresGeneration {
		generation = &e.Generation
	}
	type wire struct {
		Version       string            `json:"version"`
		Sequence      uint64            `json:"sequence"`
		MonotonicMS   uint64            `json:"monotonic_ms"`
		Type          EventType         `json:"type"`
		BrowserID     string            `json:"browser_id,omitempty"`
		TargetID      string            `json:"target_id,omitempty"`
		Generation    *uint64           `json:"generation,omitempty"`
		Payload       json.RawMessage   `json:"payload,omitempty"`
		PayloadSHA256 string            `json:"payload_sha256,omitempty"`
		Redaction     RedactionMetadata `json:"redaction"`
	}
	return json.Marshal(wire{
		Version:       e.Version,
		Sequence:      e.Sequence,
		MonotonicMS:   e.MonotonicMS,
		Type:          e.Type,
		BrowserID:     e.BrowserID,
		TargetID:      e.TargetID,
		Generation:    generation,
		Payload:       payload,
		PayloadSHA256: e.PayloadSHA256,
		Redaction:     e.Redaction.normalized(),
	})
}

// UnmarshalJSON applies the same strict checks used by ValidateEventStream.
func (e *Event) UnmarshalJSON(data []byte) error {
	if e == nil {
		return errors.New("cannot unmarshal browser event into nil receiver")
	}
	parsed, err := parseEvent(data)
	if err != nil {
		return err
	}
	*e = parsed
	return nil
}

// EventValidationError identifies a safe structural validation failure. The
// line is zero for a standalone Event.Validate call.
type EventValidationError struct {
	Line  int
	Field string
	Cause error
}

func (e *EventValidationError) Error() string {
	if e == nil {
		return ErrInvalidBrowserEvent.Error()
	}
	prefix := ErrInvalidBrowserEvent.Error()
	if e.Line > 0 {
		prefix += fmt.Sprintf(" at line %d", e.Line)
	}
	if e.Field != "" {
		prefix += " " + e.Field
	}
	if e.Cause != nil {
		return prefix + ": " + e.Cause.Error()
	}
	return prefix
}

func (e *EventValidationError) Unwrap() error {
	if e == nil {
		return ErrInvalidBrowserEvent
	}
	if e.Cause == nil {
		return ErrInvalidBrowserEvent
	}
	return errors.Join(ErrInvalidBrowserEvent, e.Cause)
}

func newEventValidationError(line int, field string, format string, args ...any) error {
	return &EventValidationError{Line: line, Field: field, Cause: fmt.Errorf(format, args...)}
}

// ValidateEventStream parses and validates a complete UTF-8 browser JSONL
// artifact. A final newline is optional, but blank lines are not allowed.
func ValidateEventStream(data []byte) ([]Event, error) {
	if !utf8.Valid(data) {
		return nil, newEventValidationError(0, "stream", "input is not valid UTF-8")
	}
	if len(data) == 0 {
		return nil, newEventValidationError(0, "stream", "event stream is empty")
	}
	lines := bytes.Split(data, []byte{'\n'})
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	events := make([]Event, 0, len(lines))
	var previousMS uint64
	for index, line := range lines {
		lineNumber := index + 1
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(bytes.TrimSpace(line)) == 0 {
			return nil, newEventValidationError(lineNumber, "line", "blank lines are not allowed")
		}
		event, err := parseEvent(line)
		if err != nil {
			return nil, withLine(err, lineNumber)
		}
		wantSequence := uint64(lineNumber)
		if event.Sequence != wantSequence {
			return nil, newEventValidationError(lineNumber, "sequence", "want contiguous sequence %d, got %d", wantSequence, event.Sequence)
		}
		if lineNumber > 1 && event.MonotonicMS < previousMS {
			return nil, newEventValidationError(lineNumber, "monotonic_ms", "decreased from %d to %d", previousMS, event.MonotonicMS)
		}
		previousMS = event.MonotonicMS
		events = append(events, event)
	}
	return events, nil
}

// DecodeEvents is an alias for ValidateEventStream for callers that prefer a
// decoder-shaped name.
func DecodeEvents(data []byte) ([]Event, error) {
	return ValidateEventStream(data)
}

// LoadEvents reads and validates a browser JSONL artifact from a stream.
func LoadEvents(reader io.Reader) ([]Event, error) {
	if reader == nil {
		return nil, newEventValidationError(0, "stream", "reader is nil")
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read browser event stream: %w", err)
	}
	return ValidateEventStream(data)
}

// LoadEventsFile reads and validates a browser JSONL artifact from disk.
func LoadEventsFile(path string) ([]Event, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read browser event stream %q: %w", path, err)
	}
	return ValidateEventStream(data)
}

// MarshalEvents validates stream ordering and emits canonical UTF-8 JSONL.
func MarshalEvents(events []Event) ([]byte, error) {
	if len(events) == 0 {
		return nil, newEventValidationError(0, "stream", "event stream is empty")
	}
	var output bytes.Buffer
	for index, event := range events {
		if event.Sequence != uint64(index+1) {
			return nil, newEventValidationError(index+1, "sequence", "want contiguous sequence %d, got %d", index+1, event.Sequence)
		}
		if index > 0 && event.MonotonicMS < events[index-1].MonotonicMS {
			return nil, newEventValidationError(index+1, "monotonic_ms", "decreased from %d to %d", events[index-1].MonotonicMS, event.MonotonicMS)
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			return nil, withLine(err, index+1)
		}
		output.Write(encoded)
		output.WriteByte('\n')
	}
	return output.Bytes(), nil
}

func validateEvent(event Event) error {
	if event.Version != BrowserEventsVersion {
		return newEventValidationError(0, "version", "want %q, got %q", BrowserEventsVersion, event.Version)
	}
	if event.Sequence == 0 {
		return newEventValidationError(0, "sequence", "must be at least 1")
	}
	definition, ok := eventDefinitions[event.Type]
	if !ok {
		return newEventValidationError(0, "type", "unknown event type %q", event.Type)
	}
	if err := validateEventContext(event, definition); err != nil {
		return err
	}
	if event.Payload != nil && event.PayloadSHA256 != "" {
		return newEventValidationError(0, "payload", "payload and payload_sha256 are mutually exclusive")
	}
	if event.Payload == nil && event.PayloadSHA256 == "" {
		return newEventValidationError(0, "payload", "exactly one of payload or payload_sha256 is required")
	}
	if event.Payload != nil {
		if _, err := normalizeJSON(event.Payload); err != nil {
			return newEventValidationError(0, "payload", "must be one JSON value: %v", err)
		}
		if err := validatePayloadControls(event.Type, event.Payload, definition.payloadFields); err != nil {
			return err
		}
	}
	if event.PayloadSHA256 != "" && !isLowerSHA256(event.PayloadSHA256) {
		return newEventValidationError(0, "payload_sha256", "must be lowercase 64-character hexadecimal")
	}
	if err := event.Redaction.Validate(); err != nil {
		return newEventValidationError(0, "redaction", "%v", err)
	}
	return nil
}

func validateEventContext(event Event, definition eventDefinition) error {
	if definition.requiresBrowser || definition.optionalBrowser {
		if definition.requiresBrowser && strings.TrimSpace(event.BrowserID) == "" {
			return newEventValidationError(0, "browser_id", "is required for %s", event.Type)
		}
		if event.BrowserID != "" {
			if err := validateOpaqueID(event.BrowserID); err != nil {
				return newEventValidationError(0, "browser_id", "%v", err)
			}
		}
	} else if event.BrowserID != "" {
		return newEventValidationError(0, "browser_id", "is not valid for %s", event.Type)
	}
	if definition.requiresTarget {
		if strings.TrimSpace(event.TargetID) == "" {
			return newEventValidationError(0, "target_id", "is required for %s", event.Type)
		}
		if err := validateOpaqueID(event.TargetID); err != nil {
			return newEventValidationError(0, "target_id", "%v", err)
		}
	} else if event.TargetID != "" {
		return newEventValidationError(0, "target_id", "is not valid for %s", event.Type)
	}
	if definition.requiresGeneration {
		// Direct Event values cannot distinguish an omitted zero from an explicit
		// zero; the type itself makes the field required and MarshalJSON emits it.
		return nil
	}
	if event.Generation != 0 || event.generationSet {
		return newEventValidationError(0, "generation", "is not valid for %s", event.Type)
	}
	return nil
}

func validateOpaqueID(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("must not be empty")
	}
	if strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f || r == ' ' || r == '\t' || r == '\r' || r == '\n' }) >= 0 {
		return errors.New("must be a normalized opaque ID")
	}
	if strings.Contains(value, "://") || strings.ContainsAny(value, "@?#") {
		return errors.New("must not contain a URL or credential data")
	}
	return nil
}

func normalizeIDPart(value string) string {
	var builder strings.Builder
	for _, r := range strings.TrimSpace(value) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func isLowerSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func normalizeJSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, errors.New("value is missing")
	}
	if !utf8.Valid(raw) {
		return nil, errors.New("value is not valid UTF-8")
	}
	if !json.Valid(raw) {
		return nil, errors.New("value is not valid JSON")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), compact.Bytes()...), nil
}

type eventDefinition struct {
	requiresBrowser    bool
	optionalBrowser    bool
	requiresTarget     bool
	requiresGeneration bool
	payloadFields      map[string]payloadFieldKind
}

type payloadFieldKind uint8

const (
	payloadAny payloadFieldKind = iota
	payloadString
	payloadBoolean
	payloadInteger
	payloadIdentifier
)

func fieldsWithKinds(entries map[string]payloadFieldKind) map[string]payloadFieldKind {
	result := make(map[string]payloadFieldKind, len(entries))
	for name, kind := range entries {
		result[name] = kind
	}
	return result
}

var eventDefinitions = map[EventType]eventDefinition{
	EventBrowserDiscoveryStarted: {
		payloadFields: fieldsWithKinds(map[string]payloadFieldKind{
			"source": payloadString, "attempt": payloadInteger, "mode": payloadString, "reason": payloadString,
		}),
	},
	EventBrowserDiscoveryCompleted: {
		optionalBrowser: true,
		payloadFields: fieldsWithKinds(map[string]payloadFieldKind{
			"candidate_count": payloadInteger, "candidates": payloadAny, "selected": payloadAny, "source": payloadString, "reason": payloadString,
		}),
	},
	EventBrowserEndpointVersion: {
		requiresBrowser: true,
		payloadFields: fieldsWithKinds(map[string]payloadFieldKind{
			"Browser": payloadString, "Protocol-Version": payloadString, "webSocketDebuggerUrl": payloadString,
			"browser": payloadString, "protocol_version": payloadString, "websocket_debugger_url": payloadString, "product": payloadString, "version": payloadString,
		}),
	},
	EventBrowserTargetsSnapshot: {
		requiresBrowser: true,
		payloadFields: fieldsWithKinds(map[string]payloadFieldKind{
			"targets": payloadAny, "target_count": payloadInteger, "selected_target_id": payloadIdentifier,
		}),
	},
	EventBrowserTargetSelected: {
		requiresBrowser: true, requiresTarget: true,
		payloadFields: fieldsWithKinds(map[string]payloadFieldKind{
			"generation": payloadInteger, "reason": payloadString, "selection_reason": payloadString,
		}),
	},
	EventBrowserChromeTargetAttached: {
		requiresBrowser: true, requiresTarget: true,
		payloadFields: fieldsWithKinds(map[string]payloadFieldKind{
			"phase": payloadString, "ownership": payloadString, "ownership_mode": payloadString, "reason": payloadString,
		}),
	},
	EventBrowserWebMCPEnabled: {
		requiresBrowser: true, requiresTarget: true, requiresGeneration: true,
		payloadFields: fieldsWithKinds(map[string]payloadFieldKind{
			"enabled": payloadBoolean, "result": payloadAny, "capability": payloadString, "status": payloadString, "error": payloadAny,
		}),
	},
	EventBrowserCatalogToolAdded: {
		requiresBrowser: true, requiresTarget: true, requiresGeneration: true,
		payloadFields: fieldsWithKinds(map[string]payloadFieldKind{
			"tools": payloadAny, "tool": payloadAny, "tool_count": payloadInteger,
		}),
	},
	EventBrowserCatalogToolRemoved: {
		requiresBrowser: true, requiresTarget: true, requiresGeneration: true,
		payloadFields: fieldsWithKinds(map[string]payloadFieldKind{
			"tool_refs": payloadAny, "tools": payloadAny, "refs": payloadAny,
		}),
	},
	EventBrowserCatalogReady: {
		requiresBrowser: true, requiresTarget: true, requiresGeneration: true,
		payloadFields: fieldsWithKinds(map[string]payloadFieldKind{
			"tool_count": payloadInteger, "schema_digest": payloadString,
		}),
	},
	EventBrowserInvocationCreated: {
		requiresBrowser: true, requiresTarget: true, requiresGeneration: true,
		payloadFields: fieldsWithKinds(map[string]payloadFieldKind{
			"invocation_id": payloadIdentifier, "tool_ref": payloadIdentifier, "tool_name": payloadString, "frame_id": payloadIdentifier,
		}),
	},
	EventBrowserInvocationApproval: {
		requiresBrowser: true, requiresTarget: true, requiresGeneration: true,
		payloadFields: fieldsWithKinds(map[string]payloadFieldKind{
			"invocation_id": payloadIdentifier, "approved": payloadBoolean, "decision": payloadString, "reason": payloadString,
		}),
	},
	EventBrowserInvocationDispatched: {
		requiresBrowser: true, requiresTarget: true, requiresGeneration: true,
		payloadFields: fieldsWithKinds(map[string]payloadFieldKind{
			"invocation_id": payloadIdentifier, "tool_ref": payloadIdentifier, "input": payloadAny, "input_sha256": payloadString, "input_digest": payloadString,
		}),
	},
	EventBrowserInvocationCompleted: {
		requiresBrowser: true, requiresTarget: true, requiresGeneration: true,
		payloadFields: fieldsWithKinds(map[string]payloadFieldKind{
			"invocation_id": payloadIdentifier, "status": payloadString, "output": payloadAny, "output_sha256": payloadString, "output_digest": payloadString, "error": payloadAny,
		}),
	},
	EventBrowserInvocationError: {
		requiresBrowser: true, requiresTarget: true, requiresGeneration: true,
		payloadFields: fieldsWithKinds(map[string]payloadFieldKind{
			"invocation_id": payloadIdentifier, "tool_ref": payloadIdentifier, "tool_name": payloadString, "code": payloadString, "error": payloadAny, "message": payloadString,
		}),
	},
	EventBrowserInvocationCancel: {
		requiresBrowser: true, requiresTarget: true, requiresGeneration: true,
		payloadFields: fieldsWithKinds(map[string]payloadFieldKind{
			"invocation_id": payloadIdentifier, "source": payloadString, "reason": payloadString,
		}),
	},
	EventBrowserInvocationCanceled: {
		requiresBrowser: true, requiresTarget: true, requiresGeneration: true,
		payloadFields: fieldsWithKinds(map[string]payloadFieldKind{
			"invocation_id": payloadIdentifier, "source": payloadString, "reason": payloadString,
		}),
	},
	EventBrowserPageGenerationChanged: {
		requiresBrowser: true, requiresTarget: true,
		payloadFields: fieldsWithKinds(map[string]payloadFieldKind{
			"previous_generation": payloadInteger, "current_generation": payloadInteger, "generation": payloadInteger, "reason": payloadString,
		}),
	},
	EventBrowserTargetDetached: {
		requiresBrowser: true, requiresTarget: true,
		payloadFields: fieldsWithKinds(map[string]payloadFieldKind{
			"reason": payloadString, "ownership": payloadString, "ownership_mode": payloadString,
		}),
	},
	EventBrowserChromeTargetClosed: {
		requiresBrowser: true, requiresTarget: true,
		payloadFields: fieldsWithKinds(map[string]payloadFieldKind{
			"reason": payloadString, "ownership": payloadString, "ownership_mode": payloadString,
		}),
	},
}

func definitionFor(eventType EventType) eventDefinition {
	return eventDefinitions[eventType]
}

func validatePayloadControls(eventType EventType, raw json.RawMessage, allowed map[string]payloadFieldKind) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		// The event payload is a JSON value. Control-field validation applies to
		// the object envelope; page-owned scalar/null values remain valid data.
		return nil
	}
	object, err := decodeJSONObject(trimmed)
	if err != nil {
		return newEventValidationError(0, "payload", "%v", err)
	}
	for name, value := range object {
		kind, ok := allowed[name]
		if !ok {
			return newEventValidationError(0, "payload."+name, "unknown control field for %s", eventType)
		}
		if err := validatePayloadField(value, kind); err != nil {
			return newEventValidationError(0, "payload."+name, "%v", err)
		}
	}
	return nil
}

func validatePayloadField(raw json.RawMessage, kind payloadFieldKind) error {
	switch kind {
	case payloadAny:
		return nil
	case payloadString:
		value, err := parseString(raw)
		if err != nil {
			return err
		}
		if value == "" {
			return errors.New("must not be empty")
		}
	case payloadBoolean:
		_, err := parseBool(raw)
		return err
	case payloadInteger:
		_, err := parseUint(raw)
		return err
	case payloadIdentifier:
		value, err := parseString(raw)
		if err != nil {
			return err
		}
		return validateOpaqueID(value)
	}
	return nil
}

func parseEvent(data []byte) (Event, error) {
	fields, err := decodeJSONObject(data)
	if err != nil {
		return Event{}, newEventValidationError(0, "object", "%v", err)
	}
	if err := rejectUnknownFields(fields, map[string]struct{}{
		"version": {}, "sequence": {}, "monotonic_ms": {}, "type": {}, "browser_id": {}, "target_id": {}, "generation": {},
		"payload": {}, "payload_sha256": {}, "redaction": {},
	}); err != nil {
		return Event{}, newEventValidationError(0, "object", "%v", err)
	}
	versionRaw, ok := fields["version"]
	if !ok {
		return Event{}, newEventValidationError(0, "version", "is required")
	}
	version, err := parseString(versionRaw)
	if err != nil {
		return Event{}, newEventValidationError(0, "version", "%v", err)
	}
	sequenceRaw, ok := fields["sequence"]
	if !ok {
		return Event{}, newEventValidationError(0, "sequence", "is required")
	}
	sequence, err := parseUint(sequenceRaw)
	if err != nil {
		return Event{}, newEventValidationError(0, "sequence", "%v", err)
	}
	monotonicRaw, ok := fields["monotonic_ms"]
	if !ok {
		return Event{}, newEventValidationError(0, "monotonic_ms", "is required")
	}
	monotonic, err := parseUint(monotonicRaw)
	if err != nil {
		return Event{}, newEventValidationError(0, "monotonic_ms", "%v", err)
	}
	typeRaw, ok := fields["type"]
	if !ok {
		return Event{}, newEventValidationError(0, "type", "is required")
	}
	eventType, err := parseString(typeRaw)
	if err != nil {
		return Event{}, newEventValidationError(0, "type", "%v", err)
	}
	event := Event{
		Version:     version,
		Sequence:    sequence,
		MonotonicMS: monotonic,
		Type:        EventType(eventType),
	}
	if raw, ok := fields["browser_id"]; ok {
		event.BrowserID, err = parseString(raw)
		if err != nil {
			return Event{}, newEventValidationError(0, "browser_id", "%v", err)
		}
	}
	if raw, ok := fields["target_id"]; ok {
		event.TargetID, err = parseString(raw)
		if err != nil {
			return Event{}, newEventValidationError(0, "target_id", "%v", err)
		}
	}
	if raw, ok := fields["generation"]; ok {
		event.Generation, err = parseUint(raw)
		if err != nil {
			return Event{}, newEventValidationError(0, "generation", "%v", err)
		}
		event.generationSet = true
	}
	payload, hasPayload := fields["payload"]
	digest, hasDigest := fields["payload_sha256"]
	if hasPayload {
		event.Payload, err = normalizeJSON(payload)
		if err != nil {
			return Event{}, newEventValidationError(0, "payload", "%v", err)
		}
	}
	if hasDigest {
		event.PayloadSHA256, err = parseString(digest)
		if err != nil {
			return Event{}, newEventValidationError(0, "payload_sha256", "%v", err)
		}
	}
	redactionRaw, ok := fields["redaction"]
	if !ok {
		return Event{}, newEventValidationError(0, "redaction", "is required")
	}
	if err := json.Unmarshal(redactionRaw, &event.Redaction); err != nil {
		return Event{}, newEventValidationError(0, "redaction", "%v", err)
	}
	if definitionFor(event.Type).requiresGeneration && !event.generationSet {
		return Event{}, newEventValidationError(0, "generation", "is required for %s", event.Type)
	}
	if err := validateEvent(event); err != nil {
		return Event{}, err
	}
	return event, nil
}
