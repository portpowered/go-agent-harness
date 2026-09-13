// Package roommedia owns the reusable participant-side PCM pumps used by a
// room runtime. It accepts host-neutral ports only; room policy, credentials,
// devices, and participant lifecycle remain outside this package.
package roommedia

import (
	"context"
	"fmt"
	"time"
)

// Error is a comparable public sentinel. It preserves errors.Is identity
// without mutable package state.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrUnavailable identifies a missing room-media dependency.
	ErrUnavailable Error = "room media service unavailable"
	// ErrInvalidRequest identifies malformed media-port configuration.
	ErrInvalidRequest Error = "invalid room media request"
	// ErrInvalidFormat identifies a non-mono or non-positive PCM16 format.
	ErrInvalidFormat Error = "invalid room media PCM16 format"
	// ErrPCM16Truncated identifies a payload that ends in a partial PCM16
	// sample. The original payload is never silently repaired or padded.
	ErrPCM16Truncated Error = "room media PCM16 payload has a truncated sample"
)

const (
	// ProviderInputRejectedReason is recorded when the provider-bound send
	// rejects a frame after it was read from the mixer.
	ProviderInputRejectedReason = "provider_input_rejected"
	// ParticipantOutputRejectedReason is recorded when a device output write
	// rejects a mixed frame.
	ParticipantOutputRejectedReason = "participant_output_rejected"
)

// PCM16Format describes mono signed little-endian PCM16 at a media boundary.
// Frame duration is owned by the injected mixer and is intentionally absent.
type PCM16Format struct {
	SampleRate int
	Channels   int
}

func (f PCM16Format) validate() error {
	if f.SampleRate <= 0 {
		return fmt.Errorf("%w: sample rate %d", ErrInvalidFormat, f.SampleRate)
	}
	if f.Channels != 1 {
		return fmt.Errorf("%w: want mono, got %d channels", ErrInvalidFormat, f.Channels)
	}
	return nil
}

// Validate checks that the boundary is a positive mono PCM16 format.
func (f PCM16Format) Validate() error { return f.validate() }

// MixedFrame is one mixer cadence frame and the source IDs that contributed
// to it. Sources are copied by the service before crossing a callback seam.
type MixedFrame struct {
	PCM     []byte
	Sources []string
}

// Mixer is the only room-side frame source required by the service.
type Mixer interface {
	Format() PCM16Format
	ReadFrameWithSources(context.Context) (MixedFrame, error)
}

// MixerFunc adapts host mixer methods without exposing a concrete mixer type.
type MixerFunc struct {
	FormatFunc func() PCM16Format
	ReadFunc   func(context.Context) (MixedFrame, error)
}

func (f MixerFunc) Format() PCM16Format {
	if f.FormatFunc == nil {
		return PCM16Format{}
	}
	return f.FormatFunc()
}

func (f MixerFunc) ReadFrameWithSources(ctx context.Context) (MixedFrame, error) {
	if f.ReadFunc == nil {
		return MixedFrame{}, ErrUnavailable
	}
	return f.ReadFunc(ctx)
}

// Input reads one fixed PCM16 capture frame into caller-owned storage.
type Input interface {
	ReadFrame(context.Context, []int16) error
}

// InputFunc adapts a host capture device without importing a device backend.
type InputFunc func(context.Context, []int16) error

func (f InputFunc) ReadFrame(ctx context.Context, frame []int16) error {
	if f == nil {
		return ErrUnavailable
	}
	return f(ctx, frame)
}

// Output accepts one fixed PCM16 device frame. The service never retains the
// supplied samples after WriteFrame returns.
type Output interface {
	WriteFrame(context.Context, []int16) error
}

// OutputFunc adapts a host playback device without importing a device backend.
type OutputFunc func(context.Context, []int16) error

func (f OutputFunc) WriteFrame(ctx context.Context, frame []int16) error {
	if f == nil {
		return ErrUnavailable
	}
	return f(ctx, frame)
}

// InputPolicy is intentionally a small host-neutral vocabulary. A room
// coordinator chooses the policy; the media service only transports it.
type InputPolicy string

const (
	InputPolicyDefault        InputPolicy = ""
	InputPolicyInterrupt      InputPolicy = "interrupt"
	InputPolicyDoNotInterrupt InputPolicy = "do_not_interrupt"
)

// ProviderInputRequest supplies the injected ports for one provider pump.
type ProviderInputRequest struct {
	Mixer              Mixer
	ParticipantID      string
	ProviderSampleRate int
	ReadContext        context.Context
	AckContext         context.Context
	Send               func(context.Context, []byte, InputPolicy) error
	Policy             func([]string) InputPolicy
	Resolve            func([]string, int, string)
	Observe            func(string, []byte) error
	ObserveReceived    func([]byte)
	ObserveDropped     func(string, int)
	ObserveRejected    func([]byte, error)
	ReplayAcks         chan<- struct{}
}

// FanoutTarget is one active peer destination for human capture. Active is
// rechecked after an error so a peer that terminated concurrently does not
// turn a normal teardown into a room failure.
type FanoutTarget struct {
	ID     string
	Format PCM16Format
	Active func() bool
	Write  func(context.Context, string, []byte) error
}

// HumanCaptureRequest supplies the injected input mixer and fan-out ports.
type HumanCaptureRequest struct {
	ParticipantID   string
	Input           Input
	Mixer           Mixer
	InputSampleRate int
	FrameSamples    int
	Targets         func() []FanoutTarget
	ObserveSent     func([]byte)
	ObserveFanout   func(string, string, []byte)
}

// HumanOutputRequest supplies the injected mixer, output sink, and bounded
// observation callbacks for a customer-facing speaker.
type HumanOutputRequest struct {
	Mixer           Mixer
	ReadContext     context.Context
	Output          Output
	Resolve         func([]string, int, string)
	ObserveReceived func([]byte)
}

// OutputBufferRequest selects the conversion and fixed device quantum for a
// stateful output buffer.
type OutputBufferRequest struct {
	Output           Output
	Format           PCM16Format
	TargetSampleRate int
	FrameSamples     int
}

// OutputBuffer owns conversion and pending partial samples between mixer
// frames. PendingSamples is diagnostic only and does not expose mutable state.
type OutputBuffer interface {
	WriteFrame(context.Context, []byte) error
	PendingSamples() int
}

// Clock is the only time source used by roommedia. A caller may provide a
// deterministic clock; nil is normalized to the explicit real source by Wire.
type Clock interface {
	Now() time.Time
}

// Service owns participant-side media conversion, pumping, fan-out, pacing
// decisions, and output buffering behind the injected ports above.
type Service interface {
	PumpProviderInput(context.Context, ProviderInputRequest) error
	CaptureHuman(context.Context, HumanCaptureRequest) error
	PumpHumanOutput(context.Context, HumanOutputRequest) error
	NewOutputBuffer(OutputBufferRequest) (OutputBuffer, error)
	ConvertProviderInput([]byte, PCM16Format, int) ([]byte, error)
	EncodePCM16([]int16) []byte
}
