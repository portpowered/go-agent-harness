package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

func (p *participantRecorder) ID() string {
	if p == nil {
		return ""
	}
	return p.id
}

func (p *participantRecorder) Artifacts() roomevidence.ArtifactPaths {
	if p == nil {
		return roomevidence.ArtifactPaths{}
	}
	return p.artifacts
}

func (p *participantRecorder) RecordDiagnostic(record roomevidence.DiagnosticRecord) error {
	if p == nil || p.owner == nil {
		return roomevidence.ErrRecorderClosed
	}
	p.owner.operationMu.Lock()
	defer p.owner.operationMu.Unlock()
	return p.recordDiagnostic(record)
}

func (p *participantRecorder) recordDiagnostic(record roomevidence.DiagnosticRecord) error {
	if err := p.openCheck(); err != nil {
		return err
	}
	at := record.At
	if at.IsZero() {
		at = p.owner.clock.source.Now().UTC()
	}
	data, err := json.Marshal(diagnosticRecord{Event: record.Event, Fields: cloneFields(record.Fields)})
	if err != nil {
		return p.MarkError(p.artifacts.Diagnostics, err)
	}
	data = redactJSON(data, p.owner.secrets)
	data, err = stampJSONAt(data, p.owner.clock, at)
	if err != nil {
		return p.MarkError(p.artifacts.Diagnostics, err)
	}
	if err := p.diagnostics.writeRaw(data); err != nil {
		return p.MarkError(p.artifacts.Diagnostics, err)
	}
	return nil
}

func (p *participantRecorder) ObserveDelta(message messages.StreamMessage) error {
	return p.observeDeltaAt(message, time.Time{})
}

func (p *participantRecorder) observeDeltaAt(message messages.StreamMessage, at time.Time) error {
	if p == nil || p.owner == nil {
		return roomevidence.ErrRecorderClosed
	}
	p.owner.operationMu.Lock()
	defer p.owner.operationMu.Unlock()
	return p.recordDelta(message, at)
}

func (p *participantRecorder) recordDelta(message messages.StreamMessage, at time.Time) error {
	if err := p.openCheck(); err != nil {
		return err
	}
	data, err := marshalStreamMessage(message)
	if err != nil {
		return p.MarkError(p.artifacts.Deltas, err)
	}
	data = redactJSON(data, p.owner.secrets)
	if at.IsZero() {
		data, err = stampJSON(data, p.owner.clock)
	} else {
		data, err = stampJSONAt(data, p.owner.clock, at)
	}
	if err != nil {
		return p.MarkError(p.artifacts.Deltas, err)
	}
	deltaErr := p.MarkError(p.artifacts.Deltas, p.deltas.writeRaw(data))
	eventErr := p.MarkError(p.artifacts.Events, p.events.writeRaw(data))
	return errors.Join(deltaErr, eventErr)
}

func (p *participantRecorder) ObserveSentAudio(pcm []byte) error {
	if p == nil || p.owner == nil {
		return roomevidence.ErrRecorderClosed
	}
	p.owner.operationMu.Lock()
	defer p.owner.operationMu.Unlock()
	return errors.Join(p.observeAudio(pcm), p.observeSentStream(pcm))
}

func (p *participantRecorder) ObserveSentStream(pcm []byte) error {
	if p == nil || p.owner == nil {
		return roomevidence.ErrRecorderClosed
	}
	p.owner.operationMu.Lock()
	defer p.owner.operationMu.Unlock()
	return p.observeSentStream(pcm)
}

func (p *participantRecorder) observeSentStream(pcm []byte) error {
	if err := p.openCheck(); err != nil {
		return err
	}
	samples, err := audioSamples(pcm)
	if err != nil {
		return p.MarkError(p.artifacts.SentPCM, err)
	}
	writeErr := p.MarkError(p.artifacts.SentPCM, p.sentPCM.write(pcm))
	offset, _ := p.owner.clock.now()
	mixErr := p.owner.mix.Add(offset, samples)
	if mixErr != nil {
		p.owner.recordError("", roomevidence.MixPath, mixErr)
	}
	if event := p.sentSpeech.transition(audio.PCM16HasSignal(pcm)); event != "" {
		if _, err := p.owner.recordOpenTimeline("speech_"+event, p.id, nil); err != nil && !errors.Is(err, roomevidence.ErrFinalized) {
			p.owner.recordError("", roomevidence.TimelinePath, err)
		}
	}
	return errors.Join(writeErr, mixErr)
}

func (p *participantRecorder) CloseSentSpeechSegment() error {
	if p == nil || p.sentSpeech == nil {
		return nil
	}
	if p.owner == nil {
		return roomevidence.ErrRecorderClosed
	}
	p.owner.operationMu.Lock()
	defer p.owner.operationMu.Unlock()
	return p.closeSentSpeechSegment()
}

func (p *participantRecorder) closeSentSpeechSegment() error {
	if event := p.sentSpeech.transition(false); event != "" && p.owner != nil {
		_, err := p.owner.recordOpenTimeline("speech_"+event, p.id, nil)
		return err
	}
	return nil
}

func (p *participantRecorder) ObserveReceivedAudio(pcm []byte) error {
	if p == nil || p.owner == nil {
		return roomevidence.ErrRecorderClosed
	}
	p.owner.operationMu.Lock()
	defer p.owner.operationMu.Unlock()
	return p.observeReceivedAudio(pcm)
}

func (p *participantRecorder) observeReceivedAudio(pcm []byte) error {
	if err := p.openCheck(); err != nil {
		return err
	}
	if _, err := audioSamples(pcm); err != nil {
		return p.MarkError(p.artifacts.ReceivedPCM, err)
	}
	writeErr := p.MarkError(p.artifacts.ReceivedPCM, p.receivedPCM.write(pcm))
	if event := p.receivedSpeech.transition(audio.PCM16HasSignal(pcm)); event != "" {
		_, timelineErr := p.owner.recordOpenTimeline("received_speech_"+event, p.id, nil)
		return errors.Join(writeErr, timelineErr)
	}
	return writeErr
}

func (p *participantRecorder) openCheck() error {
	if p == nil || p.owner == nil {
		return roomevidence.ErrRecorderClosed
	}
	return p.owner.checkOpen()
}

func (r *recorder) Observe(observation roomevidence.Observation) error {
	switch observation.Kind {
	case roomevidence.ObservationTimeline, roomevidence.ObservationFinalTimeline:
		return r.recordObservedTimeline(observation.At, observation.Event, observation.ParticipantID, observation.Fields)
	case roomevidence.ObservationStreamMessage:
		return r.recordStreamMessageTimeline(observation.ParticipantID, observation.StreamMessage)
	case roomevidence.ObservationLiveEvent:
		if err := r.RecordLiveEvent(observation.ParticipantID, observation.LiveEvent); err != nil {
			return err
		}
		message := observation.LiveEvent.Message
		if message == nil {
			return nil
		}
		participantID := liveEventParticipantID(observation.ParticipantID, observation.LiveEvent)
		if message.Type == messages.StreamTypeAudioDelta {
			if audio, ok := message.Value.(*messages.AudioDeltaValue); ok {
				if err := r.observeParticipant(roomevidence.Observation{Kind: roomevidence.ObservationParticipantAudio, ParticipantID: participantID, PCM: audio.Content}); err != nil {
					return err
				}
			}
		}
		return r.observeParticipant(roomevidence.Observation{
			Kind:          roomevidence.ObservationDelta,
			ParticipantID: participantID,
			StreamMessage: *message,
			At:            observation.LiveEvent.Timestamp,
		})
	case roomevidence.ObservationProviderError:
		return r.RecordProviderErrorTimeline(observation.ParticipantID, observation.Fields)
	case roomevidence.ObservationParticipantReady:
		return r.SetParticipantReady(observation.ParticipantReady)
	case roomevidence.ObservationParticipantTerminated:
		return r.SetParticipantTerminated(observation.ParticipantResult)
	case roomevidence.ObservationSourceAudio,
		roomevidence.ObservationReceivedAudio,
		roomevidence.ObservationSpeakerAudio,
		roomevidence.ObservationSpeechStopped,
		roomevidence.ObservationProviderAudio,
		roomevidence.ObservationPeerAudio:
		return r.observeRoomAudio(observation)
	case roomevidence.ObservationError:
		r.MarkError(observation.ParticipantID, observation.Artifact, observation.Err)
		return r.Error()
	case roomevidence.ObservationDiagnostic,
		roomevidence.ObservationDelta,
		roomevidence.ObservationParticipantAudio,
		roomevidence.ObservationSentAudio,
		roomevidence.ObservationSentStream,
		roomevidence.ObservationCloseSentSpeechSegment,
		roomevidence.ObservationReceivedParticipantAudio,
		roomevidence.ObservationAudioDropped,
		roomevidence.ObservationParticipantError:
		return r.observeParticipant(observation)
	default:
		return fmt.Errorf("unknown room evidence observation kind %q", observation.Kind)
	}
}

func (r *recorder) RecordSessionDiagnostic(record roomevidence.DiagnosticRecord) {
	if err := r.Observe(roomevidence.Observation{
		Kind:          roomevidence.ObservationDiagnostic,
		ParticipantID: record.ParticipantID,
		Diagnostic:    record,
	}); err != nil {
		r.MarkError(record.ParticipantID, "", err)
	}
}

func (r *recorder) observeRoomAudio(observation roomevidence.Observation) error {
	//nolint:exhaustive // this handler receives only room-level audio observation kinds.
	switch observation.Kind {
	case roomevidence.ObservationSourceAudio:
		r.RecordSource(observation.ParticipantID, observation.AudioFrame)
	case roomevidence.ObservationReceivedAudio:
		r.RecordReceived(observation.ParticipantID, observation.AudioFrame)
	case roomevidence.ObservationSpeakerAudio:
		if observation.AudioFrame.Samples != nil {
			r.ObserveSpeakerAudio(observation.ParticipantID, observation.TargetIDs, observation.AudioFrame)
		} else if r.latency != nil {
			r.operationMu.Lock()
			defer r.operationMu.Unlock()
			if r.checkOpen() == nil {
				r.latency.ObserveSpeakerBytes(observation.ParticipantID, observation.TargetIDs, len(observation.PCM))
			}
		}
	case roomevidence.ObservationSpeechStopped:
		r.ObserveSpeechStopped(observation.ParticipantID)
	case roomevidence.ObservationProviderAudio:
		r.ObserveProviderAudio(observation.ParticipantID, observation.RelatedID)
	case roomevidence.ObservationPeerAudio:
		if observation.AudioFrame.Samples != nil {
			r.ObservePeerAudio(observation.ParticipantID, observation.RelatedID, observation.AudioFrame)
		} else if r.latency != nil {
			r.operationMu.Lock()
			defer r.operationMu.Unlock()
			if r.checkOpen() == nil {
				r.latency.ObservePeerBytes(observation.ParticipantID, observation.RelatedID, len(observation.PCM))
			}
		}
	default:
		return fmt.Errorf("unknown room audio observation kind %q", observation.Kind)
	}
	return r.Error()
}

func (r *recorder) recordStreamMessageTimeline(participantID string, msg messages.StreamMessage) error {
	//nolint:exhaustive // only response and tool lifecycle messages project to room timeline events.
	switch msg.Type {
	case messages.StreamTypeMessageStart:
		return r.RecordTimeline("response_start", participantID, map[string]string{"response_id": msg.ResponseID})
	case messages.StreamTypeMessageEnd:
		fields := map[string]string{"response_id": msg.ResponseID}
		if value, ok := msg.Value.(*messages.MessageEndValue); ok && value != nil {
			fields["terminal_reason"] = string(value.TerminalReason)
			fields["terminal_provenance"] = string(value.TerminalProvenance)
			fields["output_state"] = string(value.OutputState)
			if err := r.RecordTimeline("response_end", participantID, fields); err != nil {
				return err
			}
			if value.TerminalReason == messages.TerminalReasonCancellation {
				return r.RecordTimeline("barge_in_cancel_acked", participantID, map[string]string{"response_id": msg.ResponseID})
			}
			return nil
		}
		return r.RecordTimeline("response_end", participantID, fields)
	case messages.StreamTypeError:
		value, ok := msg.Value.(*messages.ErrorValue)
		if !ok || value == nil {
			return nil
		}
		fields := map[string]string{"code": value.Code, "classification": value.Classification}
		if value.Classification == providers.ErrorClassResponseCancelNotActive {
			return r.RecordTimeline("barge_in_cancel_failed", participantID, fields)
		}
		return r.RecordProviderErrorTimeline(participantID, fields)
	case messages.StreamTypeToolCallStart:
		return r.RecordTimeline("tool_call_start", participantID, map[string]string{"tool_call_id": msg.ToolCallId})
	case messages.StreamTypeToolCallEnd:
		return r.RecordTimeline("tool_call_end", participantID, map[string]string{"tool_call_id": msg.ToolCallId})
	default:
		return nil
	}
}

func (r *recorder) observeParticipant(observation roomevidence.Observation) error {
	participant, err := r.participantRecorder(observation.ParticipantID)
	if err != nil {
		return err
	}
	//nolint:exhaustive // this handler receives only participant-scoped observation kinds.
	switch observation.Kind {
	case roomevidence.ObservationDiagnostic:
		diagnostic := observation.Diagnostic
		if diagnostic.Event == "" {
			diagnostic.Event = observation.Event
		}
		if diagnostic.Fields == nil {
			diagnostic.Fields = observation.Fields
		}
		if diagnostic.At.IsZero() {
			diagnostic.At = observation.At
		}
		return participant.RecordDiagnostic(diagnostic)
	case roomevidence.ObservationDelta:
		return participant.observeDeltaAt(observation.StreamMessage, observation.At)
	case roomevidence.ObservationParticipantAudio:
		return participant.ObserveAudio(observation.PCM)
	case roomevidence.ObservationSentAudio:
		return participant.ObserveSentAudio(observation.PCM)
	case roomevidence.ObservationSentStream:
		return participant.ObserveSentStream(observation.PCM)
	case roomevidence.ObservationCloseSentSpeechSegment:
		return participant.CloseSentSpeechSegment()
	case roomevidence.ObservationReceivedParticipantAudio:
		return participant.ObserveReceivedAudio(observation.PCM)
	case roomevidence.ObservationAudioDropped:
		return participant.RecordAudioDropped(observation.Artifact, observation.DroppedBytes)
	case roomevidence.ObservationParticipantError:
		return participant.MarkError(observation.Artifact, observation.Err)
	default:
		return fmt.Errorf("unknown participant evidence observation kind %q", observation.Kind)
	}
}

func (r *recorder) recordObservedTimeline(at time.Time, event, participant string, fields map[string]string) error {
	if r == nil {
		return roomevidence.ErrRecorderClosed
	}
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	if err := r.checkOpen(); err != nil {
		return err
	}
	if at.IsZero() {
		at = r.clock.source.Now().UTC()
	}
	return r.writeTimelineAt(at.UTC(), event, participant, fields)
}

func (r *recorder) participantRecorder(id string) (*participantRecorder, error) {
	participant := r.participant(id)
	if participant != nil {
		return participant, nil
	}
	return nil, fmt.Errorf("%w: %q", roomevidence.ErrParticipantUnknown, id)
}
