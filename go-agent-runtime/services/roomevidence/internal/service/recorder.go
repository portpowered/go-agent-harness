package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
)

type recorder struct {
	destination string
	manifest    rooms.Manifest
	format      rooms.AudioFormat
	startedAt   time.Time
	clock       clockState
	secrets     []string
	latency     rooms.LatencyRecorder
	mix         *mixer.PCMAccumulator
	timeline    *jsonlWriter
	timelineMu  sync.Mutex

	participants map[string]*participantRecorder
	providerErrs map[string]struct{}

	mu                   sync.Mutex
	recordErr            error
	participantRecordErr map[string]error
	artifactRecordErr    map[string]error
	finalized            bool
	finalizeOnce         sync.Once
	finalizeDone         chan struct{}
	finalizeErr          error
}

type participantRecorder struct {
	owner     *recorder
	id        string
	manifest  rooms.Participant
	artifacts roomevidence.ArtifactPaths

	wav            *wavWriter
	diagnostics    *jsonlWriter
	deltas         *jsonlWriter
	events         *jsonlWriter
	sentPCM        *pcmWriter
	receivedPCM    *pcmWriter
	sentSpeech     *speechTracker
	receivedSpeech *speechTracker
}

func (r *recorder) Destination() string {
	if r == nil {
		return ""
	}
	return r.destination
}
func (r *recorder) StartedAt() time.Time {
	if r == nil {
		return time.Time{}
	}
	return r.startedAt
}
func (r *recorder) AudioFormat() rooms.AudioFormat {
	if r == nil {
		return rooms.AudioFormat{}
	}
	return r.format
}

func (r *recorder) Participant(id string) roomevidence.ParticipantRecorder {
	if r == nil {
		return nil
	}
	return r.participants[id]
}

func (r *recorder) CapturePath(id string) string {
	if r == nil {
		return ""
	}
	participant := r.participants[id]
	if participant == nil || participant.artifacts.Capture == "" {
		return ""
	}
	return filepath.Join(r.destination, filepath.FromSlash(participant.artifacts.Capture))
}

func (r *recorder) checkOpen() error {
	if r == nil {
		return roomevidence.ErrRecorderClosed
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finalized {
		return roomevidence.ErrFinalized
	}
	return nil
}

func (r *recorder) RecordTimeline(event, participant string, fields map[string]string) error {
	if r == nil {
		return roomevidence.ErrRecorderClosed
	}
	_, err := r.recordOpenTimeline(event, participant, fields)
	return err
}

func (r *recorder) RecordFinalTimeline(event, participant string, fields map[string]string) (time.Time, error) {
	if r == nil {
		return time.Time{}, roomevidence.ErrRecorderClosed
	}
	return r.recordOpenTimeline(event, participant, fields)
}

func (r *recorder) recordOpenTimeline(event, participant string, fields map[string]string) (time.Time, error) {
	r.timelineMu.Lock()
	defer r.timelineMu.Unlock()
	at := r.clock.source.Now().UTC()
	r.mu.Lock()
	finalized := r.finalized
	r.mu.Unlock()
	if finalized {
		return at, roomevidence.ErrFinalized
	}
	return at, r.writeTimelineLocked(at, event, participant, fields)
}

func (r *recorder) writeTimelineAt(at time.Time, event, participant string, fields map[string]string) error {
	r.timelineMu.Lock()
	defer r.timelineMu.Unlock()
	return r.writeTimelineLocked(at, event, participant, fields)
}

func (r *recorder) writeTimelineLocked(at time.Time, event, participant string, fields map[string]string) error {
	if r == nil || r.timeline == nil {
		return errors.New("room timeline sink is not initialized")
	}
	offset := formatOffset(r.startedAt, at)
	entry := timelineRecord{TOffsetMS: offset, TUnixMS: at.UnixMilli(), Event: event, Participant: participant, Fields: cloneFields(fields)}
	if err := r.timeline.write(entry); err != nil {
		wrapped := fmt.Errorf("record %s: %w", event, err)
		r.recordError("", roomevidence.TimelinePath, wrapped)
		return wrapped
	}
	return nil
}

func (r *recorder) RecordProviderErrorTimeline(participant string, fields map[string]string) error {
	if r == nil {
		return roomevidence.ErrRecorderClosed
	}
	r.mu.Lock()
	if r.finalized {
		r.mu.Unlock()
		return roomevidence.ErrFinalized
	}
	if _, seen := r.providerErrs[participant]; seen {
		r.mu.Unlock()
		return nil
	}
	r.providerErrs[participant] = struct{}{}
	r.mu.Unlock()
	return r.RecordTimeline("provider_error", participant, fields)
}

func (r *recorder) SetParticipantReady(ready rooms.RoomParticipantReady) error {
	if r == nil {
		return roomevidence.ErrRecorderClosed
	}
	if err := r.checkOpen(); err != nil {
		return err
	}
	participant := r.participants[ready.ParticipantID]
	if participant == nil {
		return fmt.Errorf("%w: %q", roomevidence.ErrParticipantUnknown, ready.ParticipantID)
	}
	participant.manifest.Kind = ready.Kind
	participant.manifest.InputDevice = ready.InputDevice
	participant.manifest.OutputDevice = ready.OutputDevice
	participant.manifest.Provider = ready.Provider
	participant.manifest.Model = ready.Model
	for index := range r.manifest.Participants {
		if r.manifest.Participants[index].ID == ready.ParticipantID {
			r.manifest.Participants[index] = participant.manifest
		}
	}
	return r.RecordTimeline("participant_ready", ready.ParticipantID, nil)
}

func (r *recorder) ObserveSpeakerAudio(sourceID string, targetIDs []string, pcm []byte) {
	if r == nil || r.latency == nil {
		return
	}
	r.latency.ObserveSpeakerBytes(sourceID, targetIDs, len(pcm))
}

func (r *recorder) ObserveSpeechStopped(participantID string) {
	if r != nil && r.latency != nil {
		r.latency.ObserveSpeechStopped(participantID)
	}
}

func (r *recorder) ObserveProviderAudio(participantID, responseID string) {
	if r != nil && r.latency != nil {
		r.latency.ObserveProviderAudio(participantID, responseID)
	}
}

func (r *recorder) ObservePeerAudio(sourceID, targetID string, pcm []byte) {
	if r != nil && r.latency != nil {
		r.latency.ObservePeerBytes(sourceID, targetID, len(pcm))
	}
}

func (r *recorder) MarkError(participant, artifact string, err error) {
	r.recordError(participant, artifact, err)
}

func (r *recorder) recordError(participant, artifact string, err error) {
	if r == nil || err == nil {
		return
	}
	participant = strings.TrimSpace(participant)
	artifact = filepath.ToSlash(strings.TrimSpace(artifact))
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recordErr == nil {
		r.recordErr = fmt.Errorf("room evidence: %w", err)
	}
	if participant != "" {
		if r.participantRecordErr == nil {
			r.participantRecordErr = make(map[string]error)
		}
		if _, exists := r.participantRecordErr[participant]; !exists {
			// The first causal failure is the room's recording reason and is
			// shared with the affected participant projection. Artifact maps
			// retain the narrower path-specific wrapper below.
			r.participantRecordErr[participant] = r.recordErr
		}
	}
	if artifact != "" {
		if r.artifactRecordErr == nil {
			r.artifactRecordErr = make(map[string]error)
		}
		if _, exists := r.artifactRecordErr[artifact]; !exists {
			r.artifactRecordErr[artifact] = fmt.Errorf("room evidence artifact %s: %w", artifact, err)
		}
	}
}

func (r *recorder) Error() error {
	if r == nil {
		return roomevidence.ErrRecorderClosed
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recordErr
}

func (r *recorder) Close() error {
	if r == nil {
		return nil
	}
	return r.Finalize(rooms.RoomResult{TerminationReason: rooms.RoomTerminationStopped}, nil, time.Time{})
}

// These tiny indirections keep filesystem calls grouped with the private
// recorder policy and make the compatibility layer unable to provide a path
// or writer implementation to the runtime service.
func mkdirOutputDirectory(path string) error { return os.MkdirAll(path, evidenceDirectoryMode) }

// compile-time interface checks keep the public seam honest.
var _ roomevidence.Recorder = (*recorder)(nil)
var _ roomevidence.ParticipantRecorder = (*participantRecorder)(nil)
