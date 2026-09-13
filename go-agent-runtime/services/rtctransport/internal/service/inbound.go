package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	rtctransport "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtctransport"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

const maxInboundJitterDepth = 2 * time.Second

type inboundTrackConfig struct {
	rate, codecSamples, outputSamples, jitterPackets int
	frameDuration, jitterDepth                       time.Duration
	newTimer                                         func(time.Duration) <-chan time.Time
	resample                                         func([]int16, int, int) ([]int16, error)
}

func normalizeInboundConfig(c rtctransport.InboundTrackConfig) (inboundTrackConfig, error) {
	rate := c.SampleRate
	if rate == 0 {
		rate = rtctransport.CodecSampleRate
	}
	if rate != wavio.Rate16kHz && rate != wavio.Rate24kHz && rate != wavio.Rate48kHz {
		return inboundTrackConfig{}, inboundConfigError("sample rate", rate, "want 16000, 24000, or 48000 Hz")
	}
	duration := c.FrameDuration
	if duration == 0 {
		duration = rtctransport.DefaultInboundFrameDuration
	}
	if !validInboundDuration(duration) {
		return inboundTrackConfig{}, inboundConfigError("frame duration", duration, "want a legal Opus duration from 2.5 ms through 60 ms")
	}
	depth := c.JitterDepth
	if depth == 0 {
		depth = rtctransport.DefaultInboundJitterDepth
	}
	if depth <= 0 || depth > maxInboundJitterDepth || depth%duration != 0 {
		return inboundTrackConfig{}, inboundConfigError("jitter depth", depth, "must be positive, bounded, and frame-aligned")
	}
	codecSamples := int(int64(rtctransport.CodecSampleRate) * int64(duration) / int64(time.Second))
	outputSamples := int(int64(rate) * int64(duration) / int64(time.Second))
	timer := c.NewTimer
	if timer == nil {
		timer = time.After
	}
	resample := c.Resample
	if resample == nil {
		resample = wavio.Resample
	}
	return inboundTrackConfig{
		rate: rate, codecSamples: codecSamples, outputSamples: outputSamples,
		jitterPackets: int(depth / duration), frameDuration: duration,
		jitterDepth: depth, newTimer: timer, resample: resample,
	}, nil
}

func validInboundDuration(duration time.Duration) bool {
	return duration == 2500*time.Microsecond || duration == 5*time.Millisecond ||
		duration == 10*time.Millisecond || duration == 20*time.Millisecond ||
		duration == 40*time.Millisecond || duration == 60*time.Millisecond
}

type packetSource struct {
	read  func() (*rtp.Packet, error)
	close func() error
}

func adaptPacketSource(value any) (packetSource, error) {
	if nilValue(value) {
		return packetSource{}, rtctransport.ErrNilInboundRTPTrack
	}
	source, ok := value.(rtctransport.RTPPacketSource)
	if !ok {
		return packetSource{}, inboundConfigError("RTP source", fmt.Sprintf("%T", value), "want ReadRTP")
	}
	return packetSource{read: source.ReadRTP, close: closeSource(value)}, nil
}

func closeSource(value any) func() error {
	if closer, ok := value.(interface{ Close() error }); ok {
		return closer.Close
	}
	return func() error { return nil }
}

func nilValue(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return v.IsNil()
	case reflect.Invalid, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128,
		reflect.Array, reflect.String, reflect.Struct:
		return false
	}
	return false
}

type InboundTrack struct {
	source      packetSource
	decoder     rtctransport.OpusDecoder
	config      inboundTrackConfig
	frames      chan frameResult
	done        chan struct{}
	closedDone  chan struct{}
	closed      atomic.Bool
	closeOnce   sync.Once
	stopOnce    sync.Once
	sourceOnce  sync.Once
	sourceErr   error
	terminalMu  sync.RWMutex
	terminalErr error
	closeErr    error
}

type frameResult struct {
	frame sharedaudio.PCMFrame
	err   error
}

type packetEvent struct {
	packet *rtp.Packet
	err    error
}

var _ rtctransport.InboundTrack = (*InboundTrack)(nil)

func (s *Service) NewInboundTrack(source, opus any, config rtctransport.InboundTrackConfig) (rtctransport.InboundTrack, error) {
	cfg, err := normalizeInboundConfig(config)
	if err != nil {
		return nil, err
	}
	packetSource, err := adaptPacketSource(source)
	if err != nil {
		return nil, err
	}
	if nilValue(opus) {
		return nil, rtctransport.ErrNilOpusDecoder
	}
	decoder, ok := opus.(rtctransport.OpusDecoder)
	if !ok {
		return nil, rtctransport.ErrUnsupportedOpusDecoder
	}
	track := &InboundTrack{
		source: packetSource, decoder: decoder, config: cfg,
		frames:     make(chan frameResult, cfg.jitterPackets+1),
		done:       make(chan struct{}),
		closedDone: make(chan struct{}),
	}
	go track.readLoop()
	return track, nil
}

func (t *InboundTrack) readLoop() {
	events := make(chan packetEvent)
	go t.readSource(events)
	state := inboundPlayout{track: t, packets: make(map[int64]*rtp.Packet, t.config.jitterPackets)}
	var timer <-chan time.Time
	for {
		select {
		case <-t.done:
			close(t.frames)
			return
		case event, ok := <-events:
			if t.handlePacketEvent(&state, event, ok, &timer) {
				return
			}
		case <-timer:
			if t.handleTimer(&state, &timer) {
				return
			}
		}
	}
}

func (t *InboundTrack) handlePacketEvent(state *inboundPlayout, event packetEvent, ok bool, timer *<-chan time.Time) bool {
	if !ok {
		t.finish(io.EOF)
		return true
	}
	if event.err != nil {
		return t.finishSourceEvent(state, event.err)
	}
	if event.packet == nil {
		t.finish(inboundTrackError(rtctransport.ErrInvalidInboundRTPPacket, "packet", errors.New("source returned nil without an error")))
		return true
	}
	if err := state.push(event.packet); err != nil {
		t.finish(err)
		return true
	}
	if *timer == nil && !state.started {
		*timer = t.config.newTimer(t.config.jitterDepth)
	}
	return false
}

func (t *InboundTrack) finishSourceEvent(state *inboundPlayout, eventErr error) bool {
	if err := state.flush(); err != nil {
		t.finish(err)
	} else {
		t.finish(inboundTrackError(rtctransport.ErrInboundTrackSource, "read RTP", eventErr))
	}
	return true
}

func (t *InboundTrack) handleTimer(state *inboundPlayout, timer *<-chan time.Time) bool {
	if err := state.tick(); err != nil {
		t.finish(err)
		return true
	}
	*timer = t.config.newTimer(t.config.frameDuration)
	return false
}

func (t *InboundTrack) readSource(events chan<- packetEvent) {
	defer close(events)
	for {
		packet, err := t.source.read()
		select {
		case events <- packetEvent{packet: packet, err: err}:
		case <-t.done:
			return
		}
		if err != nil {
			return
		}
	}
}

func (t *InboundTrack) emit(samples []int16) error {
	select {
	case t.frames <- frameResult{frame: sharedaudio.PCMFrame{Samples: samples}}:
		return nil
	case <-t.done:
		return rtctransport.ErrInboundTrackClosed
	default:
		return inboundTrackError(rtctransport.ErrInboundTrackQueueOverflow, "queue", errors.New("inbound frame delivery queue is full"))
	}
}

func (t *InboundTrack) finish(err error) {
	if err == nil {
		err = io.EOF
	}
	t.terminalMu.Lock()
	t.terminalErr = err
	t.terminalMu.Unlock()
	if closeErr := t.stop(); closeErr != nil {
		t.recordSourceCloseError(closeErr)
	}
	close(t.frames)
}

//nolint:contextcheck // nil context is normalized to the media API's documented background behavior.
func (t *InboundTrack) ReadFrame(ctx context.Context) (sharedaudio.PCMFrame, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t.closed.Load() {
		return sharedaudio.PCMFrame{}, rtctransport.ErrInboundTrackClosed
	}
	select {
	case <-ctx.Done():
		return sharedaudio.PCMFrame{}, ctx.Err()
	default:
	}
	select {
	case <-t.closedDone:
		return sharedaudio.PCMFrame{}, rtctransport.ErrInboundTrackClosed
	case <-ctx.Done():
		return sharedaudio.PCMFrame{}, ctx.Err()
	case result, ok := <-t.frames:
		if t.closed.Load() {
			return sharedaudio.PCMFrame{}, rtctransport.ErrInboundTrackClosed
		}
		if !ok {
			return sharedaudio.PCMFrame{}, t.terminal()
		}
		if result.err != nil {
			return sharedaudio.PCMFrame{}, result.err
		}
		if len(result.frame.Samples) != t.config.outputSamples {
			return sharedaudio.PCMFrame{}, inboundTrackError(rtctransport.ErrInboundTrackFrame, "frame", fmt.Errorf("got %d samples, want %d", len(result.frame.Samples), t.config.outputSamples))
		}
		return result.frame, nil
	}
}

func (t *InboundTrack) Close() error {
	t.closeOnce.Do(func() {
		t.closed.Store(true)
		close(t.closedDone)
		if err := t.stop(); err != nil {
			t.closeErr = inboundTrackError(rtctransport.ErrInboundTrackSource, "close source", err)
		}
	})
	return t.closeErr
}

func (t *InboundTrack) stop() error {
	var stopErr error
	t.stopOnce.Do(func() {
		close(t.done)
		stopErr = t.closeSource()
	})
	if stopErr != nil {
		return stopErr
	}
	return t.closeSource()
}

func (t *InboundTrack) closeSource() error {
	t.sourceOnce.Do(func() {
		t.sourceErr = t.source.close()
	})
	return t.sourceErr
}

func (t *InboundTrack) recordSourceCloseError(err error) {
	t.terminalMu.Lock()
	defer t.terminalMu.Unlock()
	if t.terminalErr == nil {
		t.terminalErr = inboundTrackError(rtctransport.ErrInboundTrackSource, "close source", err)
	}
}

func (t *InboundTrack) terminal() error {
	t.terminalMu.RLock()
	defer t.terminalMu.RUnlock()
	if t.terminalErr == nil {
		return io.EOF
	}
	return t.terminalErr
}
