package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	// RoomEvidenceManifestPath is the stable filename written inside a room's
	// output directory. Every other artifact path in that manifest is relative
	// to the same directory.
	RoomEvidenceManifestPath = "run-manifest.json"
	// RoomRunManifestPath is the descriptive alias used by room-run callers.
	RoomRunManifestPath = RoomEvidenceManifestPath

	roomEvidenceSchemaVersion = 1
)

// roomEvidence owns all file-backed observations for one room. Participant
// sinks are created before any session is connected so a later startup or
// transport failure still leaves a complete, inspectable artifact set.
type roomEvidence struct {
	destination  string
	startedAt    time.Time
	manifest     room.Manifest
	secrets      []string
	participants map[string]*roomParticipantEvidence

	mu        sync.Mutex
	recordErr error
	onError   func(string, error)

	finalizeOnce sync.Once
	finalizeErr  error
}

type roomParticipantEvidence struct {
	owner *roomEvidence
	id    string

	artifacts   roomEvidenceArtifactPaths
	audio       *selfPlayWAVRecorder
	diagnostics *selfPlayJSONLWriter
	deltas      *selfPlayJSONLWriter
}

type roomEvidenceArtifactPaths struct {
	WAV         string `json:"wav"`
	Diagnostics string `json:"diagnostics"`
	Deltas      string `json:"deltas"`
}

func newRoomEvidence(destination string, manifest room.Manifest, format room.PCM16Format, secrets []string, startedAt time.Time) (*roomEvidence, error) {
	if strings.TrimSpace(destination) == "" {
		return nil, errors.New("room evidence output directory is empty")
	}
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	if format.SampleRate <= 0 {
		format = room.DefaultPCM16Format()
	}

	evidence := &roomEvidence{
		destination:  filepath.Clean(destination),
		startedAt:    startedAt.UTC(),
		manifest:     manifest,
		secrets:      append([]string(nil), secrets...),
		participants: make(map[string]*roomParticipantEvidence, len(manifest.Participants)),
	}
	stems := roomEvidenceArtifactStems(manifest.Participants)
	for _, participant := range manifest.Participants {
		stem := stems[participant.ID]
		participantEvidence := &roomParticipantEvidence{
			owner: evidence,
			id:    participant.ID,
			artifacts: roomEvidenceArtifactPaths{
				WAV:         "agent-" + stem + ".wav",
				Diagnostics: "agent-" + stem + ".diagnostics.jsonl",
				Deltas:      "agent-" + stem + ".deltas.jsonl",
			},
		}
		evidence.participants[participant.ID] = participantEvidence

		var err error
		participantEvidence.audio, err = newSelfPlayWAVRecorder(filepath.Join(evidence.destination, participantEvidence.artifacts.WAV), format.SampleRate)
		if err != nil {
			evidence.cleanupSetup()
			return nil, fmt.Errorf("create room participant %q WAV evidence: %w", participant.ID, err)
		}
		participantEvidence.diagnostics, err = newSelfPlayJSONLWriter(filepath.Join(evidence.destination, participantEvidence.artifacts.Diagnostics))
		if err != nil {
			evidence.cleanupSetup()
			return nil, fmt.Errorf("create room participant %q diagnostics evidence: %w", participant.ID, err)
		}
		participantEvidence.deltas, err = newSelfPlayJSONLWriter(filepath.Join(evidence.destination, participantEvidence.artifacts.Deltas))
		if err != nil {
			evidence.cleanupSetup()
			return nil, fmt.Errorf("create room participant %q delta evidence: %w", participant.ID, err)
		}
	}
	return evidence, nil
}

func (e *roomEvidence) participant(id string) *roomParticipantEvidence {
	if e == nil {
		return nil
	}
	return e.participants[id]
}

func (e *roomEvidence) setErrorHandler(handler func(string, error)) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.onError = handler
	e.mu.Unlock()
}

func (e *roomEvidence) recordError(participantID string, err error) {
	if e == nil || err == nil {
		return
	}
	wrapped := fmt.Errorf("participant %q evidence: %w", participantID, err)
	e.mu.Lock()
	if e.recordErr == nil {
		e.recordErr = wrapped
	}
	handler := e.onError
	e.mu.Unlock()
	if handler != nil {
		handler(participantID, wrapped)
	}
}

func (e *roomEvidence) err() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.recordErr
}

func (e *roomEvidence) cleanupSetup() {
	if e == nil {
		return
	}
	for _, participant := range e.participants {
		if participant == nil {
			continue
		}
		if participant.audio != nil {
			_ = participant.audio.close()
		}
		if participant.diagnostics != nil {
			_ = participant.diagnostics.close()
		}
		if participant.deltas != nil {
			_ = participant.deltas.close()
		}
		for _, path := range []string{
			filepath.Join(e.destination, participant.artifacts.WAV),
			filepath.Join(e.destination, participant.artifacts.Diagnostics),
			filepath.Join(e.destination, participant.artifacts.Deltas),
		} {
			_ = os.Remove(path)
		}
	}
}

// RecordSessionDiagnostic implements SessionDiagnosticSink. The diagnostic
// record is already structured and credential-free; the writer still uses
// the shared redaction path as defense in depth for provider-supplied fields.
func (p *roomParticipantEvidence) RecordSessionDiagnostic(record SessionDiagnosticRecord) {
	if p == nil || p.owner == nil || p.diagnostics == nil {
		return
	}
	data, err := json.Marshal(selfPlayDiagnosticLine{
		Event:  record.Event,
		Fields: cloneSelfPlayStringMap(record.Fields),
	})
	if err != nil {
		p.owner.recordError(p.id, fmt.Errorf("marshal diagnostic record: %w", err))
		return
	}
	data = p.owner.redactJSON(data)
	if err := p.diagnostics.writeRaw(data); err != nil {
		p.owner.recordError(p.id, err)
	}
}

func (p *roomParticipantEvidence) observeDelta(msg messages.StreamMessage) error {
	if p == nil || p.owner == nil || p.deltas == nil {
		return errors.New("room participant delta sink is not initialized")
	}
	data, err := gwtesting.MarshalStreamMessage(msg)
	if err != nil {
		return fmt.Errorf("marshal stream delta: %w", err)
	}
	return p.deltas.writeRaw(p.owner.redactJSON(data))
}

func (p *roomParticipantEvidence) observeAudio(pcm []byte) error {
	if p == nil || p.audio == nil {
		return errors.New("room participant WAV sink is not initialized")
	}
	return p.audio.write(context.Background(), pcm)
}

func (e *roomEvidence) redactText(value string) string {
	if e == nil || value == "" {
		return value
	}
	for _, secret := range e.secrets {
		value = redactSelfPlayError(value, secret)
	}
	return value
}

// redactJSON walks decoded JSON strings before re-encoding them. Redacting
// the serialized bytes directly can remove a closing quote when an error
// string contains an authorization marker, leaving an invalid artifact.
func (e *roomEvidence) redactJSON(data []byte) []byte {
	if e == nil || len(data) == 0 {
		return data
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		// All callers provide valid JSON. Keep a defensive fallback that still
		// replaces exact credential values if a future caller violates that
		// invariant.
		return []byte(e.redactText(string(data)))
	}
	redacted := redactRoomJSONValue(value, e.redactText)
	result, err := json.Marshal(redacted)
	if err != nil {
		return data
	}
	return result
}

func redactRoomJSONValue(value any, redact func(string) string) any {
	switch typed := value.(type) {
	case string:
		return redact(typed)
	case []any:
		for index := range typed {
			typed[index] = redactRoomJSONValue(typed[index], redact)
		}
	case map[string]any:
		for key, nested := range typed {
			typed[key] = redactRoomJSONValue(nested, redact)
		}
	}
	return value
}

func (e *roomEvidence) finalize(result RoomResult, runErr error, endedAt time.Time) error {
	if e == nil {
		return nil
	}
	e.finalizeOnce.Do(func() {
		var closeErr error
		for _, participant := range e.participants {
			if participant == nil {
				continue
			}
			if participant.audio != nil {
				closeErr = errors.Join(closeErr, participant.audio.close())
			}
			if participant.diagnostics != nil {
				closeErr = errors.Join(closeErr, participant.diagnostics.close())
			}
			if participant.deltas != nil {
				closeErr = errors.Join(closeErr, participant.deltas.close())
			}
		}

		recordErr := e.err()
		effectiveErr := errors.Join(runErr, recordErr, closeErr)
		manifestErr := e.writeManifest(result, effectiveErr, endedAt.UTC())
		e.finalizeErr = errors.Join(recordErr, closeErr, manifestErr)
	})
	return e.finalizeErr
}

type roomEvidenceManifest struct {
	SchemaVersion     int                                        `json:"schema_version"`
	Timing            roomEvidenceTiming                         `json:"timing"`
	Bounds            roomEvidenceBounds                         `json:"bounds"`
	TerminationReason RoomTerminationReason                      `json:"termination_reason"`
	Reason            RoomTerminationReason                      `json:"reason,omitempty"`
	Participants      map[string]roomEvidenceParticipantManifest `json:"participants"`
	TurnCounts        map[string]int                             `json:"turn_counts"`
	Artifacts         map[string]string                          `json:"artifacts"`
	Error             string                                     `json:"error,omitempty"`
}

type roomEvidenceTiming struct {
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
	Elapsed   string `json:"elapsed"`
}

type roomEvidenceBounds struct {
	MaxTurns    int    `json:"max_turns,omitempty"`
	MaxDuration string `json:"max_duration,omitempty"`
}

type roomEvidenceParticipantManifest struct {
	ID                string                       `json:"id"`
	SystemPrompt      string                       `json:"system_prompt"`
	OpeningPrompt     string                       `json:"opening_prompt,omitempty"`
	Provider          string                       `json:"provider"`
	Model             string                       `json:"model"`
	APIKeyEnv         string                       `json:"api_key_env"`
	Voice             string                       `json:"voice,omitempty"`
	Tools             []string                     `json:"tools"`
	CompletedTurns    int                          `json:"completed_turns"`
	TerminationReason ParticipantTerminationReason `json:"termination_reason"`
	Reason            ParticipantTerminationReason `json:"reason,omitempty"`
	Connected         bool                         `json:"connected"`
	Error             string                       `json:"error,omitempty"`
	Artifacts         roomEvidenceArtifactPaths    `json:"artifacts"`
}

func (e *roomEvidence) writeManifest(result RoomResult, runErr error, endedAt time.Time) error {
	if e == nil {
		return nil
	}
	if endedAt.IsZero() {
		endedAt = time.Now().UTC()
	}
	reason := result.TerminationReason
	if reason == "" {
		reason = result.Reason
	}
	if reason == "" {
		reason = RoomTerminationFailed
	}
	manifest := roomEvidenceManifest{
		SchemaVersion:     roomEvidenceSchemaVersion,
		Timing:            roomEvidenceTiming{StartedAt: e.startedAt.UTC().Format(time.RFC3339Nano), EndedAt: endedAt.UTC().Format(time.RFC3339Nano), Elapsed: endedAt.Sub(e.startedAt).String()},
		Bounds:            roomEvidenceBounds{MaxTurns: e.manifest.Room.MaxTurns, MaxDuration: durationString(e.manifest.Room.MaxDuration)},
		TerminationReason: reason,
		Reason:            reason,
		Participants:      make(map[string]roomEvidenceParticipantManifest, len(e.manifest.Participants)),
		TurnCounts:        make(map[string]int, len(e.manifest.Participants)),
		Artifacts:         make(map[string]string, len(e.manifest.Participants)*3),
	}
	if runErr != nil {
		manifest.Error = e.redactText(runErr.Error())
	}
	for _, participant := range e.manifest.Participants {
		participantEvidence := e.participant(participant.ID)
		participantResult, exists := result.Participants[participant.ID]
		if !exists {
			participantResult = RoomParticipantResult{
				ID:                participant.ID,
				ParticipantID:     participant.ID,
				TerminationReason: ParticipantTerminationError,
				Reason:            ParticipantTerminationError,
				Error:             sanitizeRoomError(runErr, e.secrets),
			}
		}
		participantReason := participantResult.TerminationReason
		if participantReason == "" {
			participantReason = participantResult.Reason
		}
		if participantReason == "" {
			participantReason = ParticipantTerminationError
		}
		paths := roomEvidenceArtifactPaths{}
		if participantEvidence != nil {
			paths = participantEvidence.artifacts
		}
		manifest.Participants[participant.ID] = roomEvidenceParticipantManifest{
			ID:                participant.ID,
			SystemPrompt:      e.redactText(participant.SystemPrompt),
			OpeningPrompt:     e.redactText(participant.OpeningPrompt),
			Provider:          e.redactText(participant.Provider),
			Model:             e.redactText(participant.Model),
			APIKeyEnv:         e.redactText(participant.APIKeyEnv),
			Voice:             e.redactText(participant.Voice),
			Tools:             redactRoomStrings(participant.Tools, e.redactText),
			CompletedTurns:    participantResult.TurnsCompleted,
			TerminationReason: participantReason,
			Reason:            participantReason,
			Connected:         participantResult.Connected,
			Error:             e.redactText(participantResult.Error),
			Artifacts:         paths,
		}
		manifest.TurnCounts[participant.ID] = participantResult.TurnsCompleted
		manifest.Artifacts[participant.ID+".wav"] = paths.WAV
		manifest.Artifacts[participant.ID+".diagnostics"] = paths.Diagnostics
		manifest.Artifacts[participant.ID+".deltas"] = paths.Deltas
	}
	return writeRoomEvidenceManifestFile(filepath.Join(e.destination, RoomEvidenceManifestPath), manifest, e.secrets)
}

func writeRoomEvidenceManifestFile(path string, manifest roomEvidenceManifest, secrets []string) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal room run manifest: %w", err)
	}
	data = redactRoomEvidenceJSON(data, secrets)
	data = append(data, '\n')

	destination := filepath.Dir(path)
	temporary, err := os.CreateTemp(destination, ".run-manifest-*.tmp")
	if err != nil {
		return fmt.Errorf("create room run manifest temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := writeSelfPlayAll(temporary, data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write room run manifest temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync room run manifest temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close room run manifest temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace room run manifest: %w", err)
	}
	removeTemporary = false
	return nil
}

func redactRoomEvidenceJSON(data []byte, secrets []string) []byte {
	if len(data) == 0 || len(secrets) == 0 {
		return data
	}
	redact := func(value string) string {
		for _, secret := range secrets {
			value = redactSelfPlayError(value, secret)
		}
		return value
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return []byte(redact(string(data)))
	}
	redacted := redactRoomJSONValue(value, redact)
	result, err := json.Marshal(redacted)
	if err != nil {
		return data
	}
	return result
}

func redactRoomStrings(values []string, redact func(string) string) []string {
	if values == nil {
		return nil
	}
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = redact(value)
	}
	return result
}

func prepareRoomEvidenceOutput(path string) (string, error) {
	destination := filepath.Clean(strings.TrimSpace(path))
	if err := ValidateRoomEvidenceOutput(path); err != nil {
		return "", err
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return "", fmt.Errorf("create room evidence output directory %q: %w", destination, err)
	}
	return destination, nil
}

// ValidateRoomEvidenceOutput checks that a room evidence destination is a
// safe, writable empty directory target without creating the destination.
// Its parent may be created for the write probe, matching the runtime
// preparation performed immediately before live session construction.
func ValidateRoomEvidenceOutput(path string) error {
	rawPath := strings.TrimSpace(path)
	destination := filepath.Clean(rawPath)
	if rawPath == "" || destination == "." {
		return errors.New("room evidence output directory is required")
	}
	return validateRoomEvidenceOutputTarget(destination)
}

func validateRoomEvidenceOutputTarget(destination string) error {
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("prepare room evidence output parent %q: %w", destination, err)
	}
	info, err := os.Lstat(destination)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("room evidence output target %q must be a non-symlink directory", destination)
		}
		entries, readErr := os.ReadDir(destination)
		if readErr != nil {
			return fmt.Errorf("inspect room evidence output directory %q: %w", destination, readErr)
		}
		if len(entries) != 0 {
			return fmt.Errorf("room evidence output directory %q is not safe: it must be empty", destination)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect room evidence output target %q: %w", destination, err)
	}
	probe, err := os.CreateTemp(parent, ".room-evidence-probe-")
	if err != nil {
		return fmt.Errorf("probe room evidence output target %q: %w", destination, err)
	}
	probePath := probe.Name()
	closeErr := probe.Close()
	removeErr := os.Remove(probePath)
	if closeErr != nil {
		return fmt.Errorf("close room evidence output probe %q: %w", destination, closeErr)
	}
	if removeErr != nil {
		return fmt.Errorf("remove room evidence output probe %q: %w", destination, removeErr)
	}
	return nil
}

func roomEvidenceArtifactStems(participants []room.Participant) map[string]string {
	counts := make(map[string]int, len(participants))
	base := make(map[string]string, len(participants))
	for _, participant := range participants {
		stem := normalizeRoomArtifactStem(participant.ID)
		base[participant.ID] = stem
		counts[stem]++
	}
	stems := make(map[string]string, len(participants))
	for _, participant := range participants {
		stem := base[participant.ID]
		if counts[stem] > 1 {
			hash := sha256.Sum256([]byte(participant.ID))
			stem += "-" + hex.EncodeToString(hash[:4])
		}
		stems[participant.ID] = stem
	}
	return stems
}

func normalizeRoomArtifactStem(id string) string {
	id = strings.TrimSpace(id)
	var builder strings.Builder
	lastUnsafe := false
	for _, value := range id {
		safe := value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '-' || value == '_' || value == '.'
		if safe {
			builder.WriteRune(value)
			lastUnsafe = false
			continue
		}
		if !lastUnsafe {
			builder.WriteByte('_')
			lastUnsafe = true
		}
	}
	stem := strings.Trim(builder.String(), ".")
	if stem == "" || stem == ".." {
		return "participant"
	}
	return stem
}

func durationString(value time.Duration) string {
	if value <= 0 {
		return ""
	}
	return value.String()
}

func roomCredentialSecrets(manifest room.Manifest, options room.ValidationOptions) []string {
	lookup := options.LookupCredential
	if lookup == nil {
		lookup = os.LookupEnv
	}
	secrets := make([]string, 0, len(manifest.Participants))
	for _, participant := range manifest.Participants {
		if value, ok := lookup(participant.APIKeyEnv); ok && value != "" {
			secrets = append(secrets, value)
		}
	}
	return secrets
}

// roomMixerConfigForOptions centralizes the format selection used by both the
// live mixer and its WAV evidence, preventing a test cadence override from
// producing a misleading artifact header.
func roomMixerConfigForOptions(opts RoomRunOptions) room.PCM16MixerConfig {
	config := opts.MixerConfig
	if opts.PCMFormat != (room.PCM16Format{}) {
		config.Format = opts.PCMFormat
	} else if opts.FrameSamples > 0 {
		format := room.DefaultPCM16Format()
		format.FrameDuration = time.Duration(opts.FrameSamples) * time.Second / time.Duration(format.SampleRate)
		config.Format = format
	}
	return config
}

func roomFormatForOptions(opts RoomRunOptions) room.PCM16Format {
	format := roomMixerConfigForOptions(opts).Format
	if format == (room.PCM16Format{}) {
		return room.DefaultPCM16Format()
	}
	return format
}
