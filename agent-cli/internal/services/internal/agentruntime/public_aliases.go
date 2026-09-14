package agentruntime

import (
	"context"
	"errors"
)

import sessiontrace "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
import sessioncontract "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
import sessiontracewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"

func bindPreparedTrace(options *SessionRunOptions, trace sessiontrace.Prepared) {
	setTraceBinding(options, trace.DeviceBinding())
	options.RuntimeObserver = trace.RuntimeObserver()
}

func finishPreparedTrace(ctx context.Context, directory string, trace sessiontrace.Prepared, runErr error) error {
	return errors.Join(runErr, trace.Finish(context.WithoutCancel(ctx), directory, runErr == nil))
}

type SessionCancellationIntent = sessiontrace.CancellationIntent
type SessionDiagnosticRecord = sessiontrace.DiagnosticRecord
type SessionDiagnosticSink = sessiontrace.DiagnosticSink
type SessionDiagnosticFunc = sessiontrace.DiagnosticFunc
type SessionStreamObserver = sessiontrace.StreamObserver
type ScheduledAudioInput = sessiontrace.ScheduledAudioInput
type SessionLivenessClock = sessiontrace.LivenessClock
type SessionLivenessTimer = sessiontrace.LivenessTimer
type sessionTerminalObservation = sessiontrace.TerminalObservation
type SessionToolDiagnostic = sessiontrace.ToolDiagnostic
type SessionToolDiagnosticSink = sessiontrace.ToolDiagnosticSink
type SessionToolDiagnosticFunc = sessiontrace.ToolDiagnosticFunc
type SessionImageContinuationError = sessioncontract.LiveImageContinuationError
type SessionToolContinuationError = sessioncontract.LiveToolContinuationError

type SessionRuntimeObservationKind = sessiontrace.SessionRuntimeObservationKind
type SessionTokenUsageSemantics = sessiontrace.SessionTokenUsageSemantics
type SessionFinalAccounting = sessiontrace.SessionFinalAccounting
type SessionRuntimeFinalAccounting = sessiontrace.SessionRuntimeFinalAccounting
type SessionRuntimeObservation = sessiontrace.SessionRuntimeObservation
type SessionRuntimeObserver = sessiontrace.RuntimeObserver

func NewSessionCancellationIntent() *SessionCancellationIntent {
	return sessiontracewire.NewCancellationIntent()
}

const (
	SessionDiagnosticEventFailure                          = sessiontrace.SessionDiagnosticEventFailure
	SessionDiagnosticEventTerminal                         = sessiontrace.SessionDiagnosticEventTerminal
	SessionDiagnosticEventTurn                             = sessiontrace.SessionDiagnosticEventTurn
	SessionDiagnosticEventToolCall                         = sessiontrace.SessionDiagnosticEventToolCall
	SessionDiagnosticEventMetrics                          = sessiontrace.SessionDiagnosticEventMetrics
	SessionDiagnosticEventRoomBound                        = sessiontrace.SessionDiagnosticEventRoomBound
	SessionDiagnosticEventPlaybackOverflow                 = sessiontrace.SessionDiagnosticEventPlaybackOverflow
	SessionDiagnosticFieldPlaybackDeviceID                 = sessiontrace.SessionDiagnosticFieldPlaybackDeviceID
	SessionDiagnosticFieldPlaybackSampleRate               = sessiontrace.SessionDiagnosticFieldPlaybackSampleRate
	SessionDiagnosticFieldPlaybackChannels                 = sessiontrace.SessionDiagnosticFieldPlaybackChannels
	SessionDiagnosticFieldPlaybackLatencyTargetMillis      = sessiontrace.SessionDiagnosticFieldPlaybackLatencyTargetMillis
	SessionDiagnosticFieldPlaybackCapacitySamples          = sessiontrace.SessionDiagnosticFieldPlaybackCapacitySamples
	SessionDiagnosticFieldPlaybackQueuedSamples            = sessiontrace.SessionDiagnosticFieldPlaybackQueuedSamples
	SessionDiagnosticFieldPlaybackPeakQueuedSamples        = sessiontrace.SessionDiagnosticFieldPlaybackPeakQueuedSamples
	SessionDiagnosticFieldPlaybackDroppedSamples           = sessiontrace.SessionDiagnosticFieldPlaybackDroppedSamples
	SessionDiagnosticFieldPlaybackOverflowEvents           = sessiontrace.SessionDiagnosticFieldPlaybackOverflowEvents
	SessionDiagnosticFieldPlaybackParticipantID            = sessiontrace.SessionDiagnosticFieldPlaybackParticipantID
	SessionDiagnosticFieldUnresolvedToolResultCount        = sessiontrace.SessionDiagnosticFieldUnresolvedToolResultCount
	SessionDiagnosticFieldUnresolvedToolCallIDs            = sessiontrace.SessionDiagnosticFieldUnresolvedToolCallIDs
	SessionDiagnosticFieldPendingImageContinuationCount    = sessiontrace.SessionDiagnosticFieldPendingImageContinuationCount
	SessionDiagnosticFieldPendingImageContinuationIDs      = sessiontrace.SessionDiagnosticFieldPendingImageContinuationIDs
	SessionDiagnosticFieldPendingToolContinuationCount     = sessiontrace.SessionDiagnosticFieldPendingToolContinuationCount
	SessionDiagnosticFieldPendingToolContinuationIDs       = sessiontrace.SessionDiagnosticFieldPendingToolContinuationIDs
	SessionDiagnosticFieldScheduledInputCount              = sessiontrace.SessionDiagnosticFieldScheduledInputCount
	SessionDiagnosticFieldDispatchedInputCount             = sessiontrace.SessionDiagnosticFieldDispatchedInputCount
	SessionDiagnosticFieldCompletedTurnCount               = sessiontrace.SessionDiagnosticFieldCompletedTurnCount
	SessionDiagnosticFieldPendingContinuationStatuses      = sessiontrace.SessionDiagnosticFieldPendingContinuationStatuses
	SessionDiagnosticFieldPendingContinuationCodes         = sessiontrace.SessionDiagnosticFieldPendingContinuationCodes
	SessionDiagnosticFieldPendingContinuationDetails       = sessiontrace.SessionDiagnosticFieldPendingContinuationDetails
	SessionDiagnosticFieldCancelledBy                      = sessiontrace.SessionDiagnosticFieldCancelledBy
	SessionDiagnosticFieldCancelledScheduledInputCount     = sessiontrace.SessionDiagnosticFieldCancelledScheduledInputCount
	SessionDiagnosticFieldCancelledToolResultCount         = sessiontrace.SessionDiagnosticFieldCancelledToolResultCount
	SessionDiagnosticFieldCancelledToolResultCallIDs       = sessiontrace.SessionDiagnosticFieldCancelledToolResultCallIDs
	SessionDiagnosticFieldCancelledToolContinuationCount   = sessiontrace.SessionDiagnosticFieldCancelledToolContinuationCount
	SessionDiagnosticFieldCancelledToolContinuationCallIDs = sessiontrace.SessionDiagnosticFieldCancelledToolContinuationCallIDs
	fieldTurnIndex                                         = "turn_index"
	SessionRuntimeObservationAudioOutput                   = sessiontrace.SessionRuntimeObservationAudioOutput
	SessionRuntimeObservationAudioInput                    = sessiontrace.SessionRuntimeObservationAudioInput
	SessionRuntimeObservationAudioPlaybackReceipt          = sessiontrace.SessionRuntimeObservationAudioPlaybackReceipt
	SessionRuntimeObservationAudioRenderTapUnavailable     = sessiontrace.SessionRuntimeObservationAudioRenderTapUnavailable
	SessionRuntimeObservationInputCommit                   = sessiontrace.SessionRuntimeObservationInputCommit
	SessionRuntimeObservationResponseCreate                = sessiontrace.SessionRuntimeObservationResponseCreate
	SessionRuntimeObservationTurnCompleted                 = sessiontrace.SessionRuntimeObservationTurnCompleted
	SessionRuntimeObservationTerminal                      = sessiontrace.SessionRuntimeObservationTerminal
	SessionTokenUsageIncremental                           = sessiontrace.SessionTokenUsageIncremental
)

const SessionUserCancelledClassification = "user_cancelled"
const ErrSessionAudioResponseIncomplete = sessioncontract.ErrLiveAudioResponseIncomplete
const SessionSilentProviderTimeoutClassification = sessiontrace.SilentProviderTimeoutClassification
