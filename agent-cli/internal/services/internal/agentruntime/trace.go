package agentruntime

import (
	"context"
	public "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	sessiontrace "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	tracewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type traceRun struct {
	prepared sessiontrace.Prepared
	path     string
}

// Stage capture through the host-neutral service and keep this CLI seam to request adaptation.
func prepareTrace(request *public.Request, options *SessionRunOptions, source clock.Source) (*traceRun, error) {
	if !request.TraceAudio && request.RecordDirectory == "" {
		return nil, nil
	}
	binding := options.RTCDeviceBinding
	prepared, err := tracewire.NewService().Prepare(sessiontrace.Request{TraceAudio: request.TraceAudio, RecordDirectory: request.RecordDirectory, Clock: source, Credentials: traceCredentials(request), Device: sessiontrace.DeviceBinding{PreGateSamplesObserver: sessiontrace.CaptureSamplesObserver(binding.PreGateSamplesObserver), UploadedSamplesObserver: sessiontrace.CaptureSamplesObserver(binding.UploadedSamplesObserver), PlaybackSamplesObserver: sessiontrace.PlaybackSamplesObserver(binding.PlaybackSamplesObserver), RenderedSamplesObserver: sessiontrace.CaptureSamplesObserver(binding.RenderedSamplesObserver), RenderedSamplesUnavailable: binding.RenderedSamplesUnavailable}})
	if err != nil {
		return nil, err
	}
	setTraceBinding(options, prepared.DeviceBinding())
	options.RuntimeObserver = traceObserverAdapter{trace: prepared.RuntimeObserver().ObserveSessionRuntime, prior: runtimeObserverCallback(options.RuntimeObserver), retain: traceRetainCommitPayload(options.RuntimeObserver)}
	return &traceRun{prepared: prepared, path: prepared.StagedPath()}, nil
}
func (r *traceRun) finish(bundle string, published bool) error {
	return r.prepared.Finish(context.Background(), bundle, published)
}

type traceObserverAdapter struct {
	trace  func(sessiontrace.SessionRuntimeObservation)
	prior  func(SessionRuntimeObservation)
	retain bool
}

func (a traceObserverAdapter) ObserveSessionRuntime(event SessionRuntimeObservation) {
	traceEvent := event
	traceEvent.Payload = append([]byte(nil), event.Payload...)
	a.prior(event)
	a.trace(sessiontrace.SessionRuntimeObservation{Kind: sessiontrace.SessionRuntimeObservationKind(traceEvent.Kind), Tick: traceEvent.Tick, Payload: traceEvent.Payload, InputCommit: traceEvent.InputCommit, ResponseID: traceEvent.ResponseID, ResponsePurpose: traceEvent.ResponsePurpose, StreamID: traceEvent.StreamID, LoopPassID: traceEvent.LoopPassID, Epoch: traceEvent.Epoch, TurnsCompleted: traceEvent.TurnsCompleted, Clean: traceEvent.Clean, Error: traceEvent.Error})
}

func (a traceObserverAdapter) ObserveProviderBoundaries() bool { return true }

func (a traceObserverAdapter) RetainCommitPayload() bool { return a.retain }
