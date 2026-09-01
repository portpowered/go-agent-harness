package services

import (
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
)

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

func sessionPlaybackDiagnosticObserver(sink SessionDiagnosticSink) RTCDevicePlaybackObserver {
	if sink == nil {
		return nil
	}
	return func(id audio.DeviceID, stats audio.PlaybackQueueStats) {
		if stats.DroppedSamples == 0 {
			return
		}
		sink.RecordSessionDiagnostic(SessionDiagnosticRecord{
			Event: SessionDiagnosticEventPlaybackOverflow,
			Fields: map[string]string{
				SessionDiagnosticFieldPlaybackDeviceID:            string(id),
				SessionDiagnosticFieldPlaybackSampleRate:          strconv.Itoa(stats.Format.SampleRate),
				SessionDiagnosticFieldPlaybackChannels:            strconv.Itoa(stats.Format.Channels),
				SessionDiagnosticFieldPlaybackLatencyTargetMillis: strconv.FormatInt(stats.LatencyTarget.Milliseconds(), 10),
				SessionDiagnosticFieldPlaybackCapacitySamples:     strconv.Itoa(stats.CapacitySamples),
				SessionDiagnosticFieldPlaybackQueuedSamples:       strconv.Itoa(stats.QueuedSamples),
				SessionDiagnosticFieldPlaybackPeakQueuedSamples:   strconv.Itoa(stats.PeakQueuedSamples),
				SessionDiagnosticFieldPlaybackDroppedSamples:      strconv.FormatUint(stats.DroppedSamples, 10),
				SessionDiagnosticFieldPlaybackOverflowEvents:      strconv.FormatUint(stats.OverflowEvents, 10),
			},
		})
	}
}
