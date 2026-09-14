package rtc

import (
	core "github.com/portpowered/go-agent-harness/go-audio/pkg/rtctransport"
)

// Deprecated: use go-agent-runtime/services/rtctransport for runtime-owned
// inbound RTP policy. This alias remains for legacy gateway consumers.
type InboundTrack = core.InboundTrack

// Deprecated: use go-agent-runtime/services/rtctransport.InboundTrackConfig.
type InboundTrackConfig = core.InboundTrackConfig

// Deprecated: use go-agent-runtime/services/rtctransport.DefaultInboundTrackConfig.
func DefaultInboundTrackConfig() InboundTrackConfig {
	return core.DefaultInboundTrackConfig()
}

// Deprecated: use go-agent-runtime/services/rtctransport.Error.
type Error = core.Error

const (
	// Deprecated: use go-agent-runtime/services/rtctransport.ErrInvalidInboundTrackConfig.
	ErrInvalidInboundTrackConfig = core.ErrInvalidInboundTrackConfig
	// Deprecated: use go-agent-runtime/services/rtctransport.ErrNilInboundRTPTrack.
	ErrNilInboundRTPTrack = core.ErrNilInboundRTPTrack
	// Deprecated: use go-agent-runtime/services/rtctransport.ErrNilOpusDecoder.
	ErrNilOpusDecoder = core.ErrNilOpusDecoder
	// Deprecated: use go-agent-runtime/services/rtctransport.ErrUnsupportedOpusDecoder.
	ErrUnsupportedOpusDecoder = core.ErrUnsupportedOpusDecoder
	// Deprecated: use go-agent-runtime/services/rtctransport.ErrInvalidInboundRTPPacket.
	ErrInvalidInboundRTPPacket = core.ErrInvalidInboundRTPPacket
	// Deprecated: use go-agent-runtime/services/rtctransport.ErrImpossibleRTPProgress.
	ErrImpossibleRTPProgress = core.ErrImpossibleRTPProgress
	// Deprecated: use go-agent-runtime/services/rtctransport.ErrInboundTrackSource.
	ErrInboundTrackSource = core.ErrInboundTrackSource
	// Deprecated: use go-agent-runtime/services/rtctransport.ErrInboundTrackDecode.
	ErrInboundTrackDecode = core.ErrInboundTrackDecode
	// Deprecated: use go-agent-runtime/services/rtctransport.ErrInboundTrackResample.
	ErrInboundTrackResample = core.ErrInboundTrackResample
	// Deprecated: use go-agent-runtime/services/rtctransport.ErrInboundTrackFrame.
	ErrInboundTrackFrame = core.ErrInboundTrackFrame
	// Deprecated: use go-agent-runtime/services/rtctransport.ErrInboundTrackQueueOverflow.
	ErrInboundTrackQueueOverflow = core.ErrInboundTrackQueueOverflow
	// Deprecated: use go-agent-runtime/services/rtctransport.ErrInboundTrackClosed.
	ErrInboundTrackClosed = core.ErrInboundTrackClosed
)

// Deprecated: use go-agent-runtime/services/rtctransport.InboundTrackError.
type InboundTrackError = core.InboundTrackError

// Deprecated: use go-agent-runtime/services/rtctransport.OpusDecoder.
type OpusDecoder = core.OpusDecoder

// Deprecated: use go-agent-runtime/services/rtctransport.RTPPacketSource.
type RTPPacketSource = core.RTPPacketSource

const (
	// Deprecated: use go-agent-runtime/services/rtctransport.CodecSampleRate.
	CodecSampleRate = core.CodecSampleRate
	// Deprecated: use go-agent-runtime/services/rtctransport.DefaultInboundLoopSampleRate.
	DefaultInboundLoopSampleRate = core.DefaultInboundLoopSampleRate
	// Deprecated: use go-agent-runtime/services/rtctransport.DefaultInboundFrameDuration.
	DefaultInboundFrameDuration = core.DefaultInboundFrameDuration
	// Deprecated: use go-agent-runtime/services/rtctransport.DefaultInboundJitterDepth.
	DefaultInboundJitterDepth = core.DefaultInboundJitterDepth
)

// Deprecated: use go-agent-runtime/services/rtctransport.NewInboundTrack.
func NewInboundTrack(source, opus any, config InboundTrackConfig) (*InboundTrack, error) {
	return core.NewInboundTrack(source, opus, config)
}
