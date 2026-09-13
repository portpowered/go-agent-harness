package agentruntime

import (
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionobservation"
	sessionobservationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionobservation/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type SessionRuntimeObservationKind = sessionobservation.SessionRuntimeObservationKind
type SessionTokenUsageSemantics = sessionobservation.SessionTokenUsageSemantics
type SessionFinalAccounting = sessionobservation.SessionFinalAccounting
type SessionRuntimeFinalAccounting = sessionobservation.SessionRuntimeFinalAccounting
type SessionRuntimeObservation = sessionobservation.SessionRuntimeObservation
type SessionRuntimeObserver = sessionobservation.SessionRuntimeObserver

const (
	SessionRuntimeObservationAudioOutput               = sessionobservation.SessionRuntimeObservationAudioOutput
	SessionRuntimeObservationAudioInput                = sessionobservation.SessionRuntimeObservationAudioInput
	SessionRuntimeObservationAudioPlaybackReceipt      = sessionobservation.SessionRuntimeObservationAudioPlaybackReceipt
	SessionRuntimeObservationAudioRenderTapUnavailable = sessionobservation.SessionRuntimeObservationAudioRenderTapUnavailable
	SessionRuntimeObservationInputCommit               = sessionobservation.SessionRuntimeObservationInputCommit
	SessionRuntimeObservationResponseCreate            = sessionobservation.SessionRuntimeObservationResponseCreate
	SessionRuntimeObservationTurnCompleted             = sessionobservation.SessionRuntimeObservationTurnCompleted
	SessionRuntimeObservationTerminal                  = sessionobservation.SessionRuntimeObservationTerminal
	SessionTokenUsageIncremental                       = sessionobservation.SessionTokenUsageIncremental
)

type sessionRuntimeObservationRecorder struct {
	service sessionobservation.Service
	// Deprecated compatibility views. Policy and mutable data live in service.
	providerBoundaryObserving bool
	inputPayload              []byte
}

func newSessionRuntimeObservationRecorder(observer SessionRuntimeObserver, source platformclock.Source) *sessionRuntimeObservationRecorder {
	if observer == nil {
		return nil
	}
	service := sessionobservationwire.NewService(observer, source)
	return &sessionRuntimeObservationRecorder{service: service, providerBoundaryObserving: service.ProviderBoundaryObservationsEnabled()}
}
func (r *sessionRuntimeObservationRecorder) enableProviderBoundaryObservations() {
	if r == nil {
		return
	}
	r.service.EnableProviderBoundaryObservations()
	r.providerBoundaryObserving = r.service.ProviderBoundaryObservationsEnabled()
}
func (r *sessionRuntimeObservationRecorder) call(fn func(sessionobservation.Service)) {
	if r != nil && r.service != nil {
		fn(r.service)
	}
}
func (r *sessionRuntimeObservationRecorder) observe(kind SessionRuntimeObservationKind, payload []byte, turns int, clean bool, runErr error) {
	r.call(func(s sessionobservation.Service) { s.Observe(kind, payload, turns, clean, runErr) })
}
func (r *sessionRuntimeObservationRecorder) audioOutputMessage(payload []byte, msg messages.StreamMessage) {
	r.call(func(s sessionobservation.Service) { s.AudioOutputMessage(payload, msg) })
}

func (r *sessionRuntimeObservationRecorder) audioPlaybackReceipt(receipt audio.PlaybackReceipt) {
	r.call(func(s sessionobservation.Service) {
		s.AudioPlaybackReceipt(sessionobservation.PlaybackReceipt{CommandID: receipt.CommandID, Epoch: receipt.Epoch, AudioEndMS: receipt.Interruption.AudioEndMS, Applied: receipt.Applied, Err: receipt.Err})
	})
}

func (r *sessionRuntimeObservationRecorder) audioInput(payload []byte) {
	r.call(func(s sessionobservation.Service) { s.AudioInput(payload) })
}

func (r *sessionRuntimeObservationRecorder) providerAudioSent(payload []byte) {
	r.call(func(s sessionobservation.Service) { s.ProviderAudioSent(payload) })
}

func (r *sessionRuntimeObservationRecorder) inputCommit() {
	r.call(func(s sessionobservation.Service) { s.InputCommit() })
}

func (r *sessionRuntimeObservationRecorder) providerInputCommit() {
	r.call(func(s sessionobservation.Service) { s.ProviderInputCommit() })
}

func (r *sessionRuntimeObservationRecorder) responseCreate(msg messages.StreamMessage) {
	r.call(func(s sessionobservation.Service) { s.ResponseCreate(msg) })
}

func (r *sessionRuntimeObservationRecorder) turnCompleted(turns int) {
	r.call(func(s sessionobservation.Service) { s.TurnCompleted(turns) })
}

func (r *sessionRuntimeObservationRecorder) terminalWithAccounting(turns int, runErr error, accounting *SessionFinalAccounting) {
	r.call(func(s sessionobservation.Service) { s.TerminalWithAccounting(turns, runErr, accounting) })
}

func (r *sessionRuntimeObservationRecorder) observeToolCall(call messages.ToolCall) {
	r.call(func(s sessionobservation.Service) { s.ObserveToolCall(call) })
}

func (r *sessionRuntimeObservationRecorder) observeToolResult(call messages.ToolCall, response messages.ToolCallResponse, failed bool) {
	r.call(func(s sessionobservation.Service) { s.ObserveToolResult(call, response, failed) })
}
