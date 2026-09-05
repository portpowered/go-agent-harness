package cli

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	serviceDevices "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"
	servicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	"github.com/spf13/cobra"
)

func TestDeviceProbeSkipIsStructuredAndExitsSuccessfully(t *testing.T) {
	root := newDeviceProbeTestRoot(&deviceProbeRegistry{})
	root.SetArgs([]string{"probe", "run", deviceProbeScenarioPath, "--devices", "real", "--json"})
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)

	if err := root.Execute(); err != nil {
		t.Fatalf("device probe skip error = %v, want successful skip", err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &result); err != nil {
		t.Fatalf("decode skip result %q: %v", stdout.String(), err)
	}
	if result["status"] != "skip" || result["reason_code"] != string(devicegw.DeviceProbeSkipNoDevices) || result["reason"] != "no audio input or output device" {
		t.Fatalf("skip result = %v, want status, code, and reason", result)
	}
	var summary map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stderr.String())), &summary); err != nil {
		t.Fatalf("decode skip summary %q: %v", stderr.String(), err)
	}
	if summary["status"] != "skip" || summary["skipped"] != float64(1) || summary["failed"] != float64(0) {
		t.Fatalf("skip summary = %v, want one skipped and zero failed", summary)
	}
}

func TestDeviceProbeWithDevicesExecutesReadyPath(t *testing.T) {
	input, err := devicegw.NewDevice(devicegw.VirtualBackendName, "input", "Microphone", devicegw.DirectionInput)
	if err != nil {
		t.Fatal(err)
	}
	output, err := devicegw.NewDevice(devicegw.VirtualBackendName, "output", "Speaker", devicegw.DirectionOutput)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	root := newDeviceProbeTestRoot(&deviceProbeRegistry{devices: []devicegw.Device{input, output}}, func(_ context.Context, scenario probe.Scenario, availability serviceDevices.DeviceProbeAvailability) (probe.ObservationSnapshot, error) {
		called = true
		if availability.Status != serviceDevices.DeviceProbeStatusReady || availability.InputDevices[0].ID != input.ID || availability.OutputDevices[0].ID != output.ID {
			t.Fatalf("ready availability = %#v, want the enumerated input/output IDs", availability)
		}
		if scenario.ID != "s2s-v9-webrtc-device-roundtrip" {
			t.Fatalf("scenario = %q, want v9 scenario", scenario.ID)
		}
		samples := make([]int16, audio.FrameSize)
		for i := range samples {
			samples[i] = 1000
		}
		return probe.ObservationSnapshot{
			PCM16Samples: samples,
			Transcript:   "I heard device round trip",
		}, nil
	})
	root.SetArgs([]string{"probe", "run", deviceProbeScenarioPath, "--devices", "real", "--json"})
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)

	if err := root.Execute(); err != nil {
		t.Fatalf("device-present execution error = %v, want a ready-path result", err)
	}
	if !called {
		t.Fatal("device-present probe did not invoke the ready-path executor")
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &result); err != nil {
		t.Fatalf("decode ready result %q: %v", stdout.String(), err)
	}
	if result["pass"] != true || result["name"] != "s2s-v9-webrtc-device-roundtrip" {
		t.Fatalf("ready result = %v, want a passing v9 result", result)
	}
	if strings.Contains(stdout.String()+stderr.String(), `"status":"skip"`) {
		t.Fatalf("device-present probe took skip path: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	var summary map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stderr.String())), &summary); err != nil {
		t.Fatalf("decode ready summary %q: %v", stderr.String(), err)
	}
	if summary["status"] != "pass" || summary["passed"] != float64(1) || summary["failed"] != float64(0) {
		t.Fatalf("ready summary = %v, want one passed scenario", summary)
	}
}

func TestDeviceProbeReadyPathUsesDeadguard(t *testing.T) {
	input, err := devicegw.NewDevice(devicegw.VirtualBackendName, "input", "Microphone", devicegw.DirectionInput)
	if err != nil {
		t.Fatal(err)
	}
	output, err := devicegw.NewDevice(devicegw.VirtualBackendName, "output", "Speaker", devicegw.DirectionOutput)
	if err != nil {
		t.Fatal(err)
	}
	root := &cobra.Command{Use: "agent", SilenceUsage: true, SilenceErrors: true}
	probeCommand := NewProbeCommand().Generate()
	registry := &deviceProbeRegistry{devices: []devicegw.Device{input, output}}
	run := NewProbeRunCommandWithDeviceService(deviceProbeService{registry: registry}, nil, nil)
	run.deviceProbeDeadline = 25 * time.Millisecond
	run.deviceProbeExec = func(ctx context.Context, _ probe.Scenario, _ serviceDevices.DeviceProbeAvailability) (probe.ObservationSnapshot, error) {
		<-ctx.Done()
		return probe.ObservationSnapshot{}, ctx.Err()
	}
	probeCommand.AddCommand(run.Generate())
	root.AddCommand(probeCommand)
	root.SetArgs([]string{"probe", "run", deviceProbeScenarioPath, "--devices", "real", "--json"})
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)

	err = root.Execute()
	if err == nil {
		t.Fatal("device ready path succeeded, want deadguard timeout")
	}
	if !strings.Contains(stdout.String(), "deadguard") && !strings.Contains(stderr.String(), "deadguard") {
		t.Fatalf("deadguard diagnostic missing from command output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestDeviceProbeRuntimeUsesBoundDevicesAndSessionOutput(t *testing.T) {
	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("create virtual registry: %v", err)
	}
	availability, err := devicegw.ProbeDeviceAvailability(registry)
	if err != nil {
		t.Fatalf("probe virtual availability: %v", err)
	}
	scenario, err := loadProbeScenario(mustReadDeviceProbeScenario(t))
	if err != nil {
		t.Fatalf("load device scenario: %v", err)
	}
	inputPlan, err := serviceDevices.ProbeInputPlan(scenario)
	if err != nil {
		t.Fatalf("device input contract: %v", err)
	}
	corpusPath, err := replayCorpusPath(inputPlan.CorpusID)
	if err != nil {
		t.Fatalf("locate authored input corpus: %v", err)
	}
	corpusWAV, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("read authored input corpus: %v", err)
	}
	rate, corpusSamples, err := wavio.Read(bytes.NewReader(corpusWAV))
	if err != nil {
		t.Fatalf("decode authored input corpus: %v", err)
	}
	if rate != wavio.Rate16kHz {
		t.Fatalf("authored input corpus rate = %d, want %d", rate, wavio.Rate16kHz)
	}
	input, err := registry.Default(devicegw.DirectionInput)
	if err != nil {
		t.Fatalf("select input: %v", err)
	}
	if availability.InputDevices[0].ID != input.ID {
		t.Fatalf("availability input = %q, default input = %q", availability.InputDevices[0].ID, input.ID)
	}
	output, err := registry.Default(devicegw.DirectionOutput)
	if err != nil {
		t.Fatalf("select output: %v", err)
	}
	seed, err := devicegw.NewDeviceSink(registry, output.ID)
	if err != nil {
		t.Fatalf("open seeded input source: %v", err)
	}
	defer func() { _ = seed.Close() }()
	const seededDeviceFrameCount = 8
	corpusStart := -1
	for offset := 0; offset+seededDeviceFrameCount*audio.FrameSize <= len(corpusSamples); offset += audio.FrameSize {
		if serviceDevices.ProbeRMS(corpusSamples[offset:offset+audio.FrameSize]) > audio.DefaultVADConfig.EnergyThreshold {
			corpusStart = offset
			break
		}
	}
	if corpusStart < 0 {
		t.Fatalf("authored input corpus has no voiced frame window")
	}
	for i := 0; i < seededDeviceFrameCount; i++ {
		frameStart := corpusStart + i*audio.FrameSize
		frame := append([]int16(nil), corpusSamples[frameStart:frameStart+audio.FrameSize]...)
		if err := seed.WriteFrame(context.Background(), frame); err != nil {
			t.Fatalf("seed authored microphone frame %d: %v", i, err)
		}
	}

	session := newDeviceProbeSession()
	if !session.receive.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeSessionOpen}) {
		t.Fatal("queue session open")
	}
	if !session.receive.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeSessionCreated}) {
		t.Fatal("queue session created")
	}
	const deviceProbeInputFrameSamples = audio.SampleRate / 50
	const deviceProbeProviderFrameSamples = wavio.Rate24kHz / 50
	response, err := wavio.Resample(voicedDeviceProbeFrame()[:deviceProbeInputFrameSamples], audio.SampleRate, wavio.Rate24kHz)
	if err != nil {
		t.Fatalf("resample response: %v", err)
	}
	responsePCM := pcm16ProbeBytes(response)
	audioObserved := make(chan []byte, 32)
	var observedInstructions string
	runContext, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go func() {
		for {
			select {
			case message := <-session.sent:
				if message.Type == messages.StreamTypeAudioDelta {
					if value, ok := message.Value.(*messages.AudioDeltaValue); ok && value != nil {
						audioObserved <- append([]byte(nil), value.Content...)
					}
					continue
				}
				if message.Type != messages.StreamTypeMessageEnd {
					continue
				}
				for _, responseMessage := range []messages.StreamMessage{
					{Type: messages.StreamTypeTranscriptDelta, Value: messages.NewTranscriptDeltaValue("device round trip")},
					{Type: messages.StreamTypeTranscriptEnd, Value: messages.NewTranscriptEndValue("device round trip")},
					{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue(responsePCM)},
					{Type: messages.StreamTypeAudioEnd, Value: messages.NewAudioEndValue()},
					{Type: messages.StreamTypeMessageEnd},
				} {
					if !session.receive.Write(runContext, responseMessage) {
						return
					}
				}
				return
			case <-runContext.Done():
				return
			}
		}
	}()

	observation, err := servicewire.NewDeviceProbeService(registry).Run(runContext, serviceDevices.DeviceProbeRequest{
		Scenario:             scenario,
		SessionInferencer:    &deviceProbeSessionInferencer{session: session},
		CaptureTime:          750 * time.Millisecond,
		InstructionsObserved: func(instructions string) { observedInstructions = instructions },
	})
	if err != nil {
		t.Fatalf("run device probe runtime: %v", err)
	}
	var capturedAudio []byte
drainAudio:
	for {
		select {
		case chunk := <-audioObserved:
			capturedAudio = append(capturedAudio, chunk...)
		default:
			break drainAudio
		}
	}
	if len(capturedAudio) == 0 || len(capturedAudio)%2 != 0 {
		t.Fatalf("runtime forwarded microphone audio payload of %d bytes, want non-empty PCM16", len(capturedAudio))
	}
	wantProviderFrames := seededDeviceFrameCount * audio.FrameSize / deviceProbeInputFrameSamples
	wantAudioBytes := wantProviderFrames * deviceProbeProviderFrameSamples * 2
	if len(capturedAudio) != wantAudioBytes {
		t.Fatalf("runtime forwarded %d authored PCM bytes, want %d bytes from %d seeded device frames", len(capturedAudio), wantAudioBytes, seededDeviceFrameCount)
	}
	capturedSamples := make([]int16, len(capturedAudio)/2)
	for i := range capturedSamples {
		capturedSamples[i] = int16(binary.LittleEndian.Uint16(capturedAudio[i*2:]))
	}
	if serviceDevices.ProbeRMS(capturedSamples) <= audio.DefaultVADConfig.EnergyThreshold {
		t.Fatalf("runtime forwarded authored input RMS = %.2f, want voiced corpus input above %.2f", serviceDevices.ProbeRMS(capturedSamples), audio.DefaultVADConfig.EnergyThreshold)
	}
	if !strings.Contains(observedInstructions, inputPlan.CorpusID) || !strings.Contains(observedInstructions, inputPlan.Utterance) {
		t.Fatalf("session instructions = %q, want authored corpus %q and utterance %q", observedInstructions, inputPlan.CorpusID, inputPlan.Utterance)
	}
	if len(observation.PCM16Samples) == 0 || serviceDevices.ProbeRMS(observation.PCM16Samples) <= audio.DefaultVADConfig.EnergyThreshold {
		t.Fatalf("runtime output samples/RMS = %d/%.2f, want non-silent output", len(observation.PCM16Samples), serviceDevices.ProbeRMS(observation.PCM16Samples))
	}
	if observation.Transcript != "device round trip" {
		t.Fatalf("runtime transcript = %q, want provider session transcript", observation.Transcript)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seeded input source: %v", err)
	}
	if got := registry.Observations(); got.OpenCount != 3 || got.ReleaseCount != 3 {
		t.Fatalf("device lifecycle observations = %+v, want seed plus bound input/output", got)
	}
}

func mustReadDeviceProbeScenario(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(deviceProbeScenarioPath)
	if err != nil {
		t.Fatalf("read device scenario: %v", err)
	}
	return data
}

func newDeviceProbeTestRoot(registry devicegw.DeviceRegistry, exec ...DeviceProbeExecFunc) *cobra.Command {
	root := &cobra.Command{Use: "agent", SilenceUsage: true, SilenceErrors: true}
	probe := NewProbeCommand().Generate()
	run := NewProbeRunCommandWithDeviceService(deviceProbeService{registry: registry}, nil, nil)
	if len(exec) > 0 {
		run.deviceProbeExec = exec[0]
	}
	probe.AddCommand(run.Generate())
	root.AddCommand(probe)
	return root
}

type deviceProbeService struct{ registry devicegw.DeviceRegistry }

func (s deviceProbeService) Enumerate(context.Context) (serviceDevices.DeviceList, error) {
	return serviceDevices.DeviceList{}, nil
}

func (s deviceProbeService) Select(context.Context, serviceDevices.DeviceSelectionRequest) (serviceDevices.DeviceSelection, error) {
	return serviceDevices.DeviceSelection{}, nil
}

func (s deviceProbeService) ProbeAvailability(context.Context) (serviceDevices.DeviceProbeAvailability, error) {
	availability, err := devicegw.ProbeDeviceAvailability(s.registry)
	if err != nil {
		return serviceDevices.DeviceProbeAvailability{}, err
	}
	result := serviceDevices.DeviceProbeAvailability{
		Status:            serviceDevices.DeviceProbeStatus(availability.Status),
		ReasonCode:        serviceDevices.DeviceProbeSkipCode(availability.ReasonCode),
		Reason:            availability.Reason,
		InputDeviceCount:  availability.InputDeviceCount,
		OutputDeviceCount: availability.OutputDeviceCount,
	}
	result.Devices = deviceProbeServiceDevices(availability.Devices)
	result.InputDevices = deviceProbeServiceDevices(availability.InputDevices)
	result.OutputDevices = deviceProbeServiceDevices(availability.OutputDevices)
	return result, nil
}

func deviceProbeServiceDevices(devices []devicegw.Device) []serviceDevices.Device {
	result := make([]serviceDevices.Device, 0, len(devices))
	for _, device := range devices {
		result = append(result, serviceDevices.Device{
			ID:          device.ID,
			Backend:     device.Backend,
			NativeID:    device.NativeID,
			Name:        device.Name,
			DisplayName: device.DisplayName,
			Direction:   serviceDevices.DeviceDirection(device.Direction),
		})
	}
	return result
}

type deviceProbeRegistry struct {
	devices []devicegw.Device
}

func (r *deviceProbeRegistry) List() ([]devicegw.Device, error) {
	return append([]devicegw.Device(nil), r.devices...), nil
}

func (r *deviceProbeRegistry) Default(devicegw.Direction) (devicegw.Device, error) {
	return devicegw.Device{}, fmt.Errorf("device probe availability must not resolve defaults")
}

func (r *deviceProbeRegistry) Open(devicegw.DeviceID) (devicegw.OpenedDevice, error) {
	return nil, fmt.Errorf("device probe availability must not open devices")
}
