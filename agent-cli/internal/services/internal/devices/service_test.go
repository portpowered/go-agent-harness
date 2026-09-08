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
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	runtimeDevicesWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/wire"
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
	probeService := runtimeDevicesWire.NewProbeService(registry, nil)
	observation, err := probeService.Run(ctx, serviceDevices.DeviceProbeRequest{
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
	if _, err := runtimeDevicesWire.NewProbeService(outputOnly, nil).Run(context.Background(), serviceDevices.DeviceProbeRequest{Scenario: serviceProbeScenario()}); err == nil || !bytes.Contains([]byte(err.Error()), []byte("status \"skip\"")) {
		t.Fatalf("Run unavailable registry error = %v, want explicit skip status", err)
	}

	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runtimeDevicesWire.NewProbeService(registry, nil).Run(ctx, serviceDevices.DeviceProbeRequest{Scenario: serviceProbeScenario()}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run cancelled context error = %v, want context.Canceled", err)
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
