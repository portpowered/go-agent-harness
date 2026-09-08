package evidence

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	roomlatency "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/evidence/latency"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// ObserveSpeakerAudio forwards source media landmarks to the room latency
// ledger. The lifecycle graph depends only on this narrow observation shape;
// keeping the forwarding method on the evidence owner avoids exposing the
// latency recorder implementation to room orchestration.
func (r *Recorder) ObserveSpeakerAudio(sourceID string, targetIDs []string, frame audio.PCMFrame) {
	if r == nil || r.latency == nil {
		return
	}
	r.latency.ObserveSpeakerAudio(sourceID, targetIDs, frame)
}

// ObservePeerAudio records the first accepted mixed frame for a latency
// transition while keeping source attribution inside the evidence owner.
func (r *Recorder) ObservePeerAudio(sourceID, targetID string, frame audio.PCMFrame) {
	if r == nil || r.latency == nil {
		return
	}
	r.latency.ObservePeerAudio(sourceID, targetID, frame)
}

// RecordSource stores audio spoken by a participant and adds it to the room
// mix. Samples are copied before enqueueing so the caller's media buffer can
// be reused immediately.
func (r *Recorder) RecordSource(participantID string, frame audio.PCMFrame) {
	participant := r.participant(participantID)
	if participant == nil {
		return
	}
	if r.maxFrameSamples > 0 && len(frame.Samples) > r.maxFrameSamples {
		r.recordError(participantID, participant.artifacts.WAV, fmt.Errorf("audio frame exceeds %d sample bound", r.maxFrameSamples))
		return
	}
	samples := append([]int16(nil), frame.Samples...)
	at := r.clock.Now()
	offset := r.offsetAt(at)
	metadata := audioFrameRecordFromFrame("audio_source", participantID, frame, at, offset)
	hasStartSample := frame.StartSample > 0 || frame.Sequence > 0 || frame.Epoch > 0 || frame.StreamID != "" || frame.Format.SampleRate > 0
	metadata.StartSampleKnown = hasStartSample
	if err := r.enqueue(participant.artifacts.WAV, func() {
		if err := participant.events.write(metadata); err != nil {
			r.recordError(participant.id, participant.artifacts.Events, err)
		}
		if err := participant.wav.write(samples); err != nil {
			r.recordError(participantID, participant.artifacts.WAV, err)
		}
		if err := participant.sent.write(samples); err != nil {
			r.recordError(participantID, participant.artifacts.SentPCM, err)
		}
		if err := r.mix.addSource(participantID, offset, frame, hasStartSample, samples); err != nil {
			r.recordError("", rooms.RoomEvidenceMixPath, err)
		}
	}); err != nil {
		return
	}
}

// RecordReceived stores the mixed audio delivered to a participant.
func (r *Recorder) RecordReceived(participantID string, frame audio.PCMFrame) {
	participant := r.participant(participantID)
	if participant == nil {
		return
	}
	if r.maxFrameSamples > 0 && len(frame.Samples) > r.maxFrameSamples {
		r.recordError(participantID, participant.artifacts.ReceivedPCM, fmt.Errorf("audio frame exceeds %d sample bound", r.maxFrameSamples))
		return
	}
	samples := append([]int16(nil), frame.Samples...)
	at := r.clock.Now()
	metadata := audioFrameRecordFromFrame("audio_received", participantID, frame, at, r.offsetAt(at))
	metadata.StartSampleKnown = frame.StartSample > 0 || frame.Sequence > 0 || frame.Epoch > 0 || frame.StreamID != "" || frame.Format.SampleRate > 0
	if err := r.enqueue(participant.artifacts.ReceivedPCM, func() {
		if err := participant.events.write(metadata); err != nil {
			r.recordError(participantID, participant.artifacts.Events, err)
		}
		if err := participant.received.write(samples); err != nil {
			r.recordError(participantID, participant.artifacts.ReceivedPCM, err)
		}
	}); err != nil {
		return
	}
}

// RecordDiagnostic writes the bounded diagnostic projection for one
// participant. Error values are represented as text and never as raw objects.
func (r *Recorder) RecordDiagnostic(participantID string, record rooms.RoomDiagnosticRecord) {
	participant := r.participant(participantID)
	if participant == nil {
		return
	}
	at := record.At
	if at.IsZero() {
		at = r.clock.Now()
	}
	fields := cloneStrings(record.Fields)
	if err := r.enqueue(participant.artifacts.Diagnostics, func() {
		value := diagnosticRecord{Event: record.Event, Fields: fields, TOffsetMS: float64(r.offsetAt(at)) / float64(time.Millisecond), TUnixMS: at.UnixMilli()}
		if err := participant.diagnostics.write(value); err != nil {
			r.recordError(participantID, participant.artifacts.Diagnostics, err)
		}
		r.writeTimelineAt(at, record.Event, participantID, fields)
	}); err != nil {
		return
	}
}

// Publish implements rooms.EventSink and records ordered live observations.
func (r *Recorder) Publish(ctx context.Context, participantID string, event session.LiveEvent) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	participant := r.participant(participantID)
	if participant == nil {
		return fmt.Errorf("room evidence participant %q is unknown", participantID)
	}
	at := event.Timestamp
	if at.IsZero() {
		at = r.clock.Now()
	}
	r.observeLatency(participantID, event)
	record := eventRecordFromLive(participantID, event, at)
	r.markDroppedEvent(participant, event, &record)
	if strings.Contains(normalizeKind(event.Kind), "terminal") {
		record.Critical = true
	}
	mergeCapability(&record, event.Capability)
	return r.enqueueLiveEvent(participant, event, record, at)
}

func (r *Recorder) observeLatency(participantID string, event session.LiveEvent) {
	if r == nil || r.latency == nil {
		return
	}
	kind := normalizeKind(event.Kind)
	timestamp := event.Timestamp
	if timestamp.IsZero() {
		timestamp = r.clock.Now()
	}
	observation := roomlatency.Observation{Timestamp: timestamp, Tick: event.Sequence, ResponseID: event.ResponseID}
	switch {
	case strings.Contains(kind, "speech_stopped") || strings.Contains(kind, "speech_stop"):
		r.latency.ObserveSpeechStopped(participantID)
	case strings.Contains(kind, "input") && strings.Contains(kind, "commit") || kind == "audio_commit":
		observation.Kind = roomlatency.ObservationInputCommit
		r.latency.ObserveRuntime(participantID, observation)
	case strings.Contains(kind, "response") && strings.Contains(kind, "create"):
		observation.Kind = roomlatency.ObservationResponseCreate
		r.latency.ObserveRuntime(participantID, observation)
	case strings.Contains(kind, "audio") && strings.Contains(kind, "delta") && event.Role != messages.RoleUser:
		if event.Timestamp.IsZero() {
			r.latency.ObserveProviderAudio(participantID, event.ResponseID)
		} else {
			r.latency.ObserveProviderAudioAt(participantID, event.ResponseID, event.Timestamp, event.Sequence)
		}
	}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func eventRecordFromLive(participantID string, event session.LiveEvent, at time.Time) eventRecord {
	return eventRecord{
		Sequence: event.Sequence, Kind: event.Kind, ParticipantID: participantID,
		SessionID: event.SessionID, ResponseID: event.ResponseID, ItemID: event.ItemID,
		ToolCallID: event.ToolCallID, Text: event.Text, Timestamp: at.UTC(),
		Error: errorText(event.Error), Dropped: event.Dropped, BrowserID: event.BrowserID,
		TargetID: event.TargetID, Generation: event.Generation, InvocationID: event.InvocationID,
		State: event.State, Reason: event.Reason, Critical: event.Critical,
	}
}

func (r *Recorder) markDroppedEvent(participant *participantRecorder, event session.LiveEvent, record *eventRecord) {
	if !strings.Contains(normalizeKind(event.Kind), "overflow") && event.Dropped == 0 {
		return
	}
	record.Critical = true
	dropped := event.Dropped
	if dropped == 0 {
		dropped = 1
	}
	r.recordError(participant.id, participant.artifacts.Events, fmt.Errorf("live event overflow dropped %d observations", dropped))
}

func mergeCapability(record *eventRecord, capability *session.LiveCapabilityEvent) {
	if capability == nil {
		return
	}
	record.CapabilityType = capability.Type
	record.CapabilitySequence = capability.Sequence
	record.ToolName = capability.ToolName
	if record.BrowserID == "" {
		record.BrowserID = capability.BrowserID
	}
	if record.TargetID == "" {
		record.TargetID = capability.TargetID
	}
	if record.Generation == 0 {
		record.Generation = capability.Generation
	}
	if record.InvocationID == "" {
		record.InvocationID = capability.InvocationID
	}
	if record.State == "" {
		record.State = capability.State
	}
	if record.Reason == "" {
		record.Reason = capability.Reason
	}
}

func (r *Recorder) enqueueLiveEvent(participant *participantRecorder, event session.LiveEvent, record eventRecord, at time.Time) error {
	return r.enqueue(participant.artifacts.Events, func() {
		if err := participant.events.write(record); err != nil {
			r.recordError(participant.id, participant.artifacts.Events, err)
		}
		if isTranscriptDelta(event.Kind) || isTranscriptEnd(event.Kind) {
			if err := participant.deltas.write(record); err != nil {
				r.recordError(participant.id, participant.artifacts.Deltas, err)
			}
		}
		r.writeTimelineAt(at, "live_"+normalizeKind(event.Kind), participant.id, liveTimelineFields(event))
	})
}

func liveTimelineFields(event session.LiveEvent) map[string]string {
	fields := map[string]string{"sequence": fmt.Sprint(event.Sequence)}
	if event.Liveness != nil {
		fields["classification"] = event.Liveness.Classification
		fields["response_id"] = event.Liveness.ResponseID
		fields["terminal_reason"] = string(event.Liveness.TerminalReason)
		fields["terminal_provenance"] = string(event.Liveness.TerminalProvenance)
		fields["output_state"] = string(event.Liveness.OutputState)
	}
	if event.Terminal != nil {
		if fields["classification"] == "" {
			fields["classification"] = event.Terminal.Classification
		}
		if fields["response_id"] == "" {
			fields["response_id"] = event.ResponseID
		}
		if fields["terminal_reason"] == "" {
			fields["terminal_reason"] = string(event.Terminal.TerminalReason)
		}
		if fields["terminal_provenance"] == "" {
			fields["terminal_provenance"] = string(event.Terminal.TerminalProvenance)
		}
		if fields["output_state"] == "" {
			fields["output_state"] = string(event.Terminal.OutputState)
		}
	}
	return fields
}

type diagnosticRecord struct {
	Event     string            `json:"event"`
	Fields    map[string]string `json:"fields,omitempty"`
	TOffsetMS float64           `json:"t_offset_ms"`
	TUnixMS   int64             `json:"t_unix_ms"`
}

type eventRecord struct {
	Sequence           uint64    `json:"sequence,omitempty"`
	Kind               string    `json:"kind"`
	ParticipantID      string    `json:"participant_id"`
	SessionID          string    `json:"session_id,omitempty"`
	CapabilityType     string    `json:"capability_type,omitempty"`
	CapabilitySequence uint64    `json:"capability_sequence,omitempty"`
	BrowserID          string    `json:"browser_id,omitempty"`
	TargetID           string    `json:"target_id,omitempty"`
	Generation         uint64    `json:"generation,omitempty"`
	InvocationID       string    `json:"invocation_id,omitempty"`
	ToolName           string    `json:"tool_name,omitempty"`
	State              string    `json:"state,omitempty"`
	Reason             string    `json:"reason,omitempty"`
	ResponseID         string    `json:"response_id,omitempty"`
	ItemID             string    `json:"item_id,omitempty"`
	ToolCallID         string    `json:"tool_call_id,omitempty"`
	Text               string    `json:"text,omitempty"`
	Timestamp          time.Time `json:"timestamp,omitempty"`
	Error              string    `json:"error,omitempty"`
	Dropped            uint64    `json:"dropped,omitempty"`
	Critical           bool      `json:"critical,omitempty"`
}

// audioFrameRecord preserves transport lineage alongside the raw PCM files.
// TOffsetMS is the recorder's arrival observation; StartSample remains the
// provider/source cursor when one was supplied, so replay tooling can tell
// jitter from captured silence.
type audioFrameRecord struct {
	Kind             string    `json:"kind"`
	ParticipantID    string    `json:"participant_id"`
	Timestamp        time.Time `json:"timestamp,omitempty"`
	TOffsetMS        float64   `json:"t_offset_ms"`
	StreamID         string    `json:"stream_id,omitempty"`
	Epoch            uint64    `json:"epoch,omitempty"`
	Sequence         uint64    `json:"sequence,omitempty"`
	StartSample      uint64    `json:"start_sample,omitempty"`
	SampleCount      int       `json:"sample_count"`
	SampleRate       int       `json:"sample_rate,omitempty"`
	Channels         int       `json:"channels,omitempty"`
	EndOfResponse    bool      `json:"end_of_response,omitempty"`
	StartSampleKnown bool      `json:"start_sample_known,omitempty"`
}

func audioFrameRecordFromFrame(kind, participantID string, frame audio.PCMFrame, at time.Time, offset time.Duration) audioFrameRecord {
	return audioFrameRecord{
		Kind: kind, ParticipantID: participantID, Timestamp: at.UTC(), TOffsetMS: float64(offset) / float64(time.Millisecond),
		StreamID: frame.StreamID, Epoch: frame.Epoch, Sequence: frame.Sequence, StartSample: frame.StartSample,
		SampleCount: len(frame.Samples), SampleRate: frame.Format.SampleRate, Channels: frame.Format.Channels,
		EndOfResponse: frame.EndOfResponse,
	}
}
