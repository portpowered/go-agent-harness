package agentruntime

import sessiontrace "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"

// Session runtime observation types are owned by the reusable session trace
// contract. These aliases preserve the CLI's existing composition API.
type SessionRuntimeObservationKind = sessiontrace.SessionRuntimeObservationKind
type SessionTokenUsageSemantics = sessiontrace.SessionTokenUsageSemantics
type SessionFinalAccounting = sessiontrace.SessionFinalAccounting
type SessionRuntimeFinalAccounting = sessiontrace.SessionRuntimeFinalAccounting
type SessionRuntimeObservation = sessiontrace.SessionRuntimeObservation
type SessionRuntimeObserver = sessiontrace.SessionRuntimeObserver

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
