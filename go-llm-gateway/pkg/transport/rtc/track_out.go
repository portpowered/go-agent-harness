package rtc

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/pion/rtp"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

// OutboundRTPClockRate is retained for legacy Pion codec negotiation. New
// runtime compositions use services/rtctransport for packetization policy.
const OutboundRTPClockRate = wavio.Rate48kHz

const (
	defaultOpusPayloadType uint8  = 111
	defaultOutboundSSRC    uint32 = 1

	ErrOutboundClosed        Error = "rtc outbound track is closed"
	ErrOutboundEmptyFrame    Error = "rtc outbound PCM frame is empty"
	ErrOutboundNilEncoder    Error = "rtc outbound Opus encoder is nil"
	ErrOutboundNilWriter     Error = "rtc outbound RTP writer is nil"
	ErrOutboundEmptyPayload  Error = "rtc outbound encoder produced an empty payload"
	ErrOutboundFrameTooLarge Error = "rtc outbound PCM frame is too large"
)

type OutboundOperationError struct {
	Operation string
	Err       error
}

func (e *OutboundOperationError) Error() string {
	return fmt.Sprintf("rtc outbound %s: %v", e.Operation, e.Err)
}

func (e *OutboundOperationError) Unwrap() error { return e.Err }

type OpusEncoder interface {
	Encode(context.Context, []int16) ([]byte, error)
}

type OpusEncoderFunc func(context.Context, []int16) ([]byte, error)

func (f OpusEncoderFunc) Encode(ctx context.Context, samples []int16) ([]byte, error) {
	return f(ctx, samples)
}

type RTPWriter interface {
	WriteRTP(context.Context, *rtp.Packet) error
}

type RTPWriterFunc func(context.Context, *rtp.Packet) error

func (f RTPWriterFunc) WriteRTP(ctx context.Context, packet *rtp.Packet) error {
	return f(ctx, packet)
}

type Pacer interface {
	Wait(context.Context, uint64) error
}

type PacerFunc func(context.Context, uint64) error

func (f PacerFunc) Wait(ctx context.Context, offset uint64) error {
	return f(ctx, offset)
}

type OutboundTrackConfig struct {
	SourceRate            int
	Encoder               OpusEncoder
	Writer                RTPWriter
	Pacer                 Pacer
	PayloadType           uint8
	SSRC                  uint32
	InitialSequenceNumber uint16
	InitialTimestamp      uint32
}

// OutboundTrack is the narrow legacy adapter kept so existing Pion callers do
// not need to migrate as part of this service-boundary slice.
type OutboundTrack struct {
	encoder    OpusEncoder
	writer     RTPWriter
	pacer      Pacer
	sourceRate int
	payload    uint8
	ssrc       uint32
	sequence   uint16
	timestamp  uint32
	samples    uint64
	mu         sync.Mutex
	writeMu    sync.Mutex
	closed     bool
	closeOnce  sync.Once
	closeErr   error
}

var _ sharedaudio.OutboundMedia = (*OutboundTrack)(nil)

func NewOutboundTrack(config OutboundTrackConfig) (*OutboundTrack, error) {
	if nilOutboundValue(config.Encoder) {
		return nil, ErrOutboundNilEncoder
	}
	if nilOutboundValue(config.Writer) {
		return nil, ErrOutboundNilWriter
	}
	if _, err := wavio.Resample(nil, config.SourceRate, OutboundRTPClockRate); err != nil {
		return nil, &OutboundOperationError{Operation: "configure source rate", Err: err}
	}
	if config.PayloadType == 0 {
		config.PayloadType = defaultOpusPayloadType
	}
	if config.SSRC == 0 {
		config.SSRC = defaultOutboundSSRC
	}
	if config.Pacer == nil {
		config.Pacer = newLegacyWallClockPacer()
	}
	return &OutboundTrack{encoder: config.Encoder, writer: config.Writer, pacer: config.Pacer, sourceRate: config.SourceRate, payload: config.PayloadType, ssrc: config.SSRC, sequence: config.InitialSequenceNumber, timestamp: config.InitialTimestamp}, nil
}

func nilOutboundValue(value any) bool {
	if value == nil {
		return true
	}
	ref := reflect.ValueOf(value)
	switch ref.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return ref.IsNil()
	default:
		return false
	}
}

func (t *OutboundTrack) WriteFrame(ctx context.Context, frame sharedaudio.PCMFrame) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(frame.Samples) == 0 {
		return ErrOutboundEmptyFrame
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return ErrOutboundClosed
	}
	sequence, timestamp, mediaSamples := t.sequence, t.timestamp, t.samples
	t.mu.Unlock()
	resampled, err := wavio.Resample(frame.Samples, t.sourceRate, OutboundRTPClockRate)
	if err != nil {
		return &OutboundOperationError{Operation: "resample", Err: err}
	}
	if len(resampled) == 0 || uint64(len(resampled)) > uint64(^uint32(0)) || uint64(len(resampled)) > ^uint64(0)-mediaSamples {
		return ErrOutboundFrameTooLarge
	}
	encoded, err := t.encoder.Encode(ctx, append([]int16(nil), resampled...))
	if err != nil {
		return &OutboundOperationError{Operation: "encode", Err: err}
	}
	if len(encoded) == 0 {
		return &OutboundOperationError{Operation: "encode", Err: ErrOutboundEmptyPayload}
	}
	packet := &rtp.Packet{Header: rtp.Header{Version: 2, Marker: mediaSamples == 0, PayloadType: t.payload, SequenceNumber: sequence, Timestamp: timestamp, SSRC: t.ssrc}, Payload: append([]byte(nil), encoded...)}
	if err := t.pacer.Wait(ctx, mediaSamples); err != nil {
		return &OutboundOperationError{Operation: "pace", Err: err}
	}
	if err := t.writer.WriteRTP(ctx, packet); err != nil {
		return &OutboundOperationError{Operation: "write RTP", Err: err}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return ErrOutboundClosed
	}
	t.sequence++
	t.timestamp += uint32(len(resampled))
	t.samples += uint64(len(resampled))
	return nil
}

func (t *OutboundTrack) Close() error {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.closed = true
		t.mu.Unlock()
		if closer, ok := t.encoder.(interface{ Close() error }); ok {
			if err := closer.Close(); err != nil {
				t.closeErr = &OutboundOperationError{Operation: "close encoder", Err: err}
			}
		}
	})
	return t.closeErr
}

type legacyWallClockPacer struct {
	mu      sync.Mutex
	started bool
	start   time.Time
}

func newLegacyWallClockPacer() Pacer { return &legacyWallClockPacer{} }

func (p *legacyWallClockPacer) Wait(ctx context.Context, offset uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	now := time.Now()
	if !p.started {
		p.start, p.started = now, true
	}
	deadline := p.start.Add(legacySampleOffsetDuration(offset))
	p.mu.Unlock()
	delay := time.Until(deadline)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func legacySampleOffsetDuration(samples uint64) time.Duration {
	seconds := samples / OutboundRTPClockRate
	remainder := samples % OutboundRTPClockRate
	return time.Duration(seconds)*time.Second + time.Duration(remainder)*time.Second/OutboundRTPClockRate
}
