// Package rtctransport exposes the runtime-facing RTC transport contract.
//
// The implementation is owned by go-audio/pkg/rtctransport. This package keeps
// the runtime service boundary and its stable source-compatible identities.
package rtctransport

import (
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	core "github.com/portpowered/go-agent-harness/go-audio/pkg/rtctransport"
)

const (
	// CodecSampleRate is the RTP/Opus clock used by every track.
	CodecSampleRate = core.CodecSampleRate
	// OutboundRTPClockRate is retained as the explicit outbound name used by
	// callers that build negotiated WebRTC codec capabilities.
	OutboundRTPClockRate = core.OutboundRTPClockRate

	DefaultInboundLoopSampleRate = core.DefaultInboundLoopSampleRate
	DefaultInboundFrameDuration  = core.DefaultInboundFrameDuration
	DefaultInboundJitterDepth    = core.DefaultInboundJitterDepth
)

// Error is an immutable transport error identity. Constants keep the public
// errors.Is targets stable without exposing mutable package variables.
type Error = core.Error

const (
	ErrInvalidInboundTrackConfig Error = core.ErrInvalidInboundTrackConfig
	ErrNilInboundRTPTrack        Error = core.ErrNilInboundRTPTrack
	ErrNilOpusDecoder            Error = core.ErrNilOpusDecoder
	ErrUnsupportedOpusDecoder    Error = core.ErrUnsupportedOpusDecoder
	ErrInvalidInboundRTPPacket   Error = core.ErrInvalidInboundRTPPacket
	ErrImpossibleRTPProgress     Error = core.ErrImpossibleRTPProgress
	ErrInboundTrackSource        Error = core.ErrInboundTrackSource
	ErrInboundTrackDecode        Error = core.ErrInboundTrackDecode
	ErrInboundTrackResample      Error = core.ErrInboundTrackResample
	ErrInboundTrackFrame         Error = core.ErrInboundTrackFrame
	ErrInboundTrackQueueOverflow Error = core.ErrInboundTrackQueueOverflow
	ErrInboundTrackClosed        Error = core.ErrInboundTrackClosed

	ErrInvalidOutboundTrackConfig Error = core.ErrInvalidOutboundTrackConfig
	ErrOutboundClosed             Error = core.ErrOutboundClosed
	ErrOutboundEmptyFrame         Error = core.ErrOutboundEmptyFrame
	ErrOutboundFrameSize          Error = core.ErrOutboundFrameSize
	ErrOutboundNilEncoder         Error = core.ErrOutboundNilEncoder
	ErrOutboundNilWriter          Error = core.ErrOutboundNilWriter
	ErrOutboundNilPacer           Error = core.ErrOutboundNilPacer
	ErrOutboundEmptyPayload       Error = core.ErrOutboundEmptyPayload
	ErrOutboundFrameTooLarge      Error = core.ErrOutboundFrameTooLarge
	ErrOutboundQueueOverflow      Error = core.ErrOutboundQueueOverflow
)

// InboundTrackError adds a stable operation and error kind while preserving
// the underlying cause for errors.Is/errors.As callers.
type InboundTrackError = core.InboundTrackError

// OutboundOperationError preserves the error returned by a resampler, codec,
// pacer, RTP writer, or encoder closer while adding the failed operation.
type OutboundOperationError = core.OutboundOperationError

// OpusDecoder is the narrow decoder seam required by inbound RTP policy.
type OpusDecoder = core.OpusDecoder

// RTPPacketSource is the narrow Pion-facing input seam.
type RTPPacketSource = core.RTPPacketSource

// InboundTrackConfig selects output PCM framing and bounded reordering.
type InboundTrackConfig = core.InboundTrackConfig

// InboundTrack is the runtime-facing caller-owned PCM receiver.
type InboundTrack interface {
	sharedaudio.InboundMedia
}

// OpusEncoder encodes one source PCM frame into one RTP Opus payload.
type OpusEncoder = core.OpusEncoder

// OpusEncoderFunc adapts a function to OpusEncoder.
type OpusEncoderFunc = core.OpusEncoderFunc

// RTPWriter writes one already packetized RTP Opus packet.
type RTPWriter = core.RTPWriter

// RTPWriterFunc adapts a function to RTPWriter.
type RTPWriterFunc = core.RTPWriterFunc

// Pacer schedules packets at a media-clock offset measured in 48 kHz samples.
type Pacer = core.Pacer

// PacerFunc adapts a function to Pacer.
type PacerFunc = core.PacerFunc

// OutboundTrackConfig configures one caller-owned PCM-to-RTP track.
type OutboundTrackConfig = core.OutboundTrackConfig

// OutboundTrack is the runtime-facing caller-owned PCM sender.
type OutboundTrack interface {
	sharedaudio.OutboundMedia
}

// Service is the reusable transport policy boundary. Construction is inert;
// each returned track owns only its injected codec/source lifecycle.
type Service interface {
	NewInboundTrack(source, opus any, config InboundTrackConfig) (InboundTrack, error)
	NewOutboundTrack(config OutboundTrackConfig) (OutboundTrack, error)
}
