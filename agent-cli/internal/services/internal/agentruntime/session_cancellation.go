package agentruntime
import (
	sessionfinalization "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfinalization"
	sessionfinalizationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfinalization/wire"
)
func sessionSIGINTCancellationOnly(err error, intent *SessionCancellationIntent) bool {
	return sessionfinalizationwire.NewService().SIGINTCancellationOnly(err, intent, sessionSIGINTOptions())
}
func sessionSIGINTErrorOnly(err error) bool {
	return sessionfinalizationwire.NewService().SIGINTErrorOnly(err, sessionSIGINTOptions())
}
func sessionSIGINTOptions() sessionfinalization.ErrorTreeOptions { return sessionfinalization.NewErrorTreeOptions(ErrSessionAudioInputEndOfTurnLost, ErrSessionScheduledAudioIncomplete, ErrSessionUnresolvedToolResults) }
func (e *SessionAudioInputError) CancellationCause() error {
	if e == nil { return nil }
	return e.Err
}
func sessionObserverFailure(observer *sessionProgressObserver) *sessionfinalization.ObserverFailure {
	if facts := observer.failureSnapshot(); facts != nil { return &sessionfinalization.ObserverFailure{TerminalReason: facts.terminalReason, Provenance: facts.provenance} }
	return nil
}
func sessionSIGINTCleanForObserver(err error, intent *SessionCancellationIntent, observer *sessionProgressObserver) bool {
	return sessionfinalizationwire.NewService().SIGINTCleanForObserver(err, intent, sessionObserverFailure(observer), sessionSIGINTOptions())
}
func sessionSIGINTObserverFailureOnly(observer *sessionProgressObserver) bool {
	return sessionfinalizationwire.NewService().SIGINTObserverFailureOnly(sessionObserverFailure(observer))
}
