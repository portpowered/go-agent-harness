package agentruntime

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	public "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	devicert "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/runtime"
)

func TestC112TraceAdapterRequiresInjectedClock(t *testing.T) {
	request := public.Request{TraceAudio: true, RecordDirectory: filepath.Join(t.TempDir(), "requested")}
	if _, err := prepareTrace(&request, &SessionRunOptions{}, nil); !errors.Is(err, sessiontrace.ErrClockRequired) {
		t.Fatalf("missing clock error = %v", err)
	}
}

func TestC112TraceAdapterKeepsDeviceErrorsAndObserverPolicy(t *testing.T) {
	priorErr := errors.New("prior playback failed")
	observer := &traceC112Observer{}
	options := SessionRunOptions{
		ModelCatalog:    testModelCatalog(),
		RuntimeObserver: observer,
		RTCDeviceBinding: RTCDeviceBindingRequest{
			PlaybackSamplesObserver: devicert.RTCDevicePlaybackSamplesObserver(func(context.Context, int, []int16) error { return priorErr }),
		},
	}
	request := public.Request{TraceAudio: true, RecordDirectory: filepath.Join(t.TempDir(), "requested")}
	prepared, err := prepareTrace(&request, &options, clock.NewDeterministic(time.Unix(0, 0).UTC(), time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := options.RuntimeObserver.(interface{ ObserveProviderBoundaries() bool }); !ok {
		t.Fatal("trace adapter dropped provider-boundary policy")
	}
	if retain, ok := options.RuntimeObserver.(interface{ RetainCommitPayload() bool }); !ok || !retain.RetainCommitPayload() {
		t.Fatal("trace adapter changed commit payload policy")
	}
	if err := options.RTCDeviceBinding.PlaybackSamplesObserver(context.Background(), 16000, []int16{1, 2}); !errors.Is(err, priorErr) {
		t.Fatalf("playback callback error = %v", err)
	}
	event := SessionRuntimeObservation{Kind: SessionRuntimeObservationResponseCreate, Tick: 11, ResponseID: "response-11"}
	options.RuntimeObserver.ObserveSessionRuntime(event)
	if len(observer.events) != 1 || observer.events[0].Tick != event.Tick || observer.events[0].ResponseID != event.ResponseID {
		t.Fatalf("prior observer events = %#v", observer)
	}
	if err := prepared.finish("", true); err != nil {
		t.Fatal(err)
	}
}

type traceC112Observer struct{ events []SessionRuntimeObservation }

func (o *traceC112Observer) ObserveSessionRuntime(event SessionRuntimeObservation) {
	o.events = append(o.events, event)
}

func (*traceC112Observer) ObserveProviderBoundaries() bool { return true }
func (*traceC112Observer) RetainCommitPayload() bool       { return true }
