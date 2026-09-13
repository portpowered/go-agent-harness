package agentruntime

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	runtimeDiagnostics "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomaudiodiagnostics"
	runtimeDiagnosticsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomaudiodiagnostics/wire"
)

const (
	SessionDiagnosticEventRoomAudioIngress        = runtimeDiagnostics.EventRoomAudioIngress
	SessionDiagnosticEventRoomAudioIngressSummary = runtimeDiagnostics.EventRoomAudioIngressSummary
	SessionDiagnosticFieldRoomID                  = runtimeDiagnostics.FieldRoomID
	SessionDiagnosticFieldParticipantID           = runtimeDiagnostics.FieldParticipantID
	SessionDiagnosticFieldSourcePeer              = runtimeDiagnostics.FieldSourcePeer
	SessionDiagnosticFieldSourcePeerID            = runtimeDiagnostics.FieldSourcePeerID
	SessionDiagnosticFieldSourcePeers             = runtimeDiagnostics.FieldSourcePeers
	SessionDiagnosticFieldDisposition             = runtimeDiagnostics.FieldDisposition
	SessionDiagnosticFieldReason                  = runtimeDiagnostics.FieldReason
	SessionDiagnosticFieldByteCount               = runtimeDiagnostics.FieldByteCount
	SessionDiagnosticFieldFrameCount              = runtimeDiagnostics.FieldFrameCount
	SessionDiagnosticFieldCumulativeBytes         = runtimeDiagnostics.FieldCumulativeBytes
	SessionDiagnosticFieldCumulativeFrames        = runtimeDiagnostics.FieldCumulativeFrames
	SessionDiagnosticFieldContentfulBytes         = runtimeDiagnostics.FieldContentfulBytes
	SessionDiagnosticFieldContentfulFrames        = runtimeDiagnostics.FieldContentfulFrames
	SessionDiagnosticFieldAcceptedBytes           = runtimeDiagnostics.FieldAcceptedBytes
	SessionDiagnosticFieldAcceptedFrames          = runtimeDiagnostics.FieldAcceptedFrames
	SessionDiagnosticFieldDeliveredBytes          = runtimeDiagnostics.FieldDeliveredBytes
	SessionDiagnosticFieldDeliveredFrames         = runtimeDiagnostics.FieldDeliveredFrames
	SessionDiagnosticFieldBackpressuredBytes      = runtimeDiagnostics.FieldBackpressuredBytes
	SessionDiagnosticFieldBackpressuredFrames     = runtimeDiagnostics.FieldBackpressuredFrames
	SessionDiagnosticFieldRejectedBytes           = runtimeDiagnostics.FieldRejectedBytes
	SessionDiagnosticFieldRejectedFrames          = runtimeDiagnostics.FieldRejectedFrames
	SessionDiagnosticFieldContentLoss             = runtimeDiagnostics.FieldContentLoss

	roomAudioIngressReasonNoContentfulPeerAudio     = runtimeDiagnostics.ReasonNoContentfulPeerAudio
	roomAudioIngressReasonParticipantTerminated     = runtimeDiagnostics.ReasonParticipantTerminated
	roomAudioIngressReasonParticipantOutputRejected = runtimeDiagnostics.ReasonParticipantOutputRejected
	roomAudioIngressReasonProviderInputRejected     = runtimeDiagnostics.ReasonProviderInputRejected
)

type RoomAudioIngressDisposition = runtimeDiagnostics.RoomAudioIngressDisposition

const (
	RoomAudioIngressDelivered     = runtimeDiagnostics.RoomAudioIngressDelivered
	RoomAudioIngressBackpressured = runtimeDiagnostics.RoomAudioIngressBackpressured
	RoomAudioIngressRejected      = runtimeDiagnostics.RoomAudioIngressRejected
)

type roomAudioDiagnosticSink struct{ sink SessionDiagnosticSink }

func (s roomAudioDiagnosticSink) Record(record runtimeDiagnostics.Record) {
	if s.sink == nil {
		return
	}
	fields := make(map[string]string, len(record.Fields))
	for key, value := range record.Fields {
		fields[key] = value
	}
	s.sink.RecordSessionDiagnostic(SessionDiagnosticRecord{Event: record.Event, Fields: fields})
}

type roomAudioIngressLedger struct{ service runtimeDiagnostics.Service }

func newRoomAudioIngressLedger(participantID string, sink SessionDiagnosticSink) *roomAudioIngressLedger {
	return &roomAudioIngressLedger{service: runtimeDiagnosticsWire.NewService(runtimeDiagnostics.Options{ParticipantID: participantID, RoomID: "room", Sink: roomAudioDiagnosticSink{sink: sink}})}
}

func newRoomParticipantIngress(plan *roomParticipantPlan, opts RoomRunOptions, evidence *roomEvidence) *roomAudioIngressLedger {
	if plan == nil {
		return nil
	}
	sink := combineDiagnosticSinks(roomParticipantDiagnosticSinks(plan, opts, evidenceParticipant(evidence, plan.manifest.ID))...)
	return newRoomAudioIngressLedger(plan.manifest.ID, sink)
}

func notifyRoomParticipantMixerReady(opts RoomRunOptions, participantID string, mixer *room.PCM16Mixer) {
	if opts.onParticipantMixerReady != nil {
		opts.onParticipantMixerReady(participantID, mixer)
	}
}

func (l *roomAudioIngressLedger) admit(sourcePeer string, disposition RoomAudioIngressDisposition, reason string, byteCount int, contentful bool) {
	if l != nil {
		if err := l.service.Admit(sourcePeer, disposition, reason, byteCount, contentful); err != nil && !errors.Is(err, runtimeDiagnostics.ErrFinished) {
			panic(err)
		}
	}
}

func (l *roomAudioIngressLedger) resolveFrame(sourcePeers []string, byteCount int, downstreamReason string) {
	if l != nil {
		l.service.ResolveFrame(sourcePeers, byteCount, downstreamReason)
	}
}

func (l *roomAudioIngressLedger) record(sourcePeer string, disposition RoomAudioIngressDisposition, reason string, byteCount int) {
	if l != nil {
		if err := l.service.Record(sourcePeer, disposition, reason, byteCount); err != nil && !errors.Is(err, runtimeDiagnostics.ErrFinished) {
			panic(err)
		}
	}
}

func (l *roomAudioIngressLedger) finish() {
	if l != nil {
		l.service.Finish()
	}
}

func roomPCMContentful(pcm []byte) bool { return runtimeDiagnostics.PCM16(pcm).Contentful() }

func roomAudioIngressDisposition(writeDisposition room.PCM16WriteDisposition, writeErr error) (RoomAudioIngressDisposition, string) {
	if writeErr != nil {
		return RoomAudioIngressRejected, roomAudioIngressRejectionReason(writeErr)
	}
	if writeDisposition == room.PCM16WriteBackpressured {
		return RoomAudioIngressBackpressured, runtimeDiagnostics.ReasonMixerAdmittedAfterWait
	}
	return RoomAudioIngressDelivered, runtimeDiagnostics.ReasonMixerAdmitted
}

func roomAudioIngressRejectionReason(err error) string {
	switch {
	case errors.Is(err, room.ErrMixerClosed):
		return runtimeDiagnostics.ReasonMixerClosed
	case errors.Is(err, room.ErrMixerInputMissing):
		return runtimeDiagnostics.ReasonMixerInputMissing
	case errors.Is(err, room.ErrMixerInputBufferFull):
		return runtimeDiagnostics.ReasonMixerInputQueueFull
	case errors.Is(err, room.ErrMixerInvalidFormat):
		return runtimeDiagnostics.ReasonInvalidPCM16
	case errors.Is(err, context.Canceled):
		return runtimeDiagnostics.ReasonContextCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return runtimeDiagnostics.ReasonContextDeadlineExceeded
	case errors.Is(err, room.ErrMixerOutputBackpressure):
		return runtimeDiagnostics.ReasonMixerOutputBackpressure
	default:
		return runtimeDiagnostics.ReasonMixerRejected
	}
}

func routeRoomPeerPCM(ctx context.Context, sourceID string, target *roomParticipantRuntime, pcm []byte) error {
	if target == nil || target.mixer == nil {
		return room.ErrMixerClosed
	}
	writeDisposition, writeErr := target.mixer.WriteContextWithDispositionAndObserver(ctx, sourceID, pcm, func(admitted room.PCM16WriteDisposition) {
		if target.ingress != nil {
			disposition, reason := roomAudioIngressDisposition(admitted, nil)
			target.ingress.admit(sourceID, disposition, reason, len(pcm), roomPCMContentful(pcm))
		}
	})
	if roomPCMContentful(pcm) && writeErr != nil && target.ingress != nil {
		disposition, reason := roomAudioIngressDisposition(writeDisposition, writeErr)
		target.ingress.record(sourceID, disposition, reason, len(pcm))
	}
	return writeErr
}

func evidenceParticipant(evidence *roomEvidence, participantID string) *roomParticipantEvidence {
	if evidence == nil {
		return nil
	}
	return evidence.participant(participantID)
}
