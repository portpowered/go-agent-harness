package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
)

const (
	recordingFileMode        os.FileMode = 0o644
	recordingDirectoryMode   os.FileMode = 0o755
	sessionLogToolResultType             = "tool_result"
)

type sessionLogEntry struct {
	TurnIndex  int                `json:"turn_index"`
	Input      sessionLogInput    `json:"input"`
	Response   sessionLogResponse `json:"response"`
	ToolEvents []sessionLogTool   `json:"tool_events,omitempty"`
}

type sessionLogInput struct {
	Text             string   `json:"text"`
	AudioOffsetBytes uint64   `json:"audio_offset_bytes"`
	AudioBytes       uint64   `json:"audio_bytes"`
	Committed        bool     `json:"committed"`
	AudioSegments    []string `json:"audio_segments,omitempty"`
}

type sessionLogResponse struct {
	Text             string   `json:"text"`
	Complete         bool     `json:"complete"`
	AudioOffsetBytes uint64   `json:"audio_offset_bytes"`
	AudioBytes       uint64   `json:"audio_bytes"`
	AudioSegments    []string `json:"audio_segments,omitempty"`
}

type sessionLogTool struct {
	Sequence   uint64
	Type       string
	ToolCallID string
	ToolName   string
	Arguments  string
	Status     string
	Content    string
	Image      *imageEvidence
}

func (e sessionLogTool) MarshalJSON() ([]byte, error) {
	if e.Type == "tool_call" {
		return json.Marshal(struct {
			Sequence   uint64 `json:"sequence"`
			Type       string `json:"type"`
			ToolCallID string `json:"tool_call_id"`
			ToolName   string `json:"tool_name"`
			Arguments  string `json:"arguments"`
		}{e.Sequence, e.Type, e.ToolCallID, e.ToolName, e.Arguments})
	}
	if e.Type == sessionLogToolResultType {
		return json.Marshal(struct {
			Sequence   uint64         `json:"sequence"`
			Type       string         `json:"type"`
			ToolCallID string         `json:"tool_call_id"`
			ToolName   string         `json:"tool_name"`
			Status     string         `json:"status"`
			Content    string         `json:"content"`
			Image      *imageEvidence `json:"image,omitempty"`
		}{e.Sequence, e.Type, e.ToolCallID, e.ToolName, e.Status, e.Content, e.Image})
	}
	return json.Marshal(struct {
		Sequence   uint64         `json:"sequence"`
		Type       string         `json:"type"`
		ToolCallID string         `json:"tool_call_id"`
		ToolName   string         `json:"tool_name"`
		Arguments  string         `json:"arguments,omitempty"`
		Status     string         `json:"status,omitempty"`
		Content    string         `json:"content,omitempty"`
		Image      *imageEvidence `json:"image,omitempty"`
	}{e.Sequence, e.Type, e.ToolCallID, e.ToolName, e.Arguments, e.Status, e.Content, e.Image})
}

func augmentBundle(options recording.SessionOptions, browser *browserRecorder, images []imageEvidence, tools []toolObservation, audio []audioTurn) error {
	browserArtifact, err := browserArtifactFor(browser, options)
	if err != nil {
		return err
	}
	if !bundleNeedsAugment(options, browserArtifact) {
		return nil
	}
	manifestPath := filepath.Join(options.Destination, "manifest.json")
	manifest, err := readManifestForAugment(manifestPath, options.Credentials)
	if err != nil {
		return err
	}
	if err := patchObservedLogs(options, &manifest, images, tools, audio); err != nil {
		return err
	}
	if err := appendAdditionalArtifacts(options, &manifest); err != nil {
		return err
	}
	if err := appendBrowserArtifact(options, &manifest, browserArtifact); err != nil {
		return err
	}
	patchManifestMetadata(&manifest, options)
	return writeManifestForAugment(manifestPath, manifest, options.Credentials)
}

func browserArtifactFor(browser *browserRecorder, options recording.SessionOptions) (*transcript.BrowserArtifact, error) {
	if browser == nil {
		return nil, nil
	}
	artifact, err := browser.artifact()
	if err != nil {
		return nil, wrapRecordingError(transcript.ErrRecordingWrite, "finalize browser events", options.Destination, err, options.Credentials)
	}
	return artifact, nil
}

func bundleNeedsAugment(options recording.SessionOptions, browser *transcript.BrowserArtifact) bool {
	return browser != nil || len(options.AdditionalArtifacts) > 0 || metadataNeedsPatch(options)
}

func readManifestForAugment(path string, credentials []string) (transcript.RecordingManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return transcript.RecordingManifest{}, wrapRecordingError(transcript.ErrRecordingWrite, "read recording manifest", path, err, credentials)
	}
	var manifest transcript.RecordingManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return transcript.RecordingManifest{}, wrapRecordingError(transcript.ErrRecordingLayout, "decode recording manifest", path, err, credentials)
	}
	return manifest, nil
}

func patchObservedLogs(options recording.SessionOptions, manifest *transcript.RecordingManifest, images []imageEvidence, tools []toolObservation, audio []audioTurn) error {
	if len(images) > 0 || len(tools) > 0 {
		if err := patchSessionLog(filepath.Join(options.Destination, "session-log.jsonl"), images, tools, options.Credentials); err != nil {
			return err
		}
		if err := refreshArtifactHash(options.Destination, manifest, "session-log.jsonl", options.Credentials); err != nil {
			return err
		}
	}
	if len(audio) > 0 {
		path := filepath.Join(options.Destination, "session-log.jsonl")
		if err := patchSessionLogAudio(path, audio, options.Credentials); err != nil {
			return err
		}
		if err := refreshArtifactHash(options.Destination, manifest, "session-log.jsonl", options.Credentials); err != nil {
			return err
		}
	}
	return nil
}

func refreshArtifactHash(destination string, manifest *transcript.RecordingManifest, relative string, credentials []string) error {
	path := filepath.Join(destination, filepath.FromSlash(relative))
	data, err := os.ReadFile(path)
	if err != nil {
		return wrapRecordingError(transcript.ErrRecordingWrite, "read session log", path, err, credentials)
	}
	updateArtifactHash(manifest, relative, data)
	return nil
}

func appendAdditionalArtifacts(options recording.SessionOptions, manifest *transcript.RecordingManifest) error {
	for _, artifact := range options.AdditionalArtifacts {
		if err := appendArtifact(options.Destination, manifest, artifact, options.Credentials); err != nil {
			return err
		}
	}
	return nil
}

func appendBrowserArtifact(options recording.SessionOptions, manifest *transcript.RecordingManifest, browser *transcript.BrowserArtifact) error {
	if browser == nil {
		return nil
	}
	normalized, err := browser.Normalize()
	if err != nil {
		return wrapRecordingError(transcript.ErrRecordingWrite, "normalize browser artifact", options.Destination, err, options.Credentials)
	}
	if hasArtifact(manifest, normalized.Path) {
		return wrapRecordingError(transcript.ErrInvalidRecording, "validate browser artifact", normalized.Path, errors.New("artifact path collides with an existing bundle artifact"), options.Credentials)
	}
	if err := writeArtifact(options.Destination, normalized.Path, normalized.Data, options.Credentials); err != nil {
		return err
	}
	manifest.FormatVersion = transcript.RecordingManifestV2Version
	updateArtifactHash(manifest, normalized.Path, normalized.Data)
	manifest.Browser = &transcript.BrowserManifest{
		Format: normalized.Format, Artifact: transcript.ArtifactHash{Path: normalized.Path, SHA256: normalized.SHA256}, Redaction: normalized.Redaction,
	}
	return nil
}

func metadataNeedsPatch(options recording.SessionOptions) bool {
	metadata := options.Metadata
	return options.Transport != "" || options.Model != "" || !options.ClockBase.IsZero() || !options.WallClockStart.IsZero() || metadata.Transport != "" || metadata.Model != "" || metadata.ClockBase != "" || metadata.WallClockStart != "" || metadata.Configuration != nil || metadata.InputDevice != (transcript.DeviceMetadata{}) || metadata.OutputDevice != (transcript.DeviceMetadata{}) || metadata.MediaSource != nil || metadata.MediaSourceURL != ""
}

func patchManifestMetadata(manifest *transcript.RecordingManifest, options recording.SessionOptions) {
	patchManifestOptions(manifest, options)
	patchManifestMetadataFields(manifest, options.Metadata)
}

func patchManifestOptions(manifest *transcript.RecordingManifest, options recording.SessionOptions) {
	if options.Transport != "" {
		manifest.Transport = options.Transport
	}
	if options.Model != "" {
		manifest.Model = options.Model
	}
	if !options.ClockBase.IsZero() {
		manifest.ClockBase = options.ClockBase.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	if !options.WallClockStart.IsZero() {
		manifest.WallClockStart = options.WallClockStart.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	}
}

func patchManifestMetadataFields(manifest *transcript.RecordingManifest, metadata transcript.RecordingMetadata) {
	if metadata.Transport != "" {
		manifest.Transport = metadata.Transport
	}
	if metadata.Model != "" {
		manifest.Model = metadata.Model
	}
	if metadata.ClockBase != "" {
		manifest.ClockBase = metadata.ClockBase
	}
	if metadata.WallClockStart != "" {
		manifest.WallClockStart = metadata.WallClockStart
	}
	patchManifestDevices(manifest, metadata)
	patchManifestMedia(manifest, metadata)
	patchManifestConfiguration(manifest, metadata.Configuration)
}

func patchManifestDevices(manifest *transcript.RecordingManifest, metadata transcript.RecordingMetadata) {
	if metadata.InputDevice != (transcript.DeviceMetadata{}) {
		manifest.InputDevice = metadata.InputDevice
	}
	if metadata.OutputDevice != (transcript.DeviceMetadata{}) {
		manifest.OutputDevice = metadata.OutputDevice
	}
}

func patchManifestMedia(manifest *transcript.RecordingManifest, metadata transcript.RecordingMetadata) {
	if metadata.MediaSource != nil {
		manifest.MediaSource = metadata.MediaSource
	}
	if metadata.MediaSourceURL != "" && manifest.MediaSource == nil {
		manifest.MediaSource = &transcript.MediaSourceMetadata{URL: metadata.MediaSourceURL}
	}
}

func patchManifestConfiguration(manifest *transcript.RecordingManifest, configuration map[string]string) {
	if len(configuration) == 0 {
		return
	}
	if manifest.Configuration == nil {
		manifest.Configuration = make(map[string]string)
	}
	for key, value := range configuration {
		manifest.Configuration[key] = value
	}
}

func writeManifestForAugment(path string, manifest transcript.RecordingManifest, credentials []string) error {
	if err := manifest.Validate(); err != nil {
		return wrapRecordingError(transcript.ErrRecordingLayout, "validate recording manifest", path, err, credentials)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return wrapRecordingError(transcript.ErrRecordingWrite, "encode recording manifest", path, err, credentials)
	}
	encoded = append(encoded, '\n')
	if err := atomicReplace(path, encoded, recordingFileMode); err != nil {
		return wrapRecordingError(transcript.ErrRecordingWrite, "write recording manifest", path, err, credentials)
	}
	return nil
}

func appendArtifact(destination string, manifest *transcript.RecordingManifest, artifact transcript.RecordingArtifact, credentials []string) error {
	if artifact.SourcePath != "" && len(artifact.Data) > 0 {
		return wrapRecordingError(transcript.ErrInvalidRecording, "validate additional artifact", destination, errors.New("artifact has both source and data"), credentials)
	}
	data := append([]byte(nil), artifact.Data...)
	if artifact.SourcePath != "" {
		var err error
		data, err = os.ReadFile(artifact.SourcePath)
		if err != nil {
			return wrapRecordingError(transcript.ErrRecordingWrite, "read additional artifact", destination, err, credentials)
		}
	}
	if artifact.Path == "" {
		return wrapRecordingError(transcript.ErrInvalidRecording, "validate additional artifact", destination, errors.New("artifact path is required"), credentials)
	}
	if hasArtifact(manifest, artifact.Path) {
		return wrapRecordingError(transcript.ErrInvalidRecording, "validate additional artifact", filepath.Join(destination, filepath.FromSlash(artifact.Path)), errors.New("artifact path collides with an existing bundle artifact"), credentials)
	}
	if err := writeArtifact(destination, artifact.Path, data, credentials); err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	manifest.Artifacts = append(manifest.Artifacts, transcript.ArtifactHash{Path: artifact.Path, SHA256: hex.EncodeToString(digest[:])})
	return nil
}

func hasArtifact(manifest *transcript.RecordingManifest, path string) bool {
	if manifest == nil {
		return false
	}
	for _, artifact := range manifest.Artifacts {
		if artifact.Path == path {
			return true
		}
	}
	return false
}

func writeArtifact(destination, relative string, data []byte, credentials []string) error {
	if !safeArtifactPath(relative) {
		return wrapRecordingError(transcript.ErrInvalidRecording, "validate artifact path", filepath.Join(destination, relative), errors.New("artifact path is unsafe"), nil)
	}
	for _, credential := range credentials {
		if credential != "" && bytes.Contains(data, []byte(credential)) {
			return wrapRecordingError(transcript.ErrRecordingUnsafeArtifact, "verify credential redaction", filepath.Join(destination, relative), errors.New("credential found in artifact"), credentials)
		}
	}
	path := filepath.Join(destination, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), recordingDirectoryMode); err != nil {
		return wrapRecordingError(transcript.ErrRecordingDestination, "prepare artifact directory", path, err, credentials)
	}
	if err := atomicReplace(path, data, recordingFileMode); err != nil {
		return wrapRecordingError(transcript.ErrRecordingWrite, "write artifact", path, err, credentials)
	}
	return nil
}

func safeArtifactPath(value string) bool {
	if value == "" || filepath.IsAbs(value) || strings.Contains(value, "\\") {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(value))
	return clean == value && clean != "." && !strings.HasPrefix(clean, "../") && clean != ".." && !strings.Contains(clean, "/../")
}

func updateArtifactHash(manifest *transcript.RecordingManifest, path string, data []byte) {
	digest := sha256.Sum256(data)
	for index := range manifest.Artifacts {
		if manifest.Artifacts[index].Path == path {
			manifest.Artifacts[index].SHA256 = hex.EncodeToString(digest[:])
			return
		}
	}
	manifest.Artifacts = append(manifest.Artifacts, transcript.ArtifactHash{Path: path, SHA256: hex.EncodeToString(digest[:])})
}

func atomicReplace(path string, data []byte, mode os.FileMode) (replaceErr error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".recording-augment-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			if closeErr := temporary.Close(); closeErr != nil {
				replaceErr = errors.Join(replaceErr, closeErr)
			}
		}
		if removeErr := os.Remove(temporaryName); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			replaceErr = errors.Join(replaceErr, removeErr)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	closed = true
	return os.Rename(temporaryName, path)
}
