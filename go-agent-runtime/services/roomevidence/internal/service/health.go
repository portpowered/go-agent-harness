package service

import (
	"path/filepath"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

type healthSnapshot struct {
	recordErr       error
	artifactErrs    map[string]error
	participantErrs map[string]error
	secrets         []string
	participants    map[string]roomevidence.ArtifactPaths
}

func (r *recorder) Health() roomevidence.Health {
	if r == nil {
		return roomevidence.Health{}
	}
	return buildHealth(r.snapshotHealth())
}

func (r *recorder) snapshotHealth() healthSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot := healthSnapshot{
		recordErr:       r.recordErr,
		artifactErrs:    make(map[string]error, len(r.artifactRecordErr)),
		participantErrs: make(map[string]error, len(r.participantRecordErr)),
		secrets:         cloneStrings(r.secrets),
		participants:    make(map[string]roomevidence.ArtifactPaths, len(r.participants)),
	}
	for path, err := range r.artifactRecordErr {
		snapshot.artifactErrs[path] = err
	}
	for id, err := range r.participantRecordErr {
		snapshot.participantErrs[id] = err
	}
	for id, participant := range r.participants {
		if participant != nil {
			snapshot.participants[id] = participant.artifacts
		}
	}
	return snapshot
}

func buildHealth(snapshot healthSnapshot) roomevidence.Health {
	return roomevidence.Health{
		Status:               recordingStatus(snapshot.recordErr, snapshot.secrets),
		DegradedArtifacts:    sanitizedErrors(snapshot.artifactErrs, snapshot.secrets),
		ParticipantStatuses:  participantStatuses(snapshot.participantErrs, snapshot.secrets),
		ParticipantArtifacts: participantArtifactsHealth(snapshot.participants, snapshot.artifactErrs, snapshot.secrets),
	}
}

func recordingStatus(err error, secrets []string) *transcript.RecordingStatus {
	if err == nil {
		return nil
	}
	return &transcript.RecordingStatus{State: transcript.RecordingStatusPartial, Reason: sanitizedError(err, secrets)}
}

func sanitizedErrors(values map[string]error, secrets []string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for path, err := range values {
		result[path] = sanitizedError(err, secrets)
	}
	return result
}

func participantStatuses(values map[string]error, secrets []string) map[string]*transcript.RecordingStatus {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]*transcript.RecordingStatus, len(values))
	for id, err := range values {
		result[id] = recordingStatus(err, secrets)
	}
	return result
}

func participantArtifactsHealth(participants map[string]roomevidence.ArtifactPaths, artifactErrs map[string]error, secrets []string) map[string]map[string]string {
	if len(participants) == 0 || len(artifactErrs) == 0 {
		return nil
	}
	result := make(map[string]map[string]string)
	for id, paths := range participants {
		for _, path := range participantArtifactList(paths) {
			if err, exists := artifactErrs[filepath.ToSlash(path)]; exists {
				if result[id] == nil {
					result[id] = make(map[string]string)
				}
				result[id][filepath.ToSlash(path)] = sanitizedError(err, secrets)
			}
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func participantArtifactList(paths roomevidence.ArtifactPaths) []string {
	return []string{paths.WAV, paths.Diagnostics, paths.Deltas, paths.SentPCM, paths.ReceivedPCM, paths.Events, paths.Capture}
}

func (r *recorder) ApplyRecordingHealth(result *rooms.RoomResult) {
	if r == nil || result == nil {
		return
	}
	health := r.Health()
	result.RecordingStatus = cloneStatus(health.Status)
	result.DegradedArtifacts = cloneMap(health.DegradedArtifacts)
	for id, participant := range result.Participants {
		participant.RecordingStatus = cloneStatus(health.ParticipantStatuses[id])
		result.Participants[id] = participant
	}
}
