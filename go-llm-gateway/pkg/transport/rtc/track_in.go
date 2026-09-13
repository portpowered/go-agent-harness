package rtc

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/pion/rtp"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
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

// InboundTrackConfig is retained for source compatibility. New runtime code
// should use services/rtctransport, whose service owns jitter and playout.
type InboundTrackConfig struct {
	SampleRate    int
	FrameDuration time.Duration
	JitterDepth   time.Duration
	NewTimer      func(time.Duration) <-chan time.Time
	Resample      func([]int16, int, int) ([]int16, error)
}

func DefaultInboundTrackConfig() InboundTrackConfig {
	return InboundTrackConfig{SampleRate: DefaultInboundLoopSampleRate, FrameDuration: DefaultInboundFrameDuration, JitterDepth: DefaultInboundJitterDepth}
}

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

type OpusDecoder interface {
	Decode([]byte) ([]int16, error)
	DecodePLC() ([]int16, error)
}

type RTPPacketSource interface{ ReadRTP() (*rtp.Packet, error) }

type inboundConfig struct {
	rate, codecSamples, outputSamples int
	resample                          func([]int16, int, int) ([]int16, error)
}

func normalizeInboundConfig(config InboundTrackConfig) (inboundConfig, error) {
	rate := config.SampleRate
	if rate == 0 {
		rate = CodecSampleRate
	}
	if rate != wavio.Rate16kHz && rate != wavio.Rate24kHz && rate != wavio.Rate48kHz {
		return inboundConfig{}, inboundConfigError("sample rate", rate, "want 16000, 24000, or 48000 Hz")
	}
	duration := config.FrameDuration
	if duration == 0 {
		duration = DefaultInboundFrameDuration
	}
	if !validInboundDuration(duration) {
		return inboundConfig{}, inboundConfigError("frame duration", duration, "want a legal Opus duration")
	}
	depth := config.JitterDepth
	if depth == 0 {
		depth = DefaultInboundJitterDepth
	}
	if depth <= 0 || depth > maxInboundJitterDepth || depth%duration != 0 {
		return inboundConfig{}, inboundConfigError("jitter depth", depth, "must be positive, bounded, and frame-aligned")
	}
	resample := config.Resample
	if resample == nil {
		resample = wavio.Resample
	}
	return inboundConfig{
		rate: rate, codecSamples: int(int64(CodecSampleRate) * int64(duration) / int64(time.Second)),
		outputSamples: int(int64(rate) * int64(duration) / int64(time.Second)), resample: resample,
	}, nil
}

func validInboundDuration(duration time.Duration) bool {
	return duration == 2500*time.Microsecond || duration == 5*time.Millisecond || duration == 10*time.Millisecond || duration == 20*time.Millisecond || duration == 40*time.Millisecond || duration == 60*time.Millisecond
}

func inboundConfigError(field string, observed any, reason string) error {
	return &InboundTrackError{Operation: "configuration", Kind: ErrInvalidInboundTrackConfig, Err: fmt.Errorf("%s: got %v (%s)", field, observed, reason)}
}

func nilInboundValue(value any) bool {
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

// InboundTrack is a compatibility adapter for legacy gateway callers. It
// performs only the one-packet Pion/codec bridge; runtime compositions use the
// service package for validation, continuity, playout, queues, and lifecycle.
type InboundTrack struct {
	source    RTPPacketSource
	close     func() error
	decoder   OpusDecoder
	config    inboundConfig
	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
	closeErr  error
	have      bool
	sequence  uint16
	timestamp uint32
	ssrc      uint32
	payload   uint8
}

var _ sharedaudio.InboundMedia = (*InboundTrack)(nil)

func NewInboundTrack(source, opus any, config InboundTrackConfig) (*InboundTrack, error) {
	cfg, err := normalizeInboundConfig(config)
	if err != nil {
		return nil, err
	}
	if nilInboundValue(source) {
		return nil, ErrNilInboundRTPTrack
	}
	packetSource, ok := source.(RTPPacketSource)
	if !ok {
		return nil, inboundConfigError("RTP source", fmt.Sprintf("%T", source), "want ReadRTP")
	}
	if nilInboundValue(opus) {
		return nil, ErrNilOpusDecoder
	}
	decoder, ok := opus.(OpusDecoder)
	if !ok {
		return nil, ErrUnsupportedOpusDecoder
	}
	closer, ok := source.(interface{ Close() error })
	closeFn := func() error { return nil }
	if ok {
		closeFn = closer.Close
	}
	return &InboundTrack{source: packetSource, close: closeFn, decoder: decoder, config: cfg}, nil
}

func (t *InboundTrack) ReadFrame(ctx context.Context) (sharedaudio.PCMFrame, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return sharedaudio.PCMFrame{}, err
	}
	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed {
		return sharedaudio.PCMFrame{}, ErrInboundTrackClosed
	}
	packet, err := t.source.ReadRTP()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return sharedaudio.PCMFrame{}, ctxErr
		}
		return sharedaudio.PCMFrame{}, &InboundTrackError{Operation: "read RTP", Kind: ErrInboundTrackSource, Err: err}
	}
	if packet == nil || packet.Version != 2 {
		return sharedaudio.PCMFrame{}, &InboundTrackError{Operation: "packet", Kind: ErrInvalidInboundRTPPacket, Err: fmt.Errorf("packet must use RTP version 2")}
	}
	if err := t.validatePacket(packet); err != nil {
		return sharedaudio.PCMFrame{}, err
	}
	samples, err := t.decoder.Decode(packet.Payload)
	if err != nil {
		return sharedaudio.PCMFrame{}, &InboundTrackError{Operation: "decode", Kind: ErrInboundTrackDecode, Err: err}
	}
	if len(samples) != t.config.codecSamples {
		return sharedaudio.PCMFrame{}, &InboundTrackError{Operation: "decode", Kind: ErrInboundTrackFrame, Err: fmt.Errorf("got %d samples, want %d", len(samples), t.config.codecSamples)}
	}
	owned := append([]int16(nil), samples...)
	if t.config.rate == CodecSampleRate {
		return sharedaudio.PCMFrame{Samples: owned}, nil
	}
	resampled, err := t.config.resample(owned, CodecSampleRate, t.config.rate)
	if err != nil {
		return sharedaudio.PCMFrame{}, &InboundTrackError{Operation: "resample", Kind: ErrInboundTrackResample, Err: err}
	}
	if len(resampled) != t.config.outputSamples {
		return sharedaudio.PCMFrame{}, &InboundTrackError{Operation: "resample", Kind: ErrInboundTrackFrame, Err: fmt.Errorf("got %d samples, want %d", len(resampled), t.config.outputSamples)}
	}
	return sharedaudio.PCMFrame{Samples: append([]int16(nil), resampled...)}, nil
}

func (t *InboundTrack) validatePacket(packet *rtp.Packet) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return ErrInboundTrackClosed
	}
	if !t.have {
		t.have, t.sequence, t.timestamp, t.ssrc, t.payload = true, packet.SequenceNumber, packet.Timestamp, packet.SSRC, packet.PayloadType
		return nil
	}
	if packet.SSRC != t.ssrc || packet.PayloadType != t.payload {
		return &InboundTrackError{Operation: "packet", Kind: ErrInvalidInboundRTPPacket, Err: errors.New("RTP identity changed within one track")}
	}
	if packet.SequenceNumber != t.sequence+1 || packet.Timestamp != t.timestamp+uint32(t.config.codecSamples) {
		return &InboundTrackError{Operation: "RTP progress", Kind: ErrImpossibleRTPProgress, Err: errors.New("RTP sequence or timestamp did not advance by one frame")}
	}
	t.sequence, t.timestamp = packet.SequenceNumber, packet.Timestamp
	return nil
}

func (t *InboundTrack) Close() error {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.closed = true
		t.mu.Unlock()
		t.closeErr = t.close()
	})
	return t.closeErr
}
