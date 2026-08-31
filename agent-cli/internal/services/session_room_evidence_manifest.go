package services

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

type roomEvidenceManifest struct {
	SchemaVersion     int                                        `json:"schema_version"`
	Finalized         bool                                       `json:"finalized"`
	Timing            roomEvidenceTiming                         `json:"timing"`
	Bounds            roomEvidenceBounds                         `json:"bounds"`
	TerminationReason RoomTerminationReason                      `json:"termination_reason"`
	Reason            RoomTerminationReason                      `json:"reason,omitempty"`
	Participants      map[string]roomEvidenceParticipantManifest `json:"participants"`
	TurnCounts        map[string]int                             `json:"turn_counts"`
	// AudioFormat names the raw PCM16 rate/channel contract shared by every
	// sent.pcm/received.pcm and the WAV/mix artifacts, so a bundle reader
	// never has to guess it.
	AudioFormat roomEvidenceAudioFormat `json:"audio_format"`
	// RoomMix and RoomTimeline are the two room-level (not per-participant)
	// artifacts: the composite "fly on the wall" mix and the ordered,
	// wall-clock-stamped log of the conversation's shape.
	RoomMix      string `json:"room_mix"`
	RoomTimeline string `json:"room_timeline"`
	// RoomLatency names the room-level latency artifact. It is deliberately a
	// dedicated field rather than an Artifacts/ArtifactIntegrity entry: those
	// two maps are the replay reader's artifact inventory, and it rejects any
	// entry there that no declared participant or room artifact role claims
	// ("orphan integrity entry") -- latency has no such role, since replay
	// admission never consumes it.
	RoomLatency string            `json:"room_latency,omitempty"`
	Artifacts   map[string]string `json:"artifacts"`
	// ArtifactIntegrity declares the size and sha256 digest of every artifact
	// this manifest references, keyed by the artifact's own bundle-relative
	// path (not its role name) -- the replay reader's artifact-metadata merge
	// (mergeRoomReplayArtifactMetadata in session_room_replay_manifest.go)
	// looks entries up by path, so this map supplies the integrity metadata
	// for every path named anywhere above (Artifacts, RoomMix, RoomTimeline,
	// and every participant's nested artifacts) regardless of which key
	// declared that path. Without it, replay admission rejects every
	// artifact as incomplete: it requires a declared size and sha256 for
	// each one it validates.
	ArtifactIntegrity map[string]roomEvidenceArtifactIntegrity `json:"artifact_integrity,omitempty"`
	// RecordingStatus follows transcript's shared complete/partial contract;
	// a partial room bundle is still a valid room result with degraded
	// evidence, not a failed conversation.
	RecordingStatus   *transcript.RecordingStatus `json:"recording_status,omitempty"`
	DegradedArtifacts map[string]string           `json:"degraded_artifacts,omitempty"`
	Error             string                      `json:"error,omitempty"`
}

// roomEvidenceAudioFormat is the full PCM contract the replay reader
// (parseRoomReplayPCMFormat in session_room_replay_manifest.go) requires:
// sample_rate/channels/encoding alone are not enough to satisfy it. Every
// field here MUST have a matching required (or aliased) field on the reader
// side — see roomReplayPCMFormatFieldCoverage in
// session_room_evidence_test.go, which asserts that coverage directly so the
// two schemas cannot silently drift apart again.
type roomEvidenceAudioFormat struct {
	SampleRate int    `json:"sample_rate"`
	Channels   int    `json:"channels"`
	Encoding   string `json:"encoding"`
	// SampleWidthBits and ByteOrder complete the raw PCM contract. The room
	// runtime only ever records signed 16-bit little-endian PCM (see
	// DefaultPCM16Format and the "pcm_s16le" Encoding below), so these are
	// fixed constants rather than derived from a variable format, but the
	// replay reader requires them as explicit fields regardless.
	SampleWidthBits int    `json:"sample_width_bits"`
	ByteOrder       string `json:"byte_order"`
}

type roomEvidenceTiming struct {
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
	Elapsed   string `json:"elapsed"`
	// ClockBase is the same instant as StartedAt, named explicitly for
	// downstream lanes that compute per-turn/cross-participant latency by
	// subtracting this anchor from every recorded t_unix_ms field. Earlier
	// bundles had no such anchor at all (or, in the unrelated single-session
	// recording path, a fixed 1970-01-01 placeholder that made latency
	// uncomputable); this is always the room's real start time.
	ClockBase string `json:"clock_base"`
}

type roomEvidenceBounds struct {
	MaxTurns    int    `json:"max_turns,omitempty"`
	MaxDuration string `json:"max_duration,omitempty"`
}

type roomEvidenceParticipantManifest struct {
	ID                     string                       `json:"id"`
	Kind                   room.ParticipantKind         `json:"kind"`
	SystemPrompt           string                       `json:"system_prompt"`
	OpeningPrompt          string                       `json:"opening_prompt,omitempty"`
	Provider               string                       `json:"provider"`
	Model                  string                       `json:"model"`
	APIKeyEnv              string                       `json:"api_key_env"`
	Voice                  string                       `json:"voice,omitempty"`
	Tools                  []string                     `json:"tools"`
	BrowserTools           *room.BrowserToolsConfig     `json:"browser_tools,omitempty"`
	CompletedTurns         int                          `json:"completed_turns"`
	TerminationReason      ParticipantTerminationReason `json:"termination_reason"`
	Reason                 ParticipantTerminationReason `json:"reason,omitempty"`
	TerminationTrigger     string                       `json:"termination_trigger,omitempty"`
	TerminationDisposition string                       `json:"termination_disposition,omitempty"`
	Connected              bool                         `json:"connected"`
	Classification         string                       `json:"classification,omitempty"`
	TerminalReason         messages.TerminalReason      `json:"terminal_reason,omitempty"`
	TerminalProvenance     messages.TerminalProvenance  `json:"terminal_provenance,omitempty"`
	OutputState            messages.TerminalOutputState `json:"output_state,omitempty"`
	InputDevice            string                       `json:"input_device,omitempty"`
	OutputDevice           string                       `json:"output_device,omitempty"`
	Error                  string                       `json:"error,omitempty"`
	Artifacts              roomEvidenceArtifactPaths    `json:"artifacts"`
	RecordingStatus        *transcript.RecordingStatus  `json:"recording_status,omitempty"`
	DegradedArtifacts      map[string]string            `json:"degraded_artifacts,omitempty"`
}

func (e *roomEvidence) writeManifest(result RoomResult, runErr error, endedAt time.Time) error {
	if e == nil {
		return nil
	}
	if endedAt.IsZero() {
		endedAt = e.source.Now().UTC()
	}
	reason := result.TerminationReason
	if reason == "" {
		reason = result.Reason
	}
	if reason == "" {
		reason = RoomTerminationFailed
	}
	recordingStatus, degradedArtifacts, participantStatuses, participantArtifacts := e.recordingHealth()
	manifest := roomEvidenceManifest{
		SchemaVersion: roomEvidenceSchemaVersion,
		Finalized:     runErr == nil,
		Timing: roomEvidenceTiming{
			StartedAt: e.startedAt.UTC().Format(time.RFC3339Nano),
			EndedAt:   endedAt.UTC().Format(time.RFC3339Nano),
			Elapsed:   endedAt.Sub(e.startedAt).String(),
			ClockBase: e.startedAt.UTC().Format(time.RFC3339Nano),
		},
		Bounds:            roomEvidenceBounds{MaxTurns: e.manifest.Room.MaxTurns, MaxDuration: durationString(e.manifest.Room.MaxDuration)},
		TerminationReason: reason,
		Reason:            reason,
		Participants:      make(map[string]roomEvidenceParticipantManifest, len(e.manifest.Participants)),
		TurnCounts:        make(map[string]int, len(e.manifest.Participants)),
		AudioFormat: roomEvidenceAudioFormat{
			SampleRate:      e.audioFormat.SampleRate,
			Channels:        e.audioFormat.Channels,
			Encoding:        roomEvidenceAudioEncoding,
			SampleWidthBits: roomEvidenceAudioSampleWidthBits,
			ByteOrder:       roomEvidenceAudioByteOrder,
		},
		RoomMix:           RoomEvidenceMixPath,
		RoomTimeline:      RoomEvidenceTimelinePath,
		RoomLatency:       RoomLatencyArtifactPath,
		Artifacts:         make(map[string]string, len(e.manifest.Participants)*7+2),
		ArtifactIntegrity: make(map[string]roomEvidenceArtifactIntegrity, len(e.manifest.Participants)*7+2),
		RecordingStatus:   cloneRoomRecordingStatus(recordingStatus),
		DegradedArtifacts: cloneRoomStringMap(degradedArtifacts),
	}
	manifest.Artifacts["room_mix"] = RoomEvidenceMixPath
	manifest.Artifacts["room_timeline"] = RoomEvidenceTimelinePath
	e.hashArtifactInto(manifest.ArtifactIntegrity, RoomEvidenceMixPath)
	e.hashArtifactInto(manifest.ArtifactIntegrity, RoomEvidenceTimelinePath)
	// room-latency.json is diagnostic-only: no replay artifact role ever
	// claims it, so it is deliberately not added to Artifacts/
	// ArtifactIntegrity. Declaring it there without any role claiming
	// ownership would make replay admission's integrity-inventory check
	// reject it as an orphan entry.
	if runErr != nil {
		manifest.Error = e.redactText(runErr.Error())
	}
	for _, participant := range e.manifest.Participants {
		participantEvidence := e.participant(participant.ID)
		participantResult, exists := result.Participants[participant.ID]
		if !exists {
			participantResult = RoomParticipantResult{
				ID:                     participant.ID,
				ParticipantID:          participant.ID,
				TerminationReason:      ParticipantTerminationError,
				Reason:                 ParticipantTerminationError,
				TerminationTrigger:     ParticipantTerminationTriggerSessionFailure,
				TerminationDisposition: ParticipantTerminationDispositionFailed,
				Classification:         providers.ErrorClassUnknown,
				TerminalReason:         messages.TerminalReasonTerminalFailure,
				TerminalProvenance:     messages.TerminalProvenanceSession,
				OutputState:            messages.TerminalOutputNone,
				Error:                  sanitizeRoomError(runErr, e.secrets),
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
			ID:                     participant.ID,
			Kind:                   room.NormalizeParticipantKind(participant.Kind),
			SystemPrompt:           e.redactText(participant.SystemPrompt),
			OpeningPrompt:          e.redactText(participant.OpeningPrompt),
			Provider:               e.redactText(participant.Provider),
			Model:                  e.redactText(participant.Model),
			APIKeyEnv:              e.redactText(participant.APIKeyEnv),
			Voice:                  e.redactText(participant.Voice),
			Tools:                  redactRoomStrings(participant.Tools, e.redactText),
			BrowserTools:           participant.BrowserTools,
			CompletedTurns:         participantResult.TurnsCompleted,
			TerminationReason:      participantReason,
			Reason:                 participantReason,
			TerminationTrigger:     participantResult.TerminationTrigger,
			TerminationDisposition: participantResult.TerminationDisposition,
			Connected:              participantResult.Connected,
			Classification:         participantResult.Classification,
			TerminalReason:         participantResult.TerminalReason,
			TerminalProvenance:     participantResult.TerminalProvenance,
			OutputState:            participantResult.OutputState,
			InputDevice:            e.redactText(participant.InputDevice),
			OutputDevice:           e.redactText(participant.OutputDevice),
			Error:                  e.redactText(participantResult.Error),
			Artifacts:              paths,
			RecordingStatus:        cloneRoomRecordingStatus(participantStatuses[participant.ID]),
			DegradedArtifacts:      cloneRoomStringMap(participantArtifacts[participant.ID]),
		}
		manifest.TurnCounts[participant.ID] = participantResult.TurnsCompleted
		manifest.Artifacts[participant.ID+".wav"] = paths.WAV
		manifest.Artifacts[participant.ID+".diagnostics"] = paths.Diagnostics
		manifest.Artifacts[participant.ID+".deltas"] = paths.Deltas
		manifest.Artifacts[participant.ID+".sent_pcm"] = paths.SentPCM
		manifest.Artifacts[participant.ID+".received_pcm"] = paths.ReceivedPCM
		manifest.Artifacts[participant.ID+".events"] = paths.Events
		e.hashArtifactInto(manifest.ArtifactIntegrity, paths.WAV)
		e.hashArtifactInto(manifest.ArtifactIntegrity, paths.Diagnostics)
		e.hashArtifactInto(manifest.ArtifactIntegrity, paths.Deltas)
		e.hashArtifactInto(manifest.ArtifactIntegrity, paths.SentPCM)
		e.hashArtifactInto(manifest.ArtifactIntegrity, paths.ReceivedPCM)
		e.hashArtifactInto(manifest.ArtifactIntegrity, paths.Events)
		if paths.Capture != "" {
			manifest.Artifacts[participant.ID+".capture"] = paths.Capture
			e.hashArtifactInto(manifest.ArtifactIntegrity, paths.Capture)
		}
	}
	return writeRoomEvidenceManifestFile(filepath.Join(e.destination, RoomEvidenceManifestPath), manifest, e.secrets)
}

func cloneRoomStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func (e *roomEvidence) writeLatencyBundle() error {
	if e == nil || e.latency == nil {
		return nil
	}
	return e.latency.write(filepath.Join(e.destination, RoomLatencyArtifactPath), e.secrets)
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
