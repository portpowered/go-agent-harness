package rtc

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/pion/opus"
)

const (
	// OpusSampleRate is the only sample rate supported by the RTC Opus seam.
	OpusSampleRate = 48000
	// OpusChannels is the number of channels in the RTC Opus seam.
	OpusChannels = 1
	// OpusFrameDuration is the duration of one RTC Opus packet.
	OpusFrameDuration = 20 * time.Millisecond
	// OpusFrameSamples is the number of mono samples in one RTC Opus packet.
	OpusFrameSamples = OpusSampleRate / 50

	defaultOpusBitrate = 128000
	maxOpusPacketBytes = 1275
)

var (
	// ErrOpusInvalidConfiguration identifies an unsupported RTC Opus setup.
	ErrOpusInvalidConfiguration = errors.New("invalid RTC Opus configuration")
	// ErrOpusEmptyPCM identifies an encode call without samples.
	ErrOpusEmptyPCM = errors.New("RTC Opus PCM is empty")
	// ErrOpusInvalidPCM identifies PCM with a size other than one 20 ms frame.
	ErrOpusInvalidPCM = errors.New("RTC Opus PCM frame has invalid size")
	// ErrOpusEmptyPayload identifies a decode call without an Opus packet or an
	// encoder that returned no packet bytes.
	ErrOpusEmptyPayload = errors.New("RTC Opus payload is empty")
	// ErrOpusInvalidPayload identifies a packet that does not decode to one frame.
	ErrOpusInvalidPayload = errors.New("RTC Opus payload has invalid frame size")
	// ErrOpusNoHistory identifies packet-loss concealment before a valid packet.
	ErrOpusNoHistory = errors.New("RTC Opus decoder has no packet history")
	// ErrOpusClosed identifies use of a codec after Close.
	ErrOpusClosed = errors.New("RTC Opus codec is closed")
	// ErrOpusEncode identifies a failure in the underlying encoder.
	ErrOpusEncode = errors.New("RTC Opus encode failed")
	// ErrOpusDecode identifies a failure in the underlying decoder.
	ErrOpusDecode = errors.New("RTC Opus decode failed")
	// ErrOpusPLC identifies a failure in the underlying packet-loss concealment.
	ErrOpusPLC = errors.New("RTC Opus packet-loss concealment failed")
)

// OpusCodecConfig describes the deliberately narrow RTC Opus format.
//
// Zero values select the production defaults. The encoder and decoder accept
// only 48 kHz mono, one 20 ms frame at a time; the fields remain explicit so a
// caller receives a construction-time error when it tries to use another
// negotiated format.
type OpusCodecConfig struct {
	SampleRate    int
	Channels      int
	FrameDuration time.Duration
	Bitrate       int
}

// DefaultOpusCodecConfig returns the supported production configuration.
func DefaultOpusCodecConfig() OpusCodecConfig {
	return OpusCodecConfig{
		SampleRate:    OpusSampleRate,
		Channels:      OpusChannels,
		FrameDuration: OpusFrameDuration,
		Bitrate:       defaultOpusBitrate,
	}
}

// OpusOperationError preserves the operation classification and the exact
// underlying codec error for errors.Is/errors.As callers.
type OpusOperationError struct {
	Operation string
	Kind      error
	Err       error
}

func (e *OpusOperationError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("RTC Opus %s failed", e.Operation)
	}
	return fmt.Sprintf("RTC Opus %s failed: %v", e.Operation, e.Err)
}

func (e *OpusOperationError) Unwrap() error { return e.Err }

func (e *OpusOperationError) Is(target error) bool {
	return target == e.Kind || errors.Is(e.Err, target)
}

func wrapOpus(operation string, kind, err error) error {
	if err == nil {
		return nil
	}
	return &OpusOperationError{Operation: operation, Kind: kind, Err: err}
}

func normalizeOpusConfig(configs []OpusCodecConfig) (OpusCodecConfig, error) {
	if len(configs) > 1 {
		return OpusCodecConfig{}, wrapOpus("configuration", ErrOpusInvalidConfiguration,
			fmt.Errorf("got %d configurations, want at most one", len(configs)))
	}
	config := DefaultOpusCodecConfig()
	if len(configs) == 1 {
		provided := configs[0]
		if provided.SampleRate != 0 {
			config.SampleRate = provided.SampleRate
		}
		if provided.Channels != 0 {
			config.Channels = provided.Channels
		}
		if provided.FrameDuration != 0 {
			config.FrameDuration = provided.FrameDuration
		}
		if provided.Bitrate != 0 {
			config.Bitrate = provided.Bitrate
		}
	}
	if config.SampleRate != OpusSampleRate {
		return OpusCodecConfig{}, wrapOpus("configuration", ErrOpusInvalidConfiguration,
			fmt.Errorf("sample rate %d: want %d", config.SampleRate, OpusSampleRate))
	}
	if config.Channels != OpusChannels {
		return OpusCodecConfig{}, wrapOpus("configuration", ErrOpusInvalidConfiguration,
			fmt.Errorf("channels %d: want %d", config.Channels, OpusChannels))
	}
	if config.FrameDuration != OpusFrameDuration {
		return OpusCodecConfig{}, wrapOpus("configuration", ErrOpusInvalidConfiguration,
			fmt.Errorf("frame duration %s: want %s", config.FrameDuration, OpusFrameDuration))
	}
	if config.Bitrate < 6000 || config.Bitrate > 510000 {
		return OpusCodecConfig{}, wrapOpus("configuration", ErrOpusInvalidConfiguration,
			fmt.Errorf("bitrate %d: want 6000 through 510000", config.Bitrate))
	}
	return config, nil
}

func newPionEncoder(config OpusCodecConfig) (*opus.Encoder, error) {
	return opus.NewEncoder(
		opus.WithSampleRate(OpusSampleRate),
		opus.WithChannels(OpusChannels),
		opus.WithApplication(opus.ApplicationVoIP),
		opus.WithBitrate(config.Bitrate),
		opus.WithVBR(false),
		opus.WithConstrainedVBR(true),
		opus.WithBandwidth(opus.BandwidthFullband),
	)
}

// RTCOpusEncoder is a serialized, stateful genuine Opus encoder. Its public
// method shape matches the outbound RTC track's injectable OpusEncoder seam.
type RTCOpusEncoder struct {
	mu     sync.Mutex
	config OpusCodecConfig
	codec  *opus.Encoder
	pcm    []byte
	packet []byte
	closed bool
}

var _ interface {
	Encode(context.Context, []int16) ([]byte, error)
} = (*RTCOpusEncoder)(nil)

// NewRTCOpusEncoder constructs an independent 48 kHz mono 20 ms encoder.
func NewRTCOpusEncoder(configs ...OpusCodecConfig) (*RTCOpusEncoder, error) {
	config, err := normalizeOpusConfig(configs)
	if err != nil {
		return nil, err
	}
	codec, err := newPionEncoder(config)
	if err != nil {
		return nil, wrapOpus("construct encoder", ErrOpusInvalidConfiguration, err)
	}
	return &RTCOpusEncoder{
		config: config,
		codec:  codec,
		pcm:    make([]byte, OpusFrameSamples*2),
		packet: make([]byte, maxOpusPacketBytes),
	}, nil
}

// NewOpusEncoder is the concise constructor for NewRTCOpusEncoder.
func NewOpusEncoder(configs ...OpusCodecConfig) (*RTCOpusEncoder, error) {
	return NewRTCOpusEncoder(configs...)
}

// Encode consumes one caller-owned PCM16 frame and returns independent Opus
// packet storage. The input is neither mutated nor retained.
func (e *RTCOpusEncoder) Encode(ctx context.Context, samples []int16) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, ErrOpusClosed
	}
	if err := opusContextError(ctx); err != nil {
		return nil, wrapOpus("encode", ErrOpusEncode, err)
	}
	if len(samples) == 0 {
		return nil, ErrOpusEmptyPCM
	}
	if len(samples) != OpusFrameSamples {
		return nil, fmt.Errorf("%w: got %d samples, want %d", ErrOpusInvalidPCM, len(samples), OpusFrameSamples)
	}
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(e.pcm[i*2:], uint16(sample))
	}
	n, err := e.codec.Encode(e.pcm, e.packet)
	if err != nil {
		return nil, wrapOpus("encode", ErrOpusEncode, err)
	}
	if n <= 0 {
		return nil, ErrOpusEmptyPayload
	}
	if n > len(e.packet) {
		return nil, wrapOpus("encode", ErrOpusEncode, fmt.Errorf("packet size %d exceeds %d-byte buffer", n, len(e.packet)))
	}
	if err := opusContextError(ctx); err != nil {
		return nil, wrapOpus("encode", ErrOpusEncode, err)
	}
	return append([]byte(nil), e.packet[:n]...), nil
}

// Reset discards encoder history while retaining the configured buffers.
func (e *RTCOpusEncoder) Reset() error {
	if e == nil {
		return ErrOpusClosed
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return ErrOpusClosed
	}
	codec, err := newPionEncoder(e.config)
	if err != nil {
		return wrapOpus("reset encoder", ErrOpusEncode, err)
	}
	e.codec = codec
	return nil
}

// Close prevents further encoding. It is idempotent and owns no goroutines or
// external resources.
func (e *RTCOpusEncoder) Close() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	e.closed = true
	e.codec = nil
	e.mu.Unlock()
	return nil
}

// RTCOpusDecoder is a serialized, stateful genuine Opus decoder with real PLC.
// Its public method shape matches the inbound RTC track's injectable
// OpusDecoder seam.
type RTCOpusDecoder struct {
	mu      sync.Mutex
	config  OpusCodecConfig
	codec   *opus.Decoder
	pcm     []int16
	history bool
	closed  bool
}

var _ interface {
	Decode([]byte) ([]int16, error)
	DecodePLC() ([]int16, error)
} = (*RTCOpusDecoder)(nil)

// NewRTCOpusDecoder constructs an independent 48 kHz mono 20 ms decoder.
func NewRTCOpusDecoder(configs ...OpusCodecConfig) (*RTCOpusDecoder, error) {
	config, err := normalizeOpusConfig(configs)
	if err != nil {
		return nil, err
	}
	codec, err := opus.NewDecoderWithOutput(OpusSampleRate, OpusChannels)
	if err != nil {
		return nil, wrapOpus("construct decoder", ErrOpusInvalidConfiguration, err)
	}
	return &RTCOpusDecoder{
		config: config,
		codec:  &codec,
		pcm:    make([]int16, OpusFrameSamples),
	}, nil
}

// NewOpusDecoder is the concise constructor for NewRTCOpusDecoder.
func NewOpusDecoder(configs ...OpusCodecConfig) (*RTCOpusDecoder, error) {
	return NewRTCOpusDecoder(configs...)
}

// Decode consumes one Opus packet and returns fresh caller-owned PCM16 storage.
func (d *RTCOpusDecoder) Decode(payload []byte) ([]int16, error) {
	if d == nil {
		return nil, ErrOpusClosed
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, ErrOpusClosed
	}
	if len(payload) == 0 {
		return nil, ErrOpusEmptyPayload
	}
	n, err := d.codec.DecodeToInt16(payload, d.pcm)
	if err != nil {
		return nil, wrapOpus("decode", ErrOpusDecode, err)
	}
	if n != OpusFrameSamples {
		return nil, fmt.Errorf("%w: got %d samples, want %d", ErrOpusInvalidPayload, n, OpusFrameSamples)
	}
	d.history = true
	return append([]int16(nil), d.pcm...), nil
}

// DecodePLC synthesizes exactly one missing frame from decoder history. Cold
// PLC is rejected explicitly so callers cannot mistake zero insertion for
// production packet-loss concealment.
func (d *RTCOpusDecoder) DecodePLC() ([]int16, error) {
	if d == nil {
		return nil, ErrOpusClosed
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, ErrOpusClosed
	}
	if !d.history {
		return nil, ErrOpusNoHistory
	}
	if err := d.codec.DecodePLC(d.pcm); err != nil {
		return nil, wrapOpus("packet-loss concealment", ErrOpusPLC, err)
	}
	return append([]int16(nil), d.pcm...), nil
}

// Reset discards decoder history while retaining the configured buffers.
func (d *RTCOpusDecoder) Reset() error {
	if d == nil {
		return ErrOpusClosed
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return ErrOpusClosed
	}
	codec, err := opus.NewDecoderWithOutput(OpusSampleRate, OpusChannels)
	if err != nil {
		return wrapOpus("reset decoder", ErrOpusDecode, err)
	}
	d.codec = &codec
	d.history = false
	return nil
}

// Close prevents further decoding and PLC. It is idempotent and owns no
// goroutines or external resources.
func (d *RTCOpusDecoder) Close() error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	d.closed = true
	d.codec = nil
	d.history = false
	d.mu.Unlock()
	return nil
}

// OpusEncoderAdapter is an explicit adapter-oriented name for the concrete
// encoder, useful when the track package also declares an OpusEncoder seam.
type OpusEncoderAdapter = RTCOpusEncoder

// OpusDecoderAdapter is an explicit adapter-oriented name for the concrete
// decoder, useful when the track package also declares an OpusDecoder seam.
type OpusDecoderAdapter = RTCOpusDecoder

func opusContextError(ctx context.Context) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return ctx.Err()
}
