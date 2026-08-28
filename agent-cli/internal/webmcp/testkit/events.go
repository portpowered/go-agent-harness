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
	"strconv"
	"strings"
	"sync"
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
	return json.Marshal(wire{Mode: normalized.Mode, Rules: normalized.Rules})
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
	payload, err := normalizeJSON(e.Payload)
	if err != nil {
		return nil, fmt.Errorf("payload: %w", err)
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

// Clock supplies injected monotonic milliseconds.
type Clock interface {
	MonotonicMillis() uint64
}

// ClockFunc adapts a function to Clock.
type ClockFunc func() uint64

func (f ClockFunc) MonotonicMillis() uint64 {
	if f == nil {
		return 0
	}
	return f()
}

// FakeClock is a deterministic, concurrency-safe clock for fixtures and
// recorders. It never sleeps or consults wall time.
type FakeClock struct {
	mu      sync.Mutex
	current uint64
}

// NewFakeClock returns a fake clock starting at start milliseconds.
func NewFakeClock(start uint64) *FakeClock {
	return &FakeClock{current: start}
}

func (c *FakeClock) MonotonicMillis() uint64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

// Set moves the fake clock to an exact value. Recorder validation reports a
// stable error if a set value is lower than the previous event.
func (c *FakeClock) Set(value uint64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.current = value
	c.mu.Unlock()
}

// Advance increases the fake clock without sleeping. Overflow saturates at
// the largest representable millisecond value.
func (c *FakeClock) Advance(delta uint64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if ^uint64(0)-c.current < delta {
		c.current = ^uint64(0)
	} else {
		c.current += delta
	}
	c.mu.Unlock()
}

// IDSource supplies deterministic opaque IDs.
type IDSource interface {
	NextID(kind string) string
}

// IDSourceFunc adapts a function to IDSource.
type IDSourceFunc func(kind string) string

func (f IDSourceFunc) NextID(kind string) string {
	if f == nil {
		return ""
	}
	return f(kind)
}

// DeterministicIDSource produces stable IDs for an equivalent sequence of
// fixture operations.
type DeterministicIDSource struct {
	mu     sync.Mutex
	prefix string
	next   uint64
}

// NewDeterministicIDSource returns IDs such as fixture-invocation-001.
func NewDeterministicIDSource(prefix string) *DeterministicIDSource {
	prefix = normalizeIDPart(prefix)
	if prefix == "" {
		prefix = "fixture"
	}
	return &DeterministicIDSource{prefix: prefix}
}

// NewFakeIDs is a descriptive alias for NewDeterministicIDSource.
func NewFakeIDs(prefix string) *DeterministicIDSource {
	return NewDeterministicIDSource(prefix)
}

func (s *DeterministicIDSource) NextID(kind string) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	kind = normalizeIDPart(kind)
	if kind == "" {
		kind = "id"
	}
	return fmt.Sprintf("%s-%s-%03d", s.prefix, kind, s.next)
}

// RecorderOption configures a Recorder.
type RecorderOption func(*Recorder)

// WithClock injects a monotonic clock. A nil clock is ignored.
func WithClock(clock Clock) RecorderOption {
	return func(recorder *Recorder) {
		if clock != nil {
			recorder.clock = clock
		}
	}
}

// WithClockFunc injects a function-backed monotonic clock.
func WithClockFunc(clock func() uint64) RecorderOption {
	return WithClock(ClockFunc(clock))
}

// WithIDSource injects deterministic ID allocation. A nil source is ignored.
func WithIDSource(source IDSource) RecorderOption {
	return func(recorder *Recorder) {
		if source != nil {
			recorder.ids = source
		}
	}
}

// WithIDFunc injects a function-backed deterministic ID source.
func WithIDFunc(source func(string) string) RecorderOption {
	return WithIDSource(IDSourceFunc(source))
}

// Recorder appends validated canonical browser event lines to an io.Writer.
// Its zero event time is deterministic; callers needing generated IDs must
// provide an IDSource and use NewID.
type Recorder struct {
	mu            sync.Mutex
	writer        io.Writer
	clock         Clock
	ids           IDSource
	redactor      *Redactor
	redactionErr  error
	nextSequence  uint64
	lastMonotonic uint64
	hasEvents     bool
}

// NewRecorder constructs a recorder. The default clock always returns zero,
// which is useful for deterministic tests and remains valid because equal
// monotonic offsets are allowed.
func NewRecorder(writer io.Writer, options ...RecorderOption) (*Recorder, error) {
	if writer == nil {
		return nil, fmt.Errorf("%w: writer is nil", ErrRecorderWrite)
	}
	recorder := &Recorder{
		writer:       writer,
		clock:        ClockFunc(func() uint64 { return 0 }),
		nextSequence: 1,
	}
	for _, option := range options {
		if option != nil {
			option(recorder)
		}
	}
	if recorder.redactionErr != nil {
		return nil, recorder.redactionErr
	}
	return recorder, nil
}

// NewID obtains one deterministic ID from the configured source.
func (r *Recorder) NewID(kind string) (string, error) {
	if r == nil {
		return "", ErrIDSourceUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ids == nil {
		return "", ErrIDSourceUnavailable
	}
	id := r.ids.NextID(kind)
	if err := validateOpaqueID(id); err != nil {
		return "", fmt.Errorf("generated %s ID: %w", kind, err)
	}
	return id, nil
}

// Record assigns version, sequence, and injected monotonic time, then writes
// one event. A failed validation or write does not advance recorder state.
func (r *Recorder) Record(input EventInput) (Event, error) {
	if r == nil {
		return Event{}, fmt.Errorf("%w: recorder is nil", ErrRecorderWrite)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.clock == nil {
		return Event{}, fmt.Errorf("%w: clock is nil", ErrRecorderWrite)
	}
	event := Event{
		Version:       BrowserEventsVersion,
		Sequence:      r.nextSequence,
		MonotonicMS:   r.clock.MonotonicMillis(),
		Type:          input.Type,
		BrowserID:     input.BrowserID,
		TargetID:      input.TargetID,
		Generation:    input.Generation,
		Payload:       cloneRaw(input.Payload),
		PayloadSHA256: input.PayloadSHA256,
		Redaction:     input.Redaction,
	}
	if event.Redaction.Mode == "" {
		event.Redaction.Mode = RedactionNone
	}
	if err := r.writeLocked(event); err != nil {
		return Event{}, err
	}
	return event, nil
}

// Write appends an explicitly sequenced event. Sequence zero is assigned the
// next contiguous value; all other sequence values must match the cursor.
func (r *Recorder) Write(event Event) error {
	if r == nil {
		return fmt.Errorf("%w: recorder is nil", ErrRecorderWrite)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if event.Version == "" {
		event.Version = BrowserEventsVersion
	}
	if event.Sequence == 0 {
		event.Sequence = r.nextSequence
	}
	return r.writeLocked(event)
}

func (r *Recorder) writeLocked(event Event) error {
	if r.redactionErr != nil {
		return r.redactionErr
	}
	if r.redactor != nil {
		redacted, err := r.redactor.RedactEvent(event)
		if err != nil {
			return err
		}
		event = redacted
	}
	if event.Sequence != r.nextSequence {
		return newEventValidationError(int(r.nextSequence), "sequence", "want %d, got %d", r.nextSequence, event.Sequence)
	}
	if r.hasEvents && event.MonotonicMS < r.lastMonotonic {
		return fmt.Errorf("%w: previous=%d current=%d", ErrRecorderClock, r.lastMonotonic, event.MonotonicMS)
	}
	if err := validateEvent(event); err != nil {
		return err
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("%w: encode event: %v", ErrRecorderWrite, err)
	}
	encoded = append(encoded, '\n')
	n, writeErr := r.writer.Write(encoded)
	if writeErr == nil && n != len(encoded) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		return fmt.Errorf("%w: %v", ErrRecorderWrite, writeErr)
	}
	if event.Sequence == ^uint64(0) {
		return fmt.Errorf("%w: sequence overflow", ErrRecorderWrite)
	}
	r.nextSequence = event.Sequence + 1
	r.lastMonotonic = event.MonotonicMS
	r.hasEvents = true
	return nil
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

func fields(names ...string) map[string]payloadFieldKind {
	result := make(map[string]payloadFieldKind, len(names))
	for _, name := range names {
		result[name] = payloadAny
	}
	return result
}

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
			"invocation_id": payloadIdentifier, "code": payloadString, "error": payloadAny, "message": payloadString,
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

func withLine(err error, line int) error {
	var validation *EventValidationError
	if errors.As(err, &validation) {
		copyOf := *validation
		copyOf.Line = line
		return &copyOf
	}
	return newEventValidationError(line, "line", "%v", err)
}

func decodeJSONObject(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	fields := make(map[string]json.RawMessage)
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, errors.New("value must be a JSON object")
	}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("object key must be a string")
		}
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("duplicate field %q", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = cloneRaw(value)
	}
	end, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := end.(json.Delim); !ok || delimiter != '}' {
		return nil, errors.New("object is not terminated")
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values are not allowed")
		}
		return nil, err
	}
	return fields, nil
}

func rejectUnknownFields(fields map[string]json.RawMessage, allowed map[string]struct{}) error {
	for name := range fields {
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("unknown field %q", name)
		}
	}
	return nil
}

func parseString(raw json.RawMessage) (string, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", errors.New("must be a string")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	return value, nil
}

func parseBool(raw json.RawMessage) (bool, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, errors.New("must be a boolean")
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, err
	}
	return value, nil
}

func parseUint(raw json.RawMessage) (uint64, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return 0, errors.New("must be a non-negative integer")
	}
	value, err := strconv.ParseUint(string(trimmed), 10, 64)
	if err != nil {
		return 0, errors.New("must be a non-negative integer")
	}
	return value, nil
}
