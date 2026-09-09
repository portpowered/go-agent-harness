package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

const (
	recordingMetadataFieldLimit         = 2048
	recordingTerminalFallbackFieldLimit = 256
	recordingArtifactHashHexLength      = 64
	recordingManifestArtifactCapacity   = 8
)

func boundedRecordingConfig(config transcript.RecordingConfig, credentials []string) transcript.RecordingConfig {
	config.Metadata = boundedRecordingMetadata(config.Metadata, credentials)
	config.SessionLog = redactRecordingBytes(config.SessionLog, credentials)
	config.RecordingStatus = boundedRecordingStatus(config.RecordingStatus, credentials)
	config.Terminal = boundedRecordingTerminal(config.Terminal, credentials, recordingMetadataFieldLimit)
	return config
}

func boundedRecordingMetadata(metadata transcript.RecordingMetadata, credentials []string) transcript.RecordingMetadata {
	metadata.InputDevice.ID = redactAndBoundRecordingString(metadata.InputDevice.ID, credentials)
	metadata.InputDevice.Name = redactAndBoundRecordingString(metadata.InputDevice.Name, credentials)
	metadata.InputDevice.Driver = redactAndBoundRecordingString(metadata.InputDevice.Driver, credentials)
	metadata.OutputDevice.ID = redactAndBoundRecordingString(metadata.OutputDevice.ID, credentials)
	metadata.OutputDevice.Name = redactAndBoundRecordingString(metadata.OutputDevice.Name, credentials)
	metadata.OutputDevice.Driver = redactAndBoundRecordingString(metadata.OutputDevice.Driver, credentials)
	metadata.Transport = redactAndBoundRecordingString(metadata.Transport, credentials)
	metadata.Model = redactAndBoundRecordingString(metadata.Model, credentials)
	metadata.ClockBase = redactAndBoundRecordingString(metadata.ClockBase, credentials)
	metadata.WallClockStart = redactAndBoundRecordingString(metadata.WallClockStart, credentials)
	metadata.MediaSourceURL = redactAndBoundRecordingString(metadata.MediaSourceURL, credentials)
	if metadata.MediaSource != nil {
		mediaSource := *metadata.MediaSource
		mediaSource.URL = redactAndBoundRecordingString(mediaSource.URL, credentials)
		mediaSource.Protocol = redactAndBoundRecordingString(mediaSource.Protocol, credentials)
		mediaSource.Name = redactAndBoundRecordingString(mediaSource.Name, credentials)
		metadata.MediaSource = &mediaSource
	}
	if len(metadata.Configuration) > 0 {
		configuration := make(map[string]string, len(metadata.Configuration))
		for key, value := range metadata.Configuration {
			configuration[redactAndBoundRecordingString(key, credentials)] = redactAndBoundRecordingString(value, credentials)
		}
		metadata.Configuration = configuration
	}
	return metadata
}

func boundedRecordingStatus(status *transcript.RecordingStatus, credentials []string) *transcript.RecordingStatus {
	if status == nil {
		return nil
	}
	copy := *status
	copy.Reason = redactAndBoundRecordingString(copy.Reason, credentials)
	return &copy
}

func boundedRecordingTerminal(terminal *transcript.RecordingTerminalSummary, credentials []string, limit int) *transcript.RecordingTerminalSummary {
	if terminal == nil {
		return nil
	}
	copy := *terminal
	copy.Reason = redactAndBoundRecordingStringLimit(copy.Reason, credentials, limit)
	copy.Classification = redactAndBoundRecordingStringLimit(copy.Classification, credentials, limit)
	copy.TerminalReason = messages.TerminalReason(redactAndBoundRecordingStringLimit(string(copy.TerminalReason), credentials, limit))
	copy.TerminalProvenance = messages.TerminalProvenance(redactAndBoundRecordingStringLimit(string(copy.TerminalProvenance), credentials, limit))
	copy.OutputState = messages.TerminalOutputState(redactAndBoundRecordingStringLimit(string(copy.OutputState), credentials, limit))
	return &copy
}

func redactAndBoundRecordingString(value string, credentials []string) string {
	return redactAndBoundRecordingStringLimit(value, credentials, recordingMetadataFieldLimit)
}

func redactAndBoundRecordingStringLimit(value string, credentials []string, limit int) string {
	if limit < 0 {
		limit = 0
	}
	return boundedBytes(string(redactRecordingBytes([]byte(value), credentials)), limit)
}

func boundedBytes(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func redactRecordingBytes(value []byte, credentials []string) []byte {
	if len(value) == 0 || len(credentials) == 0 {
		return append([]byte(nil), value...)
	}
	redacted := append([]byte(nil), value...)
	for _, credential := range credentials {
		if credential == "" {
			continue
		}
		redacted = bytes.ReplaceAll(redacted, []byte(credential), []byte(transcript.RecordingRedactionMarker))
	}
	return redacted
}

func (r *directoryRecorder) prepareBundleConfig(config transcript.RecordingConfig, result error) (transcript.RecordingConfig, error, bool) {
	config = boundedRecordingConfig(config, r.options.Credentials)
	config.RecordingStatus = recordingStatusForError(result, r.options.Credentials)
	if err := r.reserveBundleMetadata(config); err == nil {
		return config, nil, true
	} else {
		metadataErr := recordingWriteError("admit recording manifest", err)
		fallback := minimalRecordingConfig(config, r.options.Credentials)
		if fallbackErr := r.reserveBundleMetadata(fallback); fallbackErr == nil {
			return fallback, metadataErr, true
		} else {
			return fallback, errors.Join(metadataErr, recordingWriteError("admit minimal recording manifest", fallbackErr)), false
		}
	}
}

func recordingStatusForError(err error, credentials []string) *transcript.RecordingStatus {
	if err == nil {
		return nil
	}
	return boundedRecordingStatus(&transcript.RecordingStatus{
		State:  transcript.RecordingStatusPartial,
		Reason: err.Error(),
	}, credentials)
}

func minimalRecordingConfig(config transcript.RecordingConfig, credentials []string) transcript.RecordingConfig {
	providerCapture := ""
	if config.Metadata.Configuration != nil {
		providerCapture = config.Metadata.Configuration["provider_capture"]
	}
	config.SessionLog = nil
	config.Metadata = transcript.RecordingMetadata{
		Transport: "runtime",
		Configuration: map[string]string{
			"provider_capture": providerCapture,
		},
	}
	config.Corpus = nil
	config.RecordingStatus = &transcript.RecordingStatus{
		State:  transcript.RecordingStatusPartial,
		Reason: "recording metadata budget exceeded",
	}
	config.Terminal = boundedRecordingTerminal(config.Terminal, credentials, recordingTerminalFallbackFieldLimit)
	return boundedRecordingConfig(config, credentials)
}

func (r *directoryRecorder) reserveBundleMetadata(config transcript.RecordingConfig) error {
	manifestBytes, err := recordingManifestBytes(config)
	if err != nil {
		return err
	}
	totalBytes := manifestBytes
	items := int64(1)
	if len(config.SessionLog) > 0 {
		totalBytes += int64(len(config.SessionLog))
		items++
	}
	return r.budget.reserveMetadata(totalBytes, items)
}

func recordingManifestBytes(config transcript.RecordingConfig) (int64, error) {
	version := config.ManifestVersion
	if version == 0 {
		version = 1
	}
	manifest := transcript.RecordingManifest{
		FormatVersion:   version,
		InputDevice:     config.Metadata.InputDevice,
		OutputDevice:    config.Metadata.OutputDevice,
		Transport:       config.Metadata.Transport,
		Model:           config.Metadata.Model,
		ClockBase:       config.Metadata.ClockBase,
		RecordingStatus: config.RecordingStatus,
		WallClockStart:  config.Metadata.WallClockStart,
		MediaSource:     config.Metadata.MediaSource,
		Configuration:   config.Metadata.Configuration,
		Corpus:          config.Corpus,
		Terminal:        config.Terminal,
		Artifacts:       recordingManifestArtifactPlaceholders(config),
	}
	if config.BrowserArtifact != nil {
		path := config.BrowserArtifact.Path
		if path == "" {
			path = transcript.BrowserArtifactDefaultPath
		}
		manifest.Browser = &transcript.BrowserManifest{
			Format: config.BrowserArtifact.Format,
			Artifact: transcript.ArtifactHash{
				Path: path, SHA256: strings.Repeat("0", recordingArtifactHashHexLength),
			},
			Redaction: config.BrowserArtifact.Redaction,
		}
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return 0, err
	}
	return int64(len(encoded) + 1), nil
}

func recordingManifestArtifactPlaceholders(config transcript.RecordingConfig) []transcript.ArtifactHash {
	artifacts := make([]transcript.ArtifactHash, 0, recordingManifestArtifactCapacity)
	appendArtifact := func(path string) {
		if path == "" {
			return
		}
		artifacts = append(artifacts, transcript.ArtifactHash{Path: path, SHA256: strings.Repeat("0", recordingArtifactHashHexLength)})
	}
	if len(config.ClientTranscript) > 0 || config.ClientTranscriptPath != "" {
		appendArtifact("client.transcript.jsonl")
	}
	if len(config.AgentTranscript) > 0 || config.AgentTranscriptPath != "" {
		appendArtifact("agent.transcript.jsonl")
	}
	if len(config.SessionLog) > 0 {
		appendArtifact("session-log.jsonl")
	}
	for index := range config.InputSegments {
		appendArtifact("audio/in-" + threeDigit(index) + ".pcm")
	}
	for index := range config.InputSegmentPaths {
		appendArtifact("audio/in-" + threeDigit(index) + ".pcm")
	}
	for index := range config.OutputSegments {
		appendArtifact("audio/out-" + threeDigit(index) + ".pcm")
	}
	for index := range config.OutputSegmentPaths {
		appendArtifact("audio/out-" + threeDigit(index) + ".pcm")
	}
	if config.BrowserArtifact != nil {
		path := config.BrowserArtifact.Path
		if path == "" {
			path = transcript.BrowserArtifactDefaultPath
		}
		appendArtifact(path)
	}
	for _, artifact := range config.AdditionalArtifacts {
		appendArtifact(artifact.Path)
	}
	return artifacts
}

func threeDigit(value int) string {
	return fmt.Sprintf("%03d", value)
}
