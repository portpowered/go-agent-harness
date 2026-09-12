// Package roomaudiodiagnostics owns bounded, provider-neutral accounting for
// audio ranges entering a room participant. The package has no knowledge of
// sessions, devices, credentials, or a particular mixer implementation.
package roomaudiodiagnostics

const (
	EventRoomAudioIngress        = "room_audio_ingress"
	EventRoomAudioIngressSummary = "room_audio_ingress_summary"

	FieldRoomID              = "room_id"
	FieldParticipantID       = "participant_id"
	FieldSourcePeer          = "source_peer"
	FieldSourcePeerID        = "source_peer_id"
	FieldSourcePeers         = "source_peers"
	FieldDisposition         = "disposition"
	FieldReason              = "reason"
	FieldByteCount           = "byte_count"
	FieldFrameCount          = "frame_count"
	FieldCumulativeBytes     = "cumulative_bytes"
	FieldCumulativeFrames    = "cumulative_frames"
	FieldContentfulBytes     = "contentful_bytes"
	FieldContentfulFrames    = "contentful_frames"
	FieldAcceptedBytes       = "accepted_bytes"
	FieldAcceptedFrames      = "accepted_frames"
	FieldDeliveredBytes      = "delivered_bytes"
	FieldDeliveredFrames     = "delivered_frames"
	FieldBackpressuredBytes  = "backpressured_bytes"
	FieldBackpressuredFrames = "backpressured_frames"
	FieldRejectedBytes       = "rejected_bytes"
	FieldRejectedFrames      = "rejected_frames"
	FieldContentLoss         = "content_loss"
)

const (
	// RoomAudioIngressDisposition is the stable result of one admitted range.
	// Backpressured is accepted audio that waited for bounded capacity.
	RoomAudioIngressDelivered     RoomAudioIngressDisposition = "delivered"
	RoomAudioIngressBackpressured RoomAudioIngressDisposition = "backpressured"
	RoomAudioIngressRejected      RoomAudioIngressDisposition = "rejected"

	Delivered     = RoomAudioIngressDelivered
	Backpressured = RoomAudioIngressBackpressured
	Rejected      = RoomAudioIngressRejected

	ReasonNoContentfulPeerAudio          = "no_contentful_peer_audio"
	ReasonParticipantTerminated          = "participant_terminated"
	ReasonParticipantOutputRejected      = "participant_output_rejected"
	ReasonProviderInputRejected          = "provider_input_rejected"
	ReasonMixerAdmitted                  = "mixer_admitted"
	ReasonMixerAdmittedAfterWait         = "mixer_admitted_after_backpressure"
	ReasonMixerAdmittedAfterBackpressure = ReasonMixerAdmittedAfterWait
	ReasonMixerClosed                    = "mixer_closed"
	ReasonMixerInputMissing              = "mixer_input_missing"
	ReasonMixerInputQueueFull            = "mixer_input_queue_full"
	ReasonInvalidPCM16                   = "invalid_pcm16"
	ReasonContextCanceled                = "context_canceled"
	ReasonContextDeadlineExceeded        = "context_deadline_exceeded"
	ReasonMixerOutputBackpressure        = "mixer_output_backpressure"
	ReasonMixerRejected                  = "mixer_rejected"
	ReasonInvalidDisposition             = "invalid_disposition"

	MixedSource    = "room-mix"
	NoPeerSource   = "none"
	MaxFirstEvents = 32
)

// SessionDiagnosticEventRoomAudioIngress and its field aliases retain the
// names used by existing CLI diagnostics consumers.
const (
	SessionDiagnosticEventRoomAudioIngress        = EventRoomAudioIngress
	SessionDiagnosticEventRoomAudioIngressSummary = EventRoomAudioIngressSummary

	SessionDiagnosticFieldRoomID              = FieldRoomID
	SessionDiagnosticFieldParticipantID       = FieldParticipantID
	SessionDiagnosticFieldSourcePeer          = FieldSourcePeer
	SessionDiagnosticFieldSourcePeerID        = FieldSourcePeerID
	SessionDiagnosticFieldSourcePeers         = FieldSourcePeers
	SessionDiagnosticFieldDisposition         = FieldDisposition
	SessionDiagnosticFieldReason              = FieldReason
	SessionDiagnosticFieldByteCount           = FieldByteCount
	SessionDiagnosticFieldFrameCount          = FieldFrameCount
	SessionDiagnosticFieldCumulativeBytes     = FieldCumulativeBytes
	SessionDiagnosticFieldCumulativeFrames    = FieldCumulativeFrames
	SessionDiagnosticFieldContentfulBytes     = FieldContentfulBytes
	SessionDiagnosticFieldContentfulFrames    = FieldContentfulFrames
	SessionDiagnosticFieldAcceptedBytes       = FieldAcceptedBytes
	SessionDiagnosticFieldAcceptedFrames      = FieldAcceptedFrames
	SessionDiagnosticFieldDeliveredBytes      = FieldDeliveredBytes
	SessionDiagnosticFieldDeliveredFrames     = FieldDeliveredFrames
	SessionDiagnosticFieldBackpressuredBytes  = FieldBackpressuredBytes
	SessionDiagnosticFieldBackpressuredFrames = FieldBackpressuredFrames
	SessionDiagnosticFieldRejectedBytes       = FieldRejectedBytes
	SessionDiagnosticFieldRejectedFrames      = FieldRejectedFrames
	SessionDiagnosticFieldContentLoss         = FieldContentLoss
)

// RoomAudioIngressDisposition is intentionally also available as Disposition
// for hosts that use the shorter contract spelling.
type RoomAudioIngressDisposition string

type Disposition = RoomAudioIngressDisposition

// Record is one bounded, credential-free diagnostic observation. Fields are
// newly allocated by the service and are not caller-owned mutable state.
type Record struct {
	Event  string
	Fields map[string]string
}

// Sink receives observations after the service releases its state lock. A
// sink must return promptly and may safely call back into the service.
type Sink interface {
	Record(Record)
}

// SinkFunc adapts a function to Sink.
type SinkFunc func(Record)

func (f SinkFunc) Record(record Record) {
	if f != nil {
		f(record)
	}
}

// PCM16 is a PCM byte range awaiting ingress attribution.
type PCM16 []byte

// Contentful reports whether a PCM byte range contains any non-zero sample
// byte. It deliberately runs before a mixer frame is available.
func (pcm PCM16) Contentful() bool {
	for _, value := range pcm {
		if value != 0 {
			return true
		}
	}
	return false
}

// Options identifies one independent participant ledger. A zero RoomID uses
// the room runtime's historical "room" identifier.
type Options struct {
	ParticipantID string
	RoomID        string
	Sink          Sink
}

// Service owns one participant's FIFO attribution and cumulative accounting.
// Every method is safe for concurrent use. Construction is provided by the
// dedicated roomaudiodiagnostics/wire package.
type Service interface {
	Admit(sourcePeer string, disposition Disposition, reason string, byteCount int, contentful bool) error
	ResolveFrame(sourcePeers []string, byteCount int, downstreamReason string)
	RejectPending(reason string)
	Record(sourcePeer string, disposition Disposition, reason string, byteCount int) error
	Finish()
}
