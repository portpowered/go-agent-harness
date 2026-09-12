package devicebinding_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devicebinding"
	devicebindingwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devicebinding/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

func TestRequestSelectionUsesPresenceAndOpaqueIDs(t *testing.T) {
	tests := []struct {
		name                string
		request             devicebinding.Request
		input, output, both bool
	}{
		{name: "omitted", request: devicebinding.Request{}},
		{name: "empty input flag", request: devicebinding.Request{InputPresent: true}, input: true, both: true},
		{name: "opaque input ID", request: devicebinding.Request{InputDevice: "backend:native"}, input: true, both: true},
		{name: "empty output flag", request: devicebinding.Request{OutputPresent: true}, output: true, both: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.request.InputSelected(); got != test.input {
				t.Fatalf("InputSelected() = %v, want %v", got, test.input)
			}
			if got := test.request.OutputSelected(); got != test.output {
				t.Fatalf("OutputSelected() = %v, want %v", got, test.output)
			}
			if got := test.request.Selected(); got != test.both {
				t.Fatalf("Selected() = %v, want %v", got, test.both)
			}
		})
	}
}

func TestServiceNoSelectionDoesNotTouchRegistry(t *testing.T) {
	binding, err := devicebindingwire.NewService().Open(devicebinding.Request{})
	if err != nil {
		t.Fatalf("Open(no selection) error = %v", err)
	}
	if binding != nil {
		t.Fatalf("Open(no selection) = %#v, want nil", binding)
	}
}

func TestBindingErrorAndNilBindingAreSafe(t *testing.T) {
	cause := errors.New("device disappeared")
	err := &devicebinding.BindingError{Flag: "--audio-in-device", Direction: devicegw.DirectionInput, DeviceID: "virtual:gone", Err: cause}
	if !errors.Is(err, cause) {
		t.Fatal("BindingError did not preserve its cause")
	}
	if err.Error() == "" || err.Unwrap() != cause {
		t.Fatalf("BindingError formatting or unwrap failed: %q", err)
	}
	withoutCause := (&devicebinding.BindingError{Flag: "--audio-out-device", Direction: devicegw.DirectionOutput, DeviceID: "virtual:gone"}).Error()
	if withoutCause == "" {
		t.Fatal("BindingError without cause has empty text")
	}
	var nilErr *devicebinding.BindingError
	if nilErr.Error() != "<nil>" || nilErr.Unwrap() != nil {
		t.Fatalf("nil BindingError methods = %q/%v", nilErr.Error(), nilErr.Unwrap())
	}
	var nilBinding *devicebinding.Binding
	if err := nilBinding.Close(); err != nil {
		t.Fatalf("nil Binding.Close() error = %v", err)
	}
}

func TestBindingCloseOrdersSinkSourceFeedbackAndJoinsErrors(t *testing.T) {
	events := []string{}
	sourceErr, sinkErr, feedbackErr := errors.New("source close"), errors.New("sink close"), errors.New("feedback close")
	binding := &devicebinding.Binding{
		Source:   &testSource{closeProbe: closeProbe{label: "source", events: &events, err: sourceErr}},
		Sink:     &testSink{closeProbe: closeProbe{label: "sink", events: &events, err: sinkErr}},
		Feedback: &testFeedback{closeProbe: closeProbe{label: "feedback", events: &events, err: feedbackErr}},
	}
	err := binding.Close()
	if !errors.Is(err, sourceErr) || !errors.Is(err, sinkErr) || !errors.Is(err, feedbackErr) {
		t.Fatalf("Close() error = %v, want all cleanup causes", err)
	}
	if got, want := events, []string{"sink", "source", "feedback"}; !equalStrings(got, want) {
		t.Fatalf("close order = %v, want %v", got, want)
	}
	if err := binding.Close(); err == nil || len(events) != 3 {
		t.Fatalf("repeated Close() = %v with events %v, want preserved error and no duplicate close", err, events)
	}
}

func TestServiceNormalizesDefaultsAndWiresFeedbackAndObservers(t *testing.T) {
	registry := virtualRegistry(t)
	var playbackSnapshots, captureSnapshots atomic.Int32
	binding, err := devicebindingwire.NewService().Open(devicebinding.Request{
		Registry: registry, InputDevice: " DeFaUlT ", OutputDevice: "DEFAULT",
		InputPresent: true, OutputPresent: true,
		PlaybackObserver: func(devicegw.DeviceID, audio.PlaybackQueueStats) { playbackSnapshots.Add(1) },
		CaptureObserver:  func(devicegw.DeviceID, audio.CaptureQueueStats) { captureSnapshots.Add(1) },
	})
	if err != nil {
		t.Fatalf("Open(defaults) error = %v", err)
	}
	if binding == nil || binding.Source == nil || binding.Sink == nil || binding.Capture == nil {
		t.Fatalf("Open(defaults) returned incomplete binding: %#v", binding)
	}
	if binding.Source.DeviceID() != "virtual:input" || binding.Sink.DeviceID() != "virtual:output" {
		t.Fatalf("default endpoints = %q/%q, want virtual:input/virtual:output", binding.Source.DeviceID(), binding.Sink.DeviceID())
	}
	if binding.Feedback == nil {
		t.Fatal("both selected directions did not create feedback gate")
	}
	if err := binding.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := binding.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if got := registry.Observations().ReleaseCount; got != 2 {
		t.Fatalf("release count = %d, want 2 after idempotent close", got)
	}
	if playbackSnapshots.Load() != 1 || captureSnapshots.Load() != 1 {
		t.Fatalf("teardown snapshots = playback %d, capture %d, want one each", playbackSnapshots.Load(), captureSnapshots.Load())
	}
}

func TestServiceReportsUnavailableRenderBoundary(t *testing.T) {
	registry := &noRenderRegistry{VirtualRegistry: virtualRegistry(t)}
	var unavailable atomic.Int32
	binding, err := devicebindingwire.NewService().Open(devicebinding.Request{
		Registry: registry, OutputPresent: true,
		RenderedSamplesObserver:    func(int, []int16) {},
		RenderedSamplesUnavailable: func() { unavailable.Add(1) },
	})
	if err != nil {
		t.Fatalf("Open(no render boundary) error = %v", err)
	}
	if binding == nil || binding.Sink == nil {
		t.Fatalf("Open(no render boundary) returned incomplete binding: %#v", binding)
	}
	if got := unavailable.Load(); got != 1 {
		t.Fatalf("unavailable-render calls = %d, want 1", got)
	}
	if err := binding.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestServiceOutputFailureReturnsTypedErrorAndRollsBackInput(t *testing.T) {
	registry := virtualRegistry(t)
	binding, err := devicebindingwire.NewService().Open(devicebinding.Request{
		Registry: registry, InputDevice: "virtual:input", OutputDevice: "virtual:missing",
		InputPresent: true, OutputPresent: true,
	})
	if binding != nil {
		t.Fatalf("failed Open returned binding: %#v", binding)
	}
	if err == nil {
		t.Fatal("failed Open returned nil error")
	}
	var bindingErr *devicebinding.BindingError
	if !errors.As(err, &bindingErr) {
		t.Fatalf("error %T = %v, want BindingError", err, err)
	}
	if bindingErr.Flag != "--audio-out-device" || bindingErr.Direction != devicegw.DirectionOutput || bindingErr.DeviceID != "virtual:missing" {
		t.Fatalf("binding error = %#v, want output selector metadata", bindingErr)
	}
	if !errors.Is(err, devicegw.ErrDeviceNotFound) {
		t.Fatalf("error = %v, want ErrDeviceNotFound", err)
	}
	if got := registry.Observations().ReleaseCount; got != 1 {
		t.Fatalf("rollback release count = %d, want 1", got)
	}
}

func virtualRegistry(t *testing.T) *devicegw.VirtualRegistry {
	t.Helper()
	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("NewVirtualRegistry() error = %v", err)
	}
	return registry
}

type closeProbe struct {
	label  string
	events *[]string
	err    error
}

func (p *closeProbe) Close() error {
	*p.events = append(*p.events, p.label)
	return p.err
}

type testSource struct{ closeProbe }

func (*testSource) DeviceID() devicegw.DeviceID                     { return "test:input" }
func (*testSource) SourceSampleRate() int                           { return audio.SampleRate }
func (*testSource) ProviderSampleRate() int                         { return audio.SampleRate }
func (*testSource) Pump(context.Context, audio.OutboundMedia) error { return nil }

type testSink struct{ closeProbe }

func (*testSink) DeviceID() devicegw.DeviceID                    { return "test:output" }
func (*testSink) SampleRate() int                                { return audio.SampleRate }
func (*testSink) ProviderSampleRate() int                        { return audio.SampleRate }
func (*testSink) Pump(context.Context, audio.InboundMedia) error { return nil }

type testFeedback struct{ closeProbe }

func (*testFeedback) CapturePosition() time.Duration { return 0 }

type noRenderRegistry struct{ *devicegw.VirtualRegistry }

func (r *noRenderRegistry) Open(id devicegw.DeviceID) (devicegw.OpenedDevice, error) {
	stream, err := r.VirtualRegistry.Open(id)
	if err != nil {
		return nil, err
	}
	return &noRenderStream{stream: stream.(*devicegw.VirtualStream)}, nil
}

func (r *noRenderRegistry) OpenWithFormat(id devicegw.DeviceID, format audio.DeviceFormat) (devicegw.OpenedDevice, error) {
	stream, err := r.VirtualRegistry.OpenWithFormat(id, format)
	if err != nil {
		return nil, err
	}
	return &noRenderStream{stream: stream.(*devicegw.VirtualStream)}, nil
}

type noRenderStream struct{ stream *devicegw.VirtualStream }

func (s *noRenderStream) Close() error { return s.stream.Close() }
func (s *noRenderStream) DeviceDirection() devicegw.Direction {
	return s.stream.DeviceDirection()
}
func (s *noRenderStream) DeviceFormat() audio.DeviceFormat { return s.stream.DeviceFormat() }
func (s *noRenderStream) WriteFrame(ctx context.Context, frame []int16) error {
	return s.stream.WriteFrame(ctx, frame)
}
func (s *noRenderStream) ReadFrame(ctx context.Context, frame []int16) error {
	return s.stream.ReadFrame(ctx, frame)
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
