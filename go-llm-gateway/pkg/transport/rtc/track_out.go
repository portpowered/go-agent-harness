package rtc

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

const (
	// OutboundRTPClockRate is the RTP clock used by negotiated WebRTC Opus.
	OutboundRTPClockRate = wavio.Rate48kHz

	defaultOpusPayloadType uint8  = 111
	defaultOutboundSSRC    uint32 = 1
)

var (
	// ErrOutboundClosed identifies a write attempted after, or interrupted by,
	// closing the outbound track.
	ErrOutboundClosed = errors.New("rtc outbound track is closed")
	// ErrOutboundEmptyFrame identifies a frame without PCM samples.
	ErrOutboundEmptyFrame = errors.New("rtc outbound PCM frame is empty")
	// ErrOutboundNilEncoder identifies a configuration without an Opus encoder.
	ErrOutboundNilEncoder = errors.New("rtc outbound Opus encoder is nil")
	// ErrOutboundNilWriter identifies a configuration without an RTP writer.
	ErrOutboundNilWriter = errors.New("rtc outbound RTP writer is nil")
	// ErrOutboundEmptyPayload identifies an encoder that produced no Opus data.
	ErrOutboundEmptyPayload = errors.New("rtc outbound encoder produced an empty payload")
	// ErrOutboundFrameTooLarge identifies a frame whose media clock cannot be
	// represented by the RTP timestamp or the pacing sample counter.
	ErrOutboundFrameTooLarge = errors.New("rtc outbound PCM frame is too large")
)

// OutboundOperationError preserves the error returned by a resampler, codec,
// pacer, RTP writer, or encoder closer while adding the failed operation.
type OutboundOperationError struct {
	Operation string
	Err       error
}

func (e *OutboundOperationError) Error() string {
	return fmt.Sprintf("rtc outbound %s: %v", e.Operation, e.Err)
}

func (e *OutboundOperationError) Unwrap() error { return e.Err }

// OpusEncoder encodes one 48 kHz, mono PCM16 frame into one Opus payload.
// Implementations must consume samples before returning and must honor ctx
// while doing work. The returned payload is owned by the caller.
type OpusEncoder interface {
	Encode(ctx context.Context, samples []int16) ([]byte, error)
}

// OpusEncoderFunc adapts a function to OpusEncoder.
type OpusEncoderFunc func(context.Context, []int16) ([]byte, error)

func (f OpusEncoderFunc) Encode(ctx context.Context, samples []int16) ([]byte, error) {
	return f(ctx, samples)
}

// RTPWriter writes an already packetized RTP Opus packet. The writer owns the
// packet only for the duration of the call and must honor ctx while blocked.
type RTPWriter interface {
	WriteRTP(ctx context.Context, packet *rtp.Packet) error
}

// RTPWriterFunc adapts a function to RTPWriter.
type RTPWriterFunc func(context.Context, *rtp.Packet) error

func (f RTPWriterFunc) WriteRTP(ctx context.Context, packet *rtp.Packet) error {
	return f(ctx, packet)
}

// Pacer schedules packets at a media-clock offset measured in 48 kHz samples.
// The first packet is requested at offset zero; later packets are requested at
// the number of samples already emitted. Using samples rather than caller
// wall-clock arrival makes the RTP timeline deterministic under jitter.
type Pacer interface {
	Wait(ctx context.Context, mediaSampleOffset uint64) error
}

// PacerFunc adapts a function to Pacer.
type PacerFunc func(context.Context, uint64) error

func (f PacerFunc) Wait(ctx context.Context, mediaSampleOffset uint64) error {
	return f(ctx, mediaSampleOffset)
}

// OutboundTrackConfig configures an OutboundTrack. Encoder and Writer are
// intentionally injected: Pion supplies RTP transport, while a codec module
// supplies PCM16-to-Opus encoding.
type OutboundTrackConfig struct {
	SourceRate int
	Encoder    OpusEncoder
	Writer     RTPWriter
	Pacer      Pacer

	PayloadType           uint8
	SSRC                  uint32
	InitialSequenceNumber uint16
	InitialTimestamp      uint32
}

// OutboundTrack sends PCM16 frames as timestamped, paced RTP Opus packets.
var _ OutboundMedia = (*OutboundTrack)(nil)

type OutboundTrack struct {
	encoder    OpusEncoder
	writer     RTPWriter
	pacer      Pacer
	sourceRate int

	payloadType uint8
	ssrc        uint32
	sequence    uint16
	timestamp   uint32

	mediaSamples uint64

	writeGate chan struct{}

	lifecycleMu sync.Mutex
	lifeCtx     context.Context
	lifeCancel  context.CancelCauseFunc
	closed      bool
	active      int
	activeDone  chan struct{}

	closeOnce sync.Once
	closeErr  error
}

// NewOutboundTrack validates configuration without starting goroutines or
// acquiring any network resources. Source samples are converted to the Opus
// 48 kHz clock with wavio.Resample for every frame.
func NewOutboundTrack(config OutboundTrackConfig) (*OutboundTrack, error) {
	if config.Encoder == nil {
		return nil, ErrOutboundNilEncoder
	}
	if config.Writer == nil {
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
		config.Pacer = newWallClockPacer()
	}

	lifeCtx, lifeCancel := context.WithCancelCause(context.Background())
	track := &OutboundTrack{
		encoder:    config.Encoder,
		writer:     config.Writer,
		pacer:      config.Pacer,
		sourceRate: config.SourceRate,

		payloadType: config.PayloadType,
		ssrc:        config.SSRC,
		sequence:    config.InitialSequenceNumber,
		timestamp:   config.InitialTimestamp,

		lifeCtx:    lifeCtx,
		lifeCancel: lifeCancel,
		writeGate:  make(chan struct{}, 1),
	}
	track.writeGate <- struct{}{}
	return track, nil
}

// WriteFrame resamples, encodes, paces, and writes one caller-owned PCM16
// frame. No state is committed until the complete RTP write succeeds.
func (t *OutboundTrack) WriteFrame(ctx context.Context, frame PCMFrame) error {
	operationCtx, finish, err := t.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer finish()

	select {
	case <-operationCtx.Done():
		return wrapOutbound("write", contextCause(operationCtx))
	case <-t.writeGate:
	}
	defer func() { t.writeGate <- struct{}{} }()

	if err := contextCauseIfDone(operationCtx); err != nil {
		return wrapOutbound("write", err)
	}
	if len(frame.Samples) == 0 {
		return ErrOutboundEmptyFrame
	}

	resampled, err := wavio.Resample(frame.Samples, t.sourceRate, OutboundRTPClockRate)
	if err != nil {
		return wrapOutbound("resample", err)
	}
	if len(resampled) == 0 || uint64(len(resampled)) > uint64(^uint32(0)) || uint64(len(resampled)) > ^uint64(0)-t.mediaSamples {
		return ErrOutboundFrameTooLarge
	}
	if err := contextCauseIfDone(operationCtx); err != nil {
		return wrapOutbound("resample", err)
	}

	encoded, err := t.encoder.Encode(operationCtx, resampled)
	if err != nil {
		return wrapOutbound("encode", err)
	}
	if len(encoded) == 0 {
		return wrapOutbound("encode", ErrOutboundEmptyPayload)
	}

	packet := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			Marker:         t.mediaSamples == 0,
			PayloadType:    t.payloadType,
			SequenceNumber: t.sequence,
			Timestamp:      t.timestamp,
			SSRC:           t.ssrc,
		},
		Payload: append([]byte(nil), encoded...),
	}

	if err := t.pacer.Wait(operationCtx, t.mediaSamples); err != nil {
		return wrapOutbound("pace", err)
	}
	if err := contextCauseIfDone(operationCtx); err != nil {
		return wrapOutbound("pace", err)
	}
	if err := t.writer.WriteRTP(operationCtx, packet); err != nil {
		return wrapOutbound("write RTP", err)
	}

	t.sequence++
	t.timestamp += uint32(len(resampled))
	t.mediaSamples += uint64(len(resampled))
	return nil
}

// Close is idempotent. It cancels active writes, waits for them to release
// owned state, and closes an encoder that also implements io.Closer-like
// Close() error. The RTP writer remains caller-owned.
func (t *OutboundTrack) Close() error {
	t.closeOnce.Do(func() {
		t.lifecycleMu.Lock()
		t.closed = true
		t.lifeCancel(ErrOutboundClosed)
		done := t.activeDone
		t.lifecycleMu.Unlock()

		if done != nil {
			<-done
		}
		if closer, ok := t.encoder.(interface{ Close() error }); ok {
			if err := closer.Close(); err != nil {
				t.closeErr = wrapOutbound("close encoder", err)
			}
		}
	})
	return t.closeErr
}

func (t *OutboundTrack) beginWrite(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}

	t.lifecycleMu.Lock()
	if t.closed {
		t.lifecycleMu.Unlock()
		return nil, nil, ErrOutboundClosed
	}
	if t.active == 0 {
		t.activeDone = make(chan struct{})
	}
	t.active++
	lifeCtx := t.lifeCtx
	t.lifecycleMu.Unlock()

	operationCtx, cancel := context.WithCancelCause(ctx)
	stopLifeHook := context.AfterFunc(lifeCtx, func() {
		cancel(context.Cause(lifeCtx))
	})
	finish := func() {
		stopLifeHook()
		cancel(nil)
		t.endWrite()
	}
	return operationCtx, finish, nil
}

func (t *OutboundTrack) endWrite() {
	t.lifecycleMu.Lock()
	defer t.lifecycleMu.Unlock()
	t.active--
	if t.active == 0 {
		close(t.activeDone)
	}
}

func wrapOutbound(operation string, err error) error {
	if err == nil {
		return nil
	}
	return &OutboundOperationError{Operation: operation, Err: err}
}

func contextCauseIfDone(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return contextCause(ctx)
	default:
		return nil
	}
}

func contextCause(ctx context.Context) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return context.Canceled
}

type wallClockPacer struct {
	started bool
	start   time.Time
	now     func() time.Time
	wait    func(context.Context, time.Duration) error
}

func newWallClockPacer() Pacer {
	return &wallClockPacer{
		now:  time.Now,
		wait: waitWallClock,
	}
}

func (p *wallClockPacer) Wait(ctx context.Context, mediaSampleOffset uint64) error {
	if err := contextCauseIfDone(ctx); err != nil {
		return err
	}

	now := p.now()
	offsetDuration := sampleOffsetDuration(mediaSampleOffset)
	start := p.start
	if !p.started {
		start = now
	}
	deadline := start.Add(offsetDuration)
	if p.started && !deadline.After(now) {
		// A late caller must not make a backlog burst. Re-anchor the media
		// timeline at the current emission while retaining the requested
		// interval for the next packet.
		start = now.Add(-offsetDuration)
		deadline = now
	}

	if err := p.wait(ctx, deadline.Sub(now)); err != nil {
		return err
	}
	p.started = true
	p.start = start
	return nil
}

func waitWallClock(ctx context.Context, duration time.Duration) error {
	if err := contextCauseIfDone(ctx); err != nil {
		return err
	}
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		return contextCause(ctx)
	}
}

func sampleOffsetDuration(samples uint64) time.Duration {
	const maxDuration = uint64(1<<63 - 1)
	seconds := samples / OutboundRTPClockRate
	remainder := samples % OutboundRTPClockRate
	if seconds > maxDuration/uint64(time.Second) {
		return time.Duration(maxDuration)
	}
	wholeNanos := seconds * uint64(time.Second)
	fractionNanos := remainder * uint64(time.Second) / OutboundRTPClockRate
	if wholeNanos > maxDuration-fractionNanos {
		return time.Duration(maxDuration)
	}
	return time.Duration(wholeNanos + fractionNanos)
}
