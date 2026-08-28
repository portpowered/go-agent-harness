package transcript

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// RecordingManifestV1Version is the unchanged legacy manifest version.
	RecordingManifestV1Version = RecordingManifestVersion
	// RecordingManifestV2Version adds the optional paired browser evidence.
	RecordingManifestV2Version = 2

	// BrowserEventsVersion is the only semantic browser artifact format that
	// can be paired with a recording manifest.
	BrowserEventsVersion = "webmcp.browser-events.v1"
	// BrowserArtifactDefaultPath is the canonical location for browser events
	// in a recording directory.
	BrowserArtifactDefaultPath = "browser.events.jsonl"
)

var (
	// ErrInvalidRecordingManifest identifies malformed or inconsistent manifest
	// metadata. It is separate from ErrInvalidRecording so callers can
	// distinguish a decoded manifest from a writer configuration failure.
	ErrInvalidRecordingManifest = errors.New("transcript: invalid recording manifest")
	// ErrUnknownRecordingManifestVersion identifies a version that this package
	// cannot safely interpret.
	ErrUnknownRecordingManifestVersion = errors.New("transcript: unknown recording manifest version")
	// ErrInvalidBrowserArtifact identifies malformed browser evidence supplied
	// to the recording writer.
	ErrInvalidBrowserArtifact = errors.New("transcript: invalid browser artifact")
)

var browserRedactionPolicyFieldOrder = []string{
	"url_query",
	"url_fragment",
	"tool_arguments",
	"result_json_pointers",
	"digest_tools",
	"raw_cdp",
}

// BrowserRedactionPolicy is the effective, serializable C0 browser redaction
// policy. Credentials are deliberately not represented here; they are
// supplied to the pre-persistence redaction boundary and never become
// manifest metadata.
type BrowserRedactionPolicy struct {
	URLQuery           bool     `json:"url_query"`
	URLFragment        bool     `json:"url_fragment"`
	ToolArguments      []string `json:"tool_arguments"`
	ResultJSONPointers []string `json:"result_json_pointers"`
	DigestTools        []string `json:"digest_tools"`
	RawCDP             bool     `json:"raw_cdp"`
}

// BrowserRecordingRedactionPolicy is a descriptive alias for callers that
// use recording terminology.
type BrowserRecordingRedactionPolicy = BrowserRedactionPolicy

// Validate checks the frozen policy vocabulary and value shapes.
func (p BrowserRedactionPolicy) Validate() error {
	if err := validateBrowserPolicyToolNames("tool_arguments", p.ToolArguments); err != nil {
		return err
	}
	if err := validateBrowserPolicyPointers(p.ResultJSONPointers); err != nil {
		return err
	}
	if err := validateBrowserPolicyToolNames("digest_tools", p.DigestTools); err != nil {
		return err
	}
	return nil
}

// ValidateCanonical rejects raw CDP capture, which is a separate diagnostic
// artifact and can never be strict semantic replay input.
func (p BrowserRedactionPolicy) ValidateCanonical() error {
	if err := p.Validate(); err != nil {
		return err
	}
	if p.RawCDP {
		return invalidBrowserArtifact("redaction.raw_cdp must be false")
	}
	return nil
}

// Normalize returns a deterministic effective policy. Lists are copied,
// deduplicated values are rejected, and values are sorted for stable JSON.
func (p BrowserRedactionPolicy) Normalize() (BrowserRedactionPolicy, error) {
	if err := p.Validate(); err != nil {
		return BrowserRedactionPolicy{}, err
	}
	return BrowserRedactionPolicy{
		URLQuery:           p.URLQuery,
		URLFragment:        p.URLFragment,
		ToolArguments:      normalizeBrowserPolicyList(p.ToolArguments),
		ResultJSONPointers: normalizeBrowserPolicyList(p.ResultJSONPointers),
		DigestTools:        normalizeBrowserPolicyList(p.DigestTools),
		RawCDP:             p.RawCDP,
	}, nil
}

// MarshalJSON emits all six policy keys in a stable order, including empty
// arrays. That makes effective policy metadata deterministic and unambiguous.
func (p BrowserRedactionPolicy) MarshalJSON() ([]byte, error) {
	normalized, err := p.Normalize()
	if err != nil {
		return nil, err
	}
	type wire struct {
		URLQuery           bool     `json:"url_query"`
		URLFragment        bool     `json:"url_fragment"`
		ToolArguments      []string `json:"tool_arguments"`
		ResultJSONPointers []string `json:"result_json_pointers"`
		DigestTools        []string `json:"digest_tools"`
		RawCDP             bool     `json:"raw_cdp"`
	}
	return json.Marshal(wire(normalized))
}

// UnmarshalJSON accepts only the six frozen policy fields and requires each
// one to be present. Unknown fields are rejected rather than ignored.
func (p *BrowserRedactionPolicy) UnmarshalJSON(data []byte) error {
	if p == nil {
		return errors.New("cannot unmarshal browser redaction policy into nil receiver")
	}
	fields, err := decodeRecordingJSONObject(data)
	if err != nil {
		return invalidBrowserArtifact("redaction: %v", err)
	}
	allowed := make(map[string]struct{}, len(browserRedactionPolicyFieldOrder))
	for _, field := range browserRedactionPolicyFieldOrder {
		allowed[field] = struct{}{}
	}
	if err := rejectRecordingUnknownFields(fields, allowed); err != nil {
		return invalidBrowserArtifact("redaction: %v", err)
	}
	for _, field := range browserRedactionPolicyFieldOrder {
		if _, ok := fields[field]; !ok {
			return invalidBrowserArtifact("redaction.%s is required", field)
		}
	}

	result := BrowserRedactionPolicy{}
	if result.URLQuery, err = parseRecordingBool(fields["url_query"]); err != nil {
		return invalidBrowserArtifact("redaction.url_query: %v", err)
	}
	if result.URLFragment, err = parseRecordingBool(fields["url_fragment"]); err != nil {
		return invalidBrowserArtifact("redaction.url_fragment: %v", err)
	}
	if result.ToolArguments, err = parseRecordingStringArray(fields["tool_arguments"]); err != nil {
		return invalidBrowserArtifact("redaction.tool_arguments: %v", err)
	}
	if result.ResultJSONPointers, err = parseRecordingStringArray(fields["result_json_pointers"]); err != nil {
		return invalidBrowserArtifact("redaction.result_json_pointers: %v", err)
	}
	if result.DigestTools, err = parseRecordingStringArray(fields["digest_tools"]); err != nil {
		return invalidBrowserArtifact("redaction.digest_tools: %v", err)
	}
	if result.RawCDP, err = parseRecordingBool(fields["raw_cdp"]); err != nil {
		return invalidBrowserArtifact("redaction.raw_cdp: %v", err)
	}
	normalized, err := result.Normalize()
	if err != nil {
		return err
	}
	*p = normalized
	return nil
}

// BrowserArtifact is the pre-persistence browser evidence passed to
// RecordingConfig. Data must already have gone through the semantic redaction
// boundary; the recording writer verifies that configured credentials did not
// survive and hashes exactly these bytes.
type BrowserArtifact struct {
	Format    string
	Path      string
	Data      []byte
	SHA256    string
	Redaction BrowserRedactionPolicy
}

// BrowserEvidence is a descriptive alias for BrowserArtifact.
type BrowserEvidence = BrowserArtifact

// Normalize validates and copies one browser artifact, filling its canonical
// path and digest when callers leave those convenience fields empty.
func (a BrowserArtifact) Normalize() (BrowserArtifact, error) {
	if a.Format != BrowserEventsVersion {
		return BrowserArtifact{}, invalidBrowserArtifact("format must be %q", BrowserEventsVersion)
	}
	if len(a.Data) == 0 {
		return BrowserArtifact{}, invalidBrowserArtifact("data must be non-empty")
	}
	if !utf8.Valid(a.Data) {
		return BrowserArtifact{}, invalidBrowserArtifact("data must be valid UTF-8")
	}
	if err := validateBrowserJSONL(a.Data); err != nil {
		return BrowserArtifact{}, err
	}

	artifactPath := a.Path
	if artifactPath == "" {
		artifactPath = BrowserArtifactDefaultPath
	}
	if err := validateBrowserArtifactPath(artifactPath); err != nil {
		return BrowserArtifact{}, err
	}
	policy, err := a.Redaction.Normalize()
	if err != nil {
		return BrowserArtifact{}, err
	}
	if err := policy.ValidateCanonical(); err != nil {
		return BrowserArtifact{}, err
	}

	digest := sha256.Sum256(a.Data)
	wantDigest := hex.EncodeToString(digest[:])
	if a.SHA256 != "" {
		if !isLowerSHA256(a.SHA256) {
			return BrowserArtifact{}, invalidBrowserArtifact("sha256 must be lowercase 64-hex")
		}
		if a.SHA256 != wantDigest {
			return BrowserArtifact{}, invalidBrowserArtifact("sha256 does not match data")
		}
	}
	return BrowserArtifact{
		Format:    a.Format,
		Path:      artifactPath,
		Data:      append([]byte(nil), a.Data...),
		SHA256:    wantDigest,
		Redaction: policy,
	}, nil
}

// Validate checks a browser artifact without exposing or modifying its data.
func (a BrowserArtifact) Validate() error {
	_, err := a.Normalize()
	return err
}

// BrowserManifest is the additive v2 manifest object paired with one browser
// artifact in the top-level artifacts list.
type BrowserManifest struct {
	Format    string                 `json:"format"`
	Artifact  ArtifactHash           `json:"artifact"`
	Redaction BrowserRedactionPolicy `json:"redaction"`
}

// BrowserManifestEntry is a descriptive alias for BrowserManifest.
type BrowserManifestEntry = BrowserManifest

func (b BrowserManifest) Validate() error {
	if b.Format != BrowserEventsVersion {
		return invalidBrowserArtifact("browser.format must be %q", BrowserEventsVersion)
	}
	if err := validateBrowserArtifactPath(b.Artifact.Path); err != nil {
		return invalidBrowserArtifact("browser.artifact: %v", err)
	}
	if !isLowerSHA256(b.Artifact.SHA256) {
		return invalidBrowserArtifact("browser.artifact.sha256 must be lowercase 64-hex")
	}
	if err := b.Redaction.ValidateCanonical(); err != nil {
		return err
	}
	return nil
}

func (b BrowserManifest) MarshalJSON() ([]byte, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	type alias BrowserManifest
	return json.Marshal(alias(b))
}

func (b *BrowserManifest) UnmarshalJSON(data []byte) error {
	if b == nil {
		return errors.New("cannot unmarshal browser manifest into nil receiver")
	}
	fields, err := decodeRecordingJSONObject(data)
	if err != nil {
		return invalidBrowserArtifact("browser: %v", err)
	}
	fieldOrder := []string{"format", "artifact", "redaction"}
	allowed := make(map[string]struct{}, len(fieldOrder))
	for _, field := range fieldOrder {
		allowed[field] = struct{}{}
	}
	if err := rejectRecordingUnknownFields(fields, allowed); err != nil {
		return invalidBrowserArtifact("browser: %v", err)
	}
	for _, field := range fieldOrder {
		if _, ok := fields[field]; !ok {
			return invalidBrowserArtifact("browser.%s is required", field)
		}
	}

	format, err := parseRecordingString(fields["format"])
	if err != nil {
		return invalidBrowserArtifact("browser.format: %v", err)
	}
	artifact, err := parseBrowserManifestArtifact(fields["artifact"])
	if err != nil {
		return err
	}
	var redaction BrowserRedactionPolicy
	if err := json.Unmarshal(fields["redaction"], &redaction); err != nil {
		return err
	}
	result := BrowserManifest{Format: format, Artifact: artifact, Redaction: redaction}
	if err := result.Validate(); err != nil {
		return err
	}
	*b = result
	return nil
}

// Validate checks manifest version support, artifact uniqueness, and browser
// pairing. v2 without browser evidence is intentionally valid for provider-
// only recordings.
func (m RecordingManifest) Validate() error {
	switch m.FormatVersion {
	case RecordingManifestV1Version, RecordingManifestV2Version:
		// supported
	default:
		return fmt.Errorf("%w: %d", ErrUnknownRecordingManifestVersion, m.FormatVersion)
	}

	seenPaths := make(map[string]struct{}, len(m.Artifacts))
	for index, artifact := range m.Artifacts {
		if err := validateRecordingArtifactPath(artifact.Path); err != nil {
			return invalidRecordingManifest("artifacts[%d]: %v", index, err)
		}
		if !isLowerSHA256(artifact.SHA256) {
			return invalidRecordingManifest("artifacts[%d].sha256 must be lowercase 64-hex", index)
		}
		if _, exists := seenPaths[artifact.Path]; exists {
			return invalidRecordingManifest("duplicate artifact path %q", artifact.Path)
		}
		seenPaths[artifact.Path] = struct{}{}
	}

	if m.Browser == nil {
		return nil
	}
	if m.FormatVersion != RecordingManifestV2Version {
		return invalidRecordingManifest("browser evidence requires format_version %d", RecordingManifestV2Version)
	}
	if err := m.Browser.Validate(); err != nil {
		return errors.Join(ErrInvalidRecordingManifest, err)
	}
	matching := 0
	for _, artifact := range m.Artifacts {
		if artifact.Path != m.Browser.Artifact.Path {
			continue
		}
		matching++
		if artifact.SHA256 != m.Browser.Artifact.SHA256 {
			return invalidRecordingManifest("browser artifact hash does not match artifacts list")
		}
	}
	if matching != 1 {
		return invalidRecordingManifest("browser artifact must occur exactly once in artifacts list")
	}
	return nil
}

func (m RecordingManifest) MarshalJSON() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	type alias RecordingManifest
	return json.Marshal(alias(m))
}

func (m *RecordingManifest) UnmarshalJSON(data []byte) error {
	if m == nil {
		return errors.New("cannot unmarshal recording manifest into nil receiver")
	}
	fields, err := decodeRecordingJSONObject(data)
	if err != nil {
		return invalidRecordingManifest("%v", err)
	}
	type alias RecordingManifest
	var parsed alias
	if err := json.Unmarshal(data, &parsed); err != nil {
		return errors.Join(ErrInvalidRecordingManifest, err)
	}
	if raw, present := fields["browser"]; present && isRecordingJSONNull(raw) {
		return invalidRecordingManifest("browser must be an object when present")
	}
	result := RecordingManifest(parsed)
	if err := result.Validate(); err != nil {
		return err
	}
	*m = result
	return nil
}

func normalizeBrowserArtifactForRecording(input *BrowserArtifact, destination string, redactor credentialRedactor) (*normalizedBrowserArtifact, error) {
	if input == nil {
		return nil, nil
	}
	normalized, err := input.Normalize()
	if err != nil {
		return nil, recordingError(ErrInvalidRecording, "validate browser artifact", destination, err, redactor)
	}
	if containsCredential(normalized.Data, redactor.values) {
		return nil, recordingError(
			ErrRecordingUnsafeArtifact,
			"verify browser credential redaction",
			filepath.Join(destination, filepath.FromSlash(normalized.Path)),
			errors.New("credential found in browser artifact"),
			redactor,
		)
	}
	return &normalizedBrowserArtifact{
		format:    normalized.Format,
		path:      normalized.Path,
		data:      normalized.Data,
		sha256:    normalized.SHA256,
		redaction: normalized.Redaction,
	}, nil
}

type normalizedBrowserArtifact struct {
	format    string
	path      string
	data      []byte
	sha256    string
	redaction BrowserRedactionPolicy
}

func normalizeRecordingManifestVersion(requested int, hasBrowser bool) (int, error) {
	version := requested
	if version == 0 {
		if hasBrowser {
			version = RecordingManifestV2Version
		} else {
			version = RecordingManifestV1Version
		}
	}
	if version != RecordingManifestV1Version && version != RecordingManifestV2Version {
		return 0, fmt.Errorf("unsupported format_version %d", version)
	}
	if hasBrowser && version != RecordingManifestV2Version {
		return 0, fmt.Errorf("browser evidence requires format_version %d", RecordingManifestV2Version)
	}
	return version, nil
}

func appendBrowserArtifactPath(artifactPaths, expectedPaths *[]string, artifactPath string) error {
	for _, existing := range *artifactPaths {
		if existing == artifactPath {
			return fmt.Errorf("browser artifact path %q duplicates another artifact", artifactPath)
		}
	}
	*artifactPaths = append(*artifactPaths, artifactPath)
	for parent := path.Dir(artifactPath); parent != "."; parent = path.Dir(parent) {
		if !containsRecordingPath(*expectedPaths, parent) {
			*expectedPaths = append(*expectedPaths, parent)
		}
	}
	*expectedPaths = append(*expectedPaths, artifactPath)
	return nil
}

func containsRecordingPath(paths []string, candidate string) bool {
	for _, value := range paths {
		if value == candidate {
			return true
		}
	}
	return false
}

func validateBrowserJSONL(data []byte) error {
	lines := bytes.Split(data, []byte{'\n'})
	for index, line := range lines {
		if index == len(lines)-1 && len(line) == 0 {
			continue
		}
		if len(bytes.TrimSpace(line)) == 0 {
			return invalidBrowserArtifact("line %d is empty", index+1)
		}
		if _, err := decodeRecordingJSONObject(line); err != nil {
			return invalidBrowserArtifact("line %d is not one JSON object: %v", index+1, err)
		}
	}
	return nil
}

func validateBrowserArtifactPath(value string) error {
	if err := validateRecordingArtifactPath(value); err != nil {
		return err
	}
	if !strings.HasSuffix(value, ".jsonl") {
		return errors.New("path must end with .jsonl")
	}
	if value == "audio" || strings.HasPrefix(value, "audio/") {
		return errors.New("audio paths are reserved")
	}
	return nil
}

func validateRecordingArtifactPath(value string) error {
	if value == "" {
		return errors.New("path is required")
	}
	if strings.Contains(value, "\\") || filepath.IsAbs(filepath.FromSlash(value)) || path.IsAbs(value) {
		return errors.New("path must be relative and use slash separators")
	}
	if filepath.VolumeName(filepath.FromSlash(value)) != "" || strings.ContainsRune(strings.Split(value, "/")[0], ':') {
		return errors.New("path must not contain a volume name")
	}
	if path.Clean(value) != value || value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return errors.New("path must not escape the recording directory")
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." {
			return errors.New("path contains an empty or traversal component")
		}
	}
	if value == "manifest.json" || path.Base(value) == "manifest.json" || path.Base(value) == "browser-manifest.json" {
		return errors.New("manifest paths are reserved")
	}
	return nil
}

func validateBrowserPolicyToolNames(field string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		if value == "" || strings.TrimSpace(value) != value {
			return invalidBrowserArtifact("%s[%d] must be a normalized non-empty tool name", field, index)
		}
		if !utf8.ValidString(value) {
			return invalidBrowserArtifact("%s[%d] must be valid UTF-8", field, index)
		}
		for _, character := range value {
			if unicode.IsSpace(character) || unicode.IsControl(character) {
				return invalidBrowserArtifact("%s[%d] must not contain whitespace or control characters", field, index)
			}
		}
		if _, exists := seen[value]; exists {
			return invalidBrowserArtifact("%s[%d] duplicates %q", field, index, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateBrowserPolicyPointers(values []string) error {
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		if err := validateBrowserJSONPointer(value); err != nil {
			return invalidBrowserArtifact("result_json_pointers[%d]: %v", index, err)
		}
		if _, exists := seen[value]; exists {
			return invalidBrowserArtifact("result_json_pointers[%d] duplicates %q", index, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateBrowserJSONPointer(value string) error {
	if value == "" {
		return nil
	}
	if value[0] != '/' {
		return errors.New("must be an RFC 6901 JSON Pointer")
	}
	for index := 1; index < len(value); index++ {
		if value[index] != '~' {
			continue
		}
		if index+1 >= len(value) || (value[index+1] != '0' && value[index+1] != '1') {
			return errors.New("contains an invalid ~ escape")
		}
		index++
	}
	return nil
}

func normalizeBrowserPolicyList(values []string) []string {
	result := append([]string(nil), values...)
	if result == nil {
		result = []string{}
	}
	sort.Strings(result)
	return result
}

func parseBrowserManifestArtifact(raw json.RawMessage) (ArtifactHash, error) {
	fields, err := decodeRecordingJSONObject(raw)
	if err != nil {
		return ArtifactHash{}, invalidBrowserArtifact("browser.artifact: %v", err)
	}
	fieldOrder := []string{"path", "sha256"}
	allowed := make(map[string]struct{}, len(fieldOrder))
	for _, field := range fieldOrder {
		allowed[field] = struct{}{}
	}
	if err := rejectRecordingUnknownFields(fields, allowed); err != nil {
		return ArtifactHash{}, invalidBrowserArtifact("browser.artifact: %v", err)
	}
	for _, field := range fieldOrder {
		if _, ok := fields[field]; !ok {
			return ArtifactHash{}, invalidBrowserArtifact("browser.artifact.%s is required", field)
		}
	}
	artifact := ArtifactHash{}
	if artifact.Path, err = parseRecordingString(fields["path"]); err != nil {
		return ArtifactHash{}, invalidBrowserArtifact("browser.artifact.path: %v", err)
	}
	if artifact.SHA256, err = parseRecordingString(fields["sha256"]); err != nil {
		return ArtifactHash{}, invalidBrowserArtifact("browser.artifact.sha256: %v", err)
	}
	return artifact, nil
}

func isLowerSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func invalidBrowserArtifact(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidBrowserArtifact, fmt.Sprintf(format, args...))
}

func invalidRecordingManifest(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRecordingManifest, fmt.Sprintf(format, args...))
}

func decodeRecordingJSONObject(data []byte) (map[string]json.RawMessage, error) {
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
		fields[key] = append(json.RawMessage(nil), value...)
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

func rejectRecordingUnknownFields(fields map[string]json.RawMessage, allowed map[string]struct{}) error {
	for name := range fields {
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("unknown field %q", name)
		}
	}
	return nil
}

func parseRecordingString(raw json.RawMessage) (string, error) {
	if isRecordingJSONNull(raw) {
		return "", errors.New("must be a string")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", errors.New("must be a string")
	}
	return value, nil
}

func parseRecordingBool(raw json.RawMessage) (bool, error) {
	if isRecordingJSONNull(raw) {
		return false, errors.New("must be a boolean")
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, errors.New("must be a boolean")
	}
	return value, nil
}

func parseRecordingStringArray(raw json.RawMessage) ([]string, error) {
	if isRecordingJSONNull(raw) {
		return nil, errors.New("must be an array")
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, errors.New("must be an array")
	}
	result := make([]string, len(values))
	for index, value := range values {
		parsed, err := parseRecordingString(value)
		if err != nil {
			return nil, fmt.Errorf("item %d %v", index, err)
		}
		result[index] = parsed
	}
	return result, nil
}

func isRecordingJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
