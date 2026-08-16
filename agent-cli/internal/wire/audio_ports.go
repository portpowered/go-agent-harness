package wire

import (
	"context"
	"io"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/platform/clock"
)

// DeviceRegistry is the minimal consumer-side device discovery contract. The
// audio device lane owns the concrete registry and may add richer operations
// without changing the wire boundary.
type DeviceRegistry interface {
	ListDevices() []string
}

// AudioSource is the shared PCM input contract used by the audio package.
type AudioSource = audio.AudioSource

// AudioSink is the consumer-side PCM output contract. Concrete sinks belong
// to later audio lanes and can implement this interface without importing wire.
type AudioSink interface {
	WriteFrame(ctx context.Context, samples []int16) error
	Close() error
}

// Clock is the shared platform time-source contract.
type Clock = clock.Source

// The legacy CLI initializers do not yet own audio devices. These inert values
// keep those compatibility entry points constructible without opening a
// device, consuming audio, or adding a second composition path. Callers that
// own the edges replace them through the named port API.
type inertDeviceRegistry struct{}

func (inertDeviceRegistry) ListDevices() []string { return nil }

type inertAudioSource struct{}

func (inertAudioSource) ReadFrame(context.Context, []int16) error { return io.EOF }
func (inertAudioSource) Close() error                             { return nil }

type inertAudioSink struct{}

func (inertAudioSink) WriteFrame(context.Context, []int16) error { return nil }
func (inertAudioSink) Close() error                              { return nil }

func defaultDeviceRegistry() DeviceRegistry { return inertDeviceRegistry{} }
func defaultAudioSource() AudioSource       { return inertAudioSource{} }
func defaultAudioSink() AudioSink           { return inertAudioSink{} }

var (
	_ DeviceRegistry = inertDeviceRegistry{}
	_ AudioSource    = inertAudioSource{}
	_ AudioSink      = inertAudioSink{}
	_ Clock          = clock.Real{}
)
