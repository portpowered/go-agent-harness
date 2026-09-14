// Package observability owns device playback and capture projections behind
// the public devices observer contract. It does not open devices or retain
// process-global sinks.
package observability

import (
	"context"
	"log"
	"strconv"

	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	runtimeobs "github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
)

type Service struct {
	diagnostic      runtimeDevices.DiagnosticSink
	sampler         runtimeobs.MetricSampler
	logger          runtimeobs.Logger
	playbackMetrics []metricDefinition
}

const (
	logLevelInfo = "info"
	logLevelWarn = "warn"
)

// New creates one invocation-owned observer service. Missing diagnostics use
// a fresh fallback logger owned by this instance rather than a mutable global.
func New(deps runtimeDevices.ObserverDependencies) *Service {
	diagnostic := deps.DiagnosticSink
	if diagnostic == nil {
		diagnostic = logDiagnosticSink{}
	}
	return &Service{
		diagnostic:      diagnostic,
		sampler:         runtimeobs.EnsureMetricSampler(deps.MetricSampler),
		logger:          runtimeobs.EnsureLogger(deps.Logger),
		playbackMetrics: newPlaybackMetricDefinitions(),
	}
}

func (s *Service) PlaybackObserver() runtimeDevices.PlaybackObserver {
	if s == nil {
		return nil
	}
	return s.CombinePlaybackObservers(s.PlaybackOverflowObserver(), s.PlaybackObservabilityObserver())
}

func (s *Service) PlaybackOverflowObserver() runtimeDevices.PlaybackObserver {
	if s == nil {
		return nil
	}
	return func(deviceID string, stats audio.PlaybackQueueStats) {
		s.ReportPlaybackOverflow(runtimeDevices.PlaybackOverflowReport{DeviceID: deviceID, Stats: stats})
	}
}

func (s *Service) PlaybackObservabilityObserver() runtimeDevices.PlaybackObserver {
	if s == nil {
		return nil
	}
	return func(deviceID string, stats audio.PlaybackQueueStats) {
		s.observePlayback(deviceID, stats)
	}
}

func (s *Service) CaptureObserver() runtimeDevices.CaptureObserver {
	if s == nil {
		return nil
	}
	return func(deviceID string, stats audio.CaptureQueueStats) {
		s.observeCapture(deviceID, stats)
	}
}

func (s *Service) ReportPlaybackOverflow(report runtimeDevices.PlaybackOverflowReport) {
	if s == nil || report.Stats.DroppedSamples == 0 {
		return
	}
	fields := playbackFields(report.DeviceID, report.Stats)
	if report.ParticipantID != "" {
		fields[runtimeDevices.PlaybackDiagnosticFieldParticipantID] = report.ParticipantID
	}
	s.recordDiagnostic(runtimeDevices.DiagnosticRecord{
		Event:  runtimeDevices.PlaybackOverflowDiagnosticEvent,
		Fields: fields,
	})
}

func (s *Service) CombinePlaybackObservers(observers ...runtimeDevices.PlaybackObserver) runtimeDevices.PlaybackObserver {
	active := make([]runtimeDevices.PlaybackObserver, 0, len(observers))
	for _, observer := range observers {
		if observer != nil {
			active = append(active, observer)
		}
	}
	if len(active) == 0 {
		return nil
	}
	return func(deviceID string, stats audio.PlaybackQueueStats) {
		for _, observer := range active {
			invokePlayback(observer, deviceID, stats)
		}
	}
}

func (s *Service) CombineCaptureObservers(observers ...runtimeDevices.CaptureObserver) runtimeDevices.CaptureObserver {
	active := make([]runtimeDevices.CaptureObserver, 0, len(observers))
	for _, observer := range observers {
		if observer != nil {
			active = append(active, observer)
		}
	}
	if len(active) == 0 {
		return nil
	}
	return func(deviceID string, stats audio.CaptureQueueStats) {
		for _, observer := range active {
			invokeCapture(observer, deviceID, stats)
		}
	}
}

func (s *Service) CombinePlaybackReceiptObservers(observers ...runtimeDevices.PlaybackReceiptObserver) runtimeDevices.PlaybackReceiptObserver {
	active := make([]runtimeDevices.PlaybackReceiptObserver, 0, len(observers))
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
			invokeReceipt(observer, receipt)
		}
	}
}

func invokePlayback(observer runtimeDevices.PlaybackObserver, deviceID string, stats audio.PlaybackQueueStats) {
	defer func() {
		if recover() != nil {
			return
		}
	}()
	observer(deviceID, stats)
}

func invokeCapture(observer runtimeDevices.CaptureObserver, deviceID string, stats audio.CaptureQueueStats) {
	defer func() {
		if recover() != nil {
			return
		}
	}()
	observer(deviceID, stats)
}

func invokeReceipt(observer runtimeDevices.PlaybackReceiptObserver, receipt audio.PlaybackReceipt) {
	defer func() {
		if recover() != nil {
			return
		}
	}()
	observer(receipt)
}

func (s *Service) recordDiagnostic(record runtimeDevices.DiagnosticRecord) {
	if s == nil || s.diagnostic == nil {
		return
	}
	defer func() {
		if recover() != nil {
			return
		}
	}()
	s.diagnostic.RecordDiagnostic(runtimeDevices.DiagnosticRecord{Event: record.Event, Fields: cloneFields(record.Fields)})
}

func (s *Service) observePlayback(deviceID string, stats audio.PlaybackQueueStats) {
	fields := runtimeobs.Fields{
		"device_id":   deviceID,
		"sample_rate": strconv.Itoa(stats.Format.SampleRate),
		"channels":    strconv.Itoa(stats.Format.Channels),
	}
	for _, definition := range s.playbackMetrics {
		if err := runtimeobs.TrySample(context.Background(), s.sampler, runtimeobs.MetricSample{
			Name: definition.name, Kind: definition.kind, Unit: definition.unit,
			Value: definition.value(stats), Fields: fields,
		}); err != nil {
			continue
		}
	}
	level := logLevelInfo
	if stats.UnderflowEvents > 0 || stats.OverflowEvents > 0 {
		level = logLevelWarn
	}
	logFields := runtimeobs.Fields(playbackFields(deviceID, stats))
	logFields["underflow_events"] = strconv.FormatUint(stats.UnderflowEvents, 10)
	logFields["underflow_samples"] = strconv.FormatUint(stats.UnderflowSamples, 10)
	logFields["zero_filled_samples"] = strconv.FormatUint(stats.ZeroFilledSamples, 10)
	logFields["rendered_samples"] = strconv.FormatUint(stats.RenderedSamples, 10)
	if err := runtimeobs.TryLog(context.Background(), s.logger, runtimeobs.LogRecord{
		Level: level, Message: runtimeDevices.PlaybackSnapshotLogMessage, Fields: logFields,
	}); err != nil {
		return
	}
}

func (s *Service) observeCapture(deviceID string, stats audio.CaptureQueueStats) {
	fields := runtimeobs.Fields{"device_id": deviceID, "drop_policy": stats.DropPolicy}
	for _, sample := range []runtimeobs.MetricSample{
		{Name: "audio.capture.queue.depth", Kind: "gauge", Value: float64(stats.QueuedSamples), Unit: "samples", Fields: fields},
		{Name: "audio.capture.queue.peak", Kind: "gauge", Value: float64(stats.HighWaterSamples), Unit: "samples", Fields: fields},
		{Name: "audio.capture.captured", Kind: "counter", Value: float64(stats.CapturedSamples), Unit: "samples", Fields: fields},
		{Name: "audio.capture.frames", Kind: "counter", Value: float64(stats.CompletedFrames), Unit: "frames", Fields: fields},
		{Name: "audio.capture.dropped", Kind: "counter", Value: float64(stats.DroppedFrames), Unit: "frames", Fields: fields},
		{Name: "audio.capture.dropped", Kind: "counter", Value: float64(stats.DroppedSamples), Unit: "samples", Fields: fields},
		{Name: "audio.capture.sequence_gaps", Kind: "counter", Value: float64(stats.SequenceGaps), Unit: "gaps", Fields: fields},
	} {
		if err := runtimeobs.TrySample(context.Background(), s.sampler, sample); err != nil {
			continue
		}
	}
	level := logLevelInfo
	if stats.DroppedSamples > 0 || stats.SequenceGaps > 0 {
		level = logLevelWarn
	}
	logFields := runtimeobs.Fields{
		"device_id": deviceID, "drop_policy": stats.DropPolicy,
		"dropped_frames":  strconv.FormatUint(stats.DroppedFrames, 10),
		"dropped_samples": strconv.FormatUint(stats.DroppedSamples, 10),
		"sequence_gaps":   strconv.FormatUint(stats.SequenceGaps, 10),
	}
	if err := runtimeobs.TryLog(context.Background(), s.logger, runtimeobs.LogRecord{
		Level: level, Message: runtimeDevices.CaptureSnapshotLogMessage, Fields: logFields,
	}); err != nil {
		return
	}
}

type metricDefinition struct {
	name  string
	kind  string
	unit  string
	value func(audio.PlaybackQueueStats) float64
}

func newPlaybackMetricDefinitions() []metricDefinition {
	return []metricDefinition{
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
}

func playbackFields(deviceID string, stats audio.PlaybackQueueStats) map[string]string {
	return map[string]string{
		runtimeDevices.PlaybackDiagnosticFieldDeviceID:            deviceID,
		runtimeDevices.PlaybackDiagnosticFieldSampleRate:          strconv.Itoa(stats.Format.SampleRate),
		runtimeDevices.PlaybackDiagnosticFieldChannels:            strconv.Itoa(stats.Format.Channels),
		runtimeDevices.PlaybackDiagnosticFieldLatencyTargetMillis: strconv.FormatInt(stats.LatencyTarget.Milliseconds(), 10),
		runtimeDevices.PlaybackDiagnosticFieldCapacitySamples:     strconv.Itoa(stats.CapacitySamples),
		runtimeDevices.PlaybackDiagnosticFieldQueuedSamples:       strconv.Itoa(stats.QueuedSamples),
		runtimeDevices.PlaybackDiagnosticFieldPeakQueuedSamples:   strconv.Itoa(stats.PeakQueuedSamples),
		runtimeDevices.PlaybackDiagnosticFieldDroppedSamples:      strconv.FormatUint(stats.DroppedSamples, 10),
		runtimeDevices.PlaybackDiagnosticFieldOverflowEvents:      strconv.FormatUint(stats.OverflowEvents, 10),
	}
}

func cloneFields(fields map[string]string) map[string]string {
	if fields == nil {
		return nil
	}
	cloned := make(map[string]string, len(fields))
	for key, value := range fields {
		cloned[key] = value
	}
	return cloned
}

type logDiagnosticSink struct{}

func (logDiagnosticSink) RecordDiagnostic(record runtimeDevices.DiagnosticRecord) {
	log.Printf("session diagnostic (no diagnostic sink configured): event=%s fields=%v", record.Event, record.Fields)
}

var _ runtimeDevices.ObserverService = (*Service)(nil)
