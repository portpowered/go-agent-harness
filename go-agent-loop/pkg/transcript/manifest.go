package transcript

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const (
	// RecordingManifestVersion is the version of the shareable recording
	// manifest schema. It is independent from the transcript frame version.
	RecordingManifestVersion = 1
	// RecordingRedactionMarker is used wherever a configured credential is
	// removed from a textual artifact or metadata field.
	RecordingRedactionMarker = "REDACTED"
)

var (
	// ErrInvalidRecording identifies missing or unsafe recording inputs.
	ErrInvalidRecording = errors.New("transcript: invalid recording")
	// ErrRecordingDestination identifies a destination that cannot be used.
	ErrRecordingDestination = errors.New("transcript: invalid recording destination")
	// ErrRecordingDestinationNotEmpty identifies a destination that contains
	// customer content and therefore cannot be overwritten.
	ErrRecordingDestinationNotEmpty = errors.New("transcript: recording destination not empty")
	// ErrRecordingWrite identifies a failure while emitting an artifact.
	ErrRecordingWrite = errors.New("transcript: recording artifact write failed")
	// ErrRecordingDiskFull is a stable identity for callers that inject a disk
	// full effect. The injected cause is also retained through errors.Is.
	ErrRecordingDiskFull = errors.New("transcript: recording disk full")
	// ErrRecordingUnsafeArtifact identifies a credential that survived into a
	// regular emitted artifact.
	ErrRecordingUnsafeArtifact = errors.New("transcript: unsafe recording artifact")
	// ErrRecordingLayout identifies an output that does not match the public
	// recording directory contract.
	ErrRecordingLayout = errors.New("transcript: invalid recording layout")
	// ErrEmptyRecordingCredential rejects an empty value, which would otherwise
	// match every byte and make redaction unsafe.
	ErrEmptyRecordingCredential = errors.New("transcript: empty recording credential")
)

// RecordingError preserves a stable error identity, the affected operation,
// and destination context. Its Error text is assembled without recording
// caller-supplied credential values.
type RecordingError struct {
	Kind      error
	Operation string
	Path      string
	Cause     error

	secrets [][]byte
}

func (e *RecordingError) Error() string {
	if e == nil {
		return "transcript: recording error"
	}
	parts := []string{"transcript: recording"}
	if e.Operation != "" {
		parts = append(parts, e.Operation)
	}
	if e.Path != "" {
		parts = append(parts, e.redact(e.Path))
	}
	message := strings.Join(parts, " ")
	if e.Cause != nil {
		message += ": " + e.redact(e.Cause.Error())
	}
	return message
}

// Unwrap supports both the stable recording identity and the underlying
// filesystem or injected-write cause.
func (e *RecordingError) Unwrap() error {
	if e == nil {
		return nil
	}
	identities := []error{e.Kind}
	if e.Kind == ErrRecordingDestinationNotEmpty {
		identities = append(identities, ErrRecordingDestination)
	}
	if e.Cause != nil {
		identities = append(identities, e.Cause)
	}
	return errors.Join(identities...)
}

func (e *RecordingError) redact(value string) string {
	return string(redactBytes([]byte(value), e.secrets))
}

// DeviceMetadata describes a resolved recording device. Zero-valued optional
// facts are omitted from the manifest while the containing device object stays
// present, making absent facts explicit without inventing values.
type DeviceMetadata struct {
	ID           string `json:"id,omitempty"`
	Name         string `json:"name,omitempty"`
	Driver       string `json:"driver,omitempty"`
	SampleRateHz int    `json:"sample_rate_hz,omitempty"`
	Channels     int    `json:"channels,omitempty"`
}

// MediaSourceMetadata describes an optional external media source. Passwords
// in URL user information are always replaced with RecordingRedactionMarker.
type MediaSourceMetadata struct {
	URL      string `json:"url,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Name     string `json:"name,omitempty"`
}

// CorpusHash identifies caller-supplied corpus material by a stable name and
// digest. Corpus material itself is not copied into the recording directory.
type CorpusHash struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// ArtifactHash identifies one final emitted artifact. The manifest does not
// include its own hash because that would be self-referential.
type ArtifactHash struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// RecordingTerminalSummary is the optional normalized terminal outcome for a
// recording bundle. The summary describes the lifecycle authority that ended
// the session; it is not a provider wire event and is therefore kept outside
// the transcript artifacts.
type RecordingTerminalSummary struct {
	Reason             string                       `json:"reason"`
	Classification     string                       `json:"classification"`
	TerminalReason     messages.TerminalReason      `json:"terminal_reason"`
	TerminalProvenance messages.TerminalProvenance  `json:"terminal_provenance"`
	OutputState        messages.TerminalOutputState `json:"output_state"`
}

// Validate checks that an explicitly supplied summary is complete. A nil
// summary is valid because terminal metadata is optional for legacy and
// naturally incomplete recording inputs.
func (s *RecordingTerminalSummary) Validate() error {
	if s == nil {
		return nil
	}
	fields := []struct {
		name  string
		value string
	}{
		{name: "reason", value: s.Reason},
		{name: "classification", value: s.Classification},
		{name: "terminal_reason", value: string(s.TerminalReason)},
		{name: "terminal_provenance", value: string(s.TerminalProvenance)},
		{name: "output_state", value: string(s.OutputState)},
	}
	for _, field := range fields {
		if field.value == "" {
			return fmt.Errorf("terminal summary field %q is required", field.name)
		}
	}
	return nil
}

func cloneRecordingTerminalSummary(summary *RecordingTerminalSummary) *RecordingTerminalSummary {
	if summary == nil {
		return nil
	}
	clone := *summary
	return &clone
}

// RecordingMetadata contains the reproducibility facts supplied by the caller.
// MediaSourceURL is a convenience for a source whose only supplied fact is its
// URL.
type RecordingMetadata struct {
	InputDevice    DeviceMetadata       `json:"-"`
	OutputDevice   DeviceMetadata       `json:"-"`
	Transport      string               `json:"-"`
	Model          string               `json:"-"`
	ClockBase      string               `json:"-"`
	MediaSource    *MediaSourceMetadata `json:"-"`
	MediaSourceURL string               `json:"-"`
	Configuration  map[string]string    `json:"-"`
}

// RecordingWriteFile is the narrow filesystem seam used for artifact writes.
// The default implementation uses os.WriteFile. Returning a count smaller
// than len(data), including with a nil error, is treated as io.ErrShortWrite.
// Tests and embedders can use it to model disk exhaustion without touching
// unrelated filesystem behavior.
type RecordingWriteFile func(path string, data []byte, mode os.FileMode) (int, error)

// RecordingConfig supplies all inputs needed to produce one recording bundle.
type RecordingConfig struct {
	Destination string

	ClientTranscript []byte
	AgentTranscript  []byte
	// Path fields are alternatives for callers that already persisted the
	// transcript bytes. The files are read before staging begins.
	ClientTranscriptPath string
	AgentTranscriptPath  string

	// InputSegments is optional for prompt-only sessions. When present, every
	// segment must contain bytes and is emitted as audio/in-NNN.pcm.
	InputSegments [][]byte
	// OutputSegments contains only observed assistant audio. When present,
	// every segment must contain bytes and is emitted as audio/out-NNN.pcm.
	OutputSegments [][]byte

	// SessionLog is an optional machine-readable conversation log (JSONL).
	// When non-empty it is emitted as session-log.jsonl next to the
	// transcripts and included in manifest hashes and layout verification.
	SessionLog []byte

	Metadata    RecordingMetadata
	Terminal    *RecordingTerminalSummary
	Corpus      []CorpusHash
	Credentials []string

	// ManifestVersion optionally selects the version written by the finalizer.
	// Zero retains the legacy v1 default; browser evidence always requires v2.
	ManifestVersion int
	// BrowserArtifact is redacted semantic webmcp.browser-events.v1 JSONL.
	// It is staged and hashed with the provider artifacts before manifest.json
	// is emitted.
	BrowserArtifact *BrowserArtifact

	// WriteFile is optional. It is called with the private staging path, so a
	// failed write cannot expose a partial bundle at Destination.
	WriteFile RecordingWriteFile
}

// RecordingManifest is the deterministic, versioned JSON representation
// written to manifest.json. Field order is intentionally pinned by the struct
// declaration and all variable-length collections are normalized before
// marshaling.
type RecordingManifest struct {
	FormatVersion int                       `json:"format_version"`
	InputDevice   DeviceMetadata            `json:"input_device"`
	OutputDevice  DeviceMetadata            `json:"output_device"`
	Transport     string                    `json:"transport"`
	Model         string                    `json:"model"`
	ClockBase     string                    `json:"clock_base"`
	MediaSource   *MediaSourceMetadata      `json:"media_source,omitempty"`
	Configuration map[string]string         `json:"configuration,omitempty"`
	Corpus        []CorpusHash              `json:"corpus,omitempty"`
	Terminal      *RecordingTerminalSummary `json:"terminal,omitempty"`
	Artifacts     []ArtifactHash            `json:"artifacts"`
	Browser       *BrowserManifest          `json:"browser,omitempty"`
}

// RecordingWriter is a reusable finalizer for one RecordingConfig. It does
// not touch the filesystem until Finalize is called.
type RecordingWriter struct {
	config RecordingConfig
}

// NewRecordingWriter validates configuration inputs without creating files.
func NewRecordingWriter(config RecordingConfig) (*RecordingWriter, error) {
	if _, _, err := normalizeRecordingConfig(config); err != nil {
		return nil, err
	}
	return &RecordingWriter{config: config}, nil
}

// Finalize writes the complete shareable bundle atomically.
func (w *RecordingWriter) Finalize() error {
	if w == nil {
		return &RecordingError{Kind: ErrInvalidRecording, Operation: "finalize", Cause: errors.New("nil writer")}
	}
	return WriteRecordingBundle(w.config)
}

// WriteRecordingBundle emits one deterministic recording bundle.
func WriteRecordingBundle(config RecordingConfig) error {
	normalized, redactor, err := normalizeRecordingConfig(config)
	if err != nil {
		return err
	}

	destination := filepath.Clean(normalized.destination)
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return recordingError(ErrRecordingDestination, "prepare destination", destination, err, redactor)
	}
	existingEmpty, err := inspectDestination(destination)
	if err != nil {
		return recordingErrorForDestination(err, destination, redactor)
	}

	staging, err := os.MkdirTemp(parent, "."+filepath.Base(destination)+".staging-")
	if err != nil {
		return recordingError(ErrRecordingDestination, "create staging directory", destination, err, redactor)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(staging)
		}
	}()

	if err := os.Mkdir(filepath.Join(staging, "audio"), 0o755); err != nil {
		return recordingError(ErrRecordingDestination, "create audio directory", destination, err, redactor)
	}
	writeFile := normalized.writeFile
	write := func(relative string, data []byte) error {
		if containsCredential(data, redactor.values) {
			return recordingError(
				ErrRecordingUnsafeArtifact,
				"verify credential redaction",
				filepath.Join(destination, filepath.FromSlash(relative)),
				errors.New("credential found in artifact"),
				redactor,
			)
		}
		path := filepath.Join(staging, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return recordingError(ErrRecordingDestination, "prepare artifact directory", filepath.Join(destination, filepath.FromSlash(relative)), err, redactor)
		}
		n, writeErr := writeFile(path, data, 0o644)
		if writeErr == nil && n != len(data) {
			writeErr = io.ErrShortWrite
		}
		if writeErr != nil {
			return recordingError(ErrRecordingWrite, "write artifact", filepath.Join(destination, filepath.FromSlash(relative)), writeErr, redactor)
		}
		return nil
	}

	if err := write("client.transcript.jsonl", redactor.apply(normalized.clientTranscript)); err != nil {
		return err
	}
	if err := write("agent.transcript.jsonl", redactor.apply(normalized.agentTranscript)); err != nil {
		return err
	}
	if len(normalized.sessionLog) > 0 {
		if err := write("session-log.jsonl", redactor.apply(normalized.sessionLog)); err != nil {
			return err
		}
	}
	for index, segment := range normalized.inputSegments {
		path := fmt.Sprintf("audio/in-%03d.pcm", index)
		if err := write(path, segment); err != nil {
			return err
		}
	}
	for index, segment := range normalized.outputSegments {
		path := fmt.Sprintf("audio/out-%03d.pcm", index)
		if err := write(path, segment); err != nil {
			return err
		}
	}
	if normalized.browser != nil {
		if err := write(normalized.browser.path, normalized.browser.data); err != nil {
			return err
		}
	}

	artifacts, err := hashArtifacts(staging, normalized.artifactPaths)
	if err != nil {
		return recordingError(ErrRecordingWrite, "hash artifacts", destination, err, redactor)
	}
	manifest := buildManifest(normalized, redactor, artifacts)
	manifestBytes, err := marshalRecordingManifest(manifest)
	if err != nil {
		return recordingError(ErrRecordingWrite, "encode manifest", filepath.Join(destination, "manifest.json"), err, redactor)
	}
	if err := write("manifest.json", manifestBytes); err != nil {
		return err
	}
	if err := verifyRecordingLayout(staging, normalized.expectedPaths); err != nil {
		return recordingError(ErrRecordingLayout, "verify layout", destination, err, redactor)
	}
	if err := verifyArtifactHashes(staging, artifacts); err != nil {
		return recordingError(ErrRecordingWrite, "verify artifact hashes", destination, err, redactor)
	}
	if err := scanForCredentials(staging, redactor.values); err != nil {
		return recordingError(ErrRecordingUnsafeArtifact, "verify credential redaction", destination, err, redactor)
	}
	if err := commitRecording(staging, destination, existingEmpty); err != nil {
		return recordingErrorForDestination(err, destination, redactor)
	}
	committed = true
	return nil
}

type normalizedRecording struct {
	destination      string
	clientTranscript []byte
	agentTranscript  []byte
	inputSegments    [][]byte
	outputSegments   [][]byte
	sessionLog       []byte
	metadata         RecordingMetadata
	terminal         *RecordingTerminalSummary
	corpus           []CorpusHash
	artifactPaths    []string
	expectedPaths    []string
	writeFile        RecordingWriteFile
	manifestVersion  int
	browser          *normalizedBrowserArtifact
}

type credentialRedactor struct {
	values [][]byte
}

func normalizeRecordingConfig(config RecordingConfig) (normalizedRecording, credentialRedactor, error) {
	redactor, err := newCredentialRedactor(config.Credentials)
	if err != nil {
		return normalizedRecording{}, credentialRedactor{}, err
	}
	destination := config.Destination
	if strings.TrimSpace(destination) == "" {
		return normalizedRecording{}, redactor, recordingError(ErrInvalidRecording, "validate destination", "", errors.New("destination is required"), redactor)
	}
	clientTranscript, err := readTranscriptInput(config.ClientTranscript, config.ClientTranscriptPath, "client", destination, redactor)
	if err != nil {
		return normalizedRecording{}, redactor, err
	}
	agentTranscript, err := readTranscriptInput(config.AgentTranscript, config.AgentTranscriptPath, "agent", destination, redactor)
	if err != nil {
		return normalizedRecording{}, redactor, err
	}
	if len(clientTranscript) == 0 || len(agentTranscript) == 0 {
		return normalizedRecording{}, redactor, recordingError(ErrInvalidRecording, "validate transcripts", destination, errors.New("both transcripts must be non-empty"), redactor)
	}
	inputSegments := config.InputSegments
	outputSegments := config.OutputSegments
	if err := validateSegments(inputSegments, "input", destination, redactor); err != nil {
		return normalizedRecording{}, redactor, err
	}
	if err := validateSegments(outputSegments, "output", destination, redactor); err != nil {
		return normalizedRecording{}, redactor, err
	}
	metadata := config.Metadata
	terminal := cloneRecordingTerminalSummary(config.Terminal)
	if err := terminal.Validate(); err != nil {
		return normalizedRecording{}, redactor, recordingError(ErrInvalidRecording, "validate terminal summary", destination, err, redactor)
	}
	corpus := config.Corpus
	writeFile := config.WriteFile
	if writeFile == nil {
		writeFile = defaultRecordingWriteFile
	}
	manifestVersion, err := normalizeRecordingManifestVersion(config.ManifestVersion, config.BrowserArtifact != nil)
	if err != nil {
		return normalizedRecording{}, redactor, recordingError(ErrInvalidRecording, "validate manifest version", destination, err, redactor)
	}
	browser, err := normalizeBrowserArtifactForRecording(config.BrowserArtifact, destination, redactor)
	if err != nil {
		return normalizedRecording{}, redactor, err
	}
	artifactPaths := []string{"client.transcript.jsonl", "agent.transcript.jsonl"}
	expectedPaths := []string{"client.transcript.jsonl", "agent.transcript.jsonl"}
	if len(config.SessionLog) > 0 {
		artifactPaths = append(artifactPaths, "session-log.jsonl")
		expectedPaths = append(expectedPaths, "session-log.jsonl")
	}
	expectedPaths = append(expectedPaths, "audio")
	for index := range inputSegments {
		path := fmt.Sprintf("audio/in-%03d.pcm", index)
		artifactPaths = append(artifactPaths, path)
		expectedPaths = append(expectedPaths, path)
	}
	for index := range outputSegments {
		path := fmt.Sprintf("audio/out-%03d.pcm", index)
		artifactPaths = append(artifactPaths, path)
		expectedPaths = append(expectedPaths, path)
	}
	if browser != nil {
		if err := appendBrowserArtifactPath(&artifactPaths, &expectedPaths, browser.path); err != nil {
			return normalizedRecording{}, redactor, recordingError(ErrInvalidRecording, "validate browser artifact path", destination, err, redactor)
		}
	}
	expectedPaths = append(expectedPaths, "manifest.json")
	return normalizedRecording{
		destination:      destination,
		clientTranscript: append([]byte(nil), clientTranscript...),
		agentTranscript:  append([]byte(nil), agentTranscript...),
		inputSegments:    copySegments(inputSegments),
		outputSegments:   copySegments(outputSegments),
		sessionLog:       append([]byte(nil), config.SessionLog...),
		metadata:         metadata,
		terminal:         terminal,
		corpus:           append([]CorpusHash(nil), corpus...),
		artifactPaths:    artifactPaths,
		expectedPaths:    expectedPaths,
		writeFile:        writeFile,
		manifestVersion:  manifestVersion,
		browser:          browser,
	}, redactor, nil
}

func readTranscriptInput(data []byte, path, side, destination string, redactor credentialRedactor) ([]byte, error) {
	if len(data) != 0 || path == "" {
		return data, nil
	}
	transcript, err := os.ReadFile(path)
	if err != nil {
		return nil, recordingError(ErrInvalidRecording, "read "+side+" transcript", destination, err, redactor)
	}
	return transcript, nil
}

func validateSegments(segments [][]byte, name, destination string, redactor credentialRedactor) error {
	if len(segments) == 0 {
		return nil
	}
	for index, segment := range segments {
		if len(segment) == 0 {
			return recordingError(ErrInvalidRecording, "validate "+name+" audio", destination, fmt.Errorf("segment %d is empty", index), redactor)
		}
	}
	return nil
}

func copySegments(segments [][]byte) [][]byte {
	copyOf := make([][]byte, len(segments))
	for index, segment := range segments {
		copyOf[index] = append([]byte(nil), segment...)
	}
	return copyOf
}

func newCredentialRedactor(credentials []string) (credentialRedactor, error) {
	seen := make(map[string]struct{}, len(credentials))
	values := make([][]byte, 0, len(credentials))
	for _, credential := range credentials {
		if credential == "" {
			return credentialRedactor{}, recordingError(ErrEmptyRecordingCredential, "validate credentials", "", errors.New("credential values must be non-empty"), credentialRedactor{})
		}
		if credential == RecordingRedactionMarker {
			return credentialRedactor{}, recordingError(ErrInvalidRecording, "validate credentials", "", errors.New("credential conflicts with redaction marker"), credentialRedactor{})
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
	return credentialRedactor{values: values}, nil
}

func (r credentialRedactor) apply(value []byte) []byte { return redactBytes(value, r.values) }

func redactBytes(value []byte, secrets [][]byte) []byte {
	redacted := append([]byte(nil), value...)
	for _, secret := range secrets {
		redacted = bytes.ReplaceAll(redacted, secret, []byte(RecordingRedactionMarker))
	}
	return redacted
}

func containsCredential(value []byte, secrets [][]byte) bool {
	for _, secret := range secrets {
		if bytes.Contains(value, secret) {
			return true
		}
	}
	return false
}

func (r credentialRedactor) string(value string) string {
	return string(r.apply([]byte(value)))
}

func buildManifest(recording normalizedRecording, redactor credentialRedactor, artifacts []ArtifactHash) RecordingManifest {
	metadata := recording.metadata
	configuration := mergeConfiguration(metadata.Configuration, redactor)
	mediaSource := redactMediaSource(metadata.MediaSource, metadata.MediaSourceURL, redactor)
	corpus := normalizeCorpus(recording.corpus, redactor)
	manifest := RecordingManifest{
		FormatVersion: recording.manifestVersion,
		InputDevice:   redactDevice(metadata.InputDevice, redactor),
		OutputDevice:  redactDevice(metadata.OutputDevice, redactor),
		Transport:     redactor.string(metadata.Transport),
		Model:         redactor.string(metadata.Model),
		ClockBase:     redactor.string(metadata.ClockBase),
		MediaSource:   mediaSource,
		Configuration: configuration,
		Corpus:        corpus,
		Terminal:      cloneRecordingTerminalSummary(recording.terminal),
		Artifacts:     artifacts,
	}
	if recording.browser != nil {
		manifest.Browser = &BrowserManifest{
			Format: recording.browser.format,
			Artifact: ArtifactHash{
				Path:   recording.browser.path,
				SHA256: recording.browser.sha256,
			},
			Redaction: recording.browser.redaction,
		}
	}
	return manifest
}

func redactDevice(device DeviceMetadata, redactor credentialRedactor) DeviceMetadata {
	device.ID = redactor.string(device.ID)
	device.Name = redactor.string(device.Name)
	device.Driver = redactor.string(device.Driver)
	return device
}

func mergeConfiguration(source map[string]string, redactor credentialRedactor) map[string]string {
	if len(source) == 0 {
		return nil
	}
	configuration := make(map[string]string, len(source))
	for key, value := range source {
		configuration[redactor.string(key)] = redactor.string(value)
	}
	return configuration
}

func redactMediaSource(source *MediaSourceMetadata, sourceURL string, redactor credentialRedactor) *MediaSourceMetadata {
	if source == nil && sourceURL == "" {
		return nil
	}
	redacted := MediaSourceMetadata{}
	if source != nil {
		redacted = *source
	}
	if redacted.URL == "" {
		redacted.URL = sourceURL
	}
	redacted.URL = redactURL(redacted.URL, redactor)
	redacted.Protocol = redactor.string(redacted.Protocol)
	redacted.Name = redactor.string(redacted.Name)
	return &redacted
}

func redactURL(raw string, redactor credentialRedactor) string {
	if raw == "" {
		return ""
	}
	if parsed, err := url.Parse(raw); err == nil && parsed.User != nil {
		if _, hasPassword := parsed.User.Password(); hasPassword {
			parsed.User = url.UserPassword(parsed.User.Username(), RecordingRedactionMarker)
			raw = parsed.String()
		}
	}
	return redactor.string(raw)
}

func normalizeCorpus(corpus []CorpusHash, redactor credentialRedactor) []CorpusHash {
	if len(corpus) == 0 {
		return nil
	}
	copyOf := make([]CorpusHash, len(corpus))
	for index, entry := range corpus {
		copyOf[index] = CorpusHash{
			Path:   redactor.string(entry.Path),
			SHA256: strings.ToLower(redactor.string(entry.SHA256)),
		}
	}
	sort.SliceStable(copyOf, func(i, j int) bool {
		if copyOf[i].Path != copyOf[j].Path {
			return copyOf[i].Path < copyOf[j].Path
		}
		return copyOf[i].SHA256 < copyOf[j].SHA256
	})
	return copyOf
}

func marshalRecordingManifest(manifest RecordingManifest) ([]byte, error) {
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func defaultRecordingWriteFile(path string, data []byte, mode os.FileMode) (int, error) {
	if err := os.WriteFile(path, data, mode); err != nil {
		return 0, err
	}
	return len(data), nil
}

func inspectDestination(destination string) (bool, error) {
	info, err := os.Lstat(destination)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, ErrRecordingDestination
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		return false, err
	}
	if len(entries) != 0 {
		return false, ErrRecordingDestinationNotEmpty
	}
	return true, nil
}

func commitRecording(staging, destination string, existingEmpty bool) error {
	if existingEmpty {
		entries, err := os.ReadDir(destination)
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			return ErrRecordingDestinationNotEmpty
		}
		if err := os.Remove(destination); err != nil {
			return err
		}
	}
	if err := os.Rename(staging, destination); err != nil {
		return err
	}
	return nil
}

func hashArtifacts(root string, paths []string) ([]ArtifactHash, error) {
	artifacts := make([]ArtifactHash, 0, len(paths))
	for _, relative := range paths {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", relative, err)
		}
		digest := sha256.Sum256(data)
		artifacts = append(artifacts, ArtifactHash{Path: relative, SHA256: hex.EncodeToString(digest[:])})
	}
	return artifacts, nil
}

func verifyArtifactHashes(root string, artifacts []ArtifactHash) error {
	for _, artifact := range artifacts {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(artifact.Path)))
		if err != nil {
			return fmt.Errorf("read %s: %w", artifact.Path, err)
		}
		digest := sha256.Sum256(data)
		if got := hex.EncodeToString(digest[:]); got != artifact.SHA256 {
			return fmt.Errorf("hash mismatch for %s", artifact.Path)
		}
	}
	return nil
}

func verifyRecordingLayout(root string, expected []string) error {
	want := make(map[string]struct{}, len(expected))
	for _, path := range expected {
		want[filepath.FromSlash(path)] = struct{}{}
	}
	seen := make(map[string]struct{}, len(expected))
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if _, ok := want[relative]; !ok {
			return fmt.Errorf("unexpected path %s", filepath.ToSlash(relative))
		}
		seen[relative] = struct{}{}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unexpected symlink %s", filepath.ToSlash(relative))
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unexpected non-regular artifact %s", filepath.ToSlash(relative))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(want) {
		missing := make([]string, 0, len(want)-len(seen))
		for path := range want {
			if _, ok := seen[path]; !ok {
				missing = append(missing, filepath.ToSlash(path))
			}
		}
		sort.Strings(missing)
		return fmt.Errorf("missing artifacts: %s", strings.Join(missing, ", "))
	}
	return nil
}

func scanForCredentials(root string, secrets [][]byte) error {
	if len(secrets) == 0 {
		return nil
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if containsCredential(data, secrets) {
			relative, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			return fmt.Errorf("credential found in %s", filepath.ToSlash(relative))
		}
		return nil
	})
}

func recordingError(kind error, operation, path string, cause error, redactor credentialRedactor) error {
	return &RecordingError{Kind: kind, Operation: operation, Path: path, Cause: cause, secrets: redactor.values}
}

func recordingErrorForDestination(err error, destination string, redactor credentialRedactor) error {
	if errors.Is(err, ErrRecordingDestinationNotEmpty) {
		return recordingError(ErrRecordingDestinationNotEmpty, "use destination", destination, err, redactor)
	}
	if errors.Is(err, ErrRecordingDestination) {
		return recordingError(ErrRecordingDestination, "use destination", destination, err, redactor)
	}
	return recordingError(ErrRecordingDestination, "use destination", destination, err, redactor)
}
