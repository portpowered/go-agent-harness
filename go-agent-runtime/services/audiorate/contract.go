// Package audiorate defines the host-neutral PCM16 and session-rate service.
//
// Audio conversion, validation, scheduling metadata, and rate policy are
// implemented privately and composed through the package's Wire constructor.
// Hosts depend only on these contracts and the canonical go-audio behavior.
package audiorate

import "context"

// ErrorCode is a stable, comparable error identity for the audio-rate
// boundary. Wrapped errors retain this identity for errors.Is callers.
type ErrorCode string

// Error implements error.
func (e ErrorCode) Error() string { return string(e) }

const (
	// ErrPCM16Truncated identifies a PCM16 payload with a partial sample.
	ErrPCM16Truncated ErrorCode = "session PCM16 audio has a truncated sample"
	// ErrSampleRateConflict identifies incompatible input and output rates.
	ErrSampleRateConflict ErrorCode = "session input and output sample rates conflict"
	// ErrUnsupportedSampleRate identifies a rate outside the canonical audio
	// conversion set. The wrapped go-audio error remains available as well.
	ErrUnsupportedSampleRate ErrorCode = "unsupported session audio sample rate"

	// ProviderOpenAI and ProviderGrok identify the live providers whose default
	// duplex contract is the realtime rate.
	ProviderOpenAI = "openai"
	ProviderGrok   = "grok"

	// SampleRate16kHz, SampleRate24kHz, and SampleRate48kHz are the only rates
	// accepted by the canonical PCM16 resampler.
	SampleRate16kHz = 16000
	SampleRate24kHz = 24000
	SampleRate48kHz = 48000

	// Rate16kHz, Rate24kHz, and Rate48kHz are concise compatibility names for
	// the canonical sample-rate constants.
	Rate16kHz = SampleRate16kHz
	Rate24kHz = SampleRate24kHz
	Rate48kHz = SampleRate48kHz

	// DefaultSampleRate is used for replay and non-realtime sessions when no
	// explicit rate was captured or requested.
	DefaultSampleRate = SampleRate16kHz
	// RealtimeSampleRate is the live OpenAI and Grok duplex default.
	RealtimeSampleRate = SampleRate24kHz
)

// AudioFormat is the encoding negotiated at the session boundary.
type AudioFormat string

const (
	// AudioFormatPCM16 is canonical signed little-endian mono PCM16.
	AudioFormatPCM16 AudioFormat = "pcm16"
)

// ScheduledAudioInput is one caller-supplied PCM16 input with its turn
// metadata. The service preserves ordering and all metadata while replacing
// PCM and SourceSampleRate with the negotiated provider contract.
type ScheduledAudioInput struct {
	AfterCompletedTurns int
	PCM                 []byte
	SourceSampleRate    int
	EndOfTurn           bool
}

// RateResolutionRequest contains already-captured or already-requested rate
// facts. The service does not inspect external state to derive these values.
type RateResolutionRequest struct {
	Provider            string
	Replay              bool
	CapturedInputRate   int
	CapturedOutputRate  int
	RequestedInputRate  int
	RequestedOutputRate int
}

// InputConfigurer receives the negotiated provider input contract.
type InputConfigurer interface {
	SetSessionAudioInput(AudioFormat, int)
}

// OutputConfigurer receives the negotiated provider output contract.
type OutputConfigurer interface {
	SetSessionAudioOutput(AudioFormat, int)
}

// ConfigureRequest contains the rate facts and optional provider setters for
// one session. Output is configured before input to preserve the existing
// session contract ordering.
type ConfigureRequest struct {
	Resolution RateResolutionRequest
	Input      InputConfigurer
	Output     OutputConfigurer
}

// Service owns PCM16 conversion and session audio-rate negotiation. The
// implementation is private; callers obtain it from services/audiorate/wire.
type Service interface {
	ConvertPCM(context.Context, []byte, int, int) ([]byte, error)
	ConvertScheduledAudioInputs(context.Context, []ScheduledAudioInput, int) ([]ScheduledAudioInput, error)
	ResolveSampleRate(context.Context, RateResolutionRequest) (int, error)
	ConfigureSessionAudioContract(context.Context, ConfigureRequest) (int, error)
}
