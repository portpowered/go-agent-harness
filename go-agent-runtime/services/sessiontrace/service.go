// Package sessiontrace owns the host-neutral audio trace lifecycle.
package sessiontrace

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type errorCode string

func (e errorCode) Error() string { return string(e) }

const (
	ErrClockRequired     errorCode = "session trace clock is required"
	ErrCloseTimeout      errorCode = "session trace close timed out"
	ErrDestinationExists errorCode = "audio trace destination exists"
	DefaultCloseTimeout            = time.Second
)

// CaptureSamplesObserver receives a copy-safe PCM tap notification.
type CaptureSamplesObserver func(rate int, samples []int16)

// PlaybackSamplesObserver receives the enqueued PCM tap and may preserve a
// pre-existing observer error.
type PlaybackSamplesObserver func(context.Context, int, []int16) error

// DeviceBinding contains only the callbacks needed by trace capture. Device
// ownership and all other device configuration remain in the gateway.
type DeviceBinding struct {
	PreGateSamplesObserver     CaptureSamplesObserver
	UploadedSamplesObserver    CaptureSamplesObserver
	PlaybackSamplesObserver    PlaybackSamplesObserver
	RenderedSamplesObserver    CaptureSamplesObserver
	RenderedSamplesUnavailable func()
}

// SessionRuntimeObservationKind identifies an observable runtime boundary.
type SessionRuntimeObservationKind string

const (
	SessionRuntimeObservationAudioOutput               SessionRuntimeObservationKind = "audio_output"
	SessionRuntimeObservationAudioInput                SessionRuntimeObservationKind = "audio_input"
	SessionRuntimeObservationAudioPlaybackReceipt      SessionRuntimeObservationKind = "audio_playback_receipt"
	SessionRuntimeObservationAudioRenderTapUnavailable SessionRuntimeObservationKind = "audio_render_tap_unavailable"
	SessionRuntimeObservationInputCommit               SessionRuntimeObservationKind = "input_commit"
	SessionRuntimeObservationResponseCreate            SessionRuntimeObservationKind = "response_create"
	SessionRuntimeObservationTurnCompleted             SessionRuntimeObservationKind = "turn_completed"
	SessionRuntimeObservationTerminal                  SessionRuntimeObservationKind = "terminal"
)

type SessionTokenUsageSemantics string

const SessionTokenUsageIncremental SessionTokenUsageSemantics = "incremental"

type SessionFinalAccounting struct {
	PromptTokens     uint64
	CompletionTokens uint64
	TotalTokens      uint64
	ReasoningTokens  uint64
	UsageSemantics   SessionTokenUsageSemantics
	Metrics          metrics.Snapshot
}

type SessionRuntimeFinalAccounting = SessionFinalAccounting

// SessionRuntimeObservation is one clock-stamped observation from a session.
type SessionRuntimeObservation struct {
	Kind            SessionRuntimeObservationKind
	Tick            uint64
	Timestamp       time.Time
	Payload         []byte
	TurnsCompleted  int
	InputCommit     int
	ResponseID      string
	ResponsePurpose messages.ResponsePurpose
	StreamID        string
	LoopPassID      int
	Epoch           uint64
	Clean           bool
	Error           string
	FinalAccounting *SessionFinalAccounting
}

type RuntimeObservation = SessionRuntimeObservation
type FinalAccounting = SessionFinalAccounting

// SessionRuntimeObserver receives ordered runtime events.
type SessionRuntimeObserver interface {
	ObserveSessionRuntime(SessionRuntimeObservation)
}

type RuntimeObserver = SessionRuntimeObserver

// RuntimeObserverFunc adapts a function to RuntimeObserver.
type RuntimeObserverFunc func(RuntimeObservation)

func (f RuntimeObserverFunc) ObserveSessionRuntime(observation SessionRuntimeObservation) {
	f(observation)
}

// ProviderBoundaryObserver is an optional runtime policy preference.
type ProviderBoundaryObserver interface {
	ObserveProviderBoundaries() bool
}

// CommitPayloadObserver is an optional runtime policy preference.
type CommitPayloadObserver interface {
	RetainCommitPayload() bool
}

// Request admits one trace invocation. A nil Clock is rejected whenever
// trace capture is enabled; the service never substitutes a wall clock.
type Request struct {
	TraceAudio      bool
	RecordDirectory string
	Clock           clock.Source
	Credentials     []string
	Device          DeviceBinding
	RuntimeObserver RuntimeObserver
	CloseTimeout    time.Duration
}

// Prepared owns one staged trace until Finish publishes or retains it.
type Prepared interface {
	DeviceBinding() DeviceBinding
	RuntimeObserver() RuntimeObserver
	StagedPath() string
	Finish(context.Context, string, bool) error
}

// Service creates one independent prepared trace per request.
type Service interface {
	Prepare(Request) (Prepared, error)
}
