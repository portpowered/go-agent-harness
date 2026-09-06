package evidence

import (
	"errors"
	"path/filepath"
	"strconv"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

// RecordTimeline appends one room-level lifecycle observation.
func (r *Recorder) RecordTimeline(event, participant string, fields map[string]string) {
	at := r.clock.Now()
	copyFields := cloneStrings(fields)
	if err := r.enqueue(rooms.RoomEvidenceTimelinePath, func() { r.writeTimelineAt(at, event, participant, copyFields) }); err != nil {
		return
	}
}

// SetReady records host-resolved participant metadata before finalizing the
// manifest.
func (r *Recorder) SetReady(value rooms.RoomParticipantReady) {
	participant := r.participant(value.ParticipantID)
	if participant == nil {
		return
	}
	at := r.clock.Now()
	if err := r.enqueue(rooms.RoomEvidenceTimelinePath, func() {
		participant.manifest.Kind = value.Kind
		participant.manifest.InputDevice = value.InputDevice
		participant.manifest.OutputDevice = value.OutputDevice
		participant.manifest.Provider = value.Provider
		participant.manifest.Model = value.Model
		r.writeTimelineAt(at, "participant_ready", participant.id, nil)
	}); err != nil {
		return
	}
}

// SetTerminated records a participant terminal transition for replay evidence.
func (r *Recorder) SetTerminated(value rooms.RoomParticipantResult) {
	if r.participant(value.ParticipantID) == nil {
		return
	}
	fields := map[string]string{"reason": string(value.TerminationReason), "turns": formatCount(value.TurnsCompleted)}
	at := r.clock.Now()
	if err := r.enqueue(rooms.RoomEvidenceTimelinePath, func() { r.writeTimelineAt(at, "participant_terminated", value.ParticipantID, fields) }); err != nil {
		return
	}
}

// Finalize closes all sinks and writes the integrity manifest. It is safe to
// call more than once; only the first call mutates the bundle.
func (r *Recorder) Finalize(result rooms.RoomResult, runErr error, endedAt time.Time) error {
	if r == nil {
		return nil
	}
	r.finalizeOnce.Do(func() { r.finalizeBundle(result, runErr, endedAt) })
	return r.finalizeErr
}

func (r *Recorder) finalizeBundle(result rooms.RoomResult, runErr error, endedAt time.Time) {
	r.recordTimeline("run_terminated", "", map[string]string{"reason": string(result.TerminationReason)})
	r.stopWorker()
	r.closeParticipants()
	r.closeTimeline()
	endedAt = r.resolveEndTime(endedAt)
	r.finalizeMix(endedAt)
	r.finalizeLatency()
	r.recordCaptureErrors()
	r.finalizeErr = r.writeManifest(result, runErr, endedAt)
	if r.recordErr != nil {
		r.finalizeErr = errors.Join(r.finalizeErr, r.recordErr)
	}
	r.status, r.degraded = r.recordingHealth()
}

func (r *Recorder) closeParticipants() {
	for _, participant := range r.participants {
		r.closeParticipant(participant)
	}
}

func (r *Recorder) closeTimeline() {
	if r.timeline != nil {
		r.recordError("", rooms.RoomEvidenceTimelinePath, r.timeline.close())
	}
}

func (r *Recorder) resolveEndTime(endedAt time.Time) time.Time {
	if endedAt.IsZero() {
		return r.clock.Now()
	}
	return endedAt
}

func (r *Recorder) finalizeMix(endedAt time.Time) {
	if err := r.mix.finalize(endedAt.Sub(r.startedAt), filepath.Join(r.destination, rooms.RoomEvidenceMixPath)); err != nil {
		r.recordError("", rooms.RoomEvidenceMixPath, err)
	}
}

func (r *Recorder) finalizeLatency() {
	if r.latency == nil {
		return
	}
	if err := r.latency.Write(filepath.Join(r.destination, rooms.RoomLatencyArtifactPath)); err != nil {
		r.recordError("", rooms.RoomLatencyArtifactPath, err)
	}
}

func (r *Recorder) recordCaptureErrors() {
	if err := r.validateCaptures(); err != nil {
		// validateCaptures records each participant path separately so the
		// manifest can identify exactly which provider trace is unavailable.
		r.recordError("", "", err)
	}
}

// Status returns the final evidence health projection after Finalize.
func (r *Recorder) Status() (*transcript.RecordingStatus, map[string]string) {
	if r == nil {
		return nil, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneStatus(r.status), cloneStrings(r.degraded)
}

// ApplyResult projects evidence health onto the room result after all writers
// have joined. Conversation termination remains owned by lifecycle; recording
// failures are represented separately on the room and affected participants.
func (r *Recorder) ApplyResult(result *rooms.RoomResult) {
	if r == nil || result == nil {
		return
	}
	status, degraded := r.Status()
	result.RecordingStatus = status
	result.DegradedArtifacts = degraded
	for id, participant := range result.Participants {
		if participantStatus, ok := r.participantStatus(id); ok {
			participant.RecordingStatus = participantStatus
			result.Participants[id] = participant
		}
	}
}

func (r *Recorder) participant(id string) *participantRecorder {
	if r == nil {
		return nil
	}
	return r.participants[id]
}

func (r *Recorder) offsetAt(at time.Time) time.Duration {
	if r == nil || at.IsZero() {
		return 0
	}
	offset := at.Sub(r.startedAt)
	if offset < 0 {
		return 0
	}
	return offset
}

func (r *Recorder) recordTimeline(event, participant string, fields map[string]string) {
	if r == nil {
		return
	}
	at := r.clock.Now()
	copyFields := cloneStrings(fields)
	if err := r.enqueue(rooms.RoomEvidenceTimelinePath, func() { r.writeTimelineAt(at, event, participant, copyFields) }); err != nil {
		return
	}
}

func (r *Recorder) writeTimelineAt(at time.Time, event, participant string, fields map[string]string) {
	if r == nil || r.timeline == nil {
		return
	}
	value := timelineRecord{TOffsetMS: float64(r.offsetAt(at)) / float64(time.Millisecond), TUnixMS: at.UnixMilli(), Event: event, ParticipantID: participant, Fields: cloneStrings(fields)}
	if err := r.timeline.write(value); err != nil {
		r.recordError("", rooms.RoomEvidenceTimelinePath, err)
	}
}

func formatCount(value int) string {
	return strconv.Itoa(value)
}

type timelineRecord struct {
	TOffsetMS     float64           `json:"t_offset_ms"`
	TUnixMS       int64             `json:"t_unix_ms"`
	Event         string            `json:"event"`
	ParticipantID string            `json:"participant_id,omitempty"`
	Fields        map[string]string `json:"fields,omitempty"`
}
