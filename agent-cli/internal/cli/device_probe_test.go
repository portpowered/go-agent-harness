package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
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
	if result["status"] != "skip" || result["reason_code"] != string(audio.DeviceProbeSkipNoDevices) || result["reason"] != "no audio input or output device" {
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
	input, err := audio.NewDevice(audio.VirtualBackendName, "input", "Microphone", audio.DirectionInput)
	if err != nil {
		t.Fatal(err)
	}
	output, err := audio.NewDevice(audio.VirtualBackendName, "output", "Speaker", audio.DirectionOutput)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	root := newDeviceProbeTestRoot(&deviceProbeRegistry{devices: []audio.Device{input, output}}, func(_ context.Context, scenario probe.Scenario, availability audio.DeviceProbeAvailability) (probe.ObservationSnapshot, error) {
		called = true
		if availability.Status != audio.DeviceProbeStatusReady || availability.InputDevices[0].ID != input.ID || availability.OutputDevices[0].ID != output.ID {
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

func TestDeviceProbeRuntimeUsesBoundDevicesAndSessionOutput(t *testing.T) {
	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("create virtual registry: %v", err)
	}
	availability, err := audio.ProbeDeviceAvailability(registry)
	if err != nil {
		t.Fatalf("probe virtual availability: %v", err)
	}
	input, err := registry.Default(audio.DirectionInput)
	if err != nil {
		t.Fatalf("select input: %v", err)
	}
	if availability.InputDevices[0].ID != input.ID {
		t.Fatalf("availability input = %q, default input = %q", availability.InputDevices[0].ID, input.ID)
	}
	output, err := registry.Default(audio.DirectionOutput)
	if err != nil {
		t.Fatalf("select output: %v", err)
	}
	seed, err := audio.NewDeviceSink(registry, output.ID)
	if err != nil {
		t.Fatalf("open seeded input source: %v", err)
	}
	defer func() { _ = seed.Close() }()
	for i := 0; i < 8; i++ {
		if err := seed.WriteFrame(context.Background(), voicedDeviceProbeFrame()); err != nil {
			t.Fatalf("seed microphone frame %d: %v", i, err)
		}
	}

	session := newDeviceProbeSession()
	if !session.receive.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeSessionOpen}) {
		t.Fatal("queue session open")
	}
	if !session.receive.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeSessionCreated}) {
		t.Fatal("queue session created")
	}
	response, err := wavio.Resample(voicedDeviceProbeFrame()[:deviceProbeInputFrameSamples], audio.SampleRate, wavio.Rate24kHz)
	if err != nil {
		t.Fatalf("resample response: %v", err)
	}
	responsePCM := pcm16ProbeBytes(response)
	audioObserved := make(chan []byte, 1)
	runContext, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go func() {
		for {
			select {
			case message := <-session.sent:
				if message.Type == messages.StreamTypeAudioDelta {
					if value, ok := message.Value.(*messages.AudioDeltaValue); ok && value != nil {
						select {
						case audioObserved <- append([]byte(nil), value.Content...):
						default:
						}
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

	scenario, err := loadProbeScenario(mustReadDeviceProbeScenario(t))
	if err != nil {
		t.Fatalf("load device scenario: %v", err)
	}
	observation, err := runDeviceProbeScenario(runContext, scenario, availability, registry, deviceProbeRuntimeOptions{
		SessionInferencer: &deviceProbeSessionInferencer{session: session},
		CaptureTime:       750 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("run device probe runtime: %v", err)
	}
	var capturedAudio []byte
	select {
	case capturedAudio = <-audioObserved:
	case <-runContext.Done():
		t.Fatalf("runtime did not forward microphone audio to the session: %v", runContext.Err())
	}
	if len(capturedAudio) == 0 || len(capturedAudio)%2 != 0 {
		t.Fatalf("runtime forwarded microphone audio payload of %d bytes, want non-empty PCM16", len(capturedAudio))
	}
	if len(observation.PCM16Samples) == 0 || liveDeviceProbeRMS(observation.PCM16Samples) <= audio.DefaultVADConfig.EnergyThreshold {
		t.Fatalf("runtime output samples/RMS = %d/%.2f, want non-silent output", len(observation.PCM16Samples), liveDeviceProbeRMS(observation.PCM16Samples))
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

func newDeviceProbeTestRoot(registry audio.DeviceRegistry, exec ...DeviceProbeExecFunc) *cobra.Command {
	root := &cobra.Command{Use: "agent", SilenceUsage: true, SilenceErrors: true}
	probe := NewProbeCommand().Generate()
	run := NewProbeRunCommand(registry)
	if len(exec) > 0 {
		run.deviceProbeExec = exec[0]
	}
	probe.AddCommand(run.Generate())
	root.AddCommand(probe)
	return root
}

type deviceProbeRegistry struct {
	devices []audio.Device
}

func (r *deviceProbeRegistry) List() ([]audio.Device, error) {
	return append([]audio.Device(nil), r.devices...), nil
}

func (r *deviceProbeRegistry) Default(audio.Direction) (audio.Device, error) {
	return audio.Device{}, fmt.Errorf("device probe availability must not resolve defaults")
}

func (r *deviceProbeRegistry) Open(audio.DeviceID) (audio.OpenedDevice, error) {
	return nil, fmt.Errorf("device probe availability must not open devices")
}
