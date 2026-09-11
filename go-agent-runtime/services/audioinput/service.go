// Package audioinput owns host-neutral finite audio input streaming.
//
// Hosts acquire files, stdin, or devices and pass the resulting reader/source
// through this boundary. The service owns validation, PCM framing, conversion,
// pacing, turn completion, cleanup, and bounded input observations.
package audioinput

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// ErrorKind identifies the failed audio-input boundary.
type ErrorKind string

const (
	KindEmpty           ErrorKind = "empty"
	KindMissing         ErrorKind = "missing"
	KindUnreadable      ErrorKind = "unreadable"
	KindFormat          ErrorKind = "format"
	KindConflict        ErrorKind = "conflict"
	KindRead            ErrorKind = "read"
	KindSend            ErrorKind = "send"
	KindClose           ErrorKind = "close"
	KindUninterruptible ErrorKind = "uninterruptible"
)

type errorCode string

func (e errorCode) Error() string { return string(e) }

const (
	ErrEmpty           errorCode = "audio input is empty"
	ErrMissing         errorCode = "audio input is missing"
	ErrUnreadable      errorCode = "audio input is unreadable"
	ErrFormat          errorCode = "audio input format is unsupported"
	ErrConflict        errorCode = "audio input sources conflict"
	ErrRead            errorCode = "audio input read failed"
	ErrSend            errorCode = "audio input send failed"
	ErrClose           errorCode = "audio input close failed"
	ErrUninterruptible errorCode = "audio input reader cannot be interrupted safely"
	ErrEndOfTurnLost   errorCode = "end-of-turn signal was not delivered before session shutdown"
	ErrPCM16Truncated  errorCode = "PCM16 audio has a truncated sample"
	ErrUnavailable     errorCode = "audio input service unavailable"
	ErrRateConflict    errorCode = "audio input and output sample rates conflict"
)

// Compatibility names make the runtime boundary easy to adopt from existing
// session callers without importing a command package.
const (
	ErrSessionAudioInputEmpty           = ErrEmpty
	ErrSessionAudioInputMissing         = ErrMissing
	ErrSessionAudioInputUnreadable      = ErrUnreadable
	ErrSessionAudioInputFormat          = ErrFormat
	ErrSessionAudioInputConflict        = ErrConflict
	ErrSessionAudioInputRead            = ErrRead
	ErrSessionAudioInputSend            = ErrSend
	ErrSessionAudioInputClose           = ErrClose
	ErrSessionAudioInputUninterruptible = ErrUninterruptible
	ErrSessionAudioInputEndOfTurnLost   = ErrEndOfTurnLost
	ErrSessionAudioPCM16Truncated       = ErrPCM16Truncated
)

// Error preserves the operation kind, host-provided path metadata, and the
// original cause across the public service boundary.
type Error struct {
	Kind ErrorKind
	Path string
	Err  error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return fmt.Sprintf("audio input %q: %s", e.Path, e.Kind)
	}
	return fmt.Sprintf("audio input %q: %s: %v", e.Path, e.Kind, e.Err)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return errors.Join(kindError(e.Kind), e.Err)
}

func kindError(kind ErrorKind) error {
	switch kind {
	case KindEmpty:
		return ErrEmpty
	case KindMissing:
		return ErrMissing
	case KindUnreadable:
		return ErrUnreadable
	case KindFormat:
		return ErrFormat
	case KindConflict:
		return ErrConflict
	case KindRead:
		return ErrRead
	case KindSend:
		return ErrSend
	case KindClose:
		return ErrClose
	case KindUninterruptible:
		return ErrUninterruptible
	default:
		return nil
	}
}

// Input is a host-neutral audio source and its session ports. Path is metadata
// only; filesystem policy and file/stdin acquisition remain outside runtime.
// Exactly one of Source, Reader, or Buffer should be supplied.
type Input struct {
	Path               string
	Present            bool
	DevicePresent      bool
	Source             audio.AudioSource
	Reader             io.Reader
	Buffer             []byte
	SourceSampleRate   int
	ProviderSampleRate int
	Pace               bool
	Clock              clock.Source
	CloseOnCancel      bool
	SendAudioInput     func(context.Context, []byte) error
	SendEndOfTurn      func(context.Context) error
	Observer           Observer
	CorrelationID      string
}

// InputSpec keeps CLI-compatible transport metadata out of command code while
// remaining free of filesystem, terminal, provider and device dependencies.
type InputSpec struct {
	Path               string
	Stdin              io.Reader
	SourceSampleRate   int
	CloseStdinOnCancel bool
	MaxDuration        time.Duration
	Source             audio.AudioSource
	SendAudioInput     func(context.Context, []byte) error
	SendEndOfTurn      func(context.Context) error
	Present            bool
	DevicePresent      bool
}

// Selected reports whether a host requested audio input.
func (i InputSpec) Selected() bool { return i.Present || i.Path != "" }

// SessionLoop is the minimal control/data port needed by the service. The
// concrete agent loop is deliberately not part of this public contract.
type SessionLoop interface {
	SendAudioInput(context.Context, []byte) error
	SendSessionEvent(context.Context, messages.StreamMessage) error
}

// ScheduledInput is one host-prepared finite interruption turn.
type ScheduledInput struct {
	AfterCompletedTurns int
	PCM                 []byte
	SourceSampleRate    int
	EndOfTurn           bool
}

// DispatchOptions carries the negotiated rate and optional input observer for
// one scheduled turn. The caller owns scheduling admission; this service owns
// cloning, conversion, ordered delivery, and the end-of-turn boundary.
type DispatchOptions struct {
	ProviderSampleRate int
	Clock              clock.Source
	Observer           Observer
	Path               string
	CorrelationID      string
}

// AudioObservation is emitted only after a PCM packet has been accepted by
// the send port. PCM is copied once; the service retains no utterance buffer.
type AudioObservation struct {
	Sequence           uint64
	FrameIndex         int
	Tick               uint64
	Timestamp          time.Time
	Path               string
	CorrelationID      string
	SourceSampleRate   int
	ProviderSampleRate int
	PCM                []byte
}

// Observer receives bounded, ordered input observations.
type Observer interface {
	ObserveAudioInput(AudioObservation)
}

// ObserverFunc adapts a function to Observer.
type ObserverFunc func(AudioObservation)

func (f ObserverFunc) ObserveAudioInput(observation AudioObservation) {
	if f != nil {
		f(observation)
	}
}

// Opener supplies host-owned acquisition operations while runtime owns the
// extension, source-rate, and pacing policy.
type Opener struct {
	OpenWAV      func(string) (audio.AudioSource, error)
	OpenFile     func(string, io.Reader) (audio.AudioSource, error)
	CheckPath    func(string) (fs.FileInfo, error)
	Classify     func(string, error) error
	PrepareStdin func(io.Reader, bool) (io.Reader, error)
}

// InvocationEvent is the host-neutral lifecycle observation used to release
// one finite scheduled-input batch after a dispatched browser/tool event.
type InvocationEvent struct {
	Dispatched   bool
	Type         string
	State        string
	InvocationID string
	ToolName     string
}

// TurnStopPolicy is the host-neutral decision surface for a finite input
// stream. Callbacks keep provider/session observers out of this package.
type TurnStopPolicy struct {
	AwaitingResponse           bool
	WaitForClose               bool
	RequireAssistantResponse   bool
	Terminal                   func(messages.StreamMessage) bool
	MessageEndAdmitted         func() bool
	TerminalToolFailure        func() bool
	TerminalScheduledFailure   func() bool
	AssistantResponseCompleted func() bool
}

// Service is the reusable audio-input lifecycle contract. Implementations are
// constructed by services/audioinput/wire and keep stream policy private.
type Service interface {
	Validate(Input) error
	ValidateSpec(InputSpec, error) error
	Open(InputSpec, Opener) (*ManagedSource, error)
	Read(context.Context, Input) ([]byte, int, error)
	Stream(context.Context, Input, SessionLoop) error
	Dispatch(context.Context, SessionLoop, ScheduledInput, DispatchOptions) error
	ConvertPCM([]byte, int, int) ([]byte, error)
	ConvertScheduled([]ScheduledInput, int) ([]ScheduledInput, error)
	PrepareScheduled([]string, func(string) ([]byte, int, error)) ([]ScheduledInput, error)
	ReleaseOnInvocation(context.Context, <-chan InvocationEvent, []ScheduledInput, string) (<-chan ScheduledInput, func())
	AdaptError(error, string, ErrorKind, error) error
	ClassifyOpenError(string, error) error
	PreferRate(int, int) int
	NegotiateRate(int, int, int, error) (int, error)
	ShouldStop(messages.StreamMessage, TurnStopPolicy) bool
	JoinTerminationErrors(error, error) error
	IsExpectedCancellation(error) bool
}

// ReadSeekCloser is the host-neutral port used for an already-open WAV file.
type ReadSeekCloser interface {
	io.Reader
	io.Seeker
	io.Closer
}

// AudioInputError aliases the public typed error for callers that prefer an
// explicit input-oriented name.
type AudioInputError = Error

// AudioInputErrorKind aliases ErrorKind for callers migrating from the CLI.
type AudioInputErrorKind = ErrorKind
