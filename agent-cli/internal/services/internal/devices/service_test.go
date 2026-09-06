package devices

import (
	"bytes"
	"context"
	"errors"
	"math"
	"testing"
	"time"

	serviceDevices "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/participants"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

func TestServiceEnumerateAndSelectExposeOnlyMetadata(t *testing.T) {
	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	service := New(registry)

	list, err := service.Enumerate(context.Background())
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if len(list.Devices) != 3 || list.Devices[0].ID != "virtual:input" {
		t.Fatalf("devices = %#v, want sorted virtual metadata", list.Devices)
	}
	if !list.Devices[0].Default || list.Devices[1].Default || !list.Devices[2].Default {
		t.Fatalf("defaults = %#v, want only directional defaults", list.Devices)
	}

	selection, err := service.Select(context.Background(), serviceDevices.DeviceSelectionRequest{InputSelector: "virtual:input", OutputSelector: "virtual:output"})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if selection.Input.ID != "virtual:input" || selection.Output.ID != "virtual:output" || selection.LossPolicy != "fail" {
		t.Fatalf("selection = %#v, want resolved metadata", selection)
	}
}

func TestServiceProbeAvailabilityAndCancellation(t *testing.T) {
	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	service := New(registry)
	availability, err := service.ProbeAvailability(context.Background())
	if err != nil {
		t.Fatalf("ProbeAvailability: %v", err)
	}
	if availability.Status != "ready" || availability.InputDeviceCount != 1 || availability.OutputDeviceCount != 2 {
		t.Fatalf("availability = %#v, want ready counts", availability)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Enumerate(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Enumerate(canceled) = %v, want context.Canceled", err)
	}
}

func TestServiceRunVirtualProbeUsesInputAndOutputContracts(t *testing.T) {
	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	// The virtual pair models the same output-to-input ownership as a live
	// device. Seed the microphone side through the selected output endpoint;
	// Service.Run must consume it through DeviceSource and the RTC path.
	output, err := registry.Default(devicegw.DirectionOutput)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := devicegw.NewDeviceSink(registry, output.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = seed.Close() }()
	inputFrame := serviceProbeVoicedFrame()
	for i := 0; i < 10; i++ {
		if err := seed.WriteFrame(context.Background(), inputFrame); err != nil {
			t.Fatalf("seed input frame %d: %v", i, err)
		}
	}

	session := newServiceProbeSession()
	session.receive.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeSessionOpen})
	session.receive.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeSessionCreated})
	go session.respondAfterMessageEnd()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var observedInstructions string
	service := New(registry)
	observation, err := service.Run(ctx, serviceDevices.DeviceProbeRequest{
		Scenario:             serviceProbeScenario(),
		CaptureTime:          700 * time.Millisecond,
		SessionInferencer:    serviceProbeInferencer{session: session},
		InstructionsObserved: func(value string) { observedInstructions = value },
	})
	if err != nil {
		t.Fatalf("Run virtual probe: %v", err)
	}
	if !bytes.Contains([]byte(observedInstructions), []byte("probe-corpus")) {
		t.Fatalf("instructions = %q, want authored corpus identity", observedInstructions)
	}
	if session.audioMessages == 0 {
		t.Fatal("Run did not forward any microphone audio through the session")
	}
	if observation.Transcript != "virtual response" {
		t.Fatalf("transcript = %q, want provider transcript", observation.Transcript)
	}
	if len(observation.PCM16Samples) == 0 || audio.PCM16RMSEnergy(observation.PCM16Samples) <= audio.DefaultVADConfig.EnergyThreshold {
		t.Fatalf("output samples/RMS = %d/%.2f, want voiced output", len(observation.PCM16Samples), audio.PCM16RMSEnergy(observation.PCM16Samples))
	}
}

func TestServiceRunVirtualProbeRejectsUnavailableAndCancelledRuns(t *testing.T) {
	outputOnly, err := devicegw.NewVirtualRegistry(devicegw.VirtualBackendConfig{
		Devices:  []devicegw.VirtualDeviceConfig{{ID: "output", Name: "Output", Direction: devicegw.DirectionOutput}},
		Defaults: map[devicegw.Direction]string{devicegw.DirectionOutput: "output"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(outputOnly).Run(context.Background(), serviceDevices.DeviceProbeRequest{Scenario: serviceProbeScenario()}); err == nil || !bytes.Contains([]byte(err.Error()), []byte("status \"skip\"")) {
		t.Fatalf("Run unavailable registry error = %v, want explicit skip status", err)
	}

	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(registry).Run(ctx, serviceDevices.DeviceProbeRequest{Scenario: serviceProbeScenario()}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run cancelled context error = %v, want context.Canceled", err)
	}
}

func TestServiceProbeBridgeReportsTerminalAndMalformedEvents(t *testing.T) {
	tests := []struct {
		name    string
		message messages.StreamMessage
		want    string
	}{
		{name: "provider error", message: messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValue("wire failed")}, want: "wire failed"},
		{name: "bad transcript", message: messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Value: messages.NewTranscriptDeltaValue("wrong value")}, want: "transcript end value"},
		{name: "closed session", message: messages.StreamMessage{Type: messages.StreamTypeSessionClose}, want: "session closed before response completion"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := messages.NewTypedBuffer[messages.StreamMessage](4)
			runner := &participants.ModelRunner{DeltaOutbox: out}
			bridge := newLiveDeviceProbeSessionBridge(runner, nil, nil)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() { bridge.Run(ctx); close(done) }()
			if !out.Write(ctx, tc.message) {
				t.Fatal("write bridge event")
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatalf("bridge did not stop: %v", ctx.Err())
			}
			if err := bridge.errorValue(nil); err == nil || !bytes.Contains([]byte(err.Error()), []byte(tc.want)) {
				t.Fatalf("bridge error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestServiceProbeBridgeIgnoresNonTerminalDiagnostics(t *testing.T) {
	out := messages.NewTypedBuffer[messages.StreamMessage](4)
	runner := &participants.ModelRunner{DeltaOutbox: out}
	bridge := newLiveDeviceProbeSessionBridge(runner, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { bridge.Run(ctx); close(done) }()
	if !out.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewNonTerminalErrorValue("diagnostic", "info")}) {
		t.Fatal("write non-terminal diagnostic")
	}
	if !out.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionClose}) {
		t.Fatal("write session close")
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("bridge did not stop: %v", ctx.Err())
	}
	if err := bridge.errorValue(nil); err == nil || !bytes.Contains([]byte(err.Error()), []byte("session closed")) {
		t.Fatalf("bridge error = %v, want close after diagnostic", err)
	}
}

func TestServiceProbeBridgeLifecycleAndAudioValidation(t *testing.T) {
	out := messages.NewTypedBuffer[messages.StreamMessage](8)
	runner := &participants.ModelRunner{DeltaOutbox: out}
	bridge := newLiveDeviceProbeSessionBridge(runner, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { bridge.Run(ctx); close(done) }()
	if !out.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionOpen}) {
		t.Fatal("write session open")
	}
	if err := bridge.waitOpened(ctx); err != nil {
		t.Fatalf("waitOpened = %v, want success", err)
	}
	if !out.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValueWithMediaType([]byte{1, 2}, "audio/opus")}) {
		t.Fatal("write unsupported audio")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("bridge did not stop after unsupported audio")
	}
	if err := bridge.errorValue(nil); err == nil || !bytes.Contains([]byte(err.Error()), []byte("not PCM16")) {
		t.Fatalf("bridge audio error = %v, want PCM format diagnostic", err)
	}
	if err := bridge.waitResponse(context.Background()); err == nil {
		t.Fatal("waitResponse succeeded after bridge error")
	}

	// The pure helper paths retain their explicit error contracts as well.
	if got := liveDeviceProbeSessionError(messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValue("boom")}); got == nil || !bytes.Contains([]byte(got.Error()), []byte("boom")) {
		t.Fatalf("session error = %v, want provider message", got)
	}
	if got := liveDeviceProbeSessionError(messages.StreamMessage{Type: messages.StreamTypeError}); got == nil {
		t.Fatal("nil error payload produced nil diagnostic")
	}
}

func TestServiceProbeHelperContracts(t *testing.T) {
	bridge := newLiveDeviceProbeSessionBridge(&participants.ModelRunner{DeltaOutbox: messages.NewTypedBuffer[messages.StreamMessage](1)}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := bridge.waitOpened(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitOpened cancelled = %v, want context.Canceled", err)
	}
	if err := bridge.waitResponse(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitResponse cancelled = %v, want context.Canceled", err)
	}
	bridge.setError(errors.New("first"))
	bridge.setError(errors.New("second"))
	if got := bridge.errorValue(nil); got == nil || got.Error() != "first" {
		t.Fatalf("errorValue = %v, want first error", got)
	}

	if got := selectLiveDeviceProbeDevice(nil, []devicegw.Device{{ID: "fallback"}}, devicegw.DirectionInput); got.ID != "fallback" {
		t.Fatalf("fallback device = %q, want fallback", got.ID)
	}
	if err := closeDeviceProbeResource("test", func() error { return errors.New("close failed") }); err == nil || !bytes.Contains([]byte(err.Error()), []byte("close test")) {
		t.Fatalf("close resource error = %v, want resource context", err)
	}
	if got := scenarioDeviceProbeTranscript(probe.Scenario{Expectations: []probe.ExpectedBehavior{{Type: probe.ExpectTranscriptContains, Value: "from value"}}}); got != "from value" {
		t.Fatalf("transcript expectation fallback = %q, want from value", got)
	}
	if _, err := runDeviceProbeScenario(context.Background(), serviceProbeScenario(), devicegw.DeviceProbeAvailability{}, nil, serviceDevices.DeviceProbeRequest{}); err == nil || !bytes.Contains([]byte(err.Error()), []byte("status")) {
		t.Fatalf("invalid availability error = %v, want status diagnostic", err)
	}
}

func serviceProbeScenario() probe.Scenario {
	return probe.Scenario{
		ID:           "virtual-device-probe",
		Steps:        []probe.Step{{Type: probe.StepSendAudio, CorpusID: "probe-corpus", Text: "speak the test phrase"}, {Type: probe.StepClose}},
		Expectations: []probe.ExpectedBehavior{{Type: probe.ExpectTranscriptContains, Text: "virtual response"}},
	}
}

func serviceProbeVoicedFrame() []int16 {
	frame := make([]int16, audio.FrameSize)
	for i := range frame {
		frame[i] = int16(1400 * math.Sin(2*math.Pi*440*float64(i)/audio.SampleRate))
	}
	return frame
}

type serviceProbeInferencer struct{ session messages.Session }

func (i serviceProbeInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}

type serviceProbeSession struct {
	sent          chan messages.StreamMessage
	receive       *messages.TypedBuffer[messages.StreamMessage]
	done          chan struct{}
	audioMessages int
}

func newServiceProbeSession() *serviceProbeSession {
	return &serviceProbeSession{
		sent: make(chan messages.StreamMessage, 128), receive: messages.NewTypedBuffer[messages.StreamMessage](32), done: make(chan struct{}),
	}
}

func (s *serviceProbeSession) Send(ctx context.Context, message messages.StreamMessage) bool {
	if message.Type == messages.StreamTypeAudioDelta {
		s.audioMessages++
	}
	select {
	case s.sent <- message:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *serviceProbeSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}
func (s *serviceProbeSession) Done() <-chan struct{} { return s.done }
func (s *serviceProbeSession) Close() error          { return nil }

func (s *serviceProbeSession) respondAfterMessageEnd() {
	for message := range s.sent {
		if message.Type != messages.StreamTypeMessageEnd {
			continue
		}
		pcm := make([]int16, 480)
		for i := range pcm {
			pcm[i] = int16(1200 * math.Sin(2*math.Pi*440*float64(i)/24000))
		}
		for _, response := range []messages.StreamMessage{
			{Type: messages.StreamTypeTranscriptDelta, Value: messages.NewTranscriptDeltaValue("virtual response")},
			{Type: messages.StreamTypeTranscriptEnd, Value: messages.NewTranscriptEndValue("virtual response")},
			{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue(codec.EncodePCM16(pcm))},
			{Type: messages.StreamTypeAudioEnd, Value: messages.NewAudioEndValue()},
			{Type: messages.StreamTypeMessageEnd},
		} {
			if !s.receive.Write(context.Background(), response) {
				return
			}
		}
		return
	}
}
