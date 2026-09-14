package service

import (
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

type timelineRecord struct {
	TOffsetMS   float64           `json:"t_offset_ms"`
	TUnixMS     int64             `json:"t_unix_ms"`
	Event       string            `json:"event"`
	Participant string            `json:"participant,omitempty"`
	Fields      map[string]string `json:"fields,omitempty"`
}

type diagnosticRecord struct {
	Event     string            `json:"event"`
	Fields    map[string]string `json:"fields,omitempty"`
	TOffsetMS float64           `json:"t_offset_ms"`
	TUnixMS   int64             `json:"t_unix_ms"`
}

type artifactIntegrity struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type manifestTiming struct {
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
	Elapsed   string `json:"elapsed"`
	ClockBase string `json:"clock_base"`
}

type manifestBounds struct {
	MaxTurns    int    `json:"max_turns,omitempty"`
	MaxDuration string `json:"max_duration,omitempty"`
}

type participantManifest struct {
	ID                     string                             `json:"id"`
	Kind                   rooms.ParticipantKind              `json:"kind"`
	SystemPrompt           string                             `json:"system_prompt"`
	OpeningPrompt          string                             `json:"opening_prompt,omitempty"`
	Provider               string                             `json:"provider"`
	Model                  string                             `json:"model"`
	APIKeyEnv              string                             `json:"api_key_env"`
	Voice                  string                             `json:"voice,omitempty"`
	Tools                  []string                           `json:"tools"`
	BrowserTools           *rooms.BrowserToolsConfig          `json:"browser_tools,omitempty"`
	CompletedTurns         int                                `json:"completed_turns"`
	TerminationReason      rooms.ParticipantTerminationReason `json:"termination_reason"`
	Reason                 rooms.ParticipantTerminationReason `json:"reason,omitempty"`
	TerminationTrigger     string                             `json:"termination_trigger"`
	TerminationDisposition string                             `json:"termination_disposition"`
	Classification         string                             `json:"classification"`
	TerminalReason         string                             `json:"terminal_reason"`
	TerminalProvenance     string                             `json:"terminal_provenance"`
	OutputState            string                             `json:"output_state"`
	Connected              bool                               `json:"connected"`
	InputDevice            string                             `json:"input_device,omitempty"`
	OutputDevice           string                             `json:"output_device,omitempty"`
	Error                  string                             `json:"error,omitempty"`
	Artifacts              roomevidence.ArtifactPaths         `json:"artifacts"`
	RecordingStatus        *transcript.RecordingStatus        `json:"recording_status,omitempty"`
	DegradedArtifacts      map[string]string                  `json:"degraded_artifacts,omitempty"`
}

type roomManifest struct {
	SchemaVersion     int                            `json:"schema_version"`
	Finalized         bool                           `json:"finalized"`
	Timing            manifestTiming                 `json:"timing"`
	Bounds            manifestBounds                 `json:"bounds"`
	TerminationReason rooms.RoomTerminationReason    `json:"termination_reason"`
	Reason            rooms.RoomTerminationReason    `json:"reason,omitempty"`
	Participants      map[string]participantManifest `json:"participants"`
	TurnCounts        map[string]int                 `json:"turn_counts"`
	AudioFormat       roomAudioFormat                `json:"audio_format"`
	RoomMix           string                         `json:"room_mix"`
	RoomTimeline      string                         `json:"room_timeline"`
	RoomLatency       string                         `json:"room_latency,omitempty"`
	Artifacts         map[string]string              `json:"artifacts"`
	ArtifactIntegrity map[string]artifactIntegrity   `json:"artifact_integrity,omitempty"`
	RecordingStatus   *transcript.RecordingStatus    `json:"recording_status,omitempty"`
	DegradedArtifacts map[string]string              `json:"degraded_artifacts,omitempty"`
	Error             string                         `json:"error,omitempty"`
}

type roomAudioFormat struct {
	SampleRate      int    `json:"sample_rate"`
	Channels        int    `json:"channels"`
	Encoding        string `json:"encoding"`
	SampleWidthBits int    `json:"sample_width_bits"`
	ByteOrder       string `json:"byte_order"`
}

func (r *recorder) Finalize(result rooms.RoomResult, runErr error, endedAt time.Time) error {
	if r == nil {
		return nil
	}
	r.finalizeOnce.Do(func() {
		defer close(r.finalizeDone)
		r.mu.Lock()
		r.finalized = true
		r.mu.Unlock()
		r.finalizeBundle(result, runErr, endedAt)
	})
	<-r.finalizeDone
	return r.finalizeErr
}

func (r *recorder) finalizeBundle(result rooms.RoomResult, runErr error, endedAt time.Time) {
	if endedAt.IsZero() {
		endedAt = r.clock.source.Now().UTC()
	}
	if endedAt.Before(r.startedAt) {
		endedAt = r.startedAt
	}
	finalizedAt := r.clock.source.Now().UTC()
	if err := r.writeTimelineAt(finalizedAt, "run_terminated", "", map[string]string{"reason": string(roomReason(result))}); err != nil {
		r.recordError("", roomevidence.TimelinePath, err)
	} else if finalizedAt.After(endedAt) {
		endedAt = finalizedAt
	}
	r.closeParticipantArtifacts()
	if r.timeline != nil {
		if err := r.timeline.close(); err != nil {
			r.recordError("", roomevidence.TimelinePath, err)
		}
	}
	if err := r.mix.Finalize(endedAt.Sub(r.startedAt), filepath.Join(r.destination, roomevidence.MixPath)); err != nil {
		r.recordError("", roomevidence.MixPath, err)
	}
	if r.latency != nil {
		if err := r.latency.Write(filepath.Join(r.destination, roomevidence.LatencyPath)); err != nil {
			r.recordError("", roomevidence.LatencyPath, err)
		}
	}
	manifestErr := r.writeManifest(result, runErr, endedAt.UTC())
	if manifestErr != nil {
		r.recordError("", roomevidence.ManifestPath, manifestErr)
	}
	r.mu.Lock()
	r.finalizeErr = errors.Join(r.recordErr, manifestErr)
	r.mu.Unlock()
}

func (r *recorder) closeParticipantArtifacts() {
	for _, participant := range r.participants {
		if participant == nil {
			continue
		}
		for path, closeFn := range map[string]func() error{
			participant.artifacts.WAV:         participant.wav.close,
			participant.artifacts.Diagnostics: participant.diagnostics.close,
			participant.artifacts.Deltas:      participant.deltas.close,
			participant.artifacts.Events:      participant.events.close,
			participant.artifacts.SentPCM:     participant.sentPCM.close,
			participant.artifacts.ReceivedPCM: participant.receivedPCM.close,
		} {
			if err := closeFn(); err != nil {
				r.recordError(participant.id, path, err)
			}
		}
	}
}

func roomReason(result rooms.RoomResult) rooms.RoomTerminationReason {
	if result.TerminationReason != "" {
		return result.TerminationReason
	}
	if result.Reason != "" {
		return result.Reason
	}
	return rooms.RoomTerminationFailed
}

func (r *recorder) writeManifest(result rooms.RoomResult, runErr error, endedAt time.Time) error {
	health := r.Health()
	reason := roomReason(result)
	manifest := roomManifest{
		SchemaVersion: roomevidence.SchemaVersion,
		Finalized:     runErr == nil,
		Timing: manifestTiming{
			StartedAt: r.startedAt.UTC().Format(time.RFC3339Nano),
			EndedAt:   endedAt.UTC().Format(time.RFC3339Nano),
			Elapsed:   endedAt.Sub(r.startedAt).String(),
			ClockBase: r.startedAt.UTC().Format(time.RFC3339Nano),
		},
		Bounds:            manifestBounds{MaxTurns: r.manifest.Room.MaxTurns, MaxDuration: durationString(r.manifest.Room.MaxDuration)},
		TerminationReason: reason, Reason: reason,
		Participants: make(map[string]participantManifest, len(r.participants)),
		TurnCounts:   make(map[string]int, len(r.participants)),
		AudioFormat:  roomAudioFormat{SampleRate: r.format.SampleRate, Channels: r.format.Channels, Encoding: roomevidence.AudioEncoding, SampleWidthBits: roomevidence.AudioSampleWidthBit, ByteOrder: roomevidence.AudioByteOrder},
		RoomMix:      roomevidence.MixPath, RoomTimeline: roomevidence.TimelinePath, RoomLatency: roomevidence.LatencyPath,
		Artifacts:         make(map[string]string, len(r.participants)*7+2),
		ArtifactIntegrity: make(map[string]artifactIntegrity, len(r.participants)*7+2),
		RecordingStatus:   cloneStatus(health.Status), DegradedArtifacts: cloneMap(health.DegradedArtifacts),
	}
	manifest.Artifacts["room_mix"] = roomevidence.MixPath
	manifest.Artifacts["room_timeline"] = roomevidence.TimelinePath
	r.hashArtifactInto(manifest.ArtifactIntegrity, roomevidence.MixPath)
	r.hashArtifactInto(manifest.ArtifactIntegrity, roomevidence.TimelinePath)
	if runErr != nil {
		manifest.Error = sanitizedError(runErr, r.secrets)
	}
	for _, configured := range r.manifest.Participants {
		participant := r.participants[configured.ID]
		paths := roomevidence.ArtifactPaths{}
		if participant != nil {
			paths = participant.artifacts
		}
		participantResult, exists := result.Participants[configured.ID]
		if !exists {
			participantResult = rooms.RoomParticipantResult{
				ID: configured.ID, ParticipantID: configured.ID,
				TerminationReason: rooms.ParticipantTerminationError, Reason: rooms.ParticipantTerminationError,
				TerminationTrigger: "session_failure", TerminationDisposition: "failed", Classification: "unknown",
				TerminalReason: string(messages.TerminalReasonTerminalFailure), TerminalProvenance: string(messages.TerminalProvenanceSession),
				OutputState: string(messages.TerminalOutputNone), Error: sanitizedError(runErr, r.secrets),
			}
		}
		participantReason := participantResult.TerminationReason
		if participantReason == "" {
			participantReason = participantResult.Reason
		}
		if participantReason == "" {
			participantReason = rooms.ParticipantTerminationError
		}
		manifest.Participants[configured.ID] = participantManifest{
			ID: configured.ID, Kind: normalizeParticipantKind(configured.Kind),
			SystemPrompt: sanitizedText(configured.SystemPrompt, r.secrets), OpeningPrompt: sanitizedText(configured.OpeningPrompt, r.secrets),
			Provider: sanitizedText(configured.Provider, r.secrets), Model: sanitizedText(configured.Model, r.secrets), APIKeyEnv: sanitizedText(configured.APIKeyEnv, r.secrets),
			Voice: sanitizedText(configured.Voice, r.secrets), Tools: redactStrings(configured.Tools, r.secrets), BrowserTools: configured.BrowserTools,
			CompletedTurns: participantResult.TurnsCompleted, TerminationReason: participantReason, Reason: participantReason,
			TerminationTrigger: participantResult.TerminationTrigger, TerminationDisposition: participantResult.TerminationDisposition,
			Classification: participantResult.Classification, TerminalReason: participantResult.TerminalReason, TerminalProvenance: participantResult.TerminalProvenance,
			OutputState: participantResult.OutputState, Connected: participantResult.Connected,
			InputDevice: sanitizedText(configured.InputDevice, r.secrets), OutputDevice: sanitizedText(configured.OutputDevice, r.secrets),
			Error: sanitizedText(participantResult.Error, r.secrets), Artifacts: paths,
			RecordingStatus: cloneStatus(health.ParticipantStatuses[configured.ID]), DegradedArtifacts: cloneMap(health.ParticipantArtifacts[configured.ID]),
		}
		manifest.TurnCounts[configured.ID] = participantResult.TurnsCompleted
		r.addParticipantArtifacts(&manifest, configured.ID, paths)
	}
	return writeManifestFile(filepath.Join(r.destination, roomevidence.ManifestPath), manifest, r.secrets)
}

func (r *recorder) addParticipantArtifacts(manifest *roomManifest, id string, paths roomevidence.ArtifactPaths) {
	entries := map[string]string{
		id + ".wav": paths.WAV, id + ".diagnostics": paths.Diagnostics, id + ".deltas": paths.Deltas,
		id + ".sent_pcm": paths.SentPCM, id + ".received_pcm": paths.ReceivedPCM, id + ".events": paths.Events,
	}
	for key, path := range entries {
		manifest.Artifacts[key] = path
		r.hashArtifactInto(manifest.ArtifactIntegrity, path)
	}
	if paths.Capture != "" {
		manifest.Artifacts[id+".capture"] = paths.Capture
		r.hashArtifactInto(manifest.ArtifactIntegrity, paths.Capture)
	}
}

func cloneMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func redactStrings(values []string, secrets []string) []string {
	if values == nil {
		return nil
	}
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = sanitizedText(value, secrets)
	}
	return result
}

func sanitizedText(value string, secrets []string) string { return redactText(value, secrets) }

func sanitizedError(err error, secrets []string) string {
	if err == nil {
		return "recording degraded"
	}
	value := strings.TrimSpace(redactText(err.Error(), secrets))
	if value == "" {
		return "recording degraded"
	}
	return value
}
