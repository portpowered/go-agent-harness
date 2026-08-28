package testkit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

const (
	// RedactionMarker is the stable marker used wherever configured sensitive
	// text is removed from canonical browser evidence.
	RedactionMarker = "REDACTED"

	// BrowserRedactionMarker and RecordingRedactionMarker are descriptive
	// aliases for callers that share redaction helpers with recording code.
	BrowserRedactionMarker   = RedactionMarker
	RecordingRedactionMarker = RedactionMarker
)

var (
	// ErrInvalidRedactionPolicy identifies a malformed or non-canonical policy.
	ErrInvalidRedactionPolicy = errors.New("webmcp testkit: invalid redaction policy")
	// ErrInvalidRedactionCredential identifies a credential configuration that
	// cannot safely be used for byte replacement.
	ErrInvalidRedactionCredential = errors.New("webmcp testkit: invalid redaction credential")
	// ErrRedactionCredentialSurvived identifies a configured credential that was
	// still present after the pre-persistence redaction boundary.
	ErrRedactionCredentialSurvived = errors.New("webmcp testkit: configured credential survived redaction")
	// ErrRawCDPNotAllowed identifies an attempt to use canonical semantic
	// recording for raw CDP diagnostics.
	ErrRawCDPNotAllowed = errors.New("webmcp testkit: raw CDP is not allowed in canonical browser evidence")
	// ErrRawCDPDetected identifies a raw CDP field in a semantic event payload.
	ErrRawCDPDetected = errors.New("webmcp testkit: raw CDP field in canonical browser evidence")
)

// RedactionError preserves a safe, inspectable redaction failure. Its text is
// scrubbed with the configured credentials before it is returned to a caller.
// In particular, credential-survival errors never echo the value that leaked.
type RedactionError struct {
	Kind      error
	Operation string
	Path      string
	Cause     error

	secrets [][]byte
}

func (e *RedactionError) Error() string {
	if e == nil {
		return "webmcp testkit: redaction error"
	}
	parts := []string{"webmcp testkit: redaction"}
	if e.Operation != "" {
		parts = append(parts, e.Operation)
	}
	if e.Path != "" {
		parts = append(parts, e.safe(e.Path))
	}
	message := strings.Join(parts, " ")
	if e.Cause != nil {
		message += ": " + e.safe(e.Cause.Error())
	}
	return message
}

func (e *RedactionError) Unwrap() error {
	if e == nil {
		return nil
	}
	identities := make([]error, 0, 2)
	if e.Kind != nil {
		identities = append(identities, e.Kind)
	}
	if e.Cause != nil {
		identities = append(identities, e.Cause)
	}
	return errors.Join(identities...)
}

func (e *RedactionError) safe(value string) string {
	return string(redactBytes(value, e.secrets))
}

func newRedactionError(kind error, operation, path string, cause error, secrets [][]byte) error {
	return &RedactionError{Kind: kind, Operation: operation, Path: path, Cause: cause, secrets: secrets}
}

// RedactionPolicy is the effective, serializable browser redaction policy.
// Credentials are intentionally not a field of the JSON representation: they
// are input to the redaction boundary and must never become manifest metadata.
//
// The six serialized fields are the frozen C0 policy vocabulary. MarshalJSON
// always emits all six fields, including empty arrays, so effective policies
// have one deterministic shape.
type RedactionPolicy struct {
	URLQuery           bool     `json:"url_query"`
	URLFragment        bool     `json:"url_fragment"`
	ToolArguments      []string `json:"tool_arguments"`
	ResultJSONPointers []string `json:"result_json_pointers"`
	DigestTools        []string `json:"digest_tools"`
	RawCDP             bool     `json:"raw_cdp"`

	// Credentials are accepted as an in-memory convenience for callers that
	// keep policy and secrets together. Custom JSON marshaling omits them.
	Credentials []string `json:"-"`
}

// BrowserRedactionPolicy is a descriptive alias for RedactionPolicy.
type BrowserRedactionPolicy = RedactionPolicy

// RedactionConfig combines an effective policy with credentials that must be
// removed before an artifact is serialized or hashed.
type RedactionConfig struct {
	Policy      RedactionPolicy
	Credentials []string
}

// BrowserRedactionConfig and RedactionOptions are descriptive aliases for
// RedactionConfig.
type BrowserRedactionConfig = RedactionConfig
type RedactionOptions = RedactionConfig

var redactionPolicyFieldOrder = []string{
	"url_query",
	"url_fragment",
	"tool_arguments",
	"result_json_pointers",
	"digest_tools",
	"raw_cdp",
}

// Validate checks the exact C0 policy values. RawCDP is accepted here so the
// same value can describe an explicitly enabled diagnostic configuration;
// canonical browser-event APIs call ValidateCanonical, which rejects it.
func (p RedactionPolicy) Validate() error {
	if err := validatePolicyToolNames("tool_arguments", p.ToolArguments); err != nil {
		return err
	}
	if err := validatePolicyPointers(p.ResultJSONPointers); err != nil {
		return err
	}
	if err := validatePolicyToolNames("digest_tools", p.DigestTools); err != nil {
		return err
	}
	return nil
}

// ValidateCanonical checks whether the policy may be used for the semantic
// browser-events.v1 artifact. Raw CDP capture is intentionally a separate
// diagnostic artifact and can never be canonical input.
func (p RedactionPolicy) ValidateCanonical() error {
	if err := p.Validate(); err != nil {
		return err
	}
	if p.RawCDP {
		return newRedactionError(ErrRawCDPNotAllowed, "validate policy", "raw_cdp", nil, nil)
	}
	return nil
}

// Normalize returns the deterministic effective policy used in serialized
// manifest metadata. It returns an error rather than silently repairing an
// invalid policy.
func (p RedactionPolicy) Normalize() (RedactionPolicy, error) {
	if err := p.Validate(); err != nil {
		return RedactionPolicy{}, err
	}
	return p.normalized(), nil
}

func (p RedactionPolicy) normalized() RedactionPolicy {
	return RedactionPolicy{
		URLQuery:           p.URLQuery,
		URLFragment:        p.URLFragment,
		ToolArguments:      normalizePolicyList(p.ToolArguments),
		ResultJSONPointers: normalizePolicyList(p.ResultJSONPointers),
		DigestTools:        normalizePolicyList(p.DigestTools),
		RawCDP:             p.RawCDP,
	}
}

func (p RedactionPolicy) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	normalized := p.normalized()
	type wire struct {
		URLQuery           bool     `json:"url_query"`
		URLFragment        bool     `json:"url_fragment"`
		ToolArguments      []string `json:"tool_arguments"`
		ResultJSONPointers []string `json:"result_json_pointers"`
		DigestTools        []string `json:"digest_tools"`
		RawCDP             bool     `json:"raw_cdp"`
	}
	return json.Marshal(wire{
		URLQuery:           normalized.URLQuery,
		URLFragment:        normalized.URLFragment,
		ToolArguments:      normalized.ToolArguments,
		ResultJSONPointers: normalized.ResultJSONPointers,
		DigestTools:        normalized.DigestTools,
		RawCDP:             normalized.RawCDP,
	})
}

func (p *RedactionPolicy) UnmarshalJSON(data []byte) error {
	if p == nil {
		return errors.New("cannot unmarshal redaction policy into nil receiver")
	}
	fields, err := decodeJSONObject(data)
	if err != nil {
		return wrapPolicyError("policy", err)
	}
	allowed := make(map[string]struct{}, len(redactionPolicyFieldOrder))
	for _, field := range redactionPolicyFieldOrder {
		allowed[field] = struct{}{}
	}
	if err := rejectUnknownFields(fields, allowed); err != nil {
		return wrapPolicyError("policy", err)
	}
	for _, field := range redactionPolicyFieldOrder {
		if _, ok := fields[field]; !ok {
			return wrapPolicyError(field, errors.New("is required"))
		}
	}
	result := RedactionPolicy{}
	if result.URLQuery, err = parsePolicyBool(fields["url_query"]); err != nil {
		return wrapPolicyError("url_query", err)
	}
	if result.URLFragment, err = parsePolicyBool(fields["url_fragment"]); err != nil {
		return wrapPolicyError("url_fragment", err)
	}
	if result.ToolArguments, err = parsePolicyStringArray(fields["tool_arguments"]); err != nil {
		return wrapPolicyError("tool_arguments", err)
	}
	if result.ResultJSONPointers, err = parsePolicyStringArray(fields["result_json_pointers"]); err != nil {
		return wrapPolicyError("result_json_pointers", err)
	}
	if result.DigestTools, err = parsePolicyStringArray(fields["digest_tools"]); err != nil {
		return wrapPolicyError("digest_tools", err)
	}
	if result.RawCDP, err = parsePolicyBool(fields["raw_cdp"]); err != nil {
		return wrapPolicyError("raw_cdp", err)
	}
	if err := result.Validate(); err != nil {
		return err
	}
	*p = result.normalized()
	return nil
}

func wrapPolicyError(path string, err error) error {
	if err == nil {
		return nil
	}
	return newRedactionError(ErrInvalidRedactionPolicy, "validate policy", path, err, nil)
}

func parsePolicyBool(raw json.RawMessage) (bool, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, errors.New("must be a boolean")
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, errors.New("must be a boolean")
	}
	return value, nil
}

func parsePolicyStringArray(raw json.RawMessage) ([]string, error) {
	values, err := scriptArray(raw)
	if err != nil {
		return nil, err
	}
	result := make([]string, len(values))
	for index, value := range values {
		parsed, err := parseString(value)
		if err != nil {
			return nil, fmt.Errorf("item %d must be a string", index)
		}
		result[index] = parsed
	}
	return result, nil
}

func validatePolicyToolNames(field string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		if value == "" || strings.TrimSpace(value) != value {
			return wrapPolicyError(fmt.Sprintf("%s[%d]", field, index), errors.New("must be a normalized non-empty tool name"))
		}
		if !utf8.ValidString(value) {
			return wrapPolicyError(fmt.Sprintf("%s[%d]", field, index), errors.New("must be valid UTF-8"))
		}
		for _, character := range value {
			if unicode.IsSpace(character) || unicode.IsControl(character) {
				return wrapPolicyError(fmt.Sprintf("%s[%d]", field, index), errors.New("must not contain whitespace or control characters"))
			}
		}
		if _, ok := seen[value]; ok {
			return wrapPolicyError(fmt.Sprintf("%s[%d]", field, index), fmt.Errorf("duplicate tool name %q", value))
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validatePolicyPointers(values []string) error {
	seen := make(map[string]struct{}, len(values))
	for index, pointer := range values {
		if err := validateJSONPointer(pointer); err != nil {
			return wrapPolicyError(fmt.Sprintf("result_json_pointers[%d]", index), err)
		}
		if _, ok := seen[pointer]; ok {
			return wrapPolicyError(fmt.Sprintf("result_json_pointers[%d]", index), fmt.Errorf("duplicate JSON Pointer %q", pointer))
		}
		seen[pointer] = struct{}{}
	}
	return nil
}

func normalizePolicyList(values []string) []string {
	result := append([]string(nil), values...)
	if result == nil {
		result = []string{}
	}
	sort.Strings(result)
	return result
}

// Redactor is the pure pre-persistence boundary for semantic browser events.
// It owns no files and performs no network or clock operations.
type Redactor struct {
	policy      RedactionPolicy
	credentials [][]byte
}

// NewRedactor constructs a canonical browser-event redactor. Credentials may
// be supplied in one slice; a policy's in-memory Credentials convenience field
// is included as well but is never serialized.
func NewRedactor(policy RedactionPolicy, credentials ...[]string) (*Redactor, error) {
	if err := policy.ValidateCanonical(); err != nil {
		return nil, err
	}
	configured := append([]string(nil), policy.Credentials...)
	for _, values := range credentials {
		configured = append(configured, values...)
	}
	secretBytes, err := newRedactionCredentials(configured)
	if err != nil {
		return nil, err
	}
	normalized := policy.normalized()
	if containsCredentialInPolicy(normalized, secretBytes) {
		return nil, newRedactionError(ErrRedactionCredentialSurvived, "validate policy", "policy", nil, secretBytes)
	}
	return &Redactor{policy: normalized, credentials: secretBytes}, nil
}

// NewBrowserRedactor constructs a canonical redactor from a policy/config
// pair. It is useful when the policy and credential list are decoded together.
func NewBrowserRedactor(config RedactionConfig) (*Redactor, error) {
	return NewRedactor(config.Policy, config.Credentials)
}

// Policy returns a copy of the effective policy. Credentials are not returned
// as part of it.
func (r *Redactor) Policy() RedactionPolicy {
	if r == nil {
		return RedactionPolicy{}
	}
	return r.policy.normalized()
}

// CredentialsConfigured reports whether this boundary has a configured
// credential. It does not expose the credential values.
func (r *Redactor) CredentialsConfigured() bool {
	return r != nil && len(r.credentials) > 0
}

// RedactEvent applies policy redaction to one semantic event before it can be
// serialized. For completed events, RedactEvents should be preferred because
// it can associate the result with a tool learned from an earlier event.
func (r *Redactor) RedactEvent(event Event) (Event, error) {
	return r.redactEvent(event, "")
}

// RedactEvents applies one redaction boundary to a complete semantic stream.
// Invocation-created/dispatched events establish tool identity for later
// completed events, including when the completion itself has no tool name.
func (r *Redactor) RedactEvents(events []Event) ([]Event, error) {
	if r == nil {
		return nil, newRedactionError(ErrInvalidRedactionPolicy, "redact events", "redactor", errors.New("redactor is nil"), nil)
	}
	if len(events) == 0 {
		return nil, newRedactionError(ErrInvalidBrowserEvent, "redact events", "stream", errors.New("event stream is empty"), r.credentials)
	}
	result := make([]Event, len(events))
	invocationTools := make(map[string]string)
	var previousMS uint64
	for index, event := range events {
		position := index + 1
		if event.Sequence != uint64(position) {
			return nil, newRedactionError(ErrInvalidBrowserEvent, "redact events", fmt.Sprintf("line %d sequence", position), fmt.Errorf("want contiguous sequence %d, got %d", position, event.Sequence), r.credentials)
		}
		if index > 0 && event.MonotonicMS < previousMS {
			return nil, newRedactionError(ErrInvalidBrowserEvent, "redact events", fmt.Sprintf("line %d monotonic_ms", position), fmt.Errorf("decreased from %d to %d", previousMS, event.MonotonicMS), r.credentials)
		}
		previousMS = event.MonotonicMS
		invocationID, tool := eventInvocationAndTool(event.Payload)
		if invocationID != "" {
			// A dispatched/completed event normally carries only tool_ref;
			// prefer the earlier tool name when the invocation was created.
			// The ref remains useful as a fallback for streams that begin
			// mid-invocation.
			if knownTool := invocationTools[invocationID]; knownTool != "" {
				tool = knownTool
			}
		}
		redacted, err := r.redactEvent(event, tool)
		if err != nil {
			return nil, withRedactionPosition(err, position, r.credentials)
		}
		result[index] = redacted
		if invocationID != "" && tool != "" {
			switch event.Type {
			case EventBrowserInvocationCreated, EventBrowserInvocationDispatched:
				invocationTools[invocationID] = tool
			}
		}
	}
	return result, nil
}

func withRedactionPosition(err error, position int, secrets [][]byte) error {
	if err == nil {
		return nil
	}
	var redactionErr *RedactionError
	if errors.As(err, &redactionErr) {
		copyOf := *redactionErr
		if copyOf.Path == "" {
			copyOf.Path = fmt.Sprintf("events[%d]", position-1)
		} else {
			copyOf.Path = fmt.Sprintf("events[%d].%s", position-1, copyOf.Path)
		}
		if copyOf.secrets == nil {
			copyOf.secrets = secrets
		}
		return &copyOf
	}
	return newRedactionError(ErrInvalidBrowserEvent, "redact events", fmt.Sprintf("events[%d]", position-1), err, secrets)
}

// MarshalEvents serializes the redacted stream as canonical UTF-8 JSONL.
func (r *Redactor) MarshalEvents(events []Event) ([]byte, error) {
	if r == nil {
		return nil, newRedactionError(ErrInvalidRedactionPolicy, "marshal events", "redactor", errors.New("redactor is nil"), nil)
	}
	redacted, err := r.RedactEvents(events)
	if err != nil {
		return nil, err
	}
	data, err := MarshalEvents(redacted)
	if err != nil {
		return nil, err
	}
	if err := r.ValidateArtifactBytes(data); err != nil {
		return nil, err
	}
	return data, nil
}

// HashEvents returns canonical redacted JSONL bytes and the lowercase SHA-256
// digest of exactly those bytes. Hashing happens only after redaction.
func (r *Redactor) HashEvents(events []Event) ([]byte, string, error) {
	data, err := r.MarshalEvents(events)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(data)
	return data, hex.EncodeToString(digest[:]), nil
}

// ValidateArtifactBytes checks the final persisted bytes for configured
// credentials. It is also useful to protect manifest-bound metadata before a
// caller writes it.
func (r *Redactor) ValidateArtifactBytes(data []byte) error {
	if r == nil {
		return newRedactionError(ErrInvalidRedactionPolicy, "validate artifact", "artifact", errors.New("redactor is nil"), nil)
	}
	if containsCredential(data, r.credentials) {
		return newRedactionError(ErrRedactionCredentialSurvived, "validate artifact", "artifact", nil, r.credentials)
	}
	return nil
}

// RedactEvent applies a canonical policy to one event. Credentials may be
// omitted, supplied as one variadic slice, or supplied through policy.Credentials.
func RedactEvent(event Event, policy RedactionPolicy, credentials ...[]string) (Event, error) {
	redactor, err := NewRedactor(policy, credentials...)
	if err != nil {
		return Event{}, err
	}
	return redactor.RedactEvent(event)
}

// RedactEvents applies a canonical policy to a complete event stream.
func RedactEvents(events []Event, policy RedactionPolicy, credentials ...[]string) ([]Event, error) {
	redactor, err := NewRedactor(policy, credentials...)
	if err != nil {
		return nil, err
	}
	return redactor.RedactEvents(events)
}

// MarshalRedactedEvents redacts before canonical JSONL serialization.
func MarshalRedactedEvents(events []Event, policy RedactionPolicy, credentials ...[]string) ([]byte, error) {
	redactor, err := NewRedactor(policy, credentials...)
	if err != nil {
		return nil, err
	}
	return redactor.MarshalEvents(events)
}

// MarshalRedactedEventsWithConfig is the config-shaped variant of
// MarshalRedactedEvents.
func MarshalRedactedEventsWithConfig(events []Event, config RedactionConfig) ([]byte, error) {
	redactor, err := NewBrowserRedactor(config)
	if err != nil {
		return nil, err
	}
	return redactor.MarshalEvents(events)
}

// HashRedactedEvents returns final redacted bytes and their artifact digest.
func HashRedactedEvents(events []Event, policy RedactionPolicy, credentials ...[]string) ([]byte, string, error) {
	redactor, err := NewRedactor(policy, credentials...)
	if err != nil {
		return nil, "", err
	}
	return redactor.HashEvents(events)
}

// RedactedBrowserArtifact is the durable semantic artifact produced by the
// testkit. Its digest is always calculated over Data after redaction.
type RedactedBrowserArtifact struct {
	Format    string
	Data      []byte
	SHA256    string
	Redaction RedactionPolicy
}

// RecordingArtifact adapts a redacted browser artifact to the existing
// transcript bundle writer. The conversion keeps the transcript package as
// the sole owner of manifest.json while retaining the testkit's redaction
// boundary as the source of the artifact bytes and effective policy.
func (a RedactedBrowserArtifact) RecordingArtifact(path string) transcript.BrowserArtifact {
	if path == "" {
		path = transcript.BrowserArtifactDefaultPath
	}
	return transcript.BrowserArtifact{
		Format: a.Format,
		Path:   path,
		Data:   append([]byte(nil), a.Data...),
		SHA256: a.SHA256,
		Redaction: transcript.BrowserRedactionPolicy{
			URLQuery:           a.Redaction.URLQuery,
			URLFragment:        a.Redaction.URLFragment,
			ToolArguments:      append([]string(nil), a.Redaction.ToolArguments...),
			ResultJSONPointers: append([]string(nil), a.Redaction.ResultJSONPointers...),
			DigestTools:        append([]string(nil), a.Redaction.DigestTools...),
			RawCDP:             a.Redaction.RawCDP,
		},
	}
}

// BuildRedactedBrowserArtifact performs the complete pre-persistence boundary
// and computes the digest used by a paired recording manifest.
func BuildRedactedBrowserArtifact(events []Event, config RedactionConfig) (RedactedBrowserArtifact, error) {
	redactor, err := NewBrowserRedactor(config)
	if err != nil {
		return RedactedBrowserArtifact{}, err
	}
	data, digest, err := redactor.HashEvents(events)
	if err != nil {
		return RedactedBrowserArtifact{}, err
	}
	return RedactedBrowserArtifact{
		Format:    BrowserEventsVersion,
		Data:      append([]byte(nil), data...),
		SHA256:    digest,
		Redaction: redactor.Policy(),
	}, nil
}

// WithRedaction configures Recorder to apply the canonical boundary before
// each event is written. Invalid policy/credential configuration is returned
// by NewRecorder after options are applied.
func WithRedaction(policy RedactionPolicy, credentials ...[]string) RecorderOption {
	return func(recorder *Recorder) {
		redactor, err := NewRedactor(policy, credentials...)
		recorder.redactor = redactor
		recorder.redactionErr = err
	}
}

// WithRedactionConfig is the config-shaped Recorder option.
func WithRedactionConfig(config RedactionConfig) RecorderOption {
	return func(recorder *Recorder) {
		redactor, err := NewBrowserRedactor(config)
		recorder.redactor = redactor
		recorder.redactionErr = err
	}
}

func (r *Redactor) redactEvent(event Event, tool string) (Event, error) {
	if r == nil {
		return Event{}, newRedactionError(ErrInvalidRedactionPolicy, "redact event", "redactor", errors.New("redactor is nil"), nil)
	}
	if r.policy.RawCDP {
		return Event{}, newRedactionError(ErrRawCDPNotAllowed, "redact event", "raw_cdp", nil, r.credentials)
	}
	if tool == "" {
		_, tool = eventInvocationAndTool(event.Payload)
	}
	if event.Redaction.Mode == "" {
		event.Redaction.Mode = RedactionNone
	}
	trace := redactionTrace{
		digest: event.PayloadSHA256 != "",
		rules:  map[string]bool{RedactionRuleRawCDPDisabled: true},
	}
	if event.BrowserID != "" {
		redacted, changed := redactPlainString(event.BrowserID, r.credentials)
		if changed {
			event.BrowserID = redacted
			trace.changed = true
		}
	}
	if event.TargetID != "" {
		redacted, changed := redactPlainString(event.TargetID, r.credentials)
		if changed {
			event.TargetID = redacted
			trace.changed = true
		}
	}
	if event.Payload != nil {
		payload, payloadTrace, err := r.redactJSON(event.Payload)
		if err != nil {
			return Event{}, err
		}
		event.Payload = payload
		trace.merge(payloadTrace)
	}
	if event.Payload != nil {
		payload, payloadTrace, err := r.redactToolPayload(event.Payload, event.Type, tool)
		if err != nil {
			return Event{}, err
		}
		event.Payload = payload
		trace.merge(payloadTrace)
	}
	trace.applyTo(&event)
	encoded, err := json.Marshal(event)
	if err != nil {
		return Event{}, newRedactionError(ErrInvalidBrowserEvent, "validate redacted event", "event", err, r.credentials)
	}
	if containsCredential(encoded, r.credentials) {
		return Event{}, newRedactionError(ErrRedactionCredentialSurvived, "validate redacted event", "event", nil, r.credentials)
	}
	return event, nil
}

type redactionTrace struct {
	changed bool
	digest  bool
	rules   map[string]bool
}

func (t *redactionTrace) merge(other redactionTrace) {
	if other.changed {
		t.changed = true
	}
	if other.digest {
		t.digest = true
	}
	if t.rules == nil {
		t.rules = make(map[string]bool)
	}
	for rule := range other.rules {
		t.rules[rule] = true
	}
}

func (t redactionTrace) applyTo(event *Event) {
	mode := RedactionNone
	if t.digest {
		mode = RedactionDigest
	} else if t.changed {
		mode = RedactionRedacted
	}
	rules := make([]string, 0, len(t.rules))
	for _, rule := range redactionRuleOrder {
		if t.rules[rule] {
			rules = append(rules, rule)
		}
	}
	event.Redaction = RedactionMetadata{Mode: mode, Rules: rules}
}

func (r *Redactor) redactJSON(raw json.RawMessage) (json.RawMessage, redactionTrace, error) {
	normalized, err := normalizeJSON(raw)
	if err != nil {
		return nil, redactionTrace{}, newRedactionError(ErrInvalidBrowserEvent, "redact JSON", "payload", err, r.credentials)
	}
	trimmed := bytes.TrimSpace(normalized)
	if len(trimmed) == 0 {
		return nil, redactionTrace{}, newRedactionError(ErrInvalidBrowserEvent, "redact JSON", "payload", errors.New("value is empty"), r.credentials)
	}
	switch trimmed[0] {
	case '{':
		fields, err := decodeJSONObject(trimmed)
		if err != nil {
			return nil, redactionTrace{}, newRedactionError(ErrInvalidBrowserEvent, "redact JSON", "payload", err, r.credentials)
		}
		result := make(map[string]json.RawMessage, len(fields))
		trace := redactionTrace{rules: map[string]bool{}}
		for key, value := range fields {
			if isRawCDPField(key) {
				return nil, trace, newRedactionError(ErrRawCDPDetected, "redact JSON", "payload."+key, nil, r.credentials)
			}
			redactedKey, keyChanged := redactPlainString(key, r.credentials)
			if _, exists := result[redactedKey]; exists {
				return nil, trace, newRedactionError(ErrInvalidBrowserEvent, "redact JSON", "payload", errors.New("credential replacement produced duplicate object fields"), r.credentials)
			}
			child, childTrace, err := r.redactJSON(value)
			if err != nil {
				return nil, trace, err
			}
			result[redactedKey] = child
			if keyChanged {
				trace.changed = true
			}
			trace.merge(childTrace)
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return nil, trace, err
		}
		if !bytes.Equal(encoded, normalized) {
			// A map marshal sorts keys, but key order is not a page-owned
			// semantic difference. The changed bit is only for actual policy
			// changes, not canonical object ordering.
			if !sameJSONStructure(encoded, normalized) {
				trace.changed = true
			}
		}
		return encoded, trace, nil
	case '[':
		values, err := scriptArray(trimmed)
		if err != nil {
			return nil, redactionTrace{}, newRedactionError(ErrInvalidBrowserEvent, "redact JSON", "payload", err, r.credentials)
		}
		result := make([]json.RawMessage, len(values))
		trace := redactionTrace{rules: map[string]bool{}}
		for index, value := range values {
			child, childTrace, err := r.redactJSON(value)
			if err != nil {
				return nil, trace, err
			}
			result[index] = child
			trace.merge(childTrace)
		}
		encoded, err := json.Marshal(result)
		return encoded, trace, err
	case '"':
		value, err := parseString(trimmed)
		if err != nil {
			return nil, redactionTrace{}, newRedactionError(ErrInvalidBrowserEvent, "redact JSON", "payload", err, r.credentials)
		}
		redacted, changed, queryChanged, fragmentChanged := r.redactString(value)
		if !changed {
			return normalized, redactionTrace{rules: map[string]bool{}}, nil
		}
		encoded, err := json.Marshal(redacted)
		trace := redactionTrace{changed: true, rules: map[string]bool{}}
		if queryChanged {
			trace.rules[RedactionRuleURLQuery] = true
		}
		if fragmentChanged {
			trace.rules[RedactionRuleURLFragment] = true
		}
		return encoded, trace, err
	default:
		// Numbers, booleans, and null are already normalized without passing
		// through float64, preserving large page-owned integer tokens exactly.
		return normalized, redactionTrace{rules: map[string]bool{}}, nil
	}
}

func (r *Redactor) redactToolPayload(raw json.RawMessage, eventType EventType, tool string) (json.RawMessage, redactionTrace, error) {
	if tool == "" {
		return raw, redactionTrace{rules: map[string]bool{}}, nil
	}
	if !toolNameIn(tool, r.policy.ToolArguments) && !toolNameIn(tool, r.policy.DigestTools) && eventType != EventBrowserInvocationCompleted {
		// Result pointers apply only to invocation completions, and argument
		// policy applies only to dispatched calls. Avoid touching unrelated
		// page-owned JSON merely because a name happens to match.
		return raw, redactionTrace{rules: map[string]bool{}}, nil
	}
	fields, err := decodeJSONObject(raw)
	if err != nil {
		return raw, redactionTrace{rules: map[string]bool{}}, nil
	}
	trace := redactionTrace{rules: map[string]bool{}}
	digestTool := toolNameIn(tool, r.policy.DigestTools)
	argumentTool := toolNameIn(tool, r.policy.ToolArguments)
	if eventType == EventBrowserInvocationDispatched {
		if input, ok := fields["input"]; ok {
			if digestTool {
				digest, err := digestJSON(input)
				if err != nil {
					return raw, trace, newRedactionError(ErrInvalidBrowserEvent, "redact tool input", "payload.input", err, r.credentials)
				}
				delete(fields, "input")
				fields["input_sha256"] = json.RawMessage(strconv.Quote(digest))
				trace.digest = true
			} else if argumentTool {
				fields["input"] = json.RawMessage(strconv.Quote(RedactionMarker))
				trace.changed = true
				trace.rules[RedactionRuleToolArguments] = true
			}
		}
	}
	if eventType == EventBrowserInvocationCompleted {
		if output, ok := fields["output"]; ok {
			redactedOutput := output
			if len(r.policy.ResultJSONPointers) > 0 {
				for _, pointer := range r.policy.ResultJSONPointers {
					updated, changed, err := replaceJSONPointer(redactedOutput, pointer, json.RawMessage(strconv.Quote(RedactionMarker)))
					if err != nil {
						return raw, trace, newRedactionError(ErrInvalidBrowserEvent, "redact result", "payload.output"+pointer, err, r.credentials)
					}
					if changed {
						redactedOutput = updated
						trace.changed = true
						trace.rules[RedactionRuleResultJSONPointers] = true
					}
				}
			}
			if digestTool {
				digest, err := digestJSON(redactedOutput)
				if err != nil {
					return raw, trace, newRedactionError(ErrInvalidBrowserEvent, "redact tool result", "payload.output", err, r.credentials)
				}
				delete(fields, "output")
				fields["output_sha256"] = json.RawMessage(strconv.Quote(digest))
				trace.digest = true
			} else if !bytes.Equal(redactedOutput, output) {
				fields["output"] = redactedOutput
			}
		}
	}
	encoded, err := json.Marshal(fields)
	return encoded, trace, err
}

func (r *Redactor) redactString(value string) (string, bool, bool, bool) {
	redacted := value
	queryChanged := false
	fragmentChanged := false
	if parsed, ok := parseRedactableURL(value); ok {
		if r.policy.URLQuery && (parsed.RawQuery != "" || parsed.ForceQuery) {
			parsed.RawQuery = ""
			parsed.ForceQuery = false
			queryChanged = true
		}
		if r.policy.URLFragment && (parsed.Fragment != "" || parsed.RawFragment != "") {
			parsed.Fragment = ""
			parsed.RawFragment = ""
			fragmentChanged = true
		}
		if parsed.User != nil {
			if _, hasPassword := parsed.User.Password(); hasPassword {
				parsed.User = url.UserPassword(parsed.User.Username(), RedactionMarker)
			}
		}
		redacted = parsed.String()
	}
	redactedWithCredentials, credentialChanged := redactPlainString(redacted, r.credentials)
	redacted = redactedWithCredentials
	changed := redacted != value || queryChanged || fragmentChanged || credentialChanged
	return redacted, changed, queryChanged, fragmentChanged
}

func redactPlainString(value string, credentials [][]byte) (string, bool) {
	if len(credentials) == 0 {
		return value, false
	}
	redacted := string(redactBytes(value, credentials))
	return redacted, redacted != value
}

func parseRedactableURL(value string) (*url.URL, bool) {
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

func eventInvocationAndTool(raw json.RawMessage) (string, string) {
	fields, err := decodeJSONObject(raw)
	if err != nil {
		return "", ""
	}
	invocationID := stringField(fields, "invocation_id")
	tool := stringField(fields, "tool_name")
	if tool == "" {
		tool = stringField(fields, "tool_ref")
	}
	return invocationID, tool
}

func stringField(fields map[string]json.RawMessage, name string) string {
	raw, ok := fields[name]
	if !ok {
		return ""
	}
	value, err := parseString(raw)
	if err != nil {
		return ""
	}
	return value
}

func toolNameIn(tool string, configured []string) bool {
	for _, candidate := range configured {
		if candidate == tool {
			return true
		}
	}
	return false
}

func digestJSON(raw json.RawMessage) (string, error) {
	normalized, err := normalizeJSON(raw)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(normalized)
	return hex.EncodeToString(digest[:]), nil
}

func validateJSONPointer(pointer string) error {
	if pointer == "" {
		return nil
	}
	if pointer[0] != '/' {
		return errors.New("must be an RFC 6901 JSON Pointer")
	}
	for index := 1; index < len(pointer); index++ {
		if pointer[index] != '~' {
			continue
		}
		if index+1 >= len(pointer) || (pointer[index+1] != '0' && pointer[index+1] != '1') {
			return errors.New("contains an invalid ~ escape")
		}
		index++
	}
	return nil
}

func decodeJSONPointer(pointer string) ([]string, error) {
	if err := validateJSONPointer(pointer); err != nil {
		return nil, err
	}
	if pointer == "" {
		return nil, nil
	}
	parts := strings.Split(pointer[1:], "/")
	for index, part := range parts {
		part = strings.ReplaceAll(part, "~1", "/")
		part = strings.ReplaceAll(part, "~0", "~")
		parts[index] = part
	}
	return parts, nil
}

func replaceJSONPointer(raw json.RawMessage, pointer string, replacement json.RawMessage) (json.RawMessage, bool, error) {
	tokens, err := decodeJSONPointer(pointer)
	if err != nil {
		return nil, false, err
	}
	replacement, err = normalizeJSON(replacement)
	if err != nil {
		return nil, false, err
	}
	return replaceJSONPointerTokens(raw, tokens, replacement)
}

func replaceJSONPointerTokens(raw json.RawMessage, tokens []string, replacement json.RawMessage) (json.RawMessage, bool, error) {
	normalized, err := normalizeJSON(raw)
	if err != nil {
		return nil, false, err
	}
	if len(tokens) == 0 {
		return cloneRaw(replacement), true, nil
	}
	trimmed := bytes.TrimSpace(normalized)
	switch trimmed[0] {
	case '{':
		fields, err := decodeJSONObject(trimmed)
		if err != nil {
			return nil, false, err
		}
		child, ok := fields[tokens[0]]
		if !ok {
			return normalized, false, nil
		}
		updated, changed, err := replaceJSONPointerTokens(child, tokens[1:], replacement)
		if err != nil {
			return nil, false, err
		}
		if !changed {
			return normalized, false, nil
		}
		fields[tokens[0]] = updated
		encoded, err := json.Marshal(fields)
		return encoded, true, err
	case '[':
		values, err := scriptArray(trimmed)
		if err != nil {
			return nil, false, err
		}
		index, err := parseJSONPointerArrayIndex(tokens[0])
		if err != nil || index >= len(values) {
			return normalized, false, nil
		}
		updated, changed, err := replaceJSONPointerTokens(values[index], tokens[1:], replacement)
		if err != nil {
			return nil, false, err
		}
		if !changed {
			return normalized, false, nil
		}
		values[index] = updated
		encoded, err := json.Marshal(values)
		return encoded, true, err
	default:
		return normalized, false, nil
	}
}

func parseJSONPointerArrayIndex(token string) (int, error) {
	if token == "" || (len(token) > 1 && token[0] == '0') {
		return 0, errors.New("invalid array index")
	}
	value, err := strconv.ParseUint(token, 10, 64)
	if err != nil || value > uint64(^uint(0)>>1) {
		return 0, errors.New("invalid array index")
	}
	return int(value), nil
}

func isRawCDPField(name string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(name, "-", "_"), " ", "_"))
	switch normalized {
	case "raw_cdp", "raw_cdp_frame", "raw_cdp_frames", "cdp_frame", "cdp_frames":
		return true
	default:
		return false
	}
}

func newRedactionCredentials(credentials []string) ([][]byte, error) {
	seen := make(map[string]struct{}, len(credentials))
	values := make([][]byte, 0, len(credentials))
	for _, credential := range credentials {
		if credential == "" {
			return nil, newRedactionError(ErrInvalidRedactionCredential, "validate credentials", "credentials", errors.New("credential values must be non-empty"), nil)
		}
		if credential == RedactionMarker {
			return nil, newRedactionError(ErrInvalidRedactionCredential, "validate credentials", "credentials", errors.New("credential conflicts with redaction marker"), nil)
		}
		if !utf8.ValidString(credential) {
			return nil, newRedactionError(ErrInvalidRedactionCredential, "validate credentials", "credentials", errors.New("credential must be valid UTF-8"), nil)
		}
		if _, ok := seen[credential]; ok {
			continue
		}
		seen[credential] = struct{}{}
		values = append(values, []byte(credential))
	}
	sort.Slice(values, func(i, j int) bool {
		if len(values[i]) != len(values[j]) {
			return len(values[i]) > len(values[j])
		}
		return bytes.Compare(values[i], values[j]) < 0
	})
	return values, nil
}

func containsCredentialInPolicy(policy RedactionPolicy, credentials [][]byte) bool {
	data, err := json.Marshal(policy)
	if err != nil {
		return false
	}
	return containsCredential(data, credentials)
}

func containsCredential(value []byte, secrets [][]byte) bool {
	for _, secret := range secrets {
		if bytes.Contains(value, secret) {
			return true
		}
	}
	return false
}

func redactBytes(value string, secrets [][]byte) []byte {
	redacted := []byte(value)
	for _, secret := range secrets {
		redacted = bytes.ReplaceAll(redacted, secret, []byte(RedactionMarker))
	}
	return redacted
}

// EnsureNoConfiguredCredentials is a small manifest-boundary helper. It
// validates arbitrary serialized metadata without exposing the values in an
// error message.
func EnsureNoConfiguredCredentials(data []byte, credentials []string) error {
	secretBytes, err := newRedactionCredentials(credentials)
	if err != nil {
		return err
	}
	if containsCredential(data, secretBytes) {
		return newRedactionError(ErrRedactionCredentialSurvived, "validate metadata", "metadata", nil, secretBytes)
	}
	return nil
}

// RedactRawDiagnostics applies only credential byte replacement to an
// explicitly separate diagnostic blob. It deliberately does not return an
// Event and therefore cannot be used as strict semantic replay input. The
// canonical event APIs reject raw CDP fields and raw_cdp=true.
func RedactRawDiagnostics(data []byte, credentials []string) ([]byte, error) {
	secretBytes, err := newRedactionCredentials(credentials)
	if err != nil {
		return nil, err
	}
	redacted := redactBytes(string(data), secretBytes)
	if containsCredential(redacted, secretBytes) {
		return nil, newRedactionError(ErrRedactionCredentialSurvived, "redact diagnostics", "diagnostic", nil, secretBytes)
	}
	return redacted, nil
}

// WriteRedactedEvents serializes redacted events to a writer only after the
// complete artifact has been transformed and credential-checked. A failed
// transformation never writes a partial event stream.
func WriteRedactedEvents(writer io.Writer, events []Event, policy RedactionPolicy, credentials ...[]string) error {
	if writer == nil {
		return newRedactionError(ErrRecorderWrite, "write events", "writer", errors.New("writer is nil"), nil)
	}
	data, err := MarshalRedactedEvents(events, policy, credentials...)
	if err != nil {
		return err
	}
	n, err := writer.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return newRedactionError(ErrRecorderWrite, "write events", "writer", err, nil)
	}
	return nil
}

// sameJSONStructure is intentionally used only to avoid marking a page-owned
// object as redacted merely because canonical map encoding sorted its keys.
// It compares JSON tokens without decoding numbers through float64.
func sameJSONStructure(first, second []byte) bool {
	var left, right any
	leftDecoder := json.NewDecoder(bytes.NewReader(first))
	rightDecoder := json.NewDecoder(bytes.NewReader(second))
	leftDecoder.UseNumber()
	rightDecoder.UseNumber()
	if leftDecoder.Decode(&left) != nil || rightDecoder.Decode(&right) != nil {
		return false
	}
	return jsonEquivalent(left, right)
}

func jsonEquivalent(left, right any) bool {
	switch leftValue := left.(type) {
	case map[string]any:
		rightValue, ok := right.(map[string]any)
		if !ok || len(leftValue) != len(rightValue) {
			return false
		}
		for key, value := range leftValue {
			other, ok := rightValue[key]
			if !ok || !jsonEquivalent(value, other) {
				return false
			}
		}
		return true
	case []any:
		rightValue, ok := right.([]any)
		if !ok || len(leftValue) != len(rightValue) {
			return false
		}
		for index := range leftValue {
			if !jsonEquivalent(leftValue[index], rightValue[index]) {
				return false
			}
		}
		return true
	case json.Number:
		rightValue, ok := right.(json.Number)
		return ok && leftValue == rightValue
	default:
		return fmt.Sprint(left) == fmt.Sprint(right)
	}
}
