package agentruntime

import sessiontrace "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"

type SessionRuntimeObservationKind = sessiontrace.SessionRuntimeObservationKind
type SessionTokenUsageSemantics = sessiontrace.SessionTokenUsageSemantics
type SessionFinalAccounting = sessiontrace.SessionFinalAccounting
type SessionRuntimeFinalAccounting = sessiontrace.SessionRuntimeFinalAccounting
type SessionRuntimeObservation = sessiontrace.SessionRuntimeObservation
type SessionRuntimeObserver = sessiontrace.RuntimeObserver

const (
	SessionRuntimeObservationAudioOutput               = sessiontrace.SessionRuntimeObservationAudioOutput
	SessionRuntimeObservationAudioInput                = sessiontrace.SessionRuntimeObservationAudioInput
	SessionRuntimeObservationAudioPlaybackReceipt      = sessiontrace.SessionRuntimeObservationAudioPlaybackReceipt
	SessionRuntimeObservationAudioRenderTapUnavailable = sessiontrace.SessionRuntimeObservationAudioRenderTapUnavailable
	SessionRuntimeObservationInputCommit               = sessiontrace.SessionRuntimeObservationInputCommit
	SessionRuntimeObservationResponseCreate            = sessiontrace.SessionRuntimeObservationResponseCreate
	SessionRuntimeObservationTurnCompleted             = sessiontrace.SessionRuntimeObservationTurnCompleted
	SessionRuntimeObservationTerminal                  = sessiontrace.SessionRuntimeObservationTerminal
	SessionTokenUsageIncremental                       = sessiontrace.SessionTokenUsageIncremental
)
