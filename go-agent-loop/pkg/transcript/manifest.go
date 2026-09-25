package transcript

import (
	"bytes"
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

// Bundle staging permissions: directories are traversable and artifacts are
// readable by the sharing audience.
const (
	recordingDirectoryMode os.FileMode = 0o755
	recordingFileMode      os.FileMode = 0o644
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

const (
	// RecordingStatusComplete identifies a recording that contains both
	// transcript sides. It is the implicit status for legacy bundles that do
	// not carry recording_status.
	RecordingStatusComplete = "complete"
	// RecordingStatusPartial identifies a bundle that intentionally contains
	// only the evidence available when recording degraded.
	RecordingStatusPartial = "partial"

	// RecordingStateComplete and RecordingStatePartial are concise aliases for
	// callers that prefer state-oriented names.
	RecordingStateComplete = RecordingStatusComplete
	RecordingStatePartial  = RecordingStatusPartial
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

// RecordingArtifact is an additional file emitted into a recording bundle.
// It is an input-side extension point: the public manifest remains the same
// and records the path and digest in Artifacts. Data is copied and generic
// recording credentials are redacted before it is written.
type RecordingArtifact struct {
	Path string
	// SourcePath streams a completed artifact from disk instead of retaining
	// another full in-memory copy. It is mutually exclusive with Data.
	SourcePath string
	Data       []byte
	SHA256     string
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

// RecordingStatus explains whether a bundle is complete or intentionally
// partial. A partial status must include a reason; the writer redacts
// configured credentials from that reason before it reaches the manifest.
type RecordingStatus struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// Validate checks the status vocabulary and the reason requirement for a
// partial recording. Transcript-side requirements are validated alongside the
// recording input and manifest artifact list because this type has no access
// to those bytes.
func (s RecordingStatus) Validate() error {
	switch s.State {
	case RecordingStatusComplete:
		if strings.TrimSpace(s.Reason) != "" {
			return errors.New("complete recordings must not include a reason")
		}
	case RecordingStatusPartial:
		if strings.TrimSpace(s.Reason) == "" {
			return errors.New("partial recordings require a non-empty reason")
		}
	default:
		return fmt.Errorf("unsupported recording status %q", s.State)
	}
	return nil
}

func cloneRecordingStatus(status *RecordingStatus) *RecordingStatus {
	if status == nil {
		return nil
	}
	clone := *status
	return &clone
}

// UnmarshalJSON applies the same strict shape checks used by the rest of the
// recording manifest contract. In particular, a null status is not treated as
// an explicit status object.
func (s *RecordingStatus) UnmarshalJSON(data []byte) error {
	if s == nil {
		return errors.New("cannot unmarshal recording status into nil receiver")
	}
	fields, err := decodeRecordingJSONObject(data)
	if err != nil {
		return fmt.Errorf("recording status: %v", err)
	}
	allowed := map[string]struct{}{"state": {}, "reason": {}}
	if err := rejectRecordingUnknownFields(fields, allowed); err != nil {
		return fmt.Errorf("recording status: %v", err)
	}
	if _, ok := fields["state"]; !ok {
		return errors.New("recording status: state is required")
	}
	state, err := parseRecordingString(fields["state"])
	if err != nil {
		return fmt.Errorf("recording status.state: %v", err)
	}
	reason := ""
	if raw, ok := fields["reason"]; ok {
		reason, err = parseRecordingString(raw)
		if err != nil {
			return fmt.Errorf("recording status.reason: %v", err)
		}
	}
	result := RecordingStatus{State: state, Reason: reason}
	if err := result.Validate(); err != nil {
		return err
	}
	*s = result
	return nil
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
	WallClockStart string               `json:"-"`
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

// RecordingWriteStream is the streaming counterpart to RecordingWriteFile.
// The source is already redacted and must be consumed before the callback
// returns. The returned byte count must equal the redacted bytes consumed. It
// is used for recordings that were spooled while they were being captured, so
// finalization does not materialize the whole session in memory.
type RecordingWriteStream func(path string, source io.Reader, mode os.FileMode) (int64, error)

// RecordingConfig supplies all inputs needed to produce one recording bundle.
type RecordingConfig struct {
	Destination string

	ClientTranscript []byte
	AgentTranscript  []byte
	// Path fields are alternatives for callers that already persisted the
	// transcript bytes. The files are streamed during staging.
	ClientTranscriptPath string
	AgentTranscriptPath  string

	// InputSegmentPaths and OutputSegmentPaths are streaming alternatives to
	// the in-memory segment slices. Each path must contain one non-empty PCM
	// segment and is emitted using the same deterministic audio names.
	InputSegmentPaths  []string
	OutputSegmentPaths []string

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

	Metadata RecordingMetadata
	// RecordingStatus is optional for backwards-compatible complete bundles.
	// Partial status is required when either transcript side is absent.
	RecordingStatus *RecordingStatus
	Terminal        *RecordingTerminalSummary
	Corpus          []CorpusHash
	Credentials     []string

	// ManifestVersion optionally selects the version written by the finalizer.
	// Zero retains the legacy v1 default; browser evidence always requires v2.
	ManifestVersion int
	// BrowserArtifact is redacted semantic webmcp.browser-events.v1 JSONL.
	// It is staged and hashed with the provider artifacts before manifest.json
	// is emitted.
	BrowserArtifact *BrowserArtifact

	// AdditionalArtifacts are run-scoped artifacts that use the existing
	// top-level manifest artifact list. Paths must be relative, unique, and
	// must not collide with the built-in transcript, audio, browser, or
	// manifest paths.
	AdditionalArtifacts []RecordingArtifact

	// WriteFile is optional and handles in-memory artifacts. It is called with
	// the private staging path, so a failed write cannot expose a partial bundle
	// at Destination. Streaming artifacts use WriteStream.
	WriteFile RecordingWriteFile
	// WriteStream is optional. When omitted, the default streams to a private
	// staging file. It is ignored for in-memory artifacts.
	WriteStream RecordingWriteStream
	// BeforeCommit is optional. It runs after the private bundle has been
	// staged and verified, immediately before publication at Destination. A
	// failed guard leaves the destination untouched.
	BeforeCommit func() error
}

// RecordingManifest is the deterministic, versioned JSON representation
// written to manifest.json. Field order is intentionally pinned by the struct
// declaration and all variable-length collections are normalized before
// marshaling.
type RecordingManifest struct {
	FormatVersion   int                       `json:"format_version"`
	InputDevice     DeviceMetadata            `json:"input_device"`
	OutputDevice    DeviceMetadata            `json:"output_device"`
	Transport       string                    `json:"transport"`
	Model           string                    `json:"model"`
	ClockBase       string                    `json:"clock_base"`
	RecordingStatus *RecordingStatus          `json:"recording_status,omitempty"`
	WallClockStart  string                    `json:"wall_clock_start,omitempty"`
	MediaSource     *MediaSourceMetadata      `json:"media_source,omitempty"`
	Configuration   map[string]string         `json:"configuration,omitempty"`
	Corpus          []CorpusHash              `json:"corpus,omitempty"`
	Terminal        *RecordingTerminalSummary `json:"terminal,omitempty"`
	Artifacts       []ArtifactHash            `json:"artifacts"`
	Browser         *BrowserManifest          `json:"browser,omitempty"`
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
			_ = os.RemoveAll(staging) //nolint:errcheck // Best-effort removal of an uncommitted staging directory; the recording error is already the outcome.
		}
	}()

	if err := os.Mkdir(filepath.Join(staging, "audio"), 0o755); err != nil {
		return recordingError(ErrRecordingDestination, "create audio directory", destination, err, redactor)
	}
	stage := &recordingStage{normalized: &normalized, redactor: redactor, staging: staging, destination: destination}
	if err := stage.writeArtifacts(); err != nil {
		return err
	}
	if err := stage.writeManifestAndVerify(); err != nil {
		return err
	}
	if err := commitRecording(staging, destination, existingEmpty); err != nil {
		return recordingErrorForDestination(err, destination, redactor)
	}
	committed = true
	return nil
}

// recordingStage writes one bundle's artifacts into its staging directory.
// Errors name the artifact's final destination path, never the staging path.
type recordingStage struct {
	normalized  *normalizedRecording
	redactor    credentialRedactor
	staging     string
	destination string
}

func (s *recordingStage) destinationPath(relative string) string {
	return filepath.Join(s.destination, filepath.FromSlash(relative))
}

// stagingPath prepares the parent directory of one staged artifact.
func (s *recordingStage) stagingPath(relative string) (string, error) {
	path := filepath.Join(s.staging, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), recordingDirectoryMode); err != nil {
		return "", recordingError(ErrRecordingDestination, "prepare artifact directory", s.destinationPath(relative), err, s.redactor)
	}
	return path, nil
}

func (s *recordingStage) write(relative string, data []byte) error {
	if containsCredential(data, s.redactor.values) {
		return recordingError(
			ErrRecordingUnsafeArtifact,
			"verify credential redaction",
			s.destinationPath(relative),
			errors.New("credential found in artifact"),
			s.redactor,
		)
	}
	path, err := s.stagingPath(relative)
	if err != nil {
		return err
	}
	n, writeErr := s.normalized.writeFile(path, data, recordingFileMode)
	if writeErr == nil && n != len(data) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		return recordingError(ErrRecordingWrite, "write artifact", s.destinationPath(relative), writeErr, s.redactor)
	}
	return nil
}

func (s *recordingStage) writePath(relative, sourcePath string) (returnErr error) {
	if strings.TrimSpace(sourcePath) == "" {
		return recordingError(ErrInvalidRecording, "validate artifact source", s.destinationPath(relative), errors.New("source path is required"), s.redactor)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return recordingError(ErrRecordingWrite, "open artifact source", s.destinationPath(relative), err, s.redactor)
	}
	defer func() {
		if closeErr := source.Close(); closeErr != nil && returnErr == nil {
			returnErr = recordingError(ErrRecordingWrite, "close artifact source", s.destinationPath(relative), closeErr, s.redactor)
		}
	}()
	path, err := s.stagingPath(relative)
	if err != nil {
		return err
	}
	stream := s.normalized.writeStream
	if stream == nil {
		stream = defaultRecordingWriteStream
	}
	reader := &countingReader{source: newRedactingReader(source, s.redactor)}
	written, writeErr := stream(path, reader, recordingFileMode)
	if writeErr == nil && written != reader.bytesRead {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		return recordingError(ErrRecordingWrite, "write artifact", s.destinationPath(relative), writeErr, s.redactor)
	}
	return nil
}

// writeTranscript stages an inline transcript (redacted) and then a
// file-backed one; configuration normalization admits at most one of them.
func (s *recordingStage) writeTranscript(relative string, inline []byte, sourcePath string) error {
	if len(inline) > 0 {
		if err := s.write(relative, s.redactor.apply(inline)); err != nil {
			return err
		}
	}
	if sourcePath != "" {
		return s.writePath(relative, sourcePath)
	}
	return nil
}

func (s *recordingStage) writeSegments(format string, segments [][]byte, sourcePaths []string) error {
	for index, segment := range segments {
		if err := s.write(fmt.Sprintf(format, index), segment); err != nil {
			return err
		}
	}
	for index, segmentPath := range sourcePaths {
		if err := s.writePath(fmt.Sprintf(format, index), segmentPath); err != nil {
			return err
		}
	}
	return nil
}

func (s *recordingStage) writeArtifacts() error {
	normalized := s.normalized
	if err := s.writeTranscript("client.transcript.jsonl", normalized.clientTranscript, normalized.clientTranscriptPath); err != nil {
		return err
	}
	if err := s.writeTranscript("agent.transcript.jsonl", normalized.agentTranscript, normalized.agentTranscriptPath); err != nil {
		return err
	}
	if err := s.writeTranscript("session-log.jsonl", normalized.sessionLog, ""); err != nil {
		return err
	}
	if err := s.writeSegments("audio/in-%03d.pcm", normalized.inputSegments, normalized.inputSegmentPaths); err != nil {
		return err
	}
	if err := s.writeSegments("audio/out-%03d.pcm", normalized.outputSegments, normalized.outputSegmentPaths); err != nil {
		return err
	}
	if normalized.browser != nil {
		if err := s.write(normalized.browser.path, normalized.browser.data); err != nil {
			return err
		}
	}
	if err := writeAdditionalArtifacts(normalized.additional, s.write, s.writePath); err != nil {
		return err
	}
	if err := verifyAdditionalArtifactHashes(s.staging, normalized.additional); err != nil {
		return recordingError(ErrRecordingWrite, "verify additional artifact hashes", s.destination, err, s.redactor)
	}
	return nil
}

// writeManifestAndVerify hashes the staged artifacts, stages the manifest,
// and checks layout, hashes, redaction, and the destination claim.
func (s *recordingStage) writeManifestAndVerify() error {
	artifacts, err := hashArtifacts(s.staging, s.normalized.artifactPaths)
	if err != nil {
		return recordingError(ErrRecordingWrite, "hash artifacts", s.destination, err, s.redactor)
	}
	manifest := buildManifest(*s.normalized, s.redactor, artifacts)
	manifestBytes, err := marshalRecordingManifest(manifest)
	if err != nil {
		return recordingError(ErrRecordingWrite, "encode manifest", filepath.Join(s.destination, "manifest.json"), err, s.redactor)
	}
	if err := s.write("manifest.json", manifestBytes); err != nil {
		return err
	}
	if err := verifyRecordingLayout(s.staging, s.normalized.expectedPaths); err != nil {
		return recordingError(ErrRecordingLayout, "verify layout", s.destination, err, s.redactor)
	}
	if err := verifyArtifactHashes(s.staging, artifacts); err != nil {
		return recordingError(ErrRecordingWrite, "verify artifact hashes", s.destination, err, s.redactor)
	}
	if err := scanForCredentials(s.staging, s.redactor.values); err != nil {
		return recordingError(ErrRecordingUnsafeArtifact, "verify credential redaction", s.destination, err, s.redactor)
	}
	if s.normalized.beforeCommit != nil {
		if err := s.normalized.beforeCommit(); err != nil {
			return recordingError(ErrRecordingDestination, "verify destination claim", s.destination, err, s.redactor)
		}
	}
	return nil
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
		FormatVersion:   recording.manifestVersion,
		InputDevice:     redactDevice(metadata.InputDevice, redactor),
		OutputDevice:    redactDevice(metadata.OutputDevice, redactor),
		Transport:       redactor.string(metadata.Transport),
		Model:           redactor.string(metadata.Model),
		ClockBase:       redactor.string(metadata.ClockBase),
		RecordingStatus: cloneRecordingStatus(recording.recordingStatus),
		WallClockStart:  redactor.string(metadata.WallClockStart),
		MediaSource:     mediaSource,
		Configuration:   configuration,
		Corpus:          corpus,
		Terminal:        cloneRecordingTerminalSummary(recording.terminal),
		Artifacts:       artifacts,
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
		digest, err := digestRecordingFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", relative, err)
		}
		artifacts = append(artifacts, ArtifactHash{Path: relative, SHA256: hex.EncodeToString(digest[:])})
	}
	return artifacts, nil
}

func verifyArtifactHashes(root string, artifacts []ArtifactHash) error {
	for _, artifact := range artifacts {
		digest, err := digestRecordingFile(filepath.Join(root, filepath.FromSlash(artifact.Path)))
		if err != nil {
			return fmt.Errorf("read %s: %w", artifact.Path, err)
		}
		if got := hex.EncodeToString(digest[:]); got != artifact.SHA256 {
			return fmt.Errorf("hash mismatch for %s", artifact.Path)
		}
	}
	return nil
}

func verifyAdditionalArtifactHashes(root string, artifacts []normalizedRecordingArtifact) error {
	for _, artifact := range artifacts {
		digest, err := digestRecordingFile(filepath.Join(root, filepath.FromSlash(artifact.path)))
		if err != nil {
			return fmt.Errorf("read %s: %w", artifact.path, err)
		}
		if got := hex.EncodeToString(digest[:]); got != artifact.sha256 {
			return fmt.Errorf("hash mismatch for additional artifact %s", artifact.path)
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
		contains, err := recordingFileContainsCredential(path, secrets)
		if err != nil {
			return err
		}
		if contains {
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
