package rtc

import core "github.com/portpowered/go-agent-harness/go-audio/pkg/rtctransport"

// Deprecated: use go-agent-runtime/services/rtctransport.OutboundRTPClockRate.
const OutboundRTPClockRate = core.OutboundRTPClockRate

// Deprecated: use go-agent-runtime/services/rtctransport.OutboundTrack.
type OutboundTrack = core.OutboundTrack

// Deprecated: use go-agent-runtime/services/rtctransport.OutboundTrackConfig.
type OutboundTrackConfig = core.OutboundTrackConfig

// Deprecated: use go-agent-runtime/services/rtctransport.OutboundOperationError.
type OutboundOperationError = core.OutboundOperationError

// Deprecated: use go-agent-runtime/services/rtctransport.OpusEncoder.
type OpusEncoder = core.OpusEncoder

// Deprecated: use go-agent-runtime/services/rtctransport.OpusEncoderFunc.
type OpusEncoderFunc = core.OpusEncoderFunc

// Deprecated: use go-agent-runtime/services/rtctransport.RTPWriter.
type RTPWriter = core.RTPWriter

// Deprecated: use go-agent-runtime/services/rtctransport.RTPWriterFunc.
type RTPWriterFunc = core.RTPWriterFunc

// Deprecated: use go-agent-runtime/services/rtctransport.Pacer.
type Pacer = core.Pacer

// Deprecated: use go-agent-runtime/services/rtctransport.PacerFunc.
type PacerFunc = core.PacerFunc

const (
	// Deprecated: use go-agent-runtime/services/rtctransport.ErrInvalidOutboundTrackConfig.
	ErrInvalidOutboundTrackConfig = core.ErrInvalidOutboundTrackConfig
	// Deprecated: use go-agent-runtime/services/rtctransport.ErrOutboundClosed.
	ErrOutboundClosed = core.ErrOutboundClosed
	// Deprecated: use go-agent-runtime/services/rtctransport.ErrOutboundEmptyFrame.
	ErrOutboundEmptyFrame = core.ErrOutboundEmptyFrame
	// Deprecated: use go-agent-runtime/services/rtctransport.ErrOutboundFrameSize.
	ErrOutboundFrameSize = core.ErrOutboundFrameSize
	// Deprecated: use go-agent-runtime/services/rtctransport.ErrOutboundNilEncoder.
	ErrOutboundNilEncoder = core.ErrOutboundNilEncoder
	// Deprecated: use go-agent-runtime/services/rtctransport.ErrOutboundNilWriter.
	ErrOutboundNilWriter = core.ErrOutboundNilWriter
	// Deprecated: use go-agent-runtime/services/rtctransport.ErrOutboundNilPacer.
	ErrOutboundNilPacer = core.ErrOutboundNilPacer
	// Deprecated: use go-agent-runtime/services/rtctransport.ErrOutboundEmptyPayload.
	ErrOutboundEmptyPayload = core.ErrOutboundEmptyPayload
	// Deprecated: use go-agent-runtime/services/rtctransport.ErrOutboundFrameTooLarge.
	ErrOutboundFrameTooLarge = core.ErrOutboundFrameTooLarge
	// Deprecated: use go-agent-runtime/services/rtctransport.ErrOutboundQueueOverflow.
	ErrOutboundQueueOverflow = core.ErrOutboundQueueOverflow
)

// Deprecated: use go-agent-runtime/services/rtctransport.NewOutboundTrack.
func NewOutboundTrack(config OutboundTrackConfig) (*OutboundTrack, error) {
	return core.NewOutboundTrack(config)
}
