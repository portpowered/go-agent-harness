package agentsession

import sessiontrace "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"

// Diagnostic event names and field keys form the presentation contract.
const (
	SessionDiagnosticEventFailure                          = sessiontrace.SessionDiagnosticEventFailure
	SessionDiagnosticEventTerminal                         = sessiontrace.SessionDiagnosticEventTerminal
	SessionDiagnosticEventTurn                             = sessiontrace.SessionDiagnosticEventTurn
	SessionDiagnosticEventToolCall                         = sessiontrace.SessionDiagnosticEventToolCall
	SessionDiagnosticEventMetrics                          = sessiontrace.SessionDiagnosticEventMetrics
	SessionDiagnosticEventRoomBound                        = sessiontrace.SessionDiagnosticEventRoomBound
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
	SessionDiagnosticEventPlaybackOverflow                 = sessiontrace.SessionDiagnosticEventPlaybackOverflow
	SessionDiagnosticFieldPlaybackDeviceID                 = "device_id"
	SessionDiagnosticFieldPlaybackSampleRate               = "sample_rate"
	SessionDiagnosticFieldPlaybackChannels                 = "channels"
	SessionDiagnosticFieldPlaybackLatencyTargetMillis      = "latency_target_ms"
	SessionDiagnosticFieldPlaybackCapacitySamples          = "capacity_samples"
	SessionDiagnosticFieldPlaybackQueuedSamples            = "queued_samples"
	SessionDiagnosticFieldPlaybackPeakQueuedSamples        = "peak_queued_samples"
	SessionDiagnosticFieldPlaybackDroppedSamples           = "dropped_samples"
	SessionDiagnosticFieldPlaybackOverflowEvents           = "overflow_events"
	SessionDiagnosticFieldPlaybackParticipantID            = "participant_id"
)
