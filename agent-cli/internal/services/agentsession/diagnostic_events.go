package agentsession

import sessiontrace "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"

// Diagnostic event names and field keys form the presentation contract.
const (
	SessionDiagnosticEventFailure                     = sessiontrace.SessionDiagnosticEventFailure
	SessionDiagnosticEventTurn                        = sessiontrace.SessionDiagnosticEventTurn
	SessionDiagnosticEventToolCall                    = sessiontrace.SessionDiagnosticEventToolCall
	SessionDiagnosticEventPlaybackOverflow            = sessiontrace.SessionDiagnosticEventPlaybackOverflow
	SessionDiagnosticFieldPlaybackDeviceID            = "device_id"
	SessionDiagnosticFieldPlaybackSampleRate          = "sample_rate"
	SessionDiagnosticFieldPlaybackChannels            = "channels"
	SessionDiagnosticFieldPlaybackLatencyTargetMillis = "latency_target_ms"
	SessionDiagnosticFieldPlaybackCapacitySamples     = "capacity_samples"
	SessionDiagnosticFieldPlaybackQueuedSamples       = "queued_samples"
	SessionDiagnosticFieldPlaybackPeakQueuedSamples   = "peak_queued_samples"
	SessionDiagnosticFieldPlaybackDroppedSamples      = "dropped_samples"
	SessionDiagnosticFieldPlaybackOverflowEvents      = "overflow_events"
	SessionDiagnosticFieldPlaybackParticipantID       = "participant_id"
)
