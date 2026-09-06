// Package devices defines the host independent device service boundary.
//
// Device selection and worker admission happen through this package's
// normalized request. Concrete registries and hardware workers stay behind
// the service implementation; room and session services consume the returned
// bounded media handle without importing a device backend.
package devices

import (
	"context"
	"errors"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

var (
	// ErrUnavailable identifies a service that cannot admit device media.
	ErrUnavailable = errors.New("device service unavailable")
	// ErrInvalidRequest identifies a malformed or unsupported media request.
	ErrInvalidRequest = errors.New("invalid device media request")
)

// Request is the normalized device admission input for one invocation. IDs
// remain opaque strings at this boundary; the host or device implementation
// resolves them against its registry. Direction flags are explicit because an
// empty selector means "use the registry default" for an enabled direction.
// SampleRate and Channels describe the provider-side PCM shape. The gateway
// owns its fixed worker cadence, so a room frame duration is not part of this
// device request.
type Request struct {
	InputDevice  string
	OutputDevice string
	// RemoteEndpoint selects an invocation-scoped loopback device server. The
	// service owns endpoint validation and registry construction; hosts only
	// pass the opaque endpoint value through this request.
	RemoteEndpoint  string
	CaptureEnabled  bool
	PlaybackEnabled bool
	SampleRate      int
	Channels        int
	PlaybackProfile string
	// FileInput and FileOutput carry caller-opened canonical audio ports for
	// finite invocations. Validation errors leave these ports untouched. Once
	// a worker admits a port, the service closes it if a later worker cannot be
	// admitted and the returned handle owns it on success; callers may close
	// their pre-admission bundle idempotently after either outcome. Paths,
	// format parsing, and stream ownership stay at the host edge.
	FileInput  *FileInput
	FileOutput *FileOutput
}

// FileInput is a host-opened finite PCM source. SampleRate describes the
// source stream; a non-positive value selects audio.SampleRate. Pace asks the
// runtime worker to preserve the source's encoded frame cadence instead of
// bursting the entire file into the bounded provider queue.
type FileInput struct {
	Source     audio.AudioSource
	SampleRate int
	Pace       bool
	// Continuous keeps processed PCM flowing as soon as a provider frame is
	// available. Finite inputs retain one frame of lookahead so an explicit
	// source boundary can mark the final frame EndOfResponse; a continuously
	// open source cannot wait for that lookahead without stalling its first
	// utterance.
	Continuous bool
	// OnTurnBoundary is called after a persistent source reaches an explicit
	// audio.ErrEndOfTurn marker, and once at final EOF when the last turn did
	// not already carry a marker. It is optional so finite callers can retain
	// their existing post-Pump completion control path. The callback runs on
	// the capture worker and must return before the next source turn is read.
	OnTurnBoundary func(context.Context) error
	// Scheduler is required when Pace is true. It keeps finite source timing
	// on the host's canonical clock; the runtime never falls back to wall time.
	Scheduler clock.Scheduler
}

// FileOutput is a host-opened sink for provider PCM. SampleRate describes the
// sink's native rate; a non-positive value selects audio.SampleRate. The
// runtime worker performs provider-to-sink conversion and closes the sink as
// part of its invocation handle.
type FileOutput struct {
	Sink       audio.AudioSink
	SampleRate int
}

// Capture pumps local input samples into a provider-owned outbound media
// endpoint. The implementation owns its bounded queue, framing, conversion,
// and worker lifetime.
type Capture interface {
	audio.MediaEndpoint
	Pump(context.Context, audio.OutboundMedia) error
}

// Playback pumps provider samples into a local output device. The
// implementation owns pacing, interruption handling, conversion, and worker
// lifetime.
type Playback interface {
	audio.MediaEndpoint
	Pump(context.Context, audio.InboundMedia) error
}

// PlaybackControllerProvider is an optional device capability. A live owner
// may bind the returned controller to a provider media endpoint before the
// provider starts, preserving response identity and device-clocked barge-in
// even when the first audio frame arrives immediately after admission.
type PlaybackControllerProvider interface {
	PlaybackController() audio.PlaybackController
}

// PlaybackSamplesObserverProvider is an optional physical playback tap. The
// callback runs after conversion and queue admission, so a secondary recorder
// can retain the negotiated device-rate PCM without consuming the provider
// stream a second time. Implementations must invoke the callback
// synchronously and preserve its error; the device service owns any bounded
// buffering behind that callback.
type PlaybackSamplesObserverProvider interface {
	SetPlaybackSamplesObserver(func(context.Context, int, []int16) error)
}

// MediaPorts are the optional local workers admitted for one device request.
// The service handle owns both endpoints and must be closed exactly once by
// its caller. One-way media is supported by leaving either field nil.
type MediaPorts struct {
	Capture  Capture
	Playback Playback
}

// Handle owns one invocation's admitted device workers.
type Handle interface {
	Media() MediaPorts
	Close() error
}

// Service admits device workers for one normalized request. Construction is
// inert; registry access and worker startup happen only in Open.
type Service interface {
	Open(context.Context, Request) (Handle, error)
}

// ProbeRequest is the transport-neutral configuration for a live device
// probe. Provider construction is supplied by the application graph through
// SessionFactory; registry and media workers stay private to the runtime
// device service.
type ProbeRequest struct {
	Scenario             probe.Scenario
	Provider             string
	Model                string
	APIKey               string
	BaseURL              string
	ConfigDir            string
	CaptureTime          time.Duration
	SessionInferencer    messages.SessionInferencer
	Instructions         string
	InstructionsObserved func(string)
	WebSocketDialer      transport.Dialer
}

// ProbeSessionFactory constructs the provider session used by a live probe.
// It is an application composition seam, so the reusable device runtime does
// not depend on a session implementation or a CLI package.
type ProbeSessionFactory func(ProbeRequest, string) (messages.SessionInferencer, string, error)

// ProbeService runs a physical device probe while owning device selection,
// negotiated media, and worker lifetimes.
type ProbeService interface {
	Run(context.Context, ProbeRequest) (probe.ObservationSnapshot, error)
}
