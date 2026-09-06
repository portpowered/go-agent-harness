package rtc

import sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

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
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

const (
	CodecSampleRate              = 48000
	DefaultInboundLoopSampleRate = CodecSampleRate
	DefaultInboundFrameDuration  = 20 * time.Millisecond
	DefaultInboundJitterDepth    = 60 * time.Millisecond
	maxInboundJitterDepth        = 2 * time.Second
)

var (
	ErrInvalidInboundTrackConfig = errors.New("invalid inbound RTP audio track configuration")
	ErrNilInboundRTPTrack        = errors.New("nil inbound RTP track")
	ErrNilOpusDecoder            = errors.New("nil Opus decoder")
	ErrUnsupportedOpusDecoder    = errors.New("unsupported Opus decoder seam")
	ErrInvalidInboundRTPPacket   = errors.New("invalid inbound RTP packet")
	ErrImpossibleRTPProgress     = errors.New("impossible RTP audio progress")
	ErrInboundTrackSource        = errors.New("inbound RTP track source failed")
	ErrInboundTrackDecode        = errors.New("inbound Opus decode failed")
	ErrInboundTrackResample      = errors.New("inbound PCM resample failed")
	ErrInboundTrackFrame         = errors.New("inbound PCM frame has invalid size")
	ErrInboundTrackClosed        = errors.New("inbound RTP audio track is closed")
)

type InboundTrackConfig struct {
	SampleRate    int
	FrameDuration time.Duration
	JitterDepth   time.Duration
	NewTimer      func(time.Duration) <-chan time.Time
	Resample      func([]int16, int, int) ([]int16, error)
}

func DefaultInboundTrackConfig() InboundTrackConfig {
	return InboundTrackConfig{SampleRate: DefaultInboundLoopSampleRate, FrameDuration: DefaultInboundFrameDuration,
		JitterDepth: DefaultInboundJitterDepth}
}

type InboundTrackError struct {
	Operation string
	Kind, Err error
}

func (e *InboundTrackError) Error() string {
	return fmt.Sprintf("inbound RTP track %s failed: %v", e.Operation, e.Err)
}
func (e *InboundTrackError) Unwrap() error { return e.Err }
func (e *InboundTrackError) Is(target error) bool {
	return target == e.Kind || errors.Is(e.Err, target)
}

type OpusDecoder interface {
	Decode([]byte) ([]int16, error)
	DecodePLC() ([]int16, error)
}
type RTPPacketSource interface{ ReadRTP() (*rtp.Packet, error) }

type trackConfig struct {
	rate, codecSamples, outputSamples, jitterPackets int
	frameDuration, jitterDepth                       time.Duration
	newTimer                                         func(time.Duration) <-chan time.Time
	resample                                         func([]int16, int, int) ([]int16, error)
}

func (c InboundTrackConfig) normalize() (trackConfig, error) {
	rate := c.SampleRate
	if rate == 0 {
		rate = CodecSampleRate
	}
	if rate != wavio.Rate16kHz && rate != wavio.Rate24kHz && rate != wavio.Rate48kHz {
		return trackConfig{}, configError("sample rate", rate, "want 16000, 24000, or 48000 Hz")
	}
	d := c.FrameDuration
	if d == 0 {
		d = DefaultInboundFrameDuration
	}
	if !validDuration(d) {
		return trackConfig{}, configError("frame duration", d, "want a legal Opus duration from 2.5 ms through 60 ms")
	}
	depth := c.JitterDepth
	if depth == 0 {
		depth = DefaultInboundJitterDepth
	}
	if depth <= 0 || depth > maxInboundJitterDepth || depth%d != 0 {
		return trackConfig{}, configError("jitter depth", depth, "must be positive, bounded, and frame-aligned")
	}
	codec, output := int(int64(CodecSampleRate)*int64(d)/int64(time.Second)), int(int64(rate)*int64(d)/int64(time.Second))
	timer := c.NewTimer
	if timer == nil {
		timer = time.After
	}
	resample := c.Resample
	if resample == nil {
		resample = wavio.Resample
	}
	return trackConfig{rate, codec, output, int(depth / d), d, depth, timer, resample}, nil
}
func validDuration(d time.Duration) bool {
	return d == 2500*time.Microsecond || d == 5*time.Millisecond || d == 10*time.Millisecond || d == 20*time.Millisecond || d == 40*time.Millisecond || d == 60*time.Millisecond
}

type packetSource struct {
	read  func() (*rtp.Packet, error)
	close func() error
}

func adaptSource(value any) (packetSource, error) {
	if nilValue(value) {
		return packetSource{}, ErrNilInboundRTPTrack
	}
	if s, ok := value.(RTPPacketSource); ok {
		return packetSource{s.ReadRTP, closeSource(value)}, nil
	}
	return packetSource{}, configError("RTP source", fmt.Sprintf("%T", value), "want ReadRTP")
}
func closeSource(value any) func() error {
	if c, ok := value.(interface{ Close() error }); ok {
		return c.Close
	}
	return func() error { return nil }
}
func nilValue(value any) bool {
	return value == nil || reflect.ValueOf(value).Kind() == reflect.Pointer && reflect.ValueOf(value).IsNil()
}

type InboundTrack struct {
	source    packetSource
	decoder   OpusDecoder
	config    trackConfig
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

var _ sharedaudio.InboundMedia = (*InboundTrack)(nil)

func NewInboundTrack(source, opus any, config InboundTrackConfig) (*InboundTrack, error) {
	cfg, err := config.normalize()
	if err != nil {
		return nil, err
	}
	src, err := adaptSource(source)
	if err != nil {
		return nil, err
	}
	if nilValue(opus) {
		return nil, ErrNilOpusDecoder
	}
	decoder, ok := opus.(OpusDecoder)
	if !ok {
		return nil, ErrUnsupportedOpusDecoder
	}
	t := &InboundTrack{source: src, decoder: decoder, config: cfg, frames: make(chan frameResult, cfg.jitterPackets+1), done: make(chan struct{})}
	go t.readLoop()
	return t, nil
}
func (t *InboundTrack) readLoop() {
	events := make(chan packetEvent)
	go t.readSource(events)
	state := playout{track: t, packets: make(map[int64]*rtp.Packet, t.config.jitterPackets)}
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
					t.finish(trackError(ErrInboundTrackSource, "read RTP", event.err))
				}
				return
			}
			if event.packet == nil {
				t.finish(trackError(ErrInvalidInboundRTPPacket, "packet", errors.New("source returned nil without an error")))
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
		p, err := t.source.read()
		select {
		case events <- packetEvent{p, err}:
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
		return ErrInboundTrackClosed
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
		return sharedaudio.PCMFrame{}, ErrInboundTrackClosed
	}
	select {
	case <-ctx.Done():
		return sharedaudio.PCMFrame{}, ctx.Err()
	default:
	}
	select {
	case <-t.done:
		return sharedaudio.PCMFrame{}, ErrInboundTrackClosed
	case <-ctx.Done():
		return sharedaudio.PCMFrame{}, ctx.Err()
	case result, ok := <-t.frames:
		if t.closed.Load() {
			return sharedaudio.PCMFrame{}, ErrInboundTrackClosed
		}
		if !ok {
			return sharedaudio.PCMFrame{}, io.EOF
		}
		if result.err != nil {
			return sharedaudio.PCMFrame{}, result.err
		}
		if len(result.frame.Samples) != t.config.outputSamples {
			return sharedaudio.PCMFrame{}, trackError(ErrInboundTrackFrame, "frame", fmt.Errorf("got %d samples, want %d", len(result.frame.Samples), t.config.outputSamples))
		}
		return result.frame, nil
	}
}
func (t *InboundTrack) Close() error {
	t.closeOnce.Do(func() { t.closed.Store(true); close(t.done); t.closeErr = t.source.close() })
	return t.closeErr
}

type playout struct {
	track                            *InboundTrack
	packets                          map[int64]*rtp.Packet
	have, started                    bool
	baseSeq, minSeq, nextSeq, maxSeq int64
	baseTimestamp                    uint32
	ssrc                             uint32
	payloadType                      uint8
}

func (s *playout) push(packet *rtp.Packet) error {
	if packet.Version != 2 {
		return trackError(ErrInvalidInboundRTPPacket, "packet", fmt.Errorf("version %d: want RTP version 2", packet.Version))
	}
	if !s.have {
		sequence := int64(packet.SequenceNumber)
		s.have, s.baseSeq, s.minSeq, s.baseTimestamp, s.maxSeq = true, sequence, sequence, packet.Timestamp, sequence
		s.ssrc, s.payloadType = packet.SSRC, packet.PayloadType
	}
	ext := unwrapSequence(packet.SequenceNumber, s.maxSeq)
	if s.started && ext < s.nextSeq {
		return nil
	}
	if _, ok := s.packets[ext]; ok {
		return nil
	}
	if packet.SSRC != s.ssrc {
		return trackError(ErrInvalidInboundRTPPacket, "packet", fmt.Errorf("SSRC %d changed within one audio track", packet.SSRC))
	}
	if packet.PayloadType != s.payloadType {
		return trackError(ErrInvalidInboundRTPPacket, "packet", fmt.Errorf("payload type %d changed within one audio track", packet.PayloadType))
	}
	expected := s.expectedTimestamp(ext)
	if packet.Timestamp != expected {
		return trackError(ErrImpossibleRTPProgress, "RTP progress", fmt.Errorf("sequence %d timestamp %d: want %d", packet.SequenceNumber, packet.Timestamp, expected))
	}
	if !s.started {
		if ext < s.minSeq {
			if s.minSeq-ext > int64(s.track.config.jitterPackets) {
				return trackError(ErrImpossibleRTPProgress, "RTP progress", fmt.Errorf("sequence %d exceeds initial jitter window", packet.SequenceNumber))
			}
			s.minSeq = ext
		} else if ext > s.maxSeq {
			if ext-s.maxSeq > int64(s.track.config.jitterPackets) {
				return trackError(ErrImpossibleRTPProgress, "RTP progress", fmt.Errorf("sequence %d exceeds initial jitter window", packet.SequenceNumber))
			}
			s.maxSeq = ext
		}
	} else if ext-s.nextSeq > int64(s.track.config.jitterPackets) {
		return trackError(ErrImpossibleRTPProgress, "RTP progress", fmt.Errorf("sequence %d exceeds jitter window", packet.SequenceNumber))
	}
	if ext > s.maxSeq {
		s.maxSeq = ext
	}
	s.packets[ext] = clonePacket(packet)
	return nil
}
func (s *playout) tick() error {
	if !s.started {
		s.nextSeq = s.minSeq
		s.started = true
	}
	return s.emitNext()
}
func (s *playout) flush() error {
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
func (s *playout) emitNext() error {
	packet, ok := s.packets[s.nextSeq]
	if ok {
		delete(s.packets, s.nextSeq)
	}
	if !ok {
		if !s.started {
			return nil
		}
	}
	payload := []byte(nil)
	if ok {
		payload = packet.Payload
	}
	samples, err := s.decode(payload, !ok)
	if err != nil {
		return err
	}
	if err := s.track.emit(samples); err != nil {
		return err
	}
	s.nextSeq++
	return nil
}
func (s *playout) decode(payload []byte, plc bool) ([]int16, error) {
	samples, err := func() ([]int16, error) {
		if plc {
			return s.track.decoder.DecodePLC()
		}
		return s.track.decoder.Decode(payload)
	}()
	if err != nil {
		return nil, trackError(ErrInboundTrackDecode, "decode", err)
	}
	if len(samples) != s.track.config.codecSamples {
		return nil, trackError(ErrInboundTrackFrame, "decode", fmt.Errorf("got %d samples, want %d", len(samples), s.track.config.codecSamples))
	}
	owned := append([]int16(nil), samples...)
	if s.track.config.rate == CodecSampleRate {
		return owned, nil
	}
	resampled, err := s.track.config.resample(owned, CodecSampleRate, s.track.config.rate)
	if err != nil {
		return nil, trackError(ErrInboundTrackResample, "resample", err)
	}
	if len(resampled) != s.track.config.outputSamples {
		return nil, trackError(ErrInboundTrackFrame, "resample", fmt.Errorf("got %d samples, want %d", len(resampled), s.track.config.outputSamples))
	}
	return resampled, nil
}
func (s *playout) expectedTimestamp(sequence int64) uint32 {
	return uint32(int64(s.baseTimestamp) + (sequence-s.baseSeq)*int64(s.track.config.codecSamples))
}
func trackError(kind error, operation string, err error) *InboundTrackError {
	return &InboundTrackError{Operation: operation, Kind: kind, Err: err}
}
func configError(field string, observed any, reason string) error {
	return trackError(ErrInvalidInboundTrackConfig, "configuration", fmt.Errorf("%s: got %v (%s)", field, observed, reason))
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
