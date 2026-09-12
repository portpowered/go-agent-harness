package devicebinding_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devicebinding"
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

func TestBindingErrorAndNilBindingAreSafe(t *testing.T) {
	cause := errors.New("device disappeared")
	err := &devicebinding.BindingError{Flag: "--audio-in-device", Direction: devicegw.DirectionInput, DeviceID: "virtual:gone", Err: cause}
	if !errors.Is(err, cause) || err.Error() == "" || err.Unwrap() != cause {
		t.Fatalf("BindingError lost formatting or cause: %q", err)
	}
	if got := (&devicebinding.BindingError{Flag: "--audio-out-device", Direction: devicegw.DirectionOutput, DeviceID: "virtual:gone"}).Error(); got == "" {
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
