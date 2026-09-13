// Package rtctransport owns the reusable PCM/RTP transport policy used by
// runtime compositions. Pion-specific endpoints are injected at this
// boundary; the service never discovers peers, devices, credentials, or
// process globals.
package rtctransport

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pion/rtp"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const (
	// CodecSampleRate is the RTP/Opus clock used by every track.
	CodecSampleRate = 48000
	// OutboundRTPClockRate is retained as the explicit outbound name used by
	// callers that build negotiated WebRTC codec capabilities.
	OutboundRTPClockRate = CodecSampleRate

	DefaultInboundLoopSampleRate = CodecSampleRate
	DefaultInboundFrameDuration  = 20 * time.Millisecond
	DefaultInboundJitterDepth    = 60 * time.Millisecond
)

// Error is an immutable transport error identity. Constants keep the public
// errors.Is targets stable without exposing mutable package variables.
type Error string

func (e Error) Error() string { return string(e) }

const (
	ErrInvalidInboundTrackConfig Error = "invalid inbound RTP audio track configuration"
	ErrNilInboundRTPTrack        Error = "nil inbound RTP track"
	ErrNilOpusDecoder            Error = "nil Opus decoder"
	ErrUnsupportedOpusDecoder    Error = "unsupported Opus decoder seam"
	ErrInvalidInboundRTPPacket   Error = "invalid inbound RTP packet"
	ErrImpossibleRTPProgress     Error = "impossible RTP audio progress"
	ErrInboundTrackSource        Error = "inbound RTP track source failed"
	ErrInboundTrackDecode        Error = "inbound Opus decode failed"
	ErrInboundTrackResample      Error = "inbound PCM resample failed"
	ErrInboundTrackFrame         Error = "inbound PCM frame has invalid size"
	ErrInboundTrackQueueOverflow Error = "inbound RTP audio track queue is full"
	ErrInboundTrackClosed        Error = "inbound RTP audio track is closed"

	ErrInvalidOutboundTrackConfig Error = "invalid outbound RTP audio track configuration"
	ErrOutboundClosed             Error = "rtc outbound track is closed"
	ErrOutboundEmptyFrame         Error = "rtc outbound PCM frame is empty"
	ErrOutboundFrameSize          Error = "rtc outbound PCM frame has invalid size"
	ErrOutboundNilEncoder         Error = "rtc outbound Opus encoder is nil"
	ErrOutboundNilWriter          Error = "rtc outbound RTP writer is nil"
	ErrOutboundNilPacer           Error = "rtc outbound media pacer is nil"
	ErrOutboundEmptyPayload       Error = "rtc outbound encoder produced an empty payload"
	ErrOutboundFrameTooLarge      Error = "rtc outbound PCM frame is too large"
	ErrOutboundQueueOverflow      Error = "rtc outbound track queue is full"
)

// InboundTrackError adds a stable operation and error kind while preserving
// the underlying cause for errors.Is/errors.As callers.
type InboundTrackError struct {
	Operation string
	Kind      error
	Err       error
}

func (e *InboundTrackError) Error() string {
	return fmt.Sprintf("inbound RTP track %s failed: %v", e.Operation, e.Err)
}

func (e *InboundTrackError) Unwrap() error { return e.Err }

func (e *InboundTrackError) Is(target error) bool {
	return target == e.Kind || errors.Is(e.Err, target)
}

// OutboundOperationError preserves the error returned by a resampler, codec,
// pacer, RTP writer, or encoder closer while adding the failed operation.
type OutboundOperationError struct {
	Operation string
	Kind      error
	Err       error
}

func (e *OutboundOperationError) Error() string {
	return fmt.Sprintf("rtc outbound %s: %v", e.Operation, e.Err)
}

func (e *OutboundOperationError) Unwrap() error { return e.Err }

func (e *OutboundOperationError) Is(target error) bool {
	return target == e.Kind || errors.Is(e.Err, target)
}

// OpusDecoder is the narrow decoder seam required by inbound RTP policy.
// DecodePLC is called exactly once for each admitted missing packet.
type OpusDecoder interface {
	Decode([]byte) ([]int16, error)
	DecodePLC() ([]int16, error)
}

// RTPPacketSource is the narrow Pion-facing input seam. A value may also
// implement Close() error; the transport closes that source with the track.
type RTPPacketSource interface {
	ReadRTP() (*rtp.Packet, error)
}

// InboundTrackConfig selects output PCM framing and bounded reordering.
// Zero values select the production defaults.
type InboundTrackConfig struct {
	SampleRate    int
	FrameDuration time.Duration
	JitterDepth   time.Duration
	NewTimer      func(time.Duration) <-chan time.Time
	Resample      func([]int16, int, int) ([]int16, error)
}

// InboundTrack is the caller-owned PCM receiver returned by Service.
type InboundTrack interface {
	sharedaudio.InboundMedia
}

// OpusEncoder encodes one source PCM frame into one RTP Opus payload.
type OpusEncoder interface {
	Encode(context.Context, []int16) ([]byte, error)
}

// OpusEncoderFunc adapts a function to OpusEncoder.
type OpusEncoderFunc func(context.Context, []int16) ([]byte, error)

func (f OpusEncoderFunc) Encode(ctx context.Context, samples []int16) ([]byte, error) {
	return f(ctx, samples)
}

// RTPWriter writes one already packetized RTP Opus packet and owns it only
// for the duration of the call.
type RTPWriter interface {
	WriteRTP(context.Context, *rtp.Packet) error
}

// RTPWriterFunc adapts a function to RTPWriter.
type RTPWriterFunc func(context.Context, *rtp.Packet) error

func (f RTPWriterFunc) WriteRTP(ctx context.Context, packet *rtp.Packet) error {
	return f(ctx, packet)
}

// Pacer schedules packets at a media-clock offset measured in 48 kHz samples.
type Pacer interface {
	Wait(context.Context, uint64) error
}

// PacerFunc adapts a function to Pacer.
type PacerFunc func(context.Context, uint64) error

func (f PacerFunc) Wait(ctx context.Context, mediaSampleOffset uint64) error {
	return f(ctx, mediaSampleOffset)
}

// OutboundTrackConfig configures one caller-owned PCM-to-RTP track.
type OutboundTrackConfig struct {
	SourceRate    int
	FrameDuration time.Duration
	QueueDepth    int
	Encoder       OpusEncoder
	Writer        RTPWriter
	Pacer         Pacer

	PayloadType           uint8
	SSRC                  uint32
	InitialSequenceNumber uint16
	InitialTimestamp      uint32
}

// OutboundTrack is the caller-owned PCM sender returned by Service.
type OutboundTrack interface {
	sharedaudio.OutboundMedia
}

// Service is the reusable transport policy boundary. Construction is inert;
// each returned track owns only the injected codec/source lifecycle described
// by its configuration.
type Service interface {
	NewInboundTrack(source, opus any, config InboundTrackConfig) (InboundTrack, error)
	NewOutboundTrack(config OutboundTrackConfig) (OutboundTrack, error)
}
