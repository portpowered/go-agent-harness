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
	roomanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/room"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
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
	captureSeen  map[string]bool
	providerErrs map[string]struct{}

	mu                   sync.Mutex
	operationMu          sync.Mutex
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

func (r *recorder) LatencyRecorder() rooms.LatencyRecorder {
	if r == nil {
		return nil
	}
	return r.latency
}

func (s *Service) Analyze(bundle roomevidence.Bundle) (roomevidence.Analysis, error) {
	result, err := roomanalysis.AnalyzePCM16Room(roomAnalysisInput(bundle), bundle.Tolerances.RoomConfig)
	return roomevidence.Analysis{Result: result}, err
}

func roomAnalysisInput(bundle roomevidence.Bundle) roomanalysis.PCM16RoomInput {
	input := roomanalysis.PCM16RoomInput{
		Overlaps: append([]roomanalysis.PCM16OverlapInterval(nil), bundle.Overlaps...),
		BargeIns: append([]roomanalysis.PCM16BargeInAnnotation(nil), bundle.BargeIns...),
		Loudness: append([]roomanalysis.PCM16LoudnessInterval(nil), bundle.Loudness...),
	}
	for _, participant := range bundle.Participants {
		for _, stream := range []roomevidence.AudioStream{participant.WAV, participant.Sent, participant.Received} {
			if len(stream.Samples) > 0 {
				input.Streams = append(input.Streams, cloneTimedStream(stream.PCM16TimedStream))
			}
		}
	}
	if bundle.RoomMix.StreamID != "" && len(bundle.RoomMix.Samples) > 0 {
		input.Streams = append(input.Streams, cloneTimedStream(bundle.RoomMix.PCM16TimedStream))
	}
	return input
}

func (r *recorder) participant(id string) *participantRecorder {
	if r == nil {
		return nil
	}
	return r.participants[id]
}

func (r *recorder) Artifacts(id string) roomevidence.ArtifactPaths {
	participant := r.participant(id)
	if participant == nil {
		return roomevidence.ArtifactPaths{}
	}
	return participant.Artifacts()
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
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	_, err := r.recordOpenTimeline(event, participant, fields)
	return err
}

func (r *recorder) RecordFinalTimeline(event, participant string, fields map[string]string) (time.Time, error) {
	if r == nil {
		return time.Time{}, roomevidence.ErrRecorderClosed
	}
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
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
	entry := timelineRecord{
		TOffsetMS: offset, TUnixMS: at.UnixMilli(),
		Event:         redactText(event, r.secrets),
		Participant:   redactText(participant, r.secrets),
		ParticipantID: redactText(participant, r.secrets),
		Fields:        redactFields(fields, r.secrets),
	}
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
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
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
	_, err := r.recordOpenTimeline("provider_error", participant, fields)
	return err
}

func (r *recorder) SetParticipantReady(ready rooms.RoomParticipantReady) error {
	if r == nil {
		return roomevidence.ErrRecorderClosed
	}
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
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
	_, err := r.recordOpenTimeline("participant_ready", ready.ParticipantID, nil)
	return err
}

func (r *recorder) SetParticipantTerminated(value rooms.RoomParticipantResult) error {
	if r == nil {
		return roomevidence.ErrRecorderClosed
	}
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	fields := map[string]string{
		"termination_trigger":     value.TerminationTrigger,
		"termination_disposition": value.TerminationDisposition,
		"classification":          value.Classification,
		"terminal_reason":         value.TerminalReason,
		"terminal_provenance":     value.TerminalProvenance,
		"output_state":            value.OutputState,
		"reason":                  string(value.TerminationReason),
		"turns":                   fmt.Sprintf("%d", value.TurnsCompleted),
	}
	_, err := r.recordOpenTimeline("participant_terminated", value.ParticipantID, fields)
	return err
}

func (r *recorder) RecordSource(participantID string, frame audio.PCMFrame) {
	if r == nil {
		return
	}
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	participant := r.participant(participantID)
	if participant == nil {
		return
	}
	if err := participant.observeSentAudio(codec.EncodePCM16(append([]int16(nil), frame.Samples...))); err != nil {
		r.recordError(participantID, participant.artifacts.SentPCM, err)
	}
}

func (r *recorder) RecordReceived(participantID string, frame audio.PCMFrame) {
	if r == nil {
		return
	}
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	participant := r.participant(participantID)
	if participant == nil {
		return
	}
	if err := participant.observeReceivedAudio(codec.EncodePCM16(append([]int16(nil), frame.Samples...))); err != nil {
		r.recordError(participantID, participant.artifacts.ReceivedPCM, err)
	}
}

func (r *recorder) ObserveSpeakerAudio(sourceID string, targetIDs []string, frame audio.PCMFrame) {
	if r == nil || r.latency == nil {
		return
	}
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	if r.checkOpen() != nil {
		return
	}
	r.latency.ObserveSpeakerAudio(sourceID, targetIDs, frame)
}

func (r *recorder) ObserveSpeechStopped(participantID string) {
	if r != nil && r.latency != nil {
		r.operationMu.Lock()
		defer r.operationMu.Unlock()
		if r.checkOpen() != nil {
			return
		}
		r.latency.ObserveSpeechStopped(participantID)
	}
}

func (r *recorder) ObserveProviderAudio(participantID, responseID string) {
	if r != nil && r.latency != nil {
		r.operationMu.Lock()
		defer r.operationMu.Unlock()
		if r.checkOpen() != nil {
			return
		}
		r.latency.ObserveProviderAudio(participantID, responseID)
	}
}

func (r *recorder) ObservePeerAudio(sourceID, targetID string, frame audio.PCMFrame) {
	if r != nil && r.latency != nil {
		r.operationMu.Lock()
		defer r.operationMu.Unlock()
		if r.checkOpen() != nil {
			return
		}
		r.latency.ObservePeerAudio(sourceID, targetID, frame)
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
	_, err := r.Finalize(roomevidence.Finalization{Room: rooms.RoomResult{TerminationReason: rooms.RoomTerminationStopped}})
	return err
}

// These tiny indirections keep filesystem calls grouped with the private
// recorder policy and make the compatibility layer unable to provide a path
// or writer implementation to the runtime service.
func mkdirOutputDirectory(path string) error { return os.MkdirAll(path, evidenceDirectoryMode) }

// compile-time interface checks keep the public seam honest.
var _ roomevidence.Recorder = (*recorder)(nil)
