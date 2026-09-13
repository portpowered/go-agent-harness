// Package audiooutput owns assistant-audio output composition for embedders.
//
// Hosts provide only explicit output, observation, and session dependencies;
// sink selection, framing, buffering, and shutdown remain service-owned.
package audiooutput

import (
	"context"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ErrorCode is an immutable audio-output contract error that supports
// errors.Is after service context wraps it.
type ErrorCode string

func (e ErrorCode) Error() string { return string(e) }

const (
	// ErrInvalidConfig identifies an output configuration that cannot be
	// admitted without starting a session or touching a provider.
	ErrInvalidConfig ErrorCode = "invalid audio output configuration"
	// ErrDeviceObserverUnavailable identifies a file/stdout output that has no
	// device-consumption tap.
	ErrDeviceObserverUnavailable ErrorCode = "audio output device observer is unavailable"
)

// LoudnessProcessor is the narrow output-gain seam. Implementations must not
// mutate the supplied PCM bytes and must return the bytes to be written and
// observed at the output boundary.
type LoudnessProcessor interface {
	ProcessBytes([]byte) []byte
}

// Config describes one output invocation. Path and Writer are explicit host
// inputs; the service does not read flags, environment, credentials, or
// process globals. DeviceBound selects the lazy, negotiated-rate consumption
// tap used when the same provider session is also attached to a device.
type Config struct {
	Path               string
	Writer             io.Writer
	SampleRate         int
	DeviceBound        bool
	Loudness           LoudnessProcessor
	ObserveAudioOutput func([]byte, messages.StreamMessage)
}

// Output owns one sink and its output-side lifecycle.
type Output interface {
	WriteDelta(context.Context, []byte, messages.StreamMessage) error
	ObserveDeviceSamples(context.Context, int, []int16) error
	Close() error
}

// SessionOptions carries the optional text-seed replacement at the session
// boundary. The service keeps this compatibility behavior with the output
// wrapper but does not decide prompts, flags, or provider configuration.
type SessionOptions struct {
	WirePrompt string
	SeedValue  string
	// AdaptSession may add host-owned optional capabilities to the raw session
	// before the service starts forwarding it. It is nil for ordinary embedders.
	AdaptSession func(messages.Session) messages.Session
}

// SessionInferencer is the output-decorated session factory. Wait joins the
// accepted output tail and provider close; Err preserves all observed causes.
type SessionInferencer interface {
	messages.SessionInferencer
	Wait()
	Err() error
}

// Service constructs inert output resources and session decorators. All
// invocation state is allocated by Open or Wrap, not by Wire construction.
type Service interface {
	Open(Config) (Output, error)
	Wrap(messages.SessionInferencer, Output, SessionOptions) SessionInferencer
}
