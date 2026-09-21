// Package audioio owns the host-neutral audio boundary used by session
// callers. Concrete readers, writers and processing workers remain private.
package audioio

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type ErrorCode string

func (e ErrorCode) Error() string { return string(e) }

const (
	ErrPCM16Truncated         ErrorCode = "PCM16 payload has a truncated sample"
	ErrSampleRateConflict     ErrorCode = "audio input and output sample rates conflict"
	ErrUnsupportedRate        ErrorCode = "unsupported audio sample rate"
	ProviderOpenAI                      = "openai"
	ProviderGrok                        = "grok"
	SampleRate16kHz                     = 16000
	SampleRate24kHz                     = 24000
	SampleRate48kHz                     = 48000
	DefaultSampleRate                   = SampleRate16kHz
	RealtimeSampleRate                  = SampleRate24kHz
	DefaultTranscriptionModel           = "gpt-live-transcribe"
)

const ErrEmptyInput ErrorCode = "audio input contains no frames"

type RateRequest struct {
	Provider            string
	Replay              bool
	CapturedInputRate   int
	CapturedOutputRate  int
	RequestedInputRate  int
	RequestedOutputRate int
}

type RateResolution struct {
	InputRate  int
	OutputRate int
}

type PCM16Request struct {
	PCM            []byte
	SourceRate     int
	TargetRate     int
	SourceChannels int
	TargetChannels int
}

// VoicePCMRequest is one provider-owned PCM16 payload that needs the
// service's measured voice playback correction before it is routed to any
// downstream participant or device.
type VoicePCMRequest struct {
	Voice string
	PCM   []byte
}

// ScheduledAudioInput is one finite PCM turn admitted by a persistent audio
// session. The session layer supplies scheduling policy; audioio owns the
// sample payload and its native-rate declaration.
type ScheduledAudioInput struct {
	AfterCompletedTurns int
	PCM                 []byte
	SourceSampleRate    int
	EndOfTurn           bool
}

type InputRequest struct {
	Source       audio.AudioSource
	SourceRate   int
	ProviderRate int
	Pace         bool
	Continuous   bool
	// PadFinalFrame preserves the frame-oriented file contract for finite
	// sources whose final quantum is shorter than audio.FrameSize. It is
	// separate from Continuous because finite service callers may need exact
	// count-aware tails instead.
	PadFinalFrame         bool
	EmitBoundaryOnSilence bool
	OnTurnBoundary        func(context.Context) error
	Scheduler             clock.Scheduler
}

type OutputRequest struct {
	Sink         audio.AudioSink
	SinkRate     int
	ProviderRate int
	Voice        string
	GainDB       float64
	Continuous   bool
}

type Input interface {
	Pump(context.Context, audio.OutboundMedia) error
	Close() error
}

type Output interface {
	Pump(context.Context, audio.InboundMedia) error
	Write(context.Context, audio.PCMFrame) error
	Close() error
}

type TranscriptionRequest struct {
	Provider          string
	Replay            bool
	AcceptsAudioInput bool
	Disabled          bool
	Model             string
	Override          *TranscriptionConfig
}

type TranscriptionConfig struct {
	Enabled bool
	Model   string
}

type Service interface {
	ResolveRates(context.Context, RateRequest) (RateResolution, error)
	ConvertPCM16(context.Context, PCM16Request) ([]byte, error)
	ConvertScheduledInputs(context.Context, []ScheduledAudioInput, int) ([]ScheduledAudioInput, error)
	OpenInput(context.Context, InputRequest) (Input, error)
	OpenOutput(context.Context, OutputRequest) (Output, error)
	ApplyVoicePCM16(context.Context, VoicePCMRequest) ([]byte, error)
	NewClock(clock.Source) (clock.TimerSource, error)
	NewTimer(clock.Source, time.Duration) (clock.Timer, error)
	ResolveTranscription(TranscriptionRequest) TranscriptionConfig
	VoiceGainDB(string) float64
}
