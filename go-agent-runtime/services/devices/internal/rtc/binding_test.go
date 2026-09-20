package rtc

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

type idleSession struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	once    sync.Once
}

func newIdleSession() *idleSession {
	return &idleSession{receive: messages.NewTypedBuffer[messages.StreamMessage](8), done: make(chan struct{})}
}

func (s *idleSession) Send(ctx context.Context, _ messages.StreamMessage) bool {
	return ctx.Err() == nil
}
func (s *idleSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.receive }
func (s *idleSession) Done() <-chan struct{}                                  { return s.done }
func (s *idleSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

func testRegistry(t *testing.T) *devicegw.SimulatedDuplexRegistry {
	t.Helper()
	registry, err := devicegw.NewSimulatedDuplexRegistry(devicegw.DuplexScenario{
		Render:  devicegw.ClockSpec{NominalRate: audio.SampleRate, Quanta: []int{audio.FrameSize}},
		Capture: devicegw.ClockSpec{NominalRate: audio.SampleRate, Quanta: []int{audio.FrameSize}},
	})
	if err != nil {
		t.Fatalf("NewSimulatedDuplexRegistry() error = %v", err)
	}
	return registry
}

func TestFactoryBindsSelectedDevicesAndClosesIdempotently(t *testing.T) {
	factory := NewFactory(testRegistry(t))
	binding, err := factory.BindRTC(context.Background(), devices.RTCBindingRequest{InputPresent: true, OutputPresent: true})
	if err != nil {
		t.Fatalf("BindRTC() error = %v", err)
	}
	if binding == nil || binding.Inferencer() == nil || binding.Errors() == nil {
		t.Fatal("BindRTC() returned an incomplete public binding")
	}
	selection, ok := binding.(interface{ SelectedDeviceIDs() (string, string) })
	if !ok {
		t.Fatal("binding did not expose optional device selection")
	}
	input, output := selection.SelectedDeviceIDs()
	if input == "" || output == "" {
		t.Fatalf("selected devices = %q, %q; want both directions", input, output)
	}
	if err := binding.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := binding.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestFactoryAdmitsOneWayAndRejectsInvalidAdmission(t *testing.T) {
	factory := NewFactory(testRegistry(t))
	var nilContext context.Context
	inputBinding, err := factory.BindRTC(context.Background(), devices.RTCBindingRequest{InputDevice: "default", BypassSelfHearing: true})
	if err != nil || inputBinding == nil {
		t.Fatalf("input BindRTC() = %v, %v", inputBinding, err)
	}
	if err := inputBinding.Close(); err != nil {
		t.Fatalf("input Close() error = %v", err)
	}

	outputBinding, err := factory.BindRTC(context.Background(), devices.RTCBindingRequest{OutputDevice: "default", BypassSelfHearing: true})
	if err != nil || outputBinding == nil {
		t.Fatalf("output BindRTC() = %v, %v", outputBinding, err)
	}
	if err := outputBinding.Close(); err != nil {
		t.Fatalf("output Close() error = %v", err)
	}

	if _, err := factory.BindRTC(nilContext, devices.RTCBindingRequest{InputPresent: true}); err == nil {
		t.Fatal("nil context unexpectedly accepted")
	}
	if binding, err := factory.BindRTC(context.Background(), devices.RTCBindingRequest{}); err != nil || binding != nil {
		t.Fatalf("empty request = %v, %v; want nil binding", binding, err)
	}
	if _, err := (&Factory{}).BindRTC(context.Background(), devices.RTCBindingRequest{InputPresent: true}); !errors.Is(err, devices.ErrUnavailable) {
		t.Fatalf("missing registry error = %v, want ErrUnavailable", err)
	}
}

func TestBindingContractsPreserveTypedFailures(t *testing.T) {
	var nilBinding *binding
	if nilBinding.Inferencer() != nil || nilBinding.Errors() != nil || nilBinding.Close() != nil {
		t.Fatal("nil binding did not preserve inert contract")
	}
	if got := (&bindingError{flag: "--audio-in-device", direction: devicegw.DirectionInput, id: "mic", err: devicegw.ErrDeviceNotFound}).Error(); got == "" {
		t.Fatal("binding error was empty")
	}
	if !errors.Is((&bindingError{err: devicegw.ErrDeviceNotFound}), devicegw.ErrDeviceNotFound) {
		t.Fatal("binding error lost wrapped identity")
	}
	if !errors.Is((&mediaError{err: ErrSessionMediaUnavailable}), ErrSessionMediaUnavailable) {
		t.Fatal("media error lost wrapped identity")
	}
	if _, ok := rtcMedia(nil); ok {
		t.Fatal("nil session unexpectedly exposed media")
	}
	if _, err := (&inferencer{}).ConnectSession(context.Background()); !errors.Is(err, devices.ErrUnavailable) {
		t.Fatalf("nil inferencer error = %v, want ErrUnavailable", err)
	}
}

func TestBoundSessionForwardsAndClosesProviderSession(t *testing.T) {
	provider := newIdleSession()
	bound := newBoundSession(provider, &binding{}, context.Background())
	if bound.Receive() == nil || !bound.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeResponseCreate}) {
		t.Fatal("bound session did not forward its basic session contract")
	}
	if bound.SupportsResponseRequests() || bound.SupportsCompleteMessages() || bound.SupportsCompleteMessagesWithoutResponse() {
		t.Fatal("idle session advertised unsupported optional capabilities")
	}
	if bound.SendMessage(context.Background(), messages.Message{}) || bound.SendMessageWithoutResponse(context.Background(), messages.Message{}) {
		t.Fatal("idle session advertised unsupported message capability")
	}
	if bound.FlushOutbound(context.Background()) != nil || bound.InputDrops() != 0 || bound.OutputDrops() != 0 {
		t.Fatal("idle session exposed unexpected optional state")
	}
	if bound.DrainPlayback(context.Background()) != nil {
		t.Fatal("DrainPlayback() returned an error without a device sink")
	}
	if err := bound.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := bound.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}
