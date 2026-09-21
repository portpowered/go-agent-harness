package service

import (
	"fmt"
	"time"

	roomevidence "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
)

func (r *recorder) Observe(observation roomevidence.Observation) error {
	switch observation.Kind {
	case roomevidence.ObservationTimeline:
		return r.recordObservedTimeline(observation.At, observation.Event, observation.ParticipantID, observation.Fields)
	case roomevidence.ObservationFinalTimeline:
		return r.recordObservedTimeline(observation.At, observation.Event, observation.ParticipantID, observation.Fields)
	case roomevidence.ObservationLiveEvent:
		return r.RecordLiveEvent(observation.ParticipantID, observation.LiveEvent)
	case roomevidence.ObservationProviderError:
		return r.RecordProviderErrorTimeline(observation.ParticipantID, observation.Fields)
	case roomevidence.ObservationParticipantReady:
		return r.SetParticipantReady(observation.ParticipantReady)
	case roomevidence.ObservationParticipantTerminated:
		return r.SetParticipantTerminated(observation.ParticipantResult)
	case roomevidence.ObservationSourceAudio:
		r.RecordSource(observation.ParticipantID, observation.AudioFrame)
		return r.Error()
	case roomevidence.ObservationReceivedAudio:
		r.RecordReceived(observation.ParticipantID, observation.AudioFrame)
		return r.Error()
	case roomevidence.ObservationSpeakerAudio:
		r.ObserveSpeakerAudio(observation.ParticipantID, observation.TargetIDs, observation.PCM)
		return r.Error()
	case roomevidence.ObservationSpeechStopped:
		r.ObserveSpeechStopped(observation.ParticipantID)
		return r.Error()
	case roomevidence.ObservationProviderAudio:
		r.ObserveProviderAudio(observation.ParticipantID, observation.RelatedID)
		return r.Error()
	case roomevidence.ObservationPeerAudio:
		r.ObservePeerAudio(observation.ParticipantID, observation.RelatedID, observation.PCM)
		return r.Error()
	case roomevidence.ObservationError:
		r.MarkError(observation.ParticipantID, observation.Artifact, observation.Err)
		return r.Error()
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
		participant, err := r.participantRecorder(observation.ParticipantID)
		if err != nil {
			return err
		}
		return participant.RecordDiagnostic(diagnostic)
	case roomevidence.ObservationDelta:
		participant, err := r.participantRecorder(observation.ParticipantID)
		if err != nil {
			return err
		}
		return participant.ObserveDelta(observation.StreamMessage)
	case roomevidence.ObservationParticipantAudio:
		participant, err := r.participantRecorder(observation.ParticipantID)
		if err != nil {
			return err
		}
		return participant.ObserveAudio(observation.PCM)
	case roomevidence.ObservationSentAudio:
		participant, err := r.participantRecorder(observation.ParticipantID)
		if err != nil {
			return err
		}
		return participant.ObserveSentAudio(observation.PCM)
	case roomevidence.ObservationSentStream:
		participant, err := r.participantRecorder(observation.ParticipantID)
		if err != nil {
			return err
		}
		return participant.ObserveSentStream(observation.PCM)
	case roomevidence.ObservationCloseSentSpeechSegment:
		participant, err := r.participantRecorder(observation.ParticipantID)
		if err != nil {
			return err
		}
		return participant.CloseSentSpeechSegment()
	case roomevidence.ObservationReceivedParticipantAudio:
		participant, err := r.participantRecorder(observation.ParticipantID)
		if err != nil {
			return err
		}
		return participant.ObserveReceivedAudio(observation.PCM)
	case roomevidence.ObservationAudioDropped:
		participant, err := r.participantRecorder(observation.ParticipantID)
		if err != nil {
			return err
		}
		return participant.RecordAudioDropped(observation.Artifact, observation.DroppedSamples)
	case roomevidence.ObservationParticipantError:
		participant, err := r.participantRecorder(observation.ParticipantID)
		if err != nil {
			return err
		}
		return participant.MarkError(observation.Artifact, observation.Err)
	default:
		return fmt.Errorf("unknown room evidence observation kind %q", observation.Kind)
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

func (r *recorder) participantRecorder(id string) (roomevidence.ParticipantRecorder, error) {
	participant := r.Participant(id)
	if participant != nil {
		return participant, nil
	}
	return nil, fmt.Errorf("%w: %q", roomevidence.ErrParticipantUnknown, id)
}
