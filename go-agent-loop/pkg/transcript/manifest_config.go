package transcript

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

type normalizedRecording struct {
	destination          string
	clientTranscript     []byte
	agentTranscript      []byte
	clientTranscriptPath string
	agentTranscriptPath  string
	recordingStatus      *RecordingStatus
	inputSegments        [][]byte
	outputSegments       [][]byte
	inputSegmentPaths    []string
	outputSegmentPaths   []string
	sessionLog           []byte
	metadata             RecordingMetadata
	terminal             *RecordingTerminalSummary
	corpus               []CorpusHash
	artifactPaths        []string
	expectedPaths        []string
	writeFile            RecordingWriteFile
	writeStream          RecordingWriteStream
	beforeCommit         func() error
	manifestVersion      int
	browser              *normalizedBrowserArtifact
	additional           []normalizedRecordingArtifact
}

type normalizedRecordingArtifact struct {
	sourcePath string
	path       string
	data       []byte
	sha256     string
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
	normalized, err := normalizeRecordingInputs(config, destination, redactor)
	if err != nil {
		return normalizedRecording{}, redactor, err
	}
	if err := normalized.planArtifactLayout(config, redactor); err != nil {
		return normalizedRecording{}, redactor, err
	}
	return normalized, redactor, nil
}

// normalizeRecordingInputs validates and copies every configured input in the
// order that determines which validation error a caller observes first.
func normalizeRecordingInputs(config RecordingConfig, destination string, redactor credentialRedactor) (normalizedRecording, error) {
	clientTranscriptPath, err := normalizeTranscriptPath(config.ClientTranscript, config.ClientTranscriptPath, "client", destination, redactor)
	if err != nil {
		return normalizedRecording{}, err
	}
	agentTranscriptPath, err := normalizeTranscriptPath(config.AgentTranscript, config.AgentTranscriptPath, "agent", destination, redactor)
	if err != nil {
		return normalizedRecording{}, err
	}
	clientPresent := len(config.ClientTranscript) > 0 || clientTranscriptPath != ""
	agentPresent := len(config.AgentTranscript) > 0 || agentTranscriptPath != ""
	recordingStatus, err := normalizeRecordingStatus(config.RecordingStatus, clientPresent, agentPresent, destination, redactor)
	if err != nil {
		return normalizedRecording{}, err
	}
	inputSegmentPaths, outputSegmentPaths, err := normalizeRecordingAudio(config, destination, redactor)
	if err != nil {
		return normalizedRecording{}, err
	}
	terminal := cloneRecordingTerminalSummary(config.Terminal)
	if err := terminal.Validate(); err != nil {
		return normalizedRecording{}, recordingError(ErrInvalidRecording, "validate terminal summary", destination, err, redactor)
	}
	writeFile := config.WriteFile
	if writeFile == nil {
		writeFile = defaultRecordingWriteFile
	}
	manifestVersion, err := normalizeRecordingManifestVersion(config.ManifestVersion, config.BrowserArtifact != nil)
	if err != nil {
		return normalizedRecording{}, recordingError(ErrInvalidRecording, "validate manifest version", destination, err, redactor)
	}
	browser, err := normalizeBrowserArtifactForRecording(config.BrowserArtifact, destination, redactor)
	if err != nil {
		return normalizedRecording{}, err
	}
	return normalizedRecording{
		destination:          destination,
		clientTranscript:     append([]byte(nil), config.ClientTranscript...),
		agentTranscript:      append([]byte(nil), config.AgentTranscript...),
		clientTranscriptPath: clientTranscriptPath,
		agentTranscriptPath:  agentTranscriptPath,
		recordingStatus:      recordingStatus,
		inputSegments:        copySegments(config.InputSegments),
		outputSegments:       copySegments(config.OutputSegments),
		inputSegmentPaths:    append([]string(nil), inputSegmentPaths...),
		outputSegmentPaths:   append([]string(nil), outputSegmentPaths...),
		sessionLog:           append([]byte(nil), config.SessionLog...),
		metadata:             config.Metadata,
		terminal:             terminal,
		corpus:               append([]CorpusHash(nil), config.Corpus...),
		writeFile:            writeFile,
		writeStream:          config.WriteStream,
		beforeCommit:         config.BeforeCommit,
		manifestVersion:      manifestVersion,
		browser:              browser,
	}, nil
}

// normalizeTranscriptPath returns the file-backed transcript source to copy.
// Inline bytes take precedence, and an absent optional file is not recorded.
func normalizeTranscriptPath(inline []byte, sourcePath, side, destination string, redactor credentialRedactor) (string, error) {
	if len(inline) > 0 || sourcePath == "" {
		return "", nil
	}
	if err := validateRecordingInputPath(sourcePath, side, destination, redactor); err != nil {
		return "", err
	}
	if recordingInputPathPresent(sourcePath) {
		return sourcePath, nil
	}
	return "", nil
}

// normalizeRecordingAudio validates in-memory segments and file-backed segment
// paths, which are alternatives for each direction.
func normalizeRecordingAudio(config RecordingConfig, destination string, redactor credentialRedactor) ([]string, []string, error) {
	if len(config.InputSegments) > 0 && len(config.InputSegmentPaths) > 0 {
		return nil, nil, recordingError(ErrInvalidRecording, "validate input audio", destination, errors.New("input segments and input segment paths are alternatives"), redactor)
	}
	if len(config.OutputSegments) > 0 && len(config.OutputSegmentPaths) > 0 {
		return nil, nil, recordingError(ErrInvalidRecording, "validate output audio", destination, errors.New("output segments and output segment paths are alternatives"), redactor)
	}
	inputSegmentPaths, err := normalizeRecordingInputPaths(config.InputSegmentPaths, "input", destination, redactor)
	if err != nil {
		return nil, nil, err
	}
	outputSegmentPaths, err := normalizeRecordingInputPaths(config.OutputSegmentPaths, "output", destination, redactor)
	if err != nil {
		return nil, nil, err
	}
	if err := validateSegments(config.InputSegments, "input", destination, redactor); err != nil {
		return nil, nil, err
	}
	if err := validateSegments(config.OutputSegments, "output", destination, redactor); err != nil {
		return nil, nil, err
	}
	return inputSegmentPaths, outputSegmentPaths, nil
}

// planArtifactLayout records the hashed artifact paths and the complete
// expected bundle layout, including validated additional artifacts.
func (n *normalizedRecording) planArtifactLayout(config RecordingConfig, redactor credentialRedactor) error {
	artifactPaths := make([]string, 0, 2)
	expectedPaths := make([]string, 0, 2)
	appendArtifact := func(path string) {
		artifactPaths = append(artifactPaths, path)
		expectedPaths = append(expectedPaths, path)
	}
	if len(n.clientTranscript) > 0 || n.clientTranscriptPath != "" {
		appendArtifact("client.transcript.jsonl")
	}
	if len(n.agentTranscript) > 0 || n.agentTranscriptPath != "" {
		appendArtifact("agent.transcript.jsonl")
	}
	if len(config.SessionLog) > 0 {
		appendArtifact("session-log.jsonl")
	}
	expectedPaths = append(expectedPaths, "audio")
	for index := 0; index < len(n.inputSegments)+len(n.inputSegmentPaths); index++ {
		appendArtifact(fmt.Sprintf("audio/in-%03d.pcm", index))
	}
	for index := 0; index < len(n.outputSegments)+len(n.outputSegmentPaths); index++ {
		appendArtifact(fmt.Sprintf("audio/out-%03d.pcm", index))
	}
	if n.browser != nil {
		if err := appendBrowserArtifactPath(&artifactPaths, &expectedPaths, n.browser.path); err != nil {
			return recordingError(ErrInvalidRecording, "validate browser artifact path", n.destination, err, redactor)
		}
	}
	additional, err := normalizeAdditionalRecordingArtifacts(config.AdditionalArtifacts, artifactPaths, expectedPaths, redactor, n.destination)
	if err != nil {
		return err
	}
	for _, artifact := range additional {
		artifactPaths = append(artifactPaths, artifact.path)
		appendRecordingArtifactParents(&expectedPaths, artifact.path)
		expectedPaths = append(expectedPaths, artifact.path)
	}
	n.artifactPaths = artifactPaths
	n.expectedPaths = append(expectedPaths, "manifest.json")
	n.additional = additional
	return nil
}

func normalizeRecordingStatus(
	input *RecordingStatus,
	clientPresent, agentPresent bool,
	destination string,
	redactor credentialRedactor,
) (*RecordingStatus, error) {
	if input == nil {
		if !clientPresent || !agentPresent {
			return nil, recordingError(
				ErrInvalidRecording,
				"validate recording status",
				destination,
				errors.New("one-sided transcripts require recording status partial"),
				redactor,
			)
		}
		return nil, nil
	}

	status := cloneRecordingStatus(input)
	status.Reason = strings.TrimSpace(redactor.string(status.Reason))
	if err := status.Validate(); err != nil {
		return nil, recordingError(ErrInvalidRecording, "validate recording status", destination, err, redactor)
	}
	if status.State == RecordingStatusComplete && (!clientPresent || !agentPresent) {
		return nil, recordingError(
			ErrInvalidRecording,
			"validate recording status",
			destination,
			errors.New("complete recordings require both transcripts to be non-empty"),
			redactor,
		)
	}
	if status.State == RecordingStatusPartial && !clientPresent && !agentPresent {
		return nil, recordingError(
			ErrInvalidRecording,
			"validate recording status",
			destination,
			errors.New("partial recordings require at least one non-empty transcript"),
			redactor,
		)
	}
	return status, nil
}

func normalizeAdditionalRecordingArtifacts(
	artifacts []RecordingArtifact,
	artifactPaths []string,
	expectedPaths []string,
	redactor credentialRedactor,
	destination string,
) ([]normalizedRecordingArtifact, error) {
	if len(artifacts) == 0 {
		return nil, nil
	}
	seen := reservedRecordingPaths(len(artifacts), artifactPaths, expectedPaths)
	normalized := make([]normalizedRecordingArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if err := claimAdditionalArtifactPath(artifact.Path, seen); err != nil {
			return nil, recordingError(ErrInvalidRecording, "validate additional artifact path", destination, err, redactor)
		}
		item, err := normalizeAdditionalArtifactData(artifact, redactor)
		if err != nil {
			return nil, recordingError(ErrInvalidRecording, "validate additional artifact", destination, fmt.Errorf("%q: %w", artifact.Path, err), redactor)
		}
		normalized = append(normalized, item)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].path < normalized[j].path })
	for index := 1; index < len(normalized); index++ {
		if strings.HasPrefix(normalized[index].path, normalized[index-1].path+"/") {
			return nil, recordingError(ErrInvalidRecording, "validate additional artifact path", destination, fmt.Errorf("%q is a parent of another recording path", normalized[index-1].path), redactor)
		}
	}
	return normalized, nil
}

// reservedRecordingPaths returns the built-in bundle paths that additional
// artifacts may not reuse.
func reservedRecordingPaths(capacity int, artifactPaths, expectedPaths []string) map[string]struct{} {
	seen := make(map[string]struct{}, capacity)
	for _, path := range artifactPaths {
		seen[path] = struct{}{}
	}
	for _, path := range expectedPaths {
		seen[path] = struct{}{}
	}
	// Reserve both built-in transcript names even when one side is absent from
	// this partial recording. Otherwise an additional artifact could fabricate
	// the missing transcript path and make the bundle ambiguous to readers.
	for _, path := range []string{"client.transcript.jsonl", "agent.transcript.jsonl"} {
		seen[path] = struct{}{}
	}
	return seen
}

// claimAdditionalArtifactPath validates one additional artifact path and
// records it so a later artifact cannot duplicate it.
func claimAdditionalArtifactPath(artifactPath string, seen map[string]struct{}) error {
	if err := validateRecordingArtifactPath(artifactPath); err != nil {
		return fmt.Errorf("%q: %w", artifactPath, err)
	}
	if artifactPath == "audio" || strings.HasPrefix(artifactPath, "audio/") {
		return fmt.Errorf("%q: audio paths are reserved", artifactPath)
	}
	if _, exists := seen[artifactPath]; exists {
		return fmt.Errorf("%q duplicates another recording path", artifactPath)
	}
	seen[artifactPath] = struct{}{}
	return nil
}

func appendRecordingArtifactParents(expectedPaths *[]string, artifactPath string) {
	for parent := path.Dir(artifactPath); parent != "."; parent = path.Dir(parent) {
		if !containsRecordingPath(*expectedPaths, parent) {
			*expectedPaths = append(*expectedPaths, parent)
		}
	}
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
