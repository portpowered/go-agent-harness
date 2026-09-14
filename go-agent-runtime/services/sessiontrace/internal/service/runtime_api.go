package service

import (
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func NewRuntimeRecorder(observer sessiontrace.RuntimeObserver, source clock.Source) sessiontrace.RuntimeRecorder {
	if observer == nil {
		return nil
	}
	return newSessionRuntimeObservationRecorder(observer, source)
}

func (r *sessionRuntimeObservationRecorder) EnableProviderBoundaryObservations() {
	r.enableProviderBoundaryObservations()
}

func (r *sessionRuntimeObservationRecorder) ObservesProviderBoundaries() bool {
	return r != nil && r.providerBoundaryObserving
}

func (r *sessionRuntimeObservationRecorder) Observe(kind sessiontrace.SessionRuntimeObservationKind, payload []byte, turns int, clean bool, runErr error) {
	r.observe(kind, payload, turns, clean, runErr)
}

func (r *sessionRuntimeObservationRecorder) ObserveWithInputCommit(kind sessiontrace.SessionRuntimeObservationKind, payload []byte, turns, inputCommit int, clean bool, runErr error) {
	r.observeWithInputCommit(kind, payload, turns, inputCommit, clean, runErr)
}

func (r *sessionRuntimeObservationRecorder) ObserveFinal(kind sessiontrace.SessionRuntimeObservationKind, payload []byte, turns, inputCommit int, clean bool, runErr error, accounting *sessiontrace.SessionFinalAccounting) {
	r.observeFinal(kind, payload, turns, inputCommit, clean, runErr, accounting)
}

func (r *sessionRuntimeObservationRecorder) AudioOutputMessage(payload []byte, msg messages.StreamMessage) {
	r.audioOutputMessage(payload, msg)
}

func (r *sessionRuntimeObservationRecorder) AudioPlaybackReceipt(receipt audio.PlaybackReceipt) {
	r.audioPlaybackReceipt(receipt)
}

func (r *sessionRuntimeObservationRecorder) AudioInput(payload []byte) { r.audioInput(payload) }
func (r *sessionRuntimeObservationRecorder) ProviderAudioSent(payload []byte) {
	r.providerAudioSent(payload)
}
func (r *sessionRuntimeObservationRecorder) InputCommit()         { r.inputCommit() }
func (r *sessionRuntimeObservationRecorder) ProviderInputCommit() { r.providerInputCommit() }
func (r *sessionRuntimeObservationRecorder) ResponseCreate(msg messages.StreamMessage) {
	r.responseCreate(msg)
}
func (r *sessionRuntimeObservationRecorder) TurnCompleted(turns int) { r.turnCompleted(turns) }
func (r *sessionRuntimeObservationRecorder) TerminalWithAccounting(turns int, runErr error, accounting *sessiontrace.SessionFinalAccounting) {
	r.terminalWithAccounting(turns, runErr, accounting)
}
func (r *sessionRuntimeObservationRecorder) ObserveToolCall(call messages.ToolCall) {
	r.observeToolCall(call)
}
func (r *sessionRuntimeObservationRecorder) ObserveToolResult(call messages.ToolCall, response messages.ToolCallResponse, failed bool) {
	r.observeToolResult(call, response, failed)
}
