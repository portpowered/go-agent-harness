package services

import (
	"log"
	"strconv"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
)

const (
	// SessionDiagnosticEventPlaybackOverflow is emitted once at output-device
	// teardown when the bounded speaker queue discarded samples for overflow.
	SessionDiagnosticEventPlaybackOverflow = "session_playback_overflow"

	SessionDiagnosticFieldPlaybackDeviceID            = "device_id"
	SessionDiagnosticFieldPlaybackSampleRate          = "sample_rate"
	SessionDiagnosticFieldPlaybackChannels            = "channels"
	SessionDiagnosticFieldPlaybackLatencyTargetMillis = "latency_target_ms"
	SessionDiagnosticFieldPlaybackCapacitySamples     = "capacity_samples"
	SessionDiagnosticFieldPlaybackQueuedSamples       = "queued_samples"
	SessionDiagnosticFieldPlaybackPeakQueuedSamples   = "peak_queued_samples"
	SessionDiagnosticFieldPlaybackDroppedSamples      = "dropped_samples"
	SessionDiagnosticFieldPlaybackOverflowEvents      = "overflow_events"
	// SessionDiagnosticFieldPlaybackParticipantID identifies which room
	// participant's local speaker queue dropped samples. It is set only by
	// emitRoomParticipantPlaybackOverflowDiagnostic; the single-session and
	// self-play paths have no participant to name.
	SessionDiagnosticFieldPlaybackParticipantID = "participant_id"
)

// fallbackPlaybackDiagnosticSink is the sink resolvePlaybackDiagnosticSink
// installs whenever a caller did not wire one. This is the second time this
// exact instrumentation has been found unwired end to end: #360 fixed the
// CLI's SessionRunOptions.Diagnostics after #350's counters were found never
// reaching it, and the room/self-play half was still missed. Patching each
// forgetful call site clearly does not close this class of bug, so instead
// every place in this codebase that observes a playback queue overflow
// (sessionPlaybackDiagnosticObserver below, and
// emitRoomParticipantPlaybackOverflowDiagnostic for a room's human
// participant device) resolves its sink through resolvePlaybackDiagnosticSink
// rather than trusting the caller-supplied sink directly. A dropped sample
// can now, at worst, degrade from "written to the caller's sink" to "logged
// here" -- it can never again silently vanish because nobody remembered to
// populate SessionRunOptions.Diagnostics.
var fallbackPlaybackDiagnosticSink SessionDiagnosticSink = logPlaybackDiagnosticSink{}

// logPlaybackDiagnosticSink is a named (comparable) type rather than a
// SessionDiagnosticFunc closure so tests can assert identity against
// fallbackPlaybackDiagnosticSink without tripping Go's "comparing
// uncomparable type" panic on function values.
type logPlaybackDiagnosticSink struct{}

// RecordSessionDiagnostic implements SessionDiagnosticSink.
func (logPlaybackDiagnosticSink) RecordSessionDiagnostic(record SessionDiagnosticRecord) {
	log.Printf("session diagnostic (no SessionRunOptions.Diagnostics sink configured): event=%s fields=%v", record.Event, record.Fields)
}

// resolvePlaybackDiagnosticSink returns sink unchanged when the caller
// supplied one, otherwise the package fallback. See fallbackPlaybackDiagnosticSink
// for why every playback-overflow observation point in this package routes
// through this function instead of checking sink == nil itself.
func resolvePlaybackDiagnosticSink(sink SessionDiagnosticSink) SessionDiagnosticSink {
	if sink != nil {
		return sink
	}
	return fallbackPlaybackDiagnosticSink
}

func combineRTCDevicePlaybackObservers(observers ...RTCDevicePlaybackObserver) RTCDevicePlaybackObserver {
	var active []RTCDevicePlaybackObserver
	for _, observer := range observers {
		if observer != nil {
			active = append(active, observer)
		}
	}
	if len(active) == 0 {
		return nil
	}
	return func(id audio.DeviceID, stats audio.PlaybackQueueStats) {
		for _, observer := range active {
			observer(id, stats)
		}
	}
}

// playbackOverflowDiagnosticFields formats one playback queue snapshot into
// the canonical field set shared by every playback-overflow emission point.
func playbackOverflowDiagnosticFields(id audio.DeviceID, stats audio.PlaybackQueueStats) map[string]string {
	return map[string]string{
		SessionDiagnosticFieldPlaybackDeviceID:            string(id),
		SessionDiagnosticFieldPlaybackSampleRate:          strconv.Itoa(stats.Format.SampleRate),
		SessionDiagnosticFieldPlaybackChannels:            strconv.Itoa(stats.Format.Channels),
		SessionDiagnosticFieldPlaybackLatencyTargetMillis: strconv.FormatInt(stats.LatencyTarget.Milliseconds(), 10),
		SessionDiagnosticFieldPlaybackCapacitySamples:     strconv.Itoa(stats.CapacitySamples),
		SessionDiagnosticFieldPlaybackQueuedSamples:       strconv.Itoa(stats.QueuedSamples),
		SessionDiagnosticFieldPlaybackPeakQueuedSamples:   strconv.Itoa(stats.PeakQueuedSamples),
		SessionDiagnosticFieldPlaybackDroppedSamples:      strconv.FormatUint(stats.DroppedSamples, 10),
		SessionDiagnosticFieldPlaybackOverflowEvents:      strconv.FormatUint(stats.OverflowEvents, 10),
	}
}

// sessionPlaybackDiagnosticObserver is installed as the RTCDeviceBinding's
// RTCDevicePlaybackObserver by planSessionRuntime for every SessionRunOptions
// caller (single session, browser, recording, and replay-with-devices). sink
// is resolved by the caller (see planSessionRuntime) so it is never nil in
// production; the defensive check below only protects the handful of unit
// tests that call this constructor directly with an explicit nil.
func sessionPlaybackDiagnosticObserver(sink SessionDiagnosticSink) RTCDevicePlaybackObserver {
	if sink == nil {
		return nil
	}
	return func(id audio.DeviceID, stats audio.PlaybackQueueStats) {
		if stats.DroppedSamples == 0 {
			return
		}
		sink.RecordSessionDiagnostic(SessionDiagnosticRecord{
			Event:  SessionDiagnosticEventPlaybackOverflow,
			Fields: playbackOverflowDiagnosticFields(id, stats),
		})
	}
}

// emitRoomParticipantPlaybackOverflowDiagnostic reports one room human
// participant's local speaker queue overflow at participant teardown. Room
// human participants never construct a SessionRunOptions or go through
// planSessionRuntime: they own a raw *audio.DeviceSink directly (see
// openRoomHumanDevices in session_room_orchestration.go), so
// sessionPlaybackDiagnosticObserver above never applies to them at all. This
// is the room's independent choke point for the identical class of bug --
// see fallbackPlaybackDiagnosticSink -- and it names the dropping participant
// so an operator can tell who lost audio.
func emitRoomParticipantPlaybackOverflowDiagnostic(participantID string, output *audio.DeviceSink, sink SessionDiagnosticSink) {
	if output == nil {
		return
	}
	stats := output.PlaybackStats()
	if stats.DroppedSamples == 0 {
		return
	}
	fields := playbackOverflowDiagnosticFields(output.DeviceID(), stats)
	fields[SessionDiagnosticFieldPlaybackParticipantID] = participantID
	resolvePlaybackDiagnosticSink(sink).RecordSessionDiagnostic(SessionDiagnosticRecord{
		Event:  SessionDiagnosticEventPlaybackOverflow,
		Fields: fields,
	})
}
