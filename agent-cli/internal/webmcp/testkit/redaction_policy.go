package testkit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
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
