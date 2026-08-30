package wire

import (
	"context"
	"io"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/platform/clock"
)

// DeviceRegistry is the shared registry boundary used by both device listing
// and session RTC device bindings. Keeping the alias here prevents the wire
// graph from inventing a second, discovery-only device contract.
type DeviceRegistry = audio.DeviceRegistry

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

// SessionRuntimeObserver is the optional runtime evidence sink used by
// hermetic command-level tests. It observes events emitted from inside the
// shipped session command, alongside the composed Clock.
type SessionRuntimeObserver = services.SessionRuntimeObserver

// SessionRuntimeObservation is the value delivered by SessionRuntimeObserver.
type SessionRuntimeObservation = services.SessionRuntimeObservation

const (
	SessionRuntimeObservationAudioOutput    = services.SessionRuntimeObservationAudioOutput
	SessionRuntimeObservationAudioInput     = services.SessionRuntimeObservationAudioInput
	SessionRuntimeObservationInputCommit    = services.SessionRuntimeObservationInputCommit
	SessionRuntimeObservationResponseCreate = services.SessionRuntimeObservationResponseCreate
	SessionRuntimeObservationTurnCompleted  = services.SessionRuntimeObservationTurnCompleted
	SessionRuntimeObservationTerminal       = services.SessionRuntimeObservationTerminal
)

// SessionFinalAccounting is the production-owned terminal token and metrics
// value carried by SessionRuntimeObservation.FinalAccounting.
type SessionFinalAccounting = services.SessionFinalAccounting

// SessionTokenUsageSemantics describes how provider MESSAGE.END usage values
// contribute to SessionFinalAccounting's session totals.
type SessionTokenUsageSemantics = services.SessionTokenUsageSemantics

// SessionTokenUsageIncremental is the supported session usage contract: each
// MESSAGE.END usage value contributes once for its completed turn.
const SessionTokenUsageIncremental = services.SessionTokenUsageIncremental

type inertAudioSource struct{}

func (inertAudioSource) ReadFrame(context.Context, []int16) error { return io.EOF }
func (inertAudioSource) Close() error                             { return nil }

type inertAudioSink struct{}

func (inertAudioSink) WriteFrame(context.Context, []int16) error { return nil }
func (inertAudioSink) Close() error                              { return nil }

func defaultDeviceRegistry() DeviceRegistry { return audio.NewPlatformDeviceRegistry() }
func defaultAudioSource() AudioSource       { return inertAudioSource{} }
func defaultAudioSink() AudioSink           { return inertAudioSink{} }

var (
	_ AudioSource = inertAudioSource{}
	_ AudioSink   = inertAudioSink{}
	_ Clock       = clock.Real{}
)
