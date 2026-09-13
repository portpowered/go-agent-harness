package agentruntime

import sessionobservation "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionobservation"

// Deprecated aliases: use go-agent-runtime/services/sessionobservation. These
// aliases keep the CLI observer vocabulary source-compatible during retirement.
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
