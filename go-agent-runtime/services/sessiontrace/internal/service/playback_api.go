package service

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	devicert "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/runtime"
)

type playbackDiagnostics struct {
	options sessiontrace.PlaybackDiagnosticsOptions
}

func NewPlaybackDiagnostics(options sessiontrace.PlaybackDiagnosticsOptions) sessiontrace.PlaybackDiagnostics {
	return playbackDiagnostics{options: options}
}

func (p playbackDiagnostics) PlaybackObserver(existing devicert.RTCDevicePlaybackObserver) devicert.RTCDevicePlaybackObserver {
	return combineRTCDevicePlaybackObservers(
		existing,
		sessionPlaybackDiagnosticObserver(resolvePlaybackDiagnosticSink(p.options.Sink)),
		sessionPlaybackObservabilityObserver(p.options.MetricSampler, p.options.Logger),
	)
}

func (p playbackDiagnostics) PlaybackReceiptObserver(existing devicert.RTCDevicePlaybackReceiptObserver) devicert.RTCDevicePlaybackReceiptObserver {
	if p.options.Runtime == nil {
		return existing
	}
	return combineRTCDevicePlaybackReceiptObservers(existing, p.options.Runtime.AudioPlaybackReceipt)
}

func (p playbackDiagnostics) CaptureObserver(existing devicert.RTCDeviceCaptureObserver) devicert.RTCDeviceCaptureObserver {
	return combineRTCDeviceCaptureObservers(existing, sessionCaptureObservabilityObserver(p.options.MetricSampler, p.options.Logger))
}

func (p playbackDiagnostics) RecordParticipantPlaybackOverflow(participant string, output *devicegw.DeviceSink) {
	emitRoomParticipantPlaybackOverflowDiagnostic(participant, output, p.options.Sink)
}

var _ sessiontrace.PlaybackDiagnostics = playbackDiagnostics{}
