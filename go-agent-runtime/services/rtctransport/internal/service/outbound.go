package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/pion/rtp"
	rtctransport "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtctransport"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

const (
	defaultOpusPayloadType    uint8  = 111
	defaultOutboundSSRC       uint32 = 1
	defaultOutboundQueueDepth        = 8
	maxOutboundQueueDepth            = 64
)

var _ rtctransport.OutboundTrack = (*OutboundTrack)(nil)

type OutboundTrack struct {
	encoder       rtctransport.OpusEncoder
	writer        rtctransport.RTPWriter
	pacer         rtctransport.Pacer
	sourceRate    int
	sourceSamples int

	payloadType uint8
	ssrc        uint32
	sequence    uint16
	timestamp   uint32

	mediaSamples uint64
	writeGate    chan struct{}
	queueSlots   chan struct{}

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
	if nilValue(config.Encoder) {
		return nil, rtctransport.ErrOutboundNilEncoder
	}
	if nilValue(config.Writer) {
		return nil, rtctransport.ErrOutboundNilWriter
	}
	sourceSamples, err := normalizeOutboundFrame(config)
	if err != nil {
		return nil, err
	}
	queueDepth := config.QueueDepth
	if queueDepth == 0 {
		queueDepth = defaultOutboundQueueDepth
	}
	if queueDepth < 1 || queueDepth > maxOutboundQueueDepth {
		return nil, outboundConfigError("queue depth", queueDepth, "want a bounded value from 1 through 64")
	}
	if config.PayloadType == 0 {
		config.PayloadType = defaultOpusPayloadType
	}
	if config.SSRC == 0 {
		config.SSRC = defaultOutboundSSRC
	}
	if config.Pacer == nil {
		config.Pacer = newWallClockPacer()
	} else if nilValue(config.Pacer) {
		return nil, rtctransport.ErrOutboundNilPacer
	}
	lifeCtx, lifeCancel := context.WithCancelCause(context.Background())
	track := &OutboundTrack{
		encoder: config.Encoder, writer: config.Writer, pacer: config.Pacer,
		sourceRate: config.SourceRate, sourceSamples: sourceSamples,
		payloadType: config.PayloadType,
		ssrc:        config.SSRC, sequence: config.InitialSequenceNumber,
		timestamp: config.InitialTimestamp, lifeCtx: lifeCtx, lifeCancel: lifeCancel,
		writeGate: make(chan struct{}, 1), queueSlots: make(chan struct{}, queueDepth),
	}
	track.writeGate <- struct{}{}
	return track, nil
}

func normalizeOutboundFrame(config rtctransport.OutboundTrackConfig) (int, error) {
	if _, err := wavio.Resample(nil, config.SourceRate, rtctransport.OutboundRTPClockRate); err != nil {
		return 0, outboundConfigError("source rate", config.SourceRate, err.Error())
	}
	duration := config.FrameDuration
	if duration == 0 {
		duration = rtctransport.DefaultInboundFrameDuration
	}
	if !validInboundDuration(duration) {
		return 0, outboundConfigError("frame duration", duration, "want a legal Opus duration from 2.5 ms through 60 ms")
	}
	samples := int64(config.SourceRate) * int64(duration) / int64(time.Second)
	if samples <= 0 || samples > int64(^uint(0)>>1) {
		return 0, outboundConfigError("frame duration", duration, "produces an unrepresentable source frame")
	}
	return int(samples), nil
}

func (t *OutboundTrack) WriteFrame(ctx context.Context, frame sharedaudio.PCMFrame) error {
	operationCtx, finish, err := t.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer finish()
	if err := validateOutboundFrame(frame, t.sourceSamples); err != nil {
		return err
	}
	if err := t.acquireWriteGate(operationCtx); err != nil {
		return err
	}
	defer func() { t.writeGate <- struct{}{} }()

	if err := contextCauseIfDone(operationCtx); err != nil {
		return wrapOutbound("write", err)
	}
	resampled, err := t.resampleFrame(operationCtx, frame)
	if err != nil {
		return err
	}
	packet, err := t.encodePacket(operationCtx, resampled)
	if err != nil {
		return err
	}
	if err := t.sendPacket(operationCtx, packet); err != nil {
		return err
	}
	t.sequence++
	t.timestamp += uint32(len(resampled))
	t.mediaSamples += uint64(len(resampled))
	return nil
}

func validateOutboundFrame(frame sharedaudio.PCMFrame, want int) error {
	if len(frame.Samples) == 0 {
		return rtctransport.ErrOutboundEmptyFrame
	}
	if len(frame.Samples) != want {
		return wrapOutboundWithKind("frame", rtctransport.ErrOutboundFrameSize,
			fmt.Errorf("got %d samples, want %d", len(frame.Samples), want))
	}
	return nil
}

func (t *OutboundTrack) acquireWriteGate(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return wrapOutbound("write", contextCause(ctx))
	case <-t.writeGate:
		return nil
	}
}

func (t *OutboundTrack) resampleFrame(ctx context.Context, frame sharedaudio.PCMFrame) ([]int16, error) {
	resampled, err := wavio.Resample(frame.Samples, t.sourceRate, rtctransport.OutboundRTPClockRate)
	if err != nil {
		return nil, wrapOutbound("resample", err)
	}
	if len(resampled) == 0 || uint64(len(resampled)) > uint64(^uint32(0)) || uint64(len(resampled)) > ^uint64(0)-t.mediaSamples {
		return nil, rtctransport.ErrOutboundFrameTooLarge
	}
	if err := contextCauseIfDone(ctx); err != nil {
		return nil, wrapOutbound("resample", err)
	}
	return resampled, nil
}

func (t *OutboundTrack) encodePacket(ctx context.Context, samples []int16) (*rtp.Packet, error) {
	encoded, err := t.encoder.Encode(ctx, samples)
	if err != nil {
		return nil, wrapOutbound("encode", err)
	}
	if len(encoded) == 0 {
		return nil, wrapOutbound("encode", rtctransport.ErrOutboundEmptyPayload)
	}
	return &rtp.Packet{
		Header: rtp.Header{
			Version: 2, Marker: t.mediaSamples == 0, PayloadType: t.payloadType,
			SequenceNumber: t.sequence, Timestamp: t.timestamp, SSRC: t.ssrc,
		},
		Payload: append([]byte(nil), encoded...),
	}, nil
}

func (t *OutboundTrack) sendPacket(ctx context.Context, packet *rtp.Packet) error {
	if err := t.pacer.Wait(ctx, t.mediaSamples); err != nil {
		return wrapOutbound("pace", err)
	}
	if err := contextCauseIfDone(ctx); err != nil {
		return wrapOutbound("pace", err)
	}
	if err := t.writer.WriteRTP(ctx, packet); err != nil {
		return wrapOutbound("write RTP", err)
	}
	return nil
}

func (t *OutboundTrack) Close() error {
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

func (t *OutboundTrack) beginWrite(ctx context.Context) (context.Context, func(), error) {
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
	if err := ctx.Err(); err != nil {
		t.endWrite()
		return nil, nil, err
	}
	select {
	case t.queueSlots <- struct{}{}:
	default:
		t.endWrite()
		return nil, nil, rtctransport.ErrOutboundQueueOverflow
	}
	operationCtx, cancel := context.WithCancelCause(ctx)
	stopLifeHook := context.AfterFunc(lifeCtx, func() { cancel(context.Cause(lifeCtx)) })
	finish := func() {
		stopLifeHook()
		cancel(nil)
		<-t.queueSlots
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
	return &rtctransport.OutboundOperationError{Operation: operation, Err: err}
}

func wrapOutboundWithKind(operation string, kind, err error) error {
	if err == nil {
		return nil
	}
	return &rtctransport.OutboundOperationError{Operation: operation, Kind: kind, Err: err}
}

func outboundConfigError(field string, observed any, reason string) error {
	return wrapOutboundWithKind("configuration", rtctransport.ErrInvalidOutboundTrackConfig,
		fmt.Errorf("%s: got %v (%s)", field, observed, reason))
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
