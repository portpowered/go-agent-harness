package agentruntime

import sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"

import (
	"context"
	"log"
	"strconv"

	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
)

const (
	// SessionDiagnosticEventPlaybackOverflow is emitted once at output-device
	// teardown when the bounded speaker queue discarded samples for overflow.
	SessionDiagnosticEventPlaybackOverflow = sessioncontract.SessionDiagnosticEventPlaybackOverflow

	SessionDiagnosticFieldPlaybackDeviceID            = sessioncontract.SessionDiagnosticFieldPlaybackDeviceID
	SessionDiagnosticFieldPlaybackSampleRate          = sessioncontract.SessionDiagnosticFieldPlaybackSampleRate
	SessionDiagnosticFieldPlaybackChannels            = sessioncontract.SessionDiagnosticFieldPlaybackChannels
	SessionDiagnosticFieldPlaybackLatencyTargetMillis = sessioncontract.SessionDiagnosticFieldPlaybackLatencyTargetMillis
	SessionDiagnosticFieldPlaybackCapacitySamples     = sessioncontract.SessionDiagnosticFieldPlaybackCapacitySamples
	SessionDiagnosticFieldPlaybackQueuedSamples       = sessioncontract.SessionDiagnosticFieldPlaybackQueuedSamples
	SessionDiagnosticFieldPlaybackPeakQueuedSamples   = sessioncontract.SessionDiagnosticFieldPlaybackPeakQueuedSamples
	SessionDiagnosticFieldPlaybackDroppedSamples      = sessioncontract.SessionDiagnosticFieldPlaybackDroppedSamples
	SessionDiagnosticFieldPlaybackOverflowEvents      = sessioncontract.SessionDiagnosticFieldPlaybackOverflowEvents
	// SessionDiagnosticFieldPlaybackParticipantID identifies which room
	// participant's local speaker queue dropped samples. It is set only by
	// emitRoomParticipantPlaybackOverflowDiagnostic; the single-session path
	// has no room participant to name.
	SessionDiagnosticFieldPlaybackParticipantID = sessioncontract.SessionDiagnosticFieldPlaybackParticipantID

	SessionLogMessagePlaybackSnapshot = "audio playback queue finalized"
)

var playbackMetricSamples = []struct {
	name  string
	kind  string
	unit  string
	value func(audio.PlaybackQueueStats) float64
}{
	{name: "audio.playback.queue.depth", kind: "gauge", unit: "samples", value: func(s audio.PlaybackQueueStats) float64 { return float64(s.QueuedSamples) }},
	{name: "audio.playback.queue.peak", kind: "gauge", unit: "samples", value: func(s audio.PlaybackQueueStats) float64 { return float64(s.PeakQueuedSamples) }},
	{name: "audio.playback.rendered", kind: "counter", unit: "samples", value: func(s audio.PlaybackQueueStats) float64 { return float64(s.RenderedSamples) }},
	{name: "audio.playback.callbacks", kind: "counter", unit: "callbacks", value: func(s audio.PlaybackQueueStats) float64 { return float64(s.CallbackCount) }},
	{name: "audio.playback.underflows", kind: "counter", unit: "events", value: func(s audio.PlaybackQueueStats) float64 { return float64(s.UnderflowEvents) }},
	{name: "audio.playback.underflow", kind: "counter", unit: "samples", value: func(s audio.PlaybackQueueStats) float64 { return float64(s.UnderflowSamples) }},
	{name: "audio.playback.zero_fill", kind: "counter", unit: "samples", value: func(s audio.PlaybackQueueStats) float64 { return float64(s.ZeroFilledSamples) }},
	{name: "audio.playback.overflows", kind: "counter", unit: "events", value: func(s audio.PlaybackQueueStats) float64 { return float64(s.OverflowEvents) }},
	{name: "audio.playback.dropped", kind: "counter", unit: "samples", value: func(s audio.PlaybackQueueStats) float64 { return float64(s.DroppedSamples) }},
	{name: "audio.playback.discards", kind: "counter", unit: "events", value: func(s audio.PlaybackQueueStats) float64 { return float64(s.DiscardEvents) }},
	{name: "audio.playback.discarded", kind: "counter", unit: "samples", value: func(s audio.PlaybackQueueStats) float64 { return float64(s.DiscardedSamples) }},
	{name: "audio.playback.queue.minimum", kind: "gauge", unit: "samples", value: func(s audio.PlaybackQueueStats) float64 { return float64(s.MinimumQueuedSamples) }},
}

// fallbackPlaybackDiagnosticSink is the sink resolvePlaybackDiagnosticSink
// installs whenever a caller did not wire one. This is the second time this
// exact instrumentation has been found unwired end to end: #360 fixed the
// CLI's SessionRunOptions.Diagnostics after #350's counters were found never
// reaching it, and the room playback half was still missed. Patching each
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

func combineRTCDevicePlaybackObservers(observers ...runtimeDevices.PlaybackObserver) runtimeDevices.PlaybackObserver {
	var active []runtimeDevices.PlaybackObserver
	for _, observer := range observers {
		if observer != nil {
			active = append(active, observer)
		}
	}
	if len(active) == 0 {
		return nil
	}
	return func(id string, stats audio.PlaybackQueueStats) {
		for _, observer := range active {
			observer(id, stats)
		}
	}
}

func combineRTCDeviceCaptureObservers(observers ...runtimeDevices.CaptureObserver) runtimeDevices.CaptureObserver {
	var active []runtimeDevices.CaptureObserver
	for _, observer := range observers {
		if observer != nil {
			active = append(active, observer)
		}
	}
	if len(active) == 0 {
		return nil
	}
	return func(id string, stats audio.CaptureQueueStats) {
		for _, observer := range active {
			observer(id, stats)
		}
	}
}

func combineRTCDevicePlaybackReceiptObservers(observers ...runtimeDevices.PlaybackReceiptObserver) runtimeDevices.PlaybackReceiptObserver {
	var active []runtimeDevices.PlaybackReceiptObserver
	for _, observer := range observers {
		if observer != nil {
			active = append(active, observer)
		}
	}
	if len(active) == 0 {
		return nil
	}
	return func(receipt audio.PlaybackReceipt) {
		for _, observer := range active {
			observer(receipt)
		}
	}
}

// playbackOverflowDiagnosticFields formats one playback queue snapshot into
// the canonical field set shared by every playback-overflow emission point.
func playbackOverflowDiagnosticFields(id string, stats audio.PlaybackQueueStats) map[string]string {
	return map[string]string{
		SessionDiagnosticFieldPlaybackDeviceID:            id,
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

// sessionPlaybackDiagnosticObserver is installed as the RTCBinding's
// devices.PlaybackObserver by planSessionRuntime for every SessionRunOptions
// caller (single session, browser, recording, and replay-with-devices). sink
// is resolved by the caller (see planSessionRuntime) so it is never nil in
// production; the defensive check below only protects the handful of unit
// tests that call this constructor directly with an explicit nil.
func sessionPlaybackDiagnosticObserver(sink SessionDiagnosticSink) runtimeDevices.PlaybackObserver {
	if sink == nil {
		return nil
	}
	return func(id string, stats audio.PlaybackQueueStats) {
		if stats.DroppedSamples == 0 {
			return
		}
		sink.RecordSessionDiagnostic(SessionDiagnosticRecord{
			Event:  SessionDiagnosticEventPlaybackOverflow,
			Fields: playbackOverflowDiagnosticFields(id, stats),
		})
	}
}

// sessionPlaybackObservabilityObserver exports the complete synchronized
// queue snapshot after the device service closes its handle, outside native
// callbacks.
func sessionPlaybackObservabilityObserver(ctx context.Context, sampler observability.MetricSampler, logger observability.Logger) runtimeDevices.PlaybackObserver {
	sampler = observability.EnsureMetricSampler(sampler)
	logger = observability.EnsureLogger(logger)
	return func(id string, stats audio.PlaybackQueueStats) {
		fields := observability.Fields{
			"device_id":   id,
			"sample_rate": strconv.Itoa(stats.Format.SampleRate),
			"channels":    strconv.Itoa(stats.Format.Channels),
		}
		for _, definition := range playbackMetricSamples {
			_ = observability.TrySample(ctx, sampler, observability.MetricSample{ //nolint:errcheck // telemetry cannot fail device teardown
				Name: definition.name, Kind: definition.kind, Unit: definition.unit,
				Value: definition.value(stats), Fields: fields,
			})
		}
		level := "info"
		if stats.UnderflowEvents > 0 || stats.OverflowEvents > 0 {
			level = "warn"
		}
		logFields := observability.Fields(playbackOverflowDiagnosticFields(id, stats))
		logFields["underflow_events"] = strconv.FormatUint(stats.UnderflowEvents, 10)
		logFields["underflow_samples"] = strconv.FormatUint(stats.UnderflowSamples, 10)
		logFields["zero_filled_samples"] = strconv.FormatUint(stats.ZeroFilledSamples, 10)
		logFields["rendered_samples"] = strconv.FormatUint(stats.RenderedSamples, 10)
		_ = observability.TryLog(ctx, logger, observability.LogRecord{ //nolint:errcheck // telemetry cannot fail device teardown
			Level: level, Message: SessionLogMessagePlaybackSnapshot, Fields: logFields,
		})
	}
}

func sessionCaptureObservabilityObserver(ctx context.Context, sampler observability.MetricSampler, logger observability.Logger) runtimeDevices.CaptureObserver {
	sampler = observability.EnsureMetricSampler(sampler)
	logger = observability.EnsureLogger(logger)
	return func(id string, stats audio.CaptureQueueStats) {
		fields := observability.Fields{
			"device_id":   id,
			"drop_policy": stats.DropPolicy,
		}
		metrics := []observability.MetricSample{
			{Name: "audio.capture.queue.depth", Kind: "gauge", Value: float64(stats.QueuedSamples), Unit: "samples", Fields: fields},
			{Name: "audio.capture.queue.peak", Kind: "gauge", Value: float64(stats.HighWaterSamples), Unit: "samples", Fields: fields},
			{Name: "audio.capture.captured", Kind: "counter", Value: float64(stats.CapturedSamples), Unit: "samples", Fields: fields},
			{Name: "audio.capture.frames", Kind: "counter", Value: float64(stats.CompletedFrames), Unit: "frames", Fields: fields},
			{Name: "audio.capture.dropped", Kind: "counter", Value: float64(stats.DroppedFrames), Unit: "frames", Fields: fields},
			{Name: "audio.capture.dropped", Kind: "counter", Value: float64(stats.DroppedSamples), Unit: "samples", Fields: fields},
			{Name: "audio.capture.sequence_gaps", Kind: "counter", Value: float64(stats.SequenceGaps), Unit: "gaps", Fields: fields},
		}
		for _, sample := range metrics {
			_ = observability.TrySample(ctx, sampler, sample) //nolint:errcheck // telemetry cannot fail device teardown
		}
		level := "info"
		if stats.DroppedSamples > 0 || stats.SequenceGaps > 0 {
			level = "warn"
		}
		fields["dropped_frames"] = strconv.FormatUint(stats.DroppedFrames, 10)
		fields["dropped_samples"] = strconv.FormatUint(stats.DroppedSamples, 10)
		fields["sequence_gaps"] = strconv.FormatUint(stats.SequenceGaps, 10)
		_ = observability.TryLog(ctx, logger, observability.LogRecord{ //nolint:errcheck // telemetry cannot fail device teardown
			Level: level, Message: "audio capture queue finalized", Fields: fields,
		})
	}
}

// emitRoomParticipantPlaybackOverflowDiagnostic reports one room human
// participant's local speaker queue overflow after the service-owned device
// handle has stopped accepting writes.
func emitRoomParticipantPlaybackOverflowDiagnostic(participantID string, handle runtimeDevices.Handle, sink SessionDiagnosticSink) {
	if handle == nil {
		return
	}
	provider, ok := handle.(runtimeDevices.PlaybackStatsProvider)
	if !ok {
		return
	}
	deviceID, stats := provider.PlaybackStats()
	if stats.DroppedSamples == 0 {
		return
	}
	fields := playbackOverflowDiagnosticFields(deviceID, stats)
	fields[SessionDiagnosticFieldPlaybackParticipantID] = participantID
	resolvePlaybackDiagnosticSink(sink).RecordSessionDiagnostic(SessionDiagnosticRecord{
		Event:  SessionDiagnosticEventPlaybackOverflow,
		Fields: fields,
	})
}
