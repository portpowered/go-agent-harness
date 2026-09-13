package service

import (
	"context"
	"sync"
	"time"

	"github.com/pion/rtp"
	rtctransport "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtctransport"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

const (
	defaultOpusPayloadType uint8  = 111
	defaultOutboundSSRC    uint32 = 1
)

var _ rtctransport.OutboundTrack = (*outboundTrack)(nil)

type outboundTrack struct {
	encoder    rtctransport.OpusEncoder
	writer     rtctransport.RTPWriter
	pacer      rtctransport.Pacer
	sourceRate int

	payloadType uint8
	ssrc        uint32
	sequence    uint16
	timestamp   uint32

	mediaSamples uint64
	writeGate    chan struct{}

	lifecycleMu sync.Mutex
	lifeCtx     context.Context
	lifeCancel  context.CancelCauseFunc
	closed      bool
	active      int
	activeDone  chan struct{}

	closeOnce sync.Once
	closeErr  error
}

func (s *Service) NewOutboundTrack(config rtctransport.OutboundTrackConfig) (rtctransport.OutboundTrack, error) {
	if config.Encoder == nil {
		return nil, rtctransport.ErrOutboundNilEncoder
	}
	if config.Writer == nil {
		return nil, rtctransport.ErrOutboundNilWriter
	}
	if _, err := wavio.Resample(nil, config.SourceRate, rtctransport.OutboundRTPClockRate); err != nil {
		return nil, wrapOutbound("configure source rate", err)
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
	track := &outboundTrack{
		encoder: config.Encoder, writer: config.Writer, pacer: config.Pacer,
		sourceRate: config.SourceRate, payloadType: config.PayloadType,
		ssrc: config.SSRC, sequence: config.InitialSequenceNumber,
		timestamp: config.InitialTimestamp, lifeCtx: lifeCtx, lifeCancel: lifeCancel,
		writeGate: make(chan struct{}, 1),
	}
	track.writeGate <- struct{}{}
	return track, nil
}

func (t *outboundTrack) WriteFrame(ctx context.Context, frame sharedaudio.PCMFrame) error {
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
		return rtctransport.ErrOutboundEmptyFrame
	}
	resampled, err := wavio.Resample(frame.Samples, t.sourceRate, rtctransport.OutboundRTPClockRate)
	if err != nil {
		return wrapOutbound("resample", err)
	}
	if len(resampled) == 0 || uint64(len(resampled)) > uint64(^uint32(0)) || uint64(len(resampled)) > ^uint64(0)-t.mediaSamples {
		return rtctransport.ErrOutboundFrameTooLarge
	}
	if err := contextCauseIfDone(operationCtx); err != nil {
		return wrapOutbound("resample", err)
	}
	encoded, err := t.encoder.Encode(operationCtx, resampled)
	if err != nil {
		return wrapOutbound("encode", err)
	}
	if len(encoded) == 0 {
		return wrapOutbound("encode", rtctransport.ErrOutboundEmptyPayload)
	}
	packet := &rtp.Packet{
		Header: rtp.Header{
			Version: 2, Marker: t.mediaSamples == 0, PayloadType: t.payloadType,
			SequenceNumber: t.sequence, Timestamp: t.timestamp, SSRC: t.ssrc,
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

func (t *outboundTrack) Close() error {
	t.closeOnce.Do(func() {
		t.lifecycleMu.Lock()
		t.closed = true
		t.lifeCancel(rtctransport.ErrOutboundClosed)
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

func (t *outboundTrack) beginWrite(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	t.lifecycleMu.Lock()
	if t.closed {
		t.lifecycleMu.Unlock()
		return nil, nil, rtctransport.ErrOutboundClosed
	}
	if t.active == 0 {
		t.activeDone = make(chan struct{})
	}
	t.active++
	lifeCtx := t.lifeCtx
	t.lifecycleMu.Unlock()
	operationCtx, cancel := context.WithCancelCause(ctx)
	stopLifeHook := context.AfterFunc(lifeCtx, func() { cancel(context.Cause(lifeCtx)) })
	finish := func() {
		stopLifeHook()
		cancel(nil)
		t.endWrite()
	}
	return operationCtx, finish, nil
}

func (t *outboundTrack) endWrite() {
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
	return &rtctransport.OutboundOperationError{Operation: operation, Err: err}
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

func newWallClockPacer() rtctransport.Pacer {
	return &wallClockPacer{now: time.Now, wait: waitWallClock}
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
	seconds := samples / rtctransport.OutboundRTPClockRate
	remainder := samples % rtctransport.OutboundRTPClockRate
	if seconds > maxDuration/uint64(time.Second) {
		return time.Duration(maxDuration)
	}
	wholeNanos := seconds * uint64(time.Second)
	fractionNanos := remainder * uint64(time.Second) / rtctransport.OutboundRTPClockRate
	if wholeNanos > maxDuration-fractionNanos {
		return time.Duration(maxDuration)
	}
	return time.Duration(wholeNanos + fractionNanos)
}
