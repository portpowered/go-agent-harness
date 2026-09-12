package agentruntime

import (
	devices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	deviceswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	devicert "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/runtime"
)

const (
	SessionDiagnosticEventPlaybackOverflow            = devices.PlaybackOverflowDiagnosticEvent
	SessionDiagnosticFieldPlaybackDeviceID            = devices.PlaybackDiagnosticFieldDeviceID
	SessionDiagnosticFieldPlaybackSampleRate          = devices.PlaybackDiagnosticFieldSampleRate
	SessionDiagnosticFieldPlaybackChannels            = devices.PlaybackDiagnosticFieldChannels
	SessionDiagnosticFieldPlaybackLatencyTargetMillis = devices.PlaybackDiagnosticFieldLatencyTargetMillis
	SessionDiagnosticFieldPlaybackCapacitySamples     = devices.PlaybackDiagnosticFieldCapacitySamples
	SessionDiagnosticFieldPlaybackQueuedSamples       = devices.PlaybackDiagnosticFieldQueuedSamples
	SessionDiagnosticFieldPlaybackPeakQueuedSamples   = devices.PlaybackDiagnosticFieldPeakQueuedSamples
	SessionDiagnosticFieldPlaybackDroppedSamples      = devices.PlaybackDiagnosticFieldDroppedSamples
	SessionDiagnosticFieldPlaybackOverflowEvents      = devices.PlaybackDiagnosticFieldOverflowEvents
	SessionDiagnosticFieldPlaybackParticipantID       = devices.PlaybackDiagnosticFieldParticipantID
	SessionLogMessagePlaybackSnapshot                 = devices.PlaybackSnapshotLogMessage
)

type sessionDiagnosticSinkAdapter struct{ sink SessionDiagnosticSink }

func (a sessionDiagnosticSinkAdapter) RecordDiagnostic(record devices.DiagnosticRecord) {
	if a.sink == nil {
		return
	}
	fields := make(map[string]string, len(record.Fields))
	for key, value := range record.Fields {
		fields[key] = value
	}
	a.sink.RecordSessionDiagnostic(SessionDiagnosticRecord{Event: record.Event, Fields: fields})
}

func deviceDiagnosticSink(sink SessionDiagnosticSink) devices.DiagnosticSink {
	if sink == nil {
		return nil
	}
	return sessionDiagnosticSinkAdapter{sink: sink}
}

func sessionPlaybackDiagnosticObserver(sink SessionDiagnosticSink) devicert.RTCDevicePlaybackObserver {
	service := deviceswire.NewObserverService(devices.ObserverDependencies{DiagnosticSink: deviceDiagnosticSink(sink)})
	return rtcPlaybackObserver(service.PlaybackOverflowObserver())
}

func sessionPlaybackObservabilityObserver(sampler observability.MetricSampler, logger observability.Logger) devicert.RTCDevicePlaybackObserver {
	service := deviceswire.NewObserverService(devices.ObserverDependencies{MetricSampler: sampler, Logger: logger})
	return rtcPlaybackObserver(service.PlaybackObservabilityObserver())
}

func sessionCaptureObservabilityObserver(sampler observability.MetricSampler, logger observability.Logger) devicert.RTCDeviceCaptureObserver {
	service := deviceswire.NewObserverService(devices.ObserverDependencies{MetricSampler: sampler, Logger: logger})
	return rtcCaptureObserver(service.CaptureObserver())
}

func combineRTCDevicePlaybackObservers(observers ...devicert.RTCDevicePlaybackObserver) devicert.RTCDevicePlaybackObserver {
	service := deviceswire.NewObserverService(devices.ObserverDependencies{})
	converted := make([]devices.PlaybackObserver, 0, len(observers))
	for _, observer := range observers {
		if observer != nil {
			current := observer
			converted = append(converted, func(id string, stats audio.PlaybackQueueStats) { current(devicegw.DeviceID(id), stats) })
		}
	}
	return rtcPlaybackObserver(service.CombinePlaybackObservers(converted...))
}

func combineRTCDeviceCaptureObservers(observers ...devicert.RTCDeviceCaptureObserver) devicert.RTCDeviceCaptureObserver {
	service := deviceswire.NewObserverService(devices.ObserverDependencies{})
	converted := make([]devices.CaptureObserver, 0, len(observers))
	for _, observer := range observers {
		if observer != nil {
			current := observer
			converted = append(converted, func(id string, stats audio.CaptureQueueStats) { current(devicegw.DeviceID(id), stats) })
		}
	}
	return rtcCaptureObserver(service.CombineCaptureObservers(converted...))
}

func combineRTCDevicePlaybackReceiptObservers(observers ...devicert.RTCDevicePlaybackReceiptObserver) devicert.RTCDevicePlaybackReceiptObserver {
	service := deviceswire.NewObserverService(devices.ObserverDependencies{})
	converted := make([]devices.PlaybackReceiptObserver, 0, len(observers))
	for _, observer := range observers {
		if observer != nil {
			current := observer
			converted = append(converted, func(receipt audio.PlaybackReceipt) { current(receipt) })
		}
	}
	return rtcReceiptObserver(service.CombinePlaybackReceiptObservers(converted...))
}

func rtcPlaybackObserver(observer devices.PlaybackObserver) devicert.RTCDevicePlaybackObserver {
	if observer == nil {
		return nil
	}
	return func(id devicegw.DeviceID, stats audio.PlaybackQueueStats) { observer(string(id), stats) }
}

func rtcCaptureObserver(observer devices.CaptureObserver) devicert.RTCDeviceCaptureObserver {
	if observer == nil {
		return nil
	}
	return func(id devicegw.DeviceID, stats audio.CaptureQueueStats) { observer(string(id), stats) }
}

func rtcReceiptObserver(observer devices.PlaybackReceiptObserver) devicert.RTCDevicePlaybackReceiptObserver {
	if observer == nil {
		return nil
	}
	return func(receipt audio.PlaybackReceipt) { observer(receipt) }
}

func reportRoomParticipantPlaybackOverflow(participantID string, output *devicegw.DeviceSink, sink SessionDiagnosticSink) {
	if output == nil {
		return
	}
	service := deviceswire.NewObserverService(devices.ObserverDependencies{DiagnosticSink: deviceDiagnosticSink(sink)})
	service.ReportPlaybackOverflow(devices.PlaybackOverflowReport{
		DeviceID: string(output.DeviceID()), ParticipantID: participantID, Stats: output.PlaybackStats(),
	})
}

// Deprecated: use the devices observer service through the room coordinator.
func emitRoomParticipantPlaybackOverflowDiagnostic(participantID string, output *devicegw.DeviceSink, sink SessionDiagnosticSink) {
	reportRoomParticipantPlaybackOverflow(participantID, output, sink)
}
