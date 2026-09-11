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
	if e.Type == "tool_result" {
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
	var browserArtifact *transcript.BrowserArtifact
	var err error
	if browser != nil {
		browserArtifact, err = browser.artifact()
		if err != nil {
			return wrapRecordingError(transcript.ErrRecordingWrite, "finalize browser events", options.Destination, err, options.Credentials)
		}
	}
	if browserArtifact == nil && len(options.AdditionalArtifacts) == 0 && !metadataNeedsPatch(options) {
		return nil
	}
	manifestPath := filepath.Join(options.Destination, "manifest.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return wrapRecordingError(transcript.ErrRecordingWrite, "read recording manifest", manifestPath, err, options.Credentials)
	}
	var manifest transcript.RecordingManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return wrapRecordingError(transcript.ErrRecordingLayout, "decode recording manifest", manifestPath, err, options.Credentials)
	}

	if len(images) > 0 || len(tools) > 0 {
		logPath := filepath.Join(options.Destination, "session-log.jsonl")
		if err := patchSessionLog(logPath, images, tools, options.Credentials); err != nil {
			return err
		}
		logData, readErr := os.ReadFile(logPath)
		if readErr != nil {
			return wrapRecordingError(transcript.ErrRecordingWrite, "read session log", logPath, readErr, options.Credentials)
		}
		updateArtifactHash(&manifest, "session-log.jsonl", logData)
	}
	if len(audio) > 0 {
		logPath := filepath.Join(options.Destination, "session-log.jsonl")
		if err := patchSessionLogAudio(logPath, audio, options.Credentials); err != nil {
			return err
		}
		logData, readErr := os.ReadFile(logPath)
		if readErr != nil {
			return wrapRecordingError(transcript.ErrRecordingWrite, "read session log", logPath, readErr, options.Credentials)
		}
		updateArtifactHash(&manifest, "session-log.jsonl", logData)
	}

	for _, artifact := range options.AdditionalArtifacts {
		if err := appendArtifact(options.Destination, &manifest, artifact, options.Credentials); err != nil {
			return err
		}
	}
	if browserArtifact != nil {
		normalized, normalizeErr := browserArtifact.Normalize()
		if normalizeErr != nil {
			return wrapRecordingError(transcript.ErrRecordingWrite, "normalize browser artifact", options.Destination, normalizeErr, options.Credentials)
		}
		if hasArtifact(&manifest, normalized.Path) {
			return wrapRecordingError(transcript.ErrInvalidRecording, "validate browser artifact", normalized.Path, errors.New("artifact path collides with an existing bundle artifact"), options.Credentials)
		}
		if err := writeArtifact(options.Destination, normalized.Path, normalized.Data, options.Credentials); err != nil {
			return err
		}
		manifest.FormatVersion = transcript.RecordingManifestV2Version
		updateArtifactHash(&manifest, normalized.Path, normalized.Data)
		manifest.Browser = &transcript.BrowserManifest{
			Format:    normalized.Format,
			Artifact:  transcript.ArtifactHash{Path: normalized.Path, SHA256: normalized.SHA256},
			Redaction: normalized.Redaction,
		}
	}
	patchManifestMetadata(&manifest, options)
	if err := manifest.Validate(); err != nil {
		return wrapRecordingError(transcript.ErrRecordingLayout, "validate recording manifest", manifestPath, err, options.Credentials)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return wrapRecordingError(transcript.ErrRecordingWrite, "encode recording manifest", manifestPath, err, options.Credentials)
	}
	encoded = append(encoded, '\n')
	if err := atomicReplace(manifestPath, encoded, 0o644); err != nil {
		return wrapRecordingError(transcript.ErrRecordingWrite, "write recording manifest", manifestPath, err, options.Credentials)
	}
	return nil
}

func metadataNeedsPatch(options recording.SessionOptions) bool {
	metadata := options.Metadata
	return options.Transport != "" || options.Model != "" || !options.ClockBase.IsZero() || !options.WallClockStart.IsZero() || metadata.Transport != "" || metadata.Model != "" || metadata.ClockBase != "" || metadata.WallClockStart != "" || metadata.Configuration != nil || metadata.InputDevice != (transcript.DeviceMetadata{}) || metadata.OutputDevice != (transcript.DeviceMetadata{}) || metadata.MediaSource != nil || metadata.MediaSourceURL != ""
}

func patchManifestMetadata(manifest *transcript.RecordingManifest, options recording.SessionOptions) {
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
	metadata := options.Metadata
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
	if metadata.InputDevice != (transcript.DeviceMetadata{}) {
		manifest.InputDevice = metadata.InputDevice
	}
	if metadata.OutputDevice != (transcript.DeviceMetadata{}) {
		manifest.OutputDevice = metadata.OutputDevice
	}
	if metadata.MediaSource != nil {
		manifest.MediaSource = metadata.MediaSource
	}
	if metadata.MediaSourceURL != "" && manifest.MediaSource == nil {
		manifest.MediaSource = &transcript.MediaSourceMetadata{URL: metadata.MediaSourceURL}
	}
	if len(metadata.Configuration) > 0 {
		if manifest.Configuration == nil {
			manifest.Configuration = make(map[string]string)
		}
		for key, value := range metadata.Configuration {
			manifest.Configuration[key] = value
		}
	}
}

func patchSessionLog(path string, images []imageEvidence, tools []toolObservation, credentials []string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return wrapRecordingError(transcript.ErrRecordingWrite, "read session log", path, err, credentials)
	}
	byCall := make(map[string]imageEvidence, len(images))
	for _, image := range images {
		byCall[image.ToolCallID()] = image
	}
	var fallback *imageEvidence
	if len(images) == 1 {
		fallback = &images[0]
	}
	lines := bytes.Split(data, []byte{'\n'})
	output := make([]byte, 0, len(data)+len(images)*256)
	for _, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var entry sessionLogEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return wrapRecordingError(transcript.ErrRecordingWrite, "decode session log", path, err, credentials)
		}
		callCursor := 0
		resultCursor := 0
		for index := range entry.ToolEvents {
			if callCursor < len(tools) && entry.ToolEvents[index].Type == "tool_call" {
				observation := tools[callCursor]
				entry.ToolEvents[index].ToolCallID = firstNonEmpty(entry.ToolEvents[index].ToolCallID, observation.call.ID)
				entry.ToolEvents[index].ToolName = firstNonEmpty(entry.ToolEvents[index].ToolName, observation.call.Name)
				entry.ToolEvents[index].Arguments = firstNonEmpty(entry.ToolEvents[index].Arguments, observation.call.Arguments)
				callCursor++
			}
			if resultCursor < len(tools) && entry.ToolEvents[index].Type == "tool_result" {
				observation := tools[resultCursor]
				entry.ToolEvents[index].ToolCallID = firstNonEmpty(entry.ToolEvents[index].ToolCallID, observation.call.ID)
				entry.ToolEvents[index].ToolName = firstNonEmpty(entry.ToolEvents[index].ToolName, observation.call.Name)
				if observation.failed {
					entry.ToolEvents[index].Status = "failed"
				} else if entry.ToolEvents[index].Status == "" {
					entry.ToolEvents[index].Status = "completed"
				}
				if entry.ToolEvents[index].Content == "" && observation.result.Content != "" {
					entry.ToolEvents[index].Content = observation.result.Content
				}
				resultCursor++
			}
			image, ok := byCall[entry.ToolEvents[index].ToolCallID]
			if !ok && fallback != nil && entry.ToolEvents[index].Type == "tool_result" {
				image, ok = *fallback, true
			}
			if ok && entry.ToolEvents[index].Type == "tool_result" {
				copy := image
				entry.ToolEvents[index].Image = &copy
			}
		}
		encoded, err := json.Marshal(entry)
		if err != nil {
			return wrapRecordingError(transcript.ErrRecordingWrite, "encode session log", path, err, credentials)
		}
		output = append(output, encoded...)
		output = append(output, '\n')
	}
	return atomicReplace(path, output, 0o644)
}

func firstNonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func (e imageEvidence) ToolCallID() string { return e.callID }

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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return wrapRecordingError(transcript.ErrRecordingDestination, "prepare artifact directory", path, err, credentials)
	}
	if err := atomicReplace(path, data, 0o644); err != nil {
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

func atomicReplace(path string, data []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".recording-augment-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}
