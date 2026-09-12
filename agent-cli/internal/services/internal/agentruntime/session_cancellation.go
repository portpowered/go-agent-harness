package agentruntime

import (
	sf "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfinalization"
	sfw "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfinalization/wire"
)

func sessionSIGINTCancellationOnly(err error, intent *SessionCancellationIntent) bool {
	return sfw.NewService().SIGINTCancellationOnly(err, intent, sessionSIGINTOptions())
}
func sessionSIGINTErrorOnly(err error) bool {
	return sfw.NewService().SIGINTErrorOnly(err, sessionSIGINTOptions())
}
func sessionSIGINTOptions() sf.ErrorTreeOptions {
	return (sf.ErrorTreeOptions{}).WithDefaults(ErrSessionAudioInputEndOfTurnLost, ErrSessionScheduledAudioIncomplete, ErrSessionUnresolvedToolResults)
}
func (e *SessionAudioInputError) CancellationCause() error {
	if e == nil {
		return nil
	}
	return e.Err
}
func sessionObserverFailure(observer *sessionProgressObserver) *sf.ObserverFailure {
	if facts := observer.failureSnapshot(); facts != nil {
		return &sf.ObserverFailure{TerminalReason: facts.terminalReason, Provenance: facts.provenance}
	}
	return nil
}
func sessionSIGINTCleanForObserver(err error, intent *SessionCancellationIntent, observer *sessionProgressObserver) bool {
	return sfw.NewService().SIGINTCleanForObserver(err, intent, sessionObserverFailure(observer), sessionSIGINTOptions())
}
func sessionSIGINTObserverFailureOnly(observer *sessionProgressObserver) bool {
	return sfw.NewService().SIGINTObserverFailureOnly(sessionObserverFailure(observer))
}
