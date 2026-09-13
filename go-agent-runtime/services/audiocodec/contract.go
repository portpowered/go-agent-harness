// Package audiocodec defines the host-neutral boundary for decoding an audio
// payload into the runtime's canonical PCM16 format. Filesystem policy,
// provider envelopes, tool messages, and device I/O stay outside this
// package.
package audiocodec

import (
	"context"
	"fmt"
	"time"
)

const (
	// PCM16SampleRate is the sample rate produced by Service.Convert.
	PCM16SampleRate = 16000
	// PCM16Channels is the channel count produced by Service.Convert.
	PCM16Channels = 1
	// PCM16Encoding is signed little-endian 16-bit PCM.
	PCM16Encoding = "s16le"

	// DefaultMaxInputBytes bounds the encoded source passed to the decoder.
	DefaultMaxInputBytes = 16 << 20
	// DefaultMaxOutputBytes bounds the decoded PCM16 result.
	DefaultMaxOutputBytes = 16 << 20
	// DefaultMaxStderrBytes bounds diagnostics retained from the decoder.
	DefaultMaxStderrBytes = 64 << 10
	// DefaultMaxDuration bounds a filesystem-tool conversion when its host does
	// not provide a tighter deadline.
	DefaultMaxDuration = 2 * time.Minute
)

// InputFormat is the private implementation's normalized description of the
// encoded source. It is returned for evidence and diagnostics only; callers do
// not need to select a decoder implementation.
type InputFormat string

const (
	FormatWAV  InputFormat = "wav"
	FormatMP3  InputFormat = "mp3"
	FormatFLAC InputFormat = "flac"
	FormatOGG  InputFormat = "ogg"
	FormatOpus InputFormat = "opus"
	FormatAAC  InputFormat = "aac"
	FormatM4A  InputFormat = "m4a"
	FormatWebM InputFormat = "webm"
)

// Limits makes every resource bound used by a conversion explicit. A zero
// MaxDuration leaves the caller's context as the only time bound; all byte
// limits must be positive.
type Limits struct {
	MaxInputBytes  int
	MaxOutputBytes int
	MaxStderrBytes int
	MaxDuration    time.Duration
}

// Validate checks resource bounds before any temporary file or process is
// allocated.
func (l Limits) Validate() error {
	if l.MaxInputBytes <= 0 || l.MaxOutputBytes <= 0 || l.MaxStderrBytes <= 0 || l.MaxDuration < 0 {
		return fmt.Errorf("%w: input=%d output=%d stderr=%d duration=%s", ErrInvalidRequest, l.MaxInputBytes, l.MaxOutputBytes, l.MaxStderrBytes, l.MaxDuration)
	}
	return nil
}

// Request is one immutable conversion request. Implementations must not
// mutate Input; callers may safely reuse it after Convert returns.
type Request struct {
	Input      []byte
	FormatHint string
	Limits     Limits
}

// Result is the canonical decoded audio returned by Service.Convert.
type Result struct {
	PCM16       []byte
	InputFormat InputFormat
	SampleRate  int
	Channels    int
	Encoding    string
}

// Service converts one encoded audio request into bounded mono PCM16.
type Service interface {
	Convert(context.Context, Request) (Result, error)
}

// ErrorKind identifies a stable conversion failure class. Error values also
// unwrap their causal error, so callers can retain context.Canceled,
// context.DeadlineExceeded, exec.ErrNotFound, and canonical PCM16 identities.
type ErrorKind string

const (
	ErrorInvalidRequest    ErrorKind = "invalid request"
	ErrorUnsupportedFormat ErrorKind = "unsupported format"
	ErrorInputTooLarge     ErrorKind = "input limit exceeded"
	ErrorInputFile         ErrorKind = "input file failure"
	ErrorExecutableLookup  ErrorKind = "decoder executable lookup failed"
	ErrorProcessStart      ErrorKind = "decoder process start failed"
	ErrorProcessWait       ErrorKind = "decoder process wait failed"
	ErrorDecode            ErrorKind = "decoder rejected input"
	ErrorOutputTooLarge    ErrorKind = "decoded output limit exceeded"
	ErrorStderrTooLarge    ErrorKind = "decoder diagnostics limit exceeded"
	ErrorInvalidPCM16      ErrorKind = "invalid decoded PCM16"
	ErrorCanceled          ErrorKind = "conversion canceled"
)

// ErrorCode is a comparable error identity for one stable conversion failure
// class. Constants implement error without introducing mutable package state.
type ErrorCode ErrorKind

func (c ErrorCode) Error() string { return "audiocodec: " + string(c) }

// Error is the typed failure returned by Service.Convert. Use errors.Is with
// the exported error-code constants below, and errors.As when diagnostics need
// the stable Kind/Detail fields.
type Error struct {
	Kind   ErrorKind
	Format InputFormat
	Detail string
	Cause  error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	message := "audiocodec: " + string(e.Kind)
	if e.Detail != "" {
		message += ": " + e.Detail
	}
	if e.Format != "" {
		message += " (format " + string(e.Format) + ")"
	}
	return message
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Is compares typed errors by stable failure class rather than by detail.
func (e *Error) Is(target error) bool {
	if e == nil {
		return false
	}
	switch typed := target.(type) {
	case ErrorCode:
		return e.Kind == ErrorKind(typed)
	case *Error:
		return typed != nil && e.Kind == typed.Kind
	default:
		return false
	}
}

const (
	ErrInvalidRequest    ErrorCode = ErrorCode(ErrorInvalidRequest)
	ErrUnsupportedFormat ErrorCode = ErrorCode(ErrorUnsupportedFormat)
	ErrInputTooLarge     ErrorCode = ErrorCode(ErrorInputTooLarge)
	ErrInputFile         ErrorCode = ErrorCode(ErrorInputFile)
	ErrExecutableLookup  ErrorCode = ErrorCode(ErrorExecutableLookup)
	ErrProcessStart      ErrorCode = ErrorCode(ErrorProcessStart)
	ErrProcessWait       ErrorCode = ErrorCode(ErrorProcessWait)
	ErrDecode            ErrorCode = ErrorCode(ErrorDecode)
	ErrOutputTooLarge    ErrorCode = ErrorCode(ErrorOutputTooLarge)
	ErrStderrTooLarge    ErrorCode = ErrorCode(ErrorStderrTooLarge)
	ErrInvalidPCM16      ErrorCode = ErrorCode(ErrorInvalidPCM16)
	ErrCanceled          ErrorCode = ErrorCode(ErrorCanceled)
)
