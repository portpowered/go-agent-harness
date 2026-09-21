package file

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestFactoryPumpsFiniteCaptureAndPlaybackThroughPublicPorts(t *testing.T) {
	sink := &recordingSink{}
	input := &recordingInput{frames: []sharedaudio.PCMFrame{{Samples: []int16{1000, -1000}, EndOfResponse: true}}}
	output := &recordingOutput{sink: sink, closeFn: sink.Close}
	factory := NewFactory(&testAudioService{input: input, output: output})
	handle, err := factory.Open(context.Background(), devices.Request{
		CaptureEnabled:  true,
		PlaybackEnabled: true,
		SampleRate:      sharedaudio.SampleRate,
		Channels:        sharedaudio.Channels,
		FileInput:       &devices.FileInput{Source: sharedaudio.NewSliceSource([]int16{1000, -1000})},
		FileOutput:      &devices.FileOutput{Sink: sink},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ports := handle.Media()
	if ports.Capture == nil || ports.Playback == nil {
		t.Fatalf("Media() = %+v, want both finite directions", ports)
	}

	outbound := &recordingOutbound{}
	if err := ports.Capture.Pump(context.Background(), outbound); err != nil {
		t.Fatalf("capture Pump: %v", err)
	}
	if len(outbound.frames) != 1 || !outbound.frames[0].EndOfResponse || !reflect.DeepEqual(outbound.frames[0].Samples, []int16{1000, -1000}) {
		t.Fatalf("captured frames = %+v, want one exact terminal PCM frame", outbound.frames)
	}

	inbound := &recordingInbound{frames: []sharedaudio.PCMFrame{{Samples: []int16{3, -4}, EndOfResponse: true}}}
	if err := ports.Playback.Pump(context.Background(), inbound); err != nil {
		t.Fatalf("playback Pump: %v", err)
	}
	if !reflect.DeepEqual(sink.samples, []int16{3, -4}) {
		t.Fatalf("played samples = %v, want [3 -4]", sink.samples)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if sink.closeCalls != 1 {
		t.Fatalf("sink close calls = %d, want one", sink.closeCalls)
	}
}

func TestFactoryRejectsInvalidAdmissionBeforeOpeningPorts(t *testing.T) {
	service := &testAudioService{}
	factory := NewFactory(service)
	cases := []struct {
		name    string
		request devices.Request
		want    error
	}{
		{name: "canceled", request: devices.Request{PlaybackEnabled: true, FileOutput: &devices.FileOutput{Sink: &recordingSink{}}}, want: context.Canceled},
		{name: "no direction", request: devices.Request{}, want: devices.ErrInvalidRequest},
		{name: "missing capture port", request: devices.Request{CaptureEnabled: true}, want: devices.ErrInvalidRequest},
		{name: "missing playback port", request: devices.Request{PlaybackEnabled: true}, want: devices.ErrInvalidRequest},
		{name: "nil source", request: devices.Request{FileInput: &devices.FileInput{}}, want: devices.ErrInvalidRequest},
		{name: "nil sink", request: devices.Request{FileOutput: &devices.FileOutput{}}, want: devices.ErrInvalidRequest},
		{name: "negative provider rate", request: devices.Request{PlaybackEnabled: true, SampleRate: -1, FileOutput: &devices.FileOutput{Sink: &recordingSink{}}}, want: devices.ErrInvalidRequest},
		{name: "negative channels", request: devices.Request{PlaybackEnabled: true, Channels: -1, FileOutput: &devices.FileOutput{Sink: &recordingSink{}}}, want: devices.ErrInvalidRequest},
		{name: "negative input rate", request: devices.Request{CaptureEnabled: true, FileInput: &devices.FileInput{Source: sharedaudio.NewSliceSource([]int16{1}), SampleRate: -1}}, want: devices.ErrInvalidRequest},
		{name: "negative output rate", request: devices.Request{PlaybackEnabled: true, FileOutput: &devices.FileOutput{Sink: &recordingSink{}, SampleRate: -1}}, want: devices.ErrInvalidRequest},
		{name: "stereo", request: devices.Request{PlaybackEnabled: true, Channels: 2, FileOutput: &devices.FileOutput{Sink: &recordingSink{}}}, want: devices.ErrInvalidRequest},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			callCtx, cancelCall := context.WithCancel(context.Background())
			if test.name == "canceled" {
				cancelCall()
			}
			if _, err := factory.Open(callCtx, test.request); !errors.Is(err, test.want) {
				cancelCall()
				t.Fatalf("Open error = %v, want %v", err, test.want)
			}
			cancelCall()
		})
	}
	if _, err := (*Factory)(nil).Open(context.Background(), devices.Request{PlaybackEnabled: true}); !errors.Is(err, devices.ErrUnavailable) {
		t.Fatalf("nil Factory error = %v, want ErrUnavailable", err)
	}
	if _, err := NewFactory(nil).Open(context.Background(), devices.Request{PlaybackEnabled: true}); !errors.Is(err, devices.ErrUnavailable) {
		t.Fatalf("nil audio service error = %v, want ErrUnavailable", err)
	}
	if _, err := factory.BindRTC(context.Background(), devices.RTCBindingRequest{}); !errors.Is(err, devices.ErrUnavailable) {
		t.Fatalf("BindRTC error = %v, want ErrUnavailable", err)
	}
}

func TestFileAdaptersDelegateRequests(t *testing.T) {
	input := &recordingInput{}
	output := &recordingOutput{}
	service := &testAudioService{input: input, output: output}
	source := sharedaudio.NewSliceSource([]int16{7})
	scheduler := &recordingScheduler{}
	capture, err := newCapture(context.Background(), devices.FileInput{Source: source, SampleRate: 8000, Pace: true, Continuous: true, PadFinalFrame: true, Scheduler: scheduler}, 24000, service)
	if err != nil {
		t.Fatalf("newCapture: %v", err)
	}
	if !service.inputRequest.Continuous || !service.inputRequest.Pace || !service.inputRequest.PadFinalFrame || service.inputRequest.SourceRate != 8000 || service.inputRequest.ProviderRate != 24000 || service.inputRequest.Scheduler != scheduler {
		t.Fatalf("input request = %+v, want request fields forwarded", service.inputRequest)
	}
	if err := capture.Pump(context.Background(), &recordingOutbound{}); !errors.Is(err, input.pumpErr) {
		t.Fatalf("capture Pump error = %v, want delegated error", err)
	}
	if err := capture.Close(); err != nil {
		t.Fatalf("capture Close: %v", err)
	}

	sink := &recordingSink{}
	playback, err := newPlayback(context.Background(), devices.FileOutput{Sink: sink, SampleRate: 8000, Continuous: true}, 24000, service)
	if err != nil {
		t.Fatalf("newPlayback: %v", err)
	}
	if !service.outputRequest.Continuous || service.outputRequest.SinkRate != 8000 || service.outputRequest.ProviderRate != 24000 {
		t.Fatalf("output request = %+v, want request fields forwarded", service.outputRequest)
	}
	if err := playback.Pump(context.Background(), &recordingInbound{}); !errors.Is(err, output.pumpErr) {
		t.Fatalf("playback Pump error = %v, want delegated error", err)
	}
	if err := playback.Close(); err != nil {
		t.Fatalf("playback Close: %v", err)
	}

}

func TestFilePlaybackWaitForPumpDrainsAndPreservesPumpError(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	wantErr := errors.New("playback failed")
	output := &recordingOutput{pump: func(ctx context.Context, _ sharedaudio.InboundMedia) error {
		close(started)
		select {
		case <-release:
			return wantErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	playback, err := newPlayback(context.Background(), devices.FileOutput{Sink: &recordingSink{}}, 24000, &testAudioService{output: output})
	if err != nil {
		t.Fatalf("newPlayback: %v", err)
	}
	t.Cleanup(func() {
		if err := playback.Close(); err != nil {
			t.Errorf("playback Close: %v", err)
		}
	})

	pumpErr := make(chan error, 1)
	go func() { pumpErr <- playback.Pump(context.Background(), &recordingInbound{}) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("playback pump did not start")
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	if err := playback.WaitForPump(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		cancel()
		t.Fatalf("WaitForPump before completion = %v, want deadline", err)
	}
	cancel()
	close(release)
	if err := <-pumpErr; !errors.Is(err, wantErr) {
		t.Fatalf("playback Pump = %v, want %v", err, wantErr)
	}
	if err := playback.WaitForPump(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("WaitForPump after completion = %v, want %v", err, wantErr)
	}
}

func TestFileAdaptersRejectInvalidRequests(t *testing.T) {
	service := &testAudioService{}
	source := sharedaudio.NewSliceSource([]int16{7})
	sink := &recordingSink{}
	invalidSource := devices.FileInput{Source: source, Pace: true}
	if _, err := newCapture(context.Background(), devices.FileInput{}, 24000, service); !errors.Is(err, devices.ErrInvalidRequest) {
		t.Fatalf("nil source error = %v, want ErrInvalidRequest", err)
	}
	if _, err := newCapture(context.Background(), invalidSource, -1, service); !errors.Is(err, devices.ErrInvalidRequest) {
		t.Fatalf("negative provider rate error = %v, want ErrInvalidRequest", err)
	}
	if _, err := newCapture(context.Background(), invalidSource, 24000, service); !errors.Is(err, devices.ErrInvalidRequest) {
		t.Fatalf("missing scheduler error = %v, want ErrInvalidRequest", err)
	}
	if _, err := newCapture(context.Background(), devices.FileInput{Source: source}, 24000, nil); !errors.Is(err, devices.ErrUnavailable) {
		t.Fatalf("nil audio service error = %v, want ErrUnavailable", err)
	}
	if _, err := newPlayback(context.Background(), devices.FileOutput{}, 24000, service); !errors.Is(err, devices.ErrInvalidRequest) {
		t.Fatalf("nil sink error = %v, want ErrInvalidRequest", err)
	}
	if _, err := newPlayback(context.Background(), devices.FileOutput{Sink: sink, SampleRate: -1}, 24000, service); !errors.Is(err, devices.ErrInvalidRequest) {
		t.Fatalf("negative sink rate error = %v, want ErrInvalidRequest", err)
	}
	if _, err := newPlayback(context.Background(), devices.FileOutput{Sink: sink}, -1, service); !errors.Is(err, devices.ErrInvalidRequest) {
		t.Fatalf("negative provider rate error = %v, want ErrInvalidRequest", err)
	}
	if _, err := newPlayback(context.Background(), devices.FileOutput{Sink: sink}, 24000, nil); !errors.Is(err, devices.ErrUnavailable) {
		t.Fatalf("nil audio service error = %v, want ErrUnavailable", err)
	}
}

func TestFactoryClosesAdmittedCaptureWhenPlaybackFails(t *testing.T) {
	closeErr := errors.New("capture close")
	openErr := errors.New("playback admission")
	input := &recordingInput{closeErr: closeErr}
	service := &testAudioService{input: input, outputErr: openErr}
	_, err := NewFactory(service).Open(context.Background(), devices.Request{
		CaptureEnabled:  true,
		PlaybackEnabled: true,
		FileInput:       &devices.FileInput{Source: sharedaudio.NewSliceSource([]int16{1})},
		FileOutput:      &devices.FileOutput{Sink: &recordingSink{}},
	})
	if !errors.Is(err, openErr) || !errors.Is(err, closeErr) {
		t.Fatalf("Open error = %v, want playback and capture-close errors", err)
	}
	if input.closeCalls != 1 {
		t.Fatalf("capture close calls = %d, want one", input.closeCalls)
	}
}

func TestNilMediaAdaptersAndHandleAreInert(t *testing.T) {
	var capture *fileCapture
	if err := capture.Pump(context.Background(), &recordingOutbound{}); !errors.Is(err, devices.ErrUnavailable) {
		t.Fatalf("nil capture Pump = %v, want ErrUnavailable", err)
	}
	if err := capture.Close(); err != nil {
		t.Fatalf("nil capture Close = %v", err)
	}
	var playback *filePlayback
	if err := playback.Pump(context.Background(), &recordingInbound{}); !errors.Is(err, devices.ErrUnavailable) {
		t.Fatalf("nil playback Pump = %v, want ErrUnavailable", err)
	}
	if err := playback.Close(); err != nil {
		t.Fatalf("nil playback Close = %v", err)
	}
	var handle *handle
	if got := handle.Media(); !reflect.DeepEqual(got, devices.MediaPorts{}) {
		t.Fatalf("nil handle Media = %+v, want empty ports", got)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("nil handle Close = %v", err)
	}
}

type testAudioService struct {
	audioio.Service
	input         audioio.Input
	output        audioio.Output
	inputErr      error
	outputErr     error
	inputRequest  audioio.InputRequest
	outputRequest audioio.OutputRequest
}

func (s *testAudioService) OpenInput(_ context.Context, request audioio.InputRequest) (audioio.Input, error) {
	s.inputRequest = request
	if s.inputErr != nil {
		return nil, s.inputErr
	}
	return s.input, nil
}

func (s *testAudioService) OpenOutput(_ context.Context, request audioio.OutputRequest) (audioio.Output, error) {
	s.outputRequest = request
	if s.outputErr != nil {
		return nil, s.outputErr
	}
	return s.output, nil
}

type recordingInput struct {
	pumpErr    error
	closeErr   error
	closeCalls int
	pump       func(context.Context, sharedaudio.OutboundMedia) error
	frames     []sharedaudio.PCMFrame
}

func (i *recordingInput) Pump(ctx context.Context, outbound sharedaudio.OutboundMedia) error {
	if i.pump != nil {
		return i.pump(ctx, outbound)
	}
	for _, frame := range i.frames {
		if err := outbound.WriteFrame(ctx, frame); err != nil {
			return err
		}
	}
	return i.pumpErr
}
func (i *recordingInput) Close() error {
	i.closeCalls++
	return i.closeErr
}

type recordingOutput struct {
	pumpErr    error
	closeErr   error
	closeCalls int
	pump       func(context.Context, sharedaudio.InboundMedia) error
	sink       *recordingSink
	closeFn    func() error
}

func (o *recordingOutput) Pump(ctx context.Context, inbound sharedaudio.InboundMedia) error {
	if o.pump != nil {
		return o.pump(ctx, inbound)
	}
	if o.sink != nil {
		for {
			frame, err := inbound.ReadFrame(ctx)
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			if err := o.sink.WriteSamples(ctx, frame.Samples); err != nil {
				return err
			}
		}
	}
	return o.pumpErr
}
func (o *recordingOutput) Write(context.Context, sharedaudio.PCMFrame) error { return nil }
func (o *recordingOutput) Close() error {
	o.closeCalls++
	if o.closeFn != nil {
		return o.closeFn()
	}
	return o.closeErr
}

type recordingOutbound struct {
	frames []sharedaudio.PCMFrame
}

func (o *recordingOutbound) WriteFrame(_ context.Context, frame sharedaudio.PCMFrame) error {
	frame.Samples = append([]int16(nil), frame.Samples...)
	o.frames = append(o.frames, frame)
	return nil
}
func (o *recordingOutbound) Close() error { return nil }

type recordingInbound struct {
	frames []sharedaudio.PCMFrame
	index  int
}

func (i *recordingInbound) ReadFrame(_ context.Context) (sharedaudio.PCMFrame, error) {
	if i.index == len(i.frames) {
		return sharedaudio.PCMFrame{}, io.EOF
	}
	frame := i.frames[i.index]
	i.index++
	return frame, nil
}
func (i *recordingInbound) Close() error { return nil }

type recordingSink struct {
	samples    []int16
	closeCalls int
}

func (s *recordingSink) WriteFrame(_ context.Context, samples []int16) error {
	s.samples = append(s.samples, samples...)
	return nil
}
func (s *recordingSink) WriteSamples(_ context.Context, samples []int16) error {
	s.samples = append(s.samples, samples...)
	return nil
}
func (s *recordingSink) Close() error {
	s.closeCalls++
	return nil
}

type recordingScheduler struct{}

func (*recordingScheduler) Now() time.Time                             { return time.Time{} }
func (*recordingScheduler) NewTimer(time.Duration) platformclock.Timer { return nil }
func (*recordingScheduler) Wait(context.Context, time.Duration) error  { return nil }
func (*recordingScheduler) WithDeadline(ctx context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	return context.WithDeadline(ctx, deadline)
}
func (*recordingScheduler) WithTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, timeout)
}
