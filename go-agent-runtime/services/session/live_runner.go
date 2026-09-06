package session

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// AudioTurnAdmission controls how a finite sequence of caller-owned audio
// turns enters one live provider session. The default completion-gated mode
// waits for each assistant response before admitting the next turn. Barge
// mode admits the next turn after cancelling an active, non-terminal response
// through the same ordered control path as its PCM and commit boundary.
type AudioTurnAdmission string

const (
	AudioTurnAdmissionCompletionGated AudioTurnAdmission = "completion_gated"
	AudioTurnAdmissionBarge           AudioTurnAdmission = "barge"
)

// LiveRunOptions describes one complete live invocation. The session owner
// opens the injected device service, attaches its bounded media workers to the
// provider endpoints, drains typed events, and joins every worker before it
// returns. A nil Devices value is valid for a provider-only invocation; when
// it is present, DeviceRequest must enable at least one media direction.
type LiveRunOptions struct {
	Request       LiveRequest
	Devices       devices.Service
	DeviceRequest devices.Request
	// CaptureTurns is an ordered set of caller-opened finite sources. The live
	// owner admits one source at a time, sends the configured completion
	// controls, and waits for that response before opening the next source.
	// Sources admitted by a device service are owned and closed by that service;
	// sources that were never admitted remain the caller's responsibility.
	CaptureTurns []devices.FileInput
	Events       LiveEventSink
	// CaptureCompleteControls are sent through the same ordered ingress after
	// a capture pump reaches EOF. A finite source commonly supplies one
	// LiveControlAudioCommit value here; the runtime waits for provider
	// admission before allowing the invocation to finish.
	CaptureCompleteControls []LiveControl
	// AudioTurnAdmission applies when CaptureTurns contains multiple finite
	// sources. Zero selects AudioTurnAdmissionCompletionGated.
	AudioTurnAdmission AudioTurnAdmission
	// Recorder is an optional invocation-owned evidence sink. The runtime
	// supplies ordered stream, media, and terminal observations and calls
	// Finalize after all provider/device workers have joined.
	Recorder LiveRecorder
	// PlaybackDrainTimeout bounds graceful device playback after the provider
	// has published its terminal event. It is ignored for cancellation and
	// failure paths, which stop media immediately. Zero selects the runtime's
	// bounded default.
	PlaybackDrainTimeout time.Duration
}

// LiveRunner is the complete invocation role implemented by the built-in live
// service. Keeping it separate from LiveService lets small embedders inject a
// fake OpenLive-only service while production hosts use one owner for media,
// event delivery, cancellation, and terminal joining.
type LiveRunner interface {
	RunLive(context.Context, LiveRunOptions) error
}

// LiveHandle owns one continuous provider session and its media endpoints.
// Media returns caller-owned, bounded PCM endpoints; callers must close each
// non-nil endpoint after Wait. Start is accepted exactly once. Wait before
// Start returns ErrLiveNotStarted, while Cancel and Close are safe before
// Start. Cancel requests an orderly shutdown with a typed cause; Wait joins
// every started worker and returns its terminal error. Close cancels a started
// session, joins it, closes all owned endpoints, and is idempotent. Start after
// Close returns ErrLiveClosed. OpenLive, Start and Send require a non-nil context.
type LiveHandle interface {
	Media() sharedaudio.MediaEndpoints
	Events() <-chan LiveEvent
	Start(context.Context) error
	Send(context.Context, LiveControl) error
	Cancel(error)
	Wait() error
	Close() error
}

// LiveService is the optional continuous-session role of the session owner.
// It is intentionally separate from Service so headless text hosts do not
// initialize or depend on device/audio composition.
type LiveService interface {
	OpenLive(context.Context, LiveRequest) (LiveHandle, error)
}
