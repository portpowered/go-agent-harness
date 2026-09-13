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
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

type InboundTrack struct {
	source    packetSource
	decoder   rtctransport.OpusDecoder
	config    inboundTrackConfig
	frames    chan frameResult
	done      chan struct{}
	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
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
		frames: make(chan frameResult, cfg.jitterPackets+1), done: make(chan struct{}),
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
			if !ok {
				close(t.frames)
				return
			}
			if event.err != nil {
				if err := state.flush(); err != nil {
					t.finish(err)
				} else {
					t.finish(inboundTrackError(rtctransport.ErrInboundTrackSource, "read RTP", event.err))
				}
				return
			}
			if event.packet == nil {
				t.finish(inboundTrackError(rtctransport.ErrInvalidInboundRTPPacket, "packet", errors.New("source returned nil without an error")))
				return
			}
			if err := state.push(event.packet); err != nil {
				if flushErr := state.flush(); flushErr != nil {
					t.finish(flushErr)
				} else {
					t.finish(err)
				}
				return
			}
			if timer == nil && !state.started {
				timer = t.config.newTimer(t.config.jitterDepth)
			}
		case <-timer:
			if err := state.tick(); err != nil {
				t.finish(err)
				return
			}
			timer = t.config.newTimer(t.config.frameDuration)
		}
	}
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
	}
}

func (t *InboundTrack) finish(err error) {
	if err == nil {
		err = io.EOF
	}
	select {
	case t.frames <- frameResult{err: err}:
	case <-t.done:
	}
	close(t.frames)
}

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
	case <-t.done:
		return sharedaudio.PCMFrame{}, rtctransport.ErrInboundTrackClosed
	case <-ctx.Done():
		return sharedaudio.PCMFrame{}, ctx.Err()
	case result, ok := <-t.frames:
		if t.closed.Load() {
			return sharedaudio.PCMFrame{}, rtctransport.ErrInboundTrackClosed
		}
		if !ok {
			return sharedaudio.PCMFrame{}, io.EOF
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
		close(t.done)
		if err := t.source.close(); err != nil {
			t.closeErr = inboundTrackError(rtctransport.ErrInboundTrackSource, "close source", err)
		}
	})
	return t.closeErr
}

type inboundPlayout struct {
	track                            *InboundTrack
	packets                          map[int64]*rtp.Packet
	have, started                    bool
	baseSeq, minSeq, nextSeq, maxSeq int64
	baseTimestamp                    uint32
	ssrc                             uint32
	payloadType                      uint8
}

func (s *inboundPlayout) push(packet *rtp.Packet) error {
	if packet.Version != 2 {
		return inboundTrackError(rtctransport.ErrInvalidInboundRTPPacket, "packet", fmt.Errorf("version %d: want RTP version 2", packet.Version))
	}
	if !s.have {
		sequence := int64(packet.SequenceNumber)
		s.have, s.baseSeq, s.minSeq, s.baseTimestamp, s.maxSeq = true, sequence, sequence, packet.Timestamp, sequence
		s.ssrc, s.payloadType = packet.SSRC, packet.PayloadType
	}
	extended := unwrapSequence(packet.SequenceNumber, s.maxSeq)
	if s.started && extended < s.nextSeq {
		return nil
	}
	if _, exists := s.packets[extended]; exists {
		return nil
	}
	if packet.SSRC != s.ssrc {
		return inboundTrackError(rtctransport.ErrInvalidInboundRTPPacket, "packet", fmt.Errorf("SSRC %d changed within one audio track", packet.SSRC))
	}
	if packet.PayloadType != s.payloadType {
		return inboundTrackError(rtctransport.ErrInvalidInboundRTPPacket, "packet", fmt.Errorf("payload type %d changed within one audio track", packet.PayloadType))
	}
	expected := s.expectedTimestamp(extended)
	if packet.Timestamp != expected {
		return inboundTrackError(rtctransport.ErrImpossibleRTPProgress, "RTP progress", fmt.Errorf("sequence %d timestamp %d: want %d", packet.SequenceNumber, packet.Timestamp, expected))
	}
	if !s.started {
		if extended < s.minSeq {
			if s.minSeq-extended > int64(s.track.config.jitterPackets) {
				return inboundTrackError(rtctransport.ErrImpossibleRTPProgress, "RTP progress", fmt.Errorf("sequence %d exceeds initial jitter window", packet.SequenceNumber))
			}
			s.minSeq = extended
		} else if extended > s.maxSeq {
			if extended-s.maxSeq > int64(s.track.config.jitterPackets) {
				return inboundTrackError(rtctransport.ErrImpossibleRTPProgress, "RTP progress", fmt.Errorf("sequence %d exceeds initial jitter window", packet.SequenceNumber))
			}
			s.maxSeq = extended
		}
	} else if extended-s.nextSeq > int64(s.track.config.jitterPackets) {
		return inboundTrackError(rtctransport.ErrImpossibleRTPProgress, "RTP progress", fmt.Errorf("sequence %d exceeds jitter window", packet.SequenceNumber))
	}
	if extended > s.maxSeq {
		s.maxSeq = extended
	}
	s.packets[extended] = clonePacket(packet)
	return nil
}

func (s *inboundPlayout) tick() error {
	if !s.started {
		s.nextSeq = s.minSeq
		s.started = true
	}
	return s.emitNext()
}

func (s *inboundPlayout) flush() error {
	if len(s.packets) == 0 {
		return nil
	}
	if !s.started {
		s.nextSeq = s.minSeq
		s.started = true
	}
	for s.nextSeq <= s.maxSeq {
		if err := s.emitNext(); err != nil {
			return err
		}
	}
	return nil
}

func (s *inboundPlayout) emitNext() error {
	packet, exists := s.packets[s.nextSeq]
	if exists {
		delete(s.packets, s.nextSeq)
	}
	if !exists && !s.started {
		return nil
	}
	var payload []byte
	if exists {
		payload = packet.Payload
	}
	samples, err := s.decode(payload, !exists)
	if err != nil {
		return err
	}
	if err := s.track.emit(samples); err != nil {
		return err
	}
	s.nextSeq++
	return nil
}

func (s *inboundPlayout) decode(payload []byte, plc bool) ([]int16, error) {
	samples, err := func() ([]int16, error) {
		if plc {
			return s.track.decoder.DecodePLC()
		}
		return s.track.decoder.Decode(payload)
	}()
	if err != nil {
		return nil, inboundTrackError(rtctransport.ErrInboundTrackDecode, "decode", err)
	}
	if len(samples) != s.track.config.codecSamples {
		return nil, inboundTrackError(rtctransport.ErrInboundTrackFrame, "decode", fmt.Errorf("got %d samples, want %d", len(samples), s.track.config.codecSamples))
	}
	owned := append([]int16(nil), samples...)
	if s.track.config.rate == rtctransport.CodecSampleRate {
		return owned, nil
	}
	resampled, err := s.track.config.resample(owned, rtctransport.CodecSampleRate, s.track.config.rate)
	if err != nil {
		return nil, inboundTrackError(rtctransport.ErrInboundTrackResample, "resample", err)
	}
	if len(resampled) != s.track.config.outputSamples {
		return nil, inboundTrackError(rtctransport.ErrInboundTrackFrame, "resample", fmt.Errorf("got %d samples, want %d", len(resampled), s.track.config.outputSamples))
	}
	return resampled, nil
}

func (s *inboundPlayout) expectedTimestamp(sequence int64) uint32 {
	return uint32(int64(s.baseTimestamp) + (sequence-s.baseSeq)*int64(s.track.config.codecSamples))
}

func inboundTrackError(kind error, operation string, err error) *rtctransport.InboundTrackError {
	return &rtctransport.InboundTrackError{Operation: operation, Kind: kind, Err: err}
}

func inboundConfigError(field string, observed any, reason string) error {
	return inboundTrackError(rtctransport.ErrInvalidInboundTrackConfig, "configuration", fmt.Errorf("%s: got %v (%s)", field, observed, reason))
}

func unwrapSequence(sequence uint16, reference int64) int64 {
	candidate := (reference &^ 0xffff) | int64(sequence)
	delta := candidate - reference
	if delta > 32767 {
		candidate -= 65536
	} else if delta < -32768 {
		candidate += 65536
	}
	return candidate
}

func clonePacket(packet *rtp.Packet) *rtp.Packet {
	cloned := *packet
	cloned.Payload = append([]byte(nil), packet.Payload...)
	cloned.CSRC = append([]uint32(nil), packet.CSRC...)
	return &cloned
}
