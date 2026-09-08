package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	roommanifest "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/manifest"
)

const (
	participantArtifactRoleCapacity = 7
	roomManifestArtifactCapacity    = 3
	recordingDegradedReason         = "recording degraded"
)

type manifestFile struct {
	Name   string `json:"name,omitempty"`
	Role   string `json:"role,omitempty"`
	Owner  string `json:"owner,omitempty"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Empty  bool   `json:"empty,omitempty"`
}

type manifestTiming struct {
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
	Elapsed   string `json:"elapsed"`
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
	Voice                  string                             `json:"voice,omitempty"`
	CompletedTurns         int                                `json:"completed_turns"`
	TerminationReason      rooms.ParticipantTerminationReason `json:"termination_reason"`
	Reason                 rooms.ParticipantTerminationReason `json:"reason,omitempty"`
	TerminationTrigger     string                             `json:"termination_trigger,omitempty"`
	TerminationDisposition string                             `json:"termination_disposition,omitempty"`
	Classification         string                             `json:"classification,omitempty"`
	TerminalReason         string                             `json:"terminal_reason,omitempty"`
	TerminalProvenance     string                             `json:"terminal_provenance,omitempty"`
	OutputState            string                             `json:"output_state,omitempty"`
	Connected              bool                               `json:"connected"`
	InputDevice            string                             `json:"input_device,omitempty"`
	OutputDevice           string                             `json:"output_device,omitempty"`
	Error                  string                             `json:"error,omitempty"`
	Artifacts              map[string]manifestFile            `json:"artifacts"`
	RecordingStatus        *transcript.RecordingStatus        `json:"recording_status,omitempty"`
	DegradedArtifacts      map[string]string                  `json:"degraded_artifacts,omitempty"`
}

type roomManifest struct {
	SchemaVersion     int                            `json:"schema_version"`
	Finalized         bool                           `json:"finalized"`
	ClockBase         string                         `json:"clock_base"`
	Timing            manifestTiming                 `json:"timing"`
	Bounds            manifestBounds                 `json:"bounds"`
	TerminationReason rooms.RoomTerminationReason    `json:"termination_reason"`
	Reason            rooms.RoomTerminationReason    `json:"reason"`
	Participants      map[string]participantManifest `json:"participants"`
	TurnCounts        map[string]int                 `json:"turn_counts"`
	PCMFormat         rooms.RoomReplayPCMFormat      `json:"pcm_format"`
	RoomMix           string                         `json:"room_mix"`
	RoomTimeline      string                         `json:"room_timeline"`
	RoomLatency       string                         `json:"room_latency,omitempty"`
	Artifacts         map[string]manifestFile        `json:"artifacts"`
	RecordingStatus   *transcript.RecordingStatus    `json:"recording_status,omitempty"`
	DegradedArtifacts map[string]string              `json:"degraded_artifacts,omitempty"`
	Error             string                         `json:"error,omitempty"`
}

func (r *Recorder) writeManifest(result rooms.RoomResult, runErr error, endedAt time.Time) error {
	if r == nil {
		return nil
	}
	if endedAt.IsZero() {
		endedAt = r.clock.Now()
	}
	manifest, err := r.buildManifest(result, runErr, endedAt)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal room evidence manifest: %w", err)
	}
	return writeManifestFile(filepath.Join(r.destination, rooms.RoomReplayBundleManifestPath), data)
}

func (r *Recorder) buildManifest(result rooms.RoomResult, runErr error, endedAt time.Time) (roomManifest, error) {
	status, degraded := r.recordingHealth()
	manifest := r.baseManifest(result, runErr, endedAt, status, degraded)
	if err := r.addRoomArtifacts(&manifest); err != nil {
		return roomManifest{}, err
	}
	if err := r.addParticipants(&manifest, result); err != nil {
		return roomManifest{}, err
	}
	return manifest, nil
}

func manifestReason(result rooms.RoomResult) rooms.RoomTerminationReason {
	if result.TerminationReason != "" {
		return result.TerminationReason
	}
	if result.Reason != "" {
		return result.Reason
	}
	return rooms.RoomTerminationFailed
}

func (r *Recorder) baseManifest(result rooms.RoomResult, runErr error, endedAt time.Time, status *transcript.RecordingStatus, degraded map[string]string) roomManifest {
	reason := manifestReason(result)
	manifest := roomManifest{
		SchemaVersion: recorderSchemaVersion,
		Finalized:     runErr == nil,
		ClockBase:     r.startedAt.UTC().Format(time.RFC3339Nano),
		Timing: manifestTiming{
			StartedAt: r.startedAt.UTC().Format(time.RFC3339Nano),
			EndedAt:   endedAt.UTC().Format(time.RFC3339Nano),
			Elapsed:   elapsedString(endedAt.Sub(r.startedAt)),
		},
		Bounds:            manifestBounds{MaxTurns: r.manifest.Room.MaxTurns, MaxDuration: durationString(r.manifest.Room.MaxDuration)},
		TerminationReason: reason, Reason: reason,
		Participants: make(map[string]participantManifest, len(r.participants)),
		TurnCounts:   make(map[string]int, len(r.participants)),
		PCMFormat: rooms.RoomReplayPCMFormat{
			SampleRate: r.format.SampleRate, Channels: r.format.Channels,
			SampleWidthBits: recorderWidthBits, ByteOrder: recorderByteOrder, Encoding: recorderEncoding,
		},
		RoomMix: rooms.RoomEvidenceMixPath, RoomTimeline: rooms.RoomEvidenceTimelinePath,
		RoomLatency:     rooms.RoomLatencyArtifactPath,
		Artifacts:       make(map[string]manifestFile, len(r.participants)*participantArtifactRoleCapacity+roomManifestArtifactCapacity),
		RecordingStatus: cloneStatus(status), DegradedArtifacts: cloneStrings(degraded),
	}
	if runErr != nil {
		manifest.Error = "room run failed"
	}
	return manifest
}

func (r *Recorder) addRoomArtifacts(manifest *roomManifest) error {
	roomMix, err := r.fileReference(rooms.RoomEvidenceMixPath, "room_mix", "room:room_mix")
	if err != nil {
		return err
	}
	roomTimeline, err := r.fileReference(rooms.RoomEvidenceTimelinePath, "room_timeline", "room:room_timeline")
	if err != nil {
		return err
	}
	manifest.Artifacts["room_mix"] = roomMix
	manifest.Artifacts["room_timeline"] = roomTimeline
	roomLatency, err := r.fileReference(rooms.RoomLatencyArtifactPath, "room_latency", "room:room_latency")
	if err != nil {
		return err
	}
	manifest.Artifacts["room_latency"] = roomLatency
	return nil
}

func (r *Recorder) addParticipants(manifest *roomManifest, result rooms.RoomResult) error {
	for id, participant := range r.participants {
		if participant == nil {
			continue
		}
		value, artifacts, err := r.participantManifest(id, participant, result.Participants)
		if err != nil {
			return err
		}
		manifest.Participants[id], manifest.TurnCounts[id] = value, value.CompletedTurns
		for role, ref := range artifacts {
			manifest.Artifacts[id+"."+role] = ref
		}
	}
	return nil
}

func (r *Recorder) participantManifest(id string, participant *participantRecorder, results map[string]rooms.RoomParticipantResult) (participantManifest, map[string]manifestFile, error) {
	value := participantManifest{
		ID: participant.manifest.ID, Kind: roommanifest.NormalizeParticipantKind(participant.manifest.Kind),
		SystemPrompt: participant.manifest.SystemPrompt, OpeningPrompt: participant.manifest.OpeningPrompt,
		Provider: participant.manifest.Provider, Model: participant.manifest.Model, Voice: participant.manifest.Voice,
		Artifacts: make(map[string]manifestFile, participantArtifactRoleCapacity), InputDevice: participant.manifest.InputDevice,
		OutputDevice: participant.manifest.OutputDevice,
	}
	if participantResult, ok := results[id]; ok {
		value.CompletedTurns = participantResult.TurnsCompleted
		value.TerminationReason = participantResult.TerminationReason
		value.Reason = participantResult.Reason
		value.TerminationTrigger = participantResult.TerminationTrigger
		value.TerminationDisposition = participantResult.TerminationDisposition
		value.Classification = participantResult.Classification
		value.TerminalReason = participantResult.TerminalReason
		value.TerminalProvenance = participantResult.TerminalProvenance
		value.OutputState = participantResult.OutputState
		value.Connected = participantResult.Connected
		value.Error = participantResult.Error
		value.RecordingStatus = cloneStatus(participantResult.RecordingStatus)
	} else {
		value.TerminationReason, value.Reason = rooms.ParticipantTerminationError, rooms.ParticipantTerminationError
	}
	if participantStatus, ok := r.participantStatus(id); ok {
		value.RecordingStatus = cloneStatus(participantStatus)
	}
	artifacts := participantArtifactReferences(r, id, participant)
	if artifacts == nil {
		return participantManifest{}, nil, fmt.Errorf("participant %q has no evidence artifacts", id)
	}
	for role, ref := range artifacts {
		value.Artifacts[role] = ref
	}
	value.DegradedArtifacts = r.participantDegraded(id)
	return value, artifacts, nil
}

func participantArtifactReferences(r *Recorder, id string, participant *participantRecorder) map[string]manifestFile {
	paths := participantArtifactPaths(participant.artifacts)
	result := make(map[string]manifestFile, len(paths))
	for role, path := range paths {
		ref, err := r.fileReference(path, role, "participant:"+id+":"+role)
		if err != nil {
			return nil
		}
		result[role] = ref
	}
	return result
}

func writeManifestFile(path string, data []byte) (err error) {
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".run-manifest-*.tmp")
	if err != nil {
		return fmt.Errorf("create room evidence manifest: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		if removeErr := os.Remove(temporaryPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove temporary room evidence manifest: %w", removeErr))
		}
	}()
	if chmodErr := temporary.Chmod(evidenceFileMode); chmodErr != nil {
		if closeErr := temporary.Close(); closeErr != nil {
			return errors.Join(chmodErr, fmt.Errorf("close room evidence manifest: %w", closeErr))
		}
		return chmodErr
	}
	if _, writeErr := temporary.Write(data); writeErr != nil {
		writeErr = fmt.Errorf("write room evidence manifest: %w", writeErr)
		if closeErr := temporary.Close(); closeErr != nil {
			return errors.Join(writeErr, fmt.Errorf("close room evidence manifest: %w", closeErr))
		}
		return writeErr
	}
	if syncErr := temporary.Sync(); syncErr != nil {
		syncErr = fmt.Errorf("sync room evidence manifest: %w", syncErr)
		if closeErr := temporary.Close(); closeErr != nil {
			return errors.Join(syncErr, fmt.Errorf("close room evidence manifest: %w", closeErr))
		}
		return syncErr
	}
	if closeErr := temporary.Close(); closeErr != nil {
		return fmt.Errorf("close room evidence manifest: %w", closeErr)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("install room evidence manifest: %w", err)
	}
	return nil
}

func (r *Recorder) fileReference(path, role, owner string) (manifestFile, error) {
	path = filepath.ToSlash(filepath.Clean(path))
	absolute := filepath.Join(r.destination, filepath.FromSlash(path))
	info, err := os.Stat(absolute)
	if err != nil {
		return manifestFile{}, fmt.Errorf("stat evidence artifact %q: %w", path, err)
	}
	if info.IsDir() {
		return manifestFile{}, fmt.Errorf("evidence artifact %q is a directory", path)
	}
	digest, err := fileSHA256(absolute)
	if err != nil {
		return manifestFile{}, fmt.Errorf("hash evidence artifact %q: %w", path, err)
	}
	return manifestFile{Name: filepath.Base(path), Role: role, Owner: owner, Path: path, Size: info.Size(), SHA256: digest, Empty: info.Size() == 0}, nil
}

func participantArtifactPaths(paths artifactPaths) map[string]string {
	result := map[string]string{
		"wav": paths.WAV, "diagnostics": paths.Diagnostics, "deltas": paths.Deltas,
		"sent_pcm": paths.SentPCM, "received_pcm": paths.ReceivedPCM, "events": paths.Events,
	}
	if paths.Capture != "" {
		result["capture"] = paths.Capture
	}
	return result
}

func (r *Recorder) recordingHealth() (*transcript.RecordingStatus, map[string]string) {
	if r == nil {
		return nil, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.artifactErr) == 0 && r.recordErr == nil {
		return nil, nil
	}
	degraded := make(map[string]string, len(r.artifactErr))
	for path, err := range r.artifactErr {
		degraded[filepath.ToSlash(path)] = recordingErrorText(err)
	}
	reason := "recording degraded"
	if r.recordErr != nil {
		reason = recordingErrorText(r.recordErr)
	}
	return &transcript.RecordingStatus{State: transcript.RecordingStatusPartial, Reason: reason}, degraded
}

func (r *Recorder) participantStatus(id string) (*transcript.RecordingStatus, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	participant := r.participants[id]
	if participant == nil {
		return nil, false
	}
	for _, path := range participantArtifactPaths(participant.artifacts) {
		if err, ok := r.artifactErr[path]; ok {
			return &transcript.RecordingStatus{State: transcript.RecordingStatusPartial, Reason: recordingErrorText(err)}, true
		}
	}
	return nil, false
}

func (r *Recorder) participantDegraded(id string) map[string]string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	degraded := make(map[string]string)
	participant := r.participants[id]
	if participant != nil {
		for _, path := range participantArtifactPaths(participant.artifacts) {
			if err, ok := r.artifactErr[path]; ok {
				degraded[path] = recordingErrorText(err)
			}
		}
	}
	r.mu.Unlock()
	if len(degraded) == 0 {
		return nil
	}
	return degraded
}

func recordingErrorText(err error) string {
	if err == nil {
		return recordingDegradedReason
	}
	value := strings.TrimSpace(err.Error())
	if value == "" {
		return recordingDegradedReason
	}
	return value
}

func durationString(value time.Duration) string {
	if value <= 0 {
		return ""
	}
	return value.String()
}

func elapsedString(value time.Duration) string {
	if value < 0 {
		return "0s"
	}
	return value.String()
}
