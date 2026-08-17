package rtc

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
	"github.com/pion/webrtc/v4"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

const (
	// CodecSampleRate is the mono PCM rate produced by an Opus decoder.
	CodecSampleRate = 48000
	// DefaultInboundLoopSampleRate is the default output rate.
	DefaultInboundLoopSampleRate = CodecSampleRate
	// DefaultInboundFrameDuration is one default Opus packet.
	DefaultInboundFrameDuration = 20 * time.Millisecond
	// DefaultInboundJitterDepth holds three default-sized packets.
	DefaultInboundJitterDepth = 60 * time.Millisecond
	DefaultJitterDepth        = DefaultInboundJitterDepth
	DefaultJitterBufferDepth  = DefaultInboundJitterDepth
	maxInboundJitterDepth     = 2 * time.Second
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
	ErrInboundTrackFrame         = errors.New("inbound PCM frame has invalid size")
	ErrInboundTrackClosed        = errors.New("inbound RTP audio track is closed")

	ErrTrackClosed        = ErrInboundTrackClosed
	ErrInvalidTrackConfig = ErrInvalidInboundTrackConfig
)

// InboundTrackConfig controls an inbound Opus/RTP endpoint. Zero values use defaults.
type InboundTrackConfig struct {
	SampleRate        int
	LoopSampleRate    int
	OutputSampleRate  int
	FrameDuration     time.Duration
	JitterDepth       time.Duration
	JitterBufferDepth time.Duration
}

// DefaultInboundTrackConfig returns a complete valid configuration.
func DefaultInboundTrackConfig() InboundTrackConfig {
	return InboundTrackConfig{
		SampleRate:        DefaultInboundLoopSampleRate,
		FrameDuration:     DefaultInboundFrameDuration,
		JitterDepth:       DefaultInboundJitterDepth,
		JitterBufferDepth: DefaultInboundJitterDepth,
	}
}

// InboundTrackConfigError identifies a rejected configuration field.
type InboundTrackConfigError struct {
	Field    string
	Observed any
	Reason   string
}

func (e *InboundTrackConfigError) Error() string {
	return fmt.Sprintf("invalid inbound track %s: got %v (%s)", e.Field, e.Observed, e.Reason)
}
func (e *InboundTrackConfigError) Unwrap() error { return ErrInvalidInboundTrackConfig }

// InboundTrackError wraps an operation failure while preserving its cause.
type InboundTrackError struct {
	Operation string
	Err       error
}

func (e *InboundTrackError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("inbound RTP track %s failed", e.Operation)
	}
	return fmt.Sprintf("inbound RTP track %s failed: %v", e.Operation, e.Err)
}

func (e *InboundTrackError) Unwrap() error { return e.Err }
func (e *InboundTrackError) Is(target error) bool {
	return target == ErrInboundTrackSource || errors.Is(e.Err, target)
}

// OpusDecodeError distinguishes a codec failure while preserving its cause.
type OpusDecodeError struct {
	PLC bool
	Err error
}

func (e *OpusDecodeError) Error() string {
	operation := "decode"
	if e.PLC {
		operation = "packet-loss concealment"
	}
	return fmt.Sprintf("Opus %s failed: %v", operation, e.Err)
}

func (e *OpusDecodeError) Unwrap() error { return e.Err }
func (e *OpusDecodeError) Is(target error) bool {
	return target == ErrInboundTrackDecode || errors.Is(e.Err, target)
}

// InboundRTPPacketError identifies malformed packet metadata before decode.
type InboundRTPPacketError struct {
	Field    string
	Observed any
	Reason   string
}

func (e *InboundRTPPacketError) Error() string {
	return fmt.Sprintf("invalid inbound RTP %s: got %v (%s)", e.Field, e.Observed, e.Reason)
}
func (e *InboundRTPPacketError) Unwrap() error { return ErrInvalidInboundRTPPacket }

// RTPProgressError reports a sequence/timestamp pair that cannot belong to
// the configured one-packet-per-frame Opus cadence.
type RTPProgressError struct {
	Sequence          uint16
	Timestamp         uint32
	ExpectedTimestamp uint32
	Reason            string
}

func (e *RTPProgressError) Error() string {
	return fmt.Sprintf("impossible RTP progress at sequence %d timestamp %d: want %d (%s)", e.Sequence, e.Timestamp, e.ExpectedTimestamp, e.Reason)
}
func (e *RTPProgressError) Unwrap() error { return ErrImpossibleRTPProgress }

// OpusDecoder is the slice-returning Opus decoder seam; DecodePLC returns one codec frame.
type OpusDecoder interface {
	Decode(payload []byte) ([]int16, error)
	DecodePLC() ([]int16, error)
}

// BufferedOpusDecoder describes a decoder that writes into a caller buffer.
type BufferedOpusDecoder interface {
	Decode(payload []byte, pcm []int16) (int, error)
	DecodePLC(pcm []int16) (int, error)
}

// BufferedOpusDecoderWithFEC is a common Opus binding shape.
type BufferedOpusDecoderWithFEC interface {
	Decode(payload []byte, pcm []int16, fec bool) (int, error)
	DecodePLC(pcm []int16) (int, error)
}

// OpusDecoderWithFEC is a slice-returning decoder with an FEC flag.
type OpusDecoderWithFEC interface {
	Decode(payload []byte, fec bool) ([]int16, error)
	DecodePLC() ([]int16, error)
}

// RTPPacketSource is the minimal inbound RTP source seam.
type RTPPacketSource interface {
	ReadRTP() (*rtp.Packet, error)
}

// RTPTrack is an alias for RTPPacketSource.
type RTPTrack = RTPPacketSource

// ContextRTPPacketSource is a cancellation-aware source seam.
type ContextRTPPacketSource interface {
	ReadRTP(context.Context) (*rtp.Packet, error)
}

// RTPPacketSourceFunc adapts a function into RTPPacketSource.
type RTPPacketSourceFunc func() (*rtp.Packet, error)

func (f RTPPacketSourceFunc) ReadRTP() (*rtp.Packet, error) { return f() }

// PionTrackSource adapts Pion's TrackRemote to RTPPacketSource.
type PionTrackSource struct {
	track     *webrtc.TrackRemote
	closeOnce sync.Once
	closeErr  error
}

// NewPionTrackSource validates and wraps a Pion inbound track.
func NewPionTrackSource(track *webrtc.TrackRemote) (*PionTrackSource, error) {
	if track == nil {
		return nil, ErrNilInboundRTPTrack
	}
	return &PionTrackSource{track: track}, nil
}

func (s *PionTrackSource) ReadRTP() (*rtp.Packet, error) {
	if s == nil || s.track == nil {
		return nil, ErrNilInboundRTPTrack
	}
	packet, _, err := s.track.ReadRTP()
	return packet, err
}

// Close sets a read deadline so a blocked TrackRemote read is released.
func (s *PionTrackSource) Close() error {
	if s == nil || s.track == nil {
		return ErrNilInboundRTPTrack
	}
	s.closeOnce.Do(func() { s.closeErr = s.track.SetReadDeadline(time.Now()) })
	return s.closeErr
}

type normalizedInboundTrackConfig struct {
	loopRate      int
	codecSamples  int
	outputSamples int
	jitterPackets int
}

func (c InboundTrackConfig) normalize() (normalizedInboundTrackConfig, error) {
	rate, err := chooseConfig("sample rate", c.SampleRate, c.LoopSampleRate, c.OutputSampleRate)
	if err != nil {
		return normalizedInboundTrackConfig{}, err
	}
	if rate == 0 {
		rate = DefaultInboundLoopSampleRate
	}
	if rate != wavio.Rate16kHz && rate != wavio.Rate24kHz && rate != wavio.Rate48kHz {
		return normalizedInboundTrackConfig{}, &InboundTrackConfigError{Field: "sample rate", Observed: rate, Reason: "want 16000, 24000, or 48000 Hz"}
	}

	frameDuration := c.FrameDuration
	if frameDuration == 0 {
		frameDuration = DefaultInboundFrameDuration
	}
	if !validOpusFrameDuration(frameDuration) {
		return normalizedInboundTrackConfig{}, &InboundTrackConfigError{Field: "frame duration", Observed: frameDuration, Reason: "want a legal Opus duration from 2.5 ms through 60 ms"}
	}

	jitterDepth, err := chooseConfig("jitter depth", c.JitterDepth, c.JitterBufferDepth)
	if err != nil {
		return normalizedInboundTrackConfig{}, err
	}
	if jitterDepth == 0 {
		jitterDepth = DefaultInboundJitterDepth
	}
	if jitterDepth <= 0 || jitterDepth > maxInboundJitterDepth || jitterDepth%frameDuration != 0 {
		return normalizedInboundTrackConfig{}, &InboundTrackConfigError{Field: "jitter depth", Observed: jitterDepth, Reason: "must be positive, bounded, and frame-aligned"}
	}

	codecSamples64 := int64(CodecSampleRate) * int64(frameDuration) / int64(time.Second)
	outputSamples64 := int64(rate) * int64(frameDuration) / int64(time.Second)
	if codecSamples64 <= 0 || outputSamples64 <= 0 || codecSamples64 > int64(int(^uint(0)>>1)) || outputSamples64 > int64(int(^uint(0)>>1)) {
		return normalizedInboundTrackConfig{}, &InboundTrackConfigError{Field: "frame duration", Observed: frameDuration, Reason: "sample count is not representable"}
	}

	return normalizedInboundTrackConfig{
		loopRate:      rate,
		codecSamples:  int(codecSamples64),
		outputSamples: int(outputSamples64),
		jitterPackets: int(jitterDepth / frameDuration),
	}, nil
}

func chooseConfig[T comparable](field string, values ...T) (T, error) {
	var chosen, zero T
	for _, value := range values {
		if value == zero {
			continue
		}
		if chosen != zero && chosen != value {
			return zero, &InboundTrackConfigError{Field: field, Observed: values, Reason: "equivalent fields disagree"}
		}
		chosen = value
	}
	return chosen, nil
}

func validOpusFrameDuration(duration time.Duration) bool {
	switch duration {
	case 2500 * time.Microsecond, 5 * time.Millisecond, 10 * time.Millisecond,
		20 * time.Millisecond, 40 * time.Millisecond, 60 * time.Millisecond:
		return true
	default:
		return false
	}
}

type opusDecoderAdapter struct {
	decode func(payload []byte, plc bool) ([]int16, error)
}

func adaptOpusDecoder(value any, codecSamples int) (opusDecoderAdapter, error) {
	if isNil(value) {
		return opusDecoderAdapter{}, ErrNilOpusDecoder
	}
	switch decoder := value.(type) {
	case OpusDecoder:
		return opusDecoderAdapter{decode: func(payload []byte, plc bool) ([]int16, error) {
			if plc {
				return decoder.DecodePLC()
			}
			return decoder.Decode(payload)
		}}, nil
	case OpusDecoderWithFEC:
		return opusDecoderAdapter{decode: func(payload []byte, plc bool) ([]int16, error) {
			if plc {
				return decoder.DecodePLC()
			}
			return decoder.Decode(payload, false)
		}}, nil
	case BufferedOpusDecoderWithFEC:
		return bufferedDecoderAdapter(codecSamples, func(payload []byte, pcm []int16, plc bool) (int, error) {
			if plc {
				return decoder.DecodePLC(pcm)
			}
			return decoder.Decode(payload, pcm, false)
		}), nil
	case BufferedOpusDecoder:
		return bufferedDecoderAdapter(codecSamples, func(payload []byte, pcm []int16, plc bool) (int, error) {
			if plc {
				return decoder.DecodePLC(pcm)
			}
			return decoder.Decode(payload, pcm)
		}), nil
	default:
		return opusDecoderAdapter{}, ErrUnsupportedOpusDecoder
	}
}

func bufferedDecoderAdapter(codecSamples int, decode func([]byte, []int16, bool) (int, error)) opusDecoderAdapter {
	return opusDecoderAdapter{decode: func(payload []byte, plc bool) ([]int16, error) {
		pcm := make([]int16, codecSamples)
		count, err := decode(payload, pcm, plc)
		if err != nil {
			return nil, err
		}
		if count < 0 || count > len(pcm) {
			return nil, &InboundTrackError{Operation: "decode", Err: fmt.Errorf("decoder returned %d samples", count)}
		}
		return pcm[:count], nil
	}}
}

type packetSourceAdapter struct {
	read  func(context.Context) (*rtp.Packet, error)
	close func() error
}

func adaptPacketSource(value any) (packetSourceAdapter, error) {
	if isNil(value) {
		return packetSourceAdapter{}, ErrNilInboundRTPTrack
	}
	if source, ok := value.(ContextRTPPacketSource); ok {
		return packetSourceAdapter{
			read:  source.ReadRTP,
			close: sourceClose(value),
		}, nil
	}
	if source, ok := value.(RTPPacketSource); ok {
		return packetSourceAdapter{
			read:  func(context.Context) (*rtp.Packet, error) { return source.ReadRTP() },
			close: sourceClose(value),
		}, nil
	}
	if source, ok := value.(interface{ ReadPacket() (*rtp.Packet, error) }); ok {
		return packetSourceAdapter{
			read:  func(context.Context) (*rtp.Packet, error) { return source.ReadPacket() },
			close: sourceClose(value),
		}, nil
	}
	if track, ok := value.(*webrtc.TrackRemote); ok {
		pionSource, err := NewPionTrackSource(track)
		if err != nil {
			return packetSourceAdapter{}, err
		}
		return adaptPacketSource(pionSource)
	}
	return packetSourceAdapter{}, &InboundTrackConfigError{Field: "RTP source", Observed: fmt.Sprintf("%T", value), Reason: "want ReadRTP or ReadPacket"}
}

func sourceClose(value any) func() error {
	if closer, ok := value.(interface{ Close() error }); ok {
		return closer.Close
	}
	if closer, ok := value.(io.Closer); ok {
		return closer.Close
	}
	return func() error { return nil }
}

func isNil(value any) bool {
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

// InboundTrack is a caller-owned PCM ingress endpoint backed by one RTP audio
// source. It implements InboundMedia.
type InboundTrack struct {
	source  packetSourceAdapter
	decoder opusDecoderAdapter
	config  normalizedInboundTrackConfig

	frames  chan inboundFrameResult
	done    chan struct{}
	readCtx context.Context
	cancel  context.CancelFunc

	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error

	terminalMu  sync.RWMutex
	terminalErr error
}

var _ InboundMedia = (*InboundTrack)(nil)

type inboundFrameResult struct {
	frame PCMFrame
	err   error
}

// NewInboundTrack creates a bounded Opus/RTP ingress endpoint. Validation is
// completed before the reader goroutine starts or any source reads occur.
func NewInboundTrack(source any, decoder any, config InboundTrackConfig) (*InboundTrack, error) {
	normalized, err := config.normalize()
	if err != nil {
		return nil, err
	}
	packetSource, err := adaptPacketSource(source)
	if err != nil {
		return nil, err
	}
	decoderAdapter, err := adaptOpusDecoder(decoder, normalized.codecSamples)
	if err != nil {
		return nil, err
	}

	readCtx, cancel := context.WithCancel(context.Background())
	track := &InboundTrack{
		source:  packetSource,
		decoder: decoderAdapter,
		config:  normalized,
		frames:  make(chan inboundFrameResult, normalized.jitterPackets+1),
		done:    make(chan struct{}),
		readCtx: readCtx,
		cancel:  cancel,
	}
	go track.readLoop()
	return track, nil
}

// NewInboundAudioTrack is the descriptive constructor spelling.
func NewInboundAudioTrack(source any, decoder any, config InboundTrackConfig) (*InboundTrack, error) {
	return NewInboundTrack(source, decoder, config)
}

// NewInboundMedia is a constructor alias for callers that program to the
// existing contract name.
func NewInboundMedia(source any, decoder any, config InboundTrackConfig) (InboundMedia, error) {
	return NewInboundTrack(source, decoder, config)
}

func (t *InboundTrack) readLoop() {
	state := inboundPlayoutState{
		track:   t,
		packets: make(map[int64]*rtp.Packet, t.config.jitterPackets),
	}
	for {
		packet, err := t.source.read(t.readCtx)
		if err != nil {
			if t.closed.Load() {
				t.closeFrames()
				return
			}
			if flushErr := state.flush(); flushErr != nil {
				t.finish(flushErr)
				return
			}
			t.finish(&InboundTrackError{Operation: "read RTP", Err: err})
			return
		}
		if packet == nil {
			t.finish(&InboundRTPPacketError{Field: "packet", Observed: nil, Reason: "source returned nil without an error"})
			return
		}
		if err := state.push(packet); err != nil {
			t.finish(err)
			return
		}
	}
}

func (t *InboundTrack) emit(samples []int16) error {
	result := inboundFrameResult{frame: PCMFrame{Samples: samples}}
	select {
	case t.frames <- result:
		return nil
	case <-t.done:
		return ErrInboundTrackClosed
	}
}

func (t *InboundTrack) finish(err error) {
	if err == nil {
		err = io.EOF
	}
	t.terminalMu.Lock()
	t.terminalErr = err
	t.terminalMu.Unlock()
	select {
	case t.frames <- inboundFrameResult{err: err}:
	case <-t.done:
	}
	t.closeFrames()
}

func (t *InboundTrack) closeFrames() {
	close(t.frames)
}

// ReadFrame returns one fresh, fixed-duration mono PCM16 frame.
func (t *InboundTrack) ReadFrame(ctx context.Context) (PCMFrame, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t.closed.Load() {
		return PCMFrame{}, ErrInboundTrackClosed
	}
	select {
	case <-ctx.Done():
		return PCMFrame{}, ctx.Err()
	default:
	}
	select {
	case <-t.done:
		return PCMFrame{}, ErrInboundTrackClosed
	case <-ctx.Done():
		return PCMFrame{}, ctx.Err()
	case result, ok := <-t.frames:
		if t.closed.Load() {
			return PCMFrame{}, ErrInboundTrackClosed
		}
		if !ok {
			return PCMFrame{}, t.terminal()
		}
		if result.err != nil {
			return PCMFrame{}, result.err
		}
		if len(result.frame.Samples) != t.config.outputSamples {
			return PCMFrame{}, &InboundTrackError{Operation: "frame", Err: fmt.Errorf("got %d samples, want %d", len(result.frame.Samples), t.config.outputSamples)}
		}
		return result.frame, nil
	}
}

// Close is idempotent, stops future delivery, and asks a closeable source to
// unblock its read. A source close failure keeps its errors.Is identity.
func (t *InboundTrack) Close() error {
	t.closeOnce.Do(func() {
		t.closed.Store(true)
		close(t.done)
		t.cancel()
		t.closeErr = t.source.close()
	})
	return t.closeErr
}

func (t *InboundTrack) terminal() error {
	t.terminalMu.RLock()
	defer t.terminalMu.RUnlock()
	if t.terminalErr == nil {
		return io.EOF
	}
	return t.terminalErr
}

type inboundPlayoutState struct {
	track   *InboundTrack
	packets map[int64]*rtp.Packet

	started       bool
	baseSeq       int64
	nextSeq       int64
	latestSeq     int64
	haveSequence  bool
	baseTimestamp uint32
	frameTicks    int64

	ssrc         uint32
	payloadType  uint8
	metadataSeen bool
}

func (s *inboundPlayoutState) push(packet *rtp.Packet) error {
	if packet.Version != 0 && packet.Version != 2 {
		return &InboundRTPPacketError{Field: "version", Observed: packet.Version, Reason: "want RTP version 2"}
	}
	if !s.haveSequence {
		s.baseSeq = int64(packet.SequenceNumber)
		s.latestSeq = s.baseSeq
		s.haveSequence = true
		s.baseTimestamp = packet.Timestamp
		s.frameTicks = int64(s.track.config.codecSamples)
		s.ssrc = packet.SSRC
		s.payloadType = packet.PayloadType
		s.metadataSeen = true
	}
	extSeq := unwrapSequence(packet.SequenceNumber, s.latestSeq)
	if extSeq > s.latestSeq {
		s.latestSeq = extSeq
	}
	if s.started && extSeq < s.nextSeq {
		return nil
	}
	if _, duplicate := s.packets[extSeq]; duplicate {
		return nil
	}
	if s.metadataSeen && s.ssrc != 0 && packet.SSRC != 0 && packet.SSRC != s.ssrc {
		return &InboundRTPPacketError{Field: "SSRC", Observed: packet.SSRC, Reason: "changed within one audio track"}
	}
	if expected := s.expectedTimestamp(extSeq); packet.Timestamp != expected {
		return &RTPProgressError{Sequence: packet.SequenceNumber, Timestamp: packet.Timestamp, ExpectedTimestamp: expected, Reason: "timestamp is not one Opus frame per sequence"}
	}
	if s.started && extSeq-s.nextSeq > int64(s.track.config.jitterPackets) {
		return &RTPProgressError{Sequence: packet.SequenceNumber, Timestamp: packet.Timestamp, ExpectedTimestamp: s.expectedTimestamp(extSeq), Reason: "packet exceeds bounded jitter window"}
	}
	s.packets[extSeq] = cloneRTPPacket(packet)
	if !s.started {
		if max, min := s.packetRange(); max-min > int64(s.track.config.jitterPackets) {
			return &RTPProgressError{Sequence: packet.SequenceNumber, Timestamp: packet.Timestamp, ExpectedTimestamp: s.expectedTimestamp(extSeq), Reason: "initial packets exceed bounded jitter window"}
		}
		if len(s.packets) >= s.track.config.jitterPackets {
			s.started = true
			_, s.nextSeq = s.packetRange()
		}
	}
	return s.drain(false)
}

func (s *inboundPlayoutState) flush() error {
	if len(s.packets) == 0 {
		return nil
	}
	if !s.started {
		s.started = true
		_, s.nextSeq = s.packetRange()
	}
	return s.drain(true)
}

func (s *inboundPlayoutState) drain(force bool) error {
	for s.started {
		packet, present := s.packets[s.nextSeq]
		plc := !present
		if !present {
			if len(s.packets) == 0 {
				return nil
			}
			maxSeq, _ := s.packetRange()
			if !force && maxSeq-s.nextSeq < int64(s.track.config.jitterPackets-1) {
				return nil
			}
		} else {
			delete(s.packets, s.nextSeq)
		}

		var payload []byte
		if !plc {
			payload = packet.Payload
		}
		samples, err := s.decode(payload, plc)
		if err != nil {
			return err
		}
		if err := s.track.emit(samples); err != nil {
			return err
		}
		s.nextSeq++
	}
	return nil
}

func (s *inboundPlayoutState) decode(payload []byte, plc bool) ([]int16, error) {
	samples, err := s.track.decoder.decode(payload, plc)
	if err != nil {
		return nil, &OpusDecodeError{PLC: plc, Err: err}
	}
	if len(samples) != s.track.config.codecSamples {
		return nil, &InboundTrackError{Operation: "decode", Err: &InboundTrackFrameSizeError{Observed: len(samples), Expected: s.track.config.codecSamples}}
	}
	owned := append([]int16(nil), samples...)
	if s.track.config.loopRate == CodecSampleRate {
		return owned, nil
	}
	resampled, err := wavio.Resample(owned, CodecSampleRate, s.track.config.loopRate)
	if err != nil {
		return nil, &InboundTrackError{Operation: "resample", Err: err}
	}
	if len(resampled) != s.track.config.outputSamples {
		return nil, &InboundTrackError{Operation: "resample", Err: &InboundTrackFrameSizeError{Observed: len(resampled), Expected: s.track.config.outputSamples}}
	}
	return resampled, nil
}

// InboundTrackFrameSizeError identifies a decoder/resampler cadence violation.
type InboundTrackFrameSizeError struct {
	Observed int
	Expected int
}

func (e *InboundTrackFrameSizeError) Error() string {
	return fmt.Sprintf("inbound frame has %d samples, want %d", e.Observed, e.Expected)
}

func (e *InboundTrackFrameSizeError) Unwrap() error { return ErrInboundTrackFrame }

func (e *InboundTrackFrameSizeError) Is(target error) bool {
	return target == ErrInboundTrackFrame
}

func (s *inboundPlayoutState) expectedTimestamp(extSeq int64) uint32 {
	return uint32(int64(s.baseTimestamp) + (extSeq-s.baseSeq)*s.frameTicks)
}

func (s *inboundPlayoutState) packetRange() (maxSeq, minSeq int64) {
	first := true
	for sequence := range s.packets {
		if first || sequence > maxSeq {
			maxSeq = sequence
		}
		if first || sequence < minSeq {
			minSeq = sequence
		}
		first = false
	}
	return maxSeq, minSeq
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

func cloneRTPPacket(packet *rtp.Packet) *rtp.Packet {
	cloned := *packet
	cloned.Payload = append([]byte(nil), packet.Payload...)
	cloned.CSRC = append([]uint32(nil), packet.CSRC...)
	return &cloned
}
