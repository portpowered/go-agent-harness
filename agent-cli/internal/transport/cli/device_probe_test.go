package cli

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	serviceDevices "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/participants"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
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
		if scenario.ID != v9WebRTCDeviceScenarioID {
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
	if result["pass"] != true || result["name"] != v9WebRTCDeviceScenarioID {
		t.Fatalf("ready result = %v, want a passing v9 result", result)
	}
	if strings.Contains(stdout.String()+stderr.String(), `"status":"skip"`) {
		t.Fatalf("device-present probe took skip path: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	var summary map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stderr.String())), &summary); err != nil {
		t.Fatalf("decode ready summary %q: %v", stderr.String(), err)
	}
	if summary["status"] != probeStatusPass || summary["passed"] != float64(1) || summary["failed"] != float64(0) {
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
	run := NewProbeRunCommandWithDeviceService(deviceProbeService{registry: registry}, nil, nil, newReplayRuntimeServiceForTest())
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

func newDeviceProbeTestRoot(registry devicegw.DeviceRegistry, exec ...DeviceProbeExecFunc) *cobra.Command {
	root := &cobra.Command{Use: "agent", SilenceUsage: true, SilenceErrors: true}
	probe := NewProbeCommand().Generate()
	run := NewProbeRunCommandWithDeviceService(deviceProbeService{registry: registry}, nil, nil, newReplayRuntimeServiceForTest())
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

// openReadyDeviceProbePair opens the registry's default input and output
// devices after requiring the registry to report ready.
func openReadyDeviceProbePair(t *testing.T, registry devicegw.DeviceRegistry) (*devicegw.DeviceSource, *devicegw.DeviceSink) {
	t.Helper()
	availability, err := devicegw.ProbeDeviceAvailability(registry)
	if err != nil {
		t.Fatalf("probe device availability: %v", err)
	}
	if availability.Status != devicegw.DeviceProbeStatusReady {
		t.Fatalf("virtual device probe status = %s, want ready (reason=%s)", availability.Status, availability.Reason)
	}
	input, err := registry.Default(devicegw.DirectionInput)
	if err != nil {
		t.Fatalf("select default input device: %v", err)
	}
	output, err := registry.Default(devicegw.DirectionOutput)
	if err != nil {
		t.Fatalf("select default output device: %v", err)
	}
	source, err := devicegw.NewDeviceSource(registry, input.ID)
	if err != nil {
		t.Fatalf("open selected input %q: %v", input.ID, err)
	}
	t.Cleanup(func() { closeForTest(t, source.Close) })
	sink, err := devicegw.NewDeviceSink(registry, output.ID)
	if err != nil {
		t.Fatalf("open selected output %q: %v", output.ID, err)
	}
	t.Cleanup(func() { closeForTest(t, sink.Close) })
	return source, sink
}

// sendDeviceProbeUserTurn delivers captured audio and the turn boundary to
// the session runner and requires both to reach the provider session in order.
func sendDeviceProbeUserTurn(t *testing.T, ctx context.Context, runner *participants.ModelRunner, session *deviceProbeSession, pcm []byte) {
	t.Helper()
	select {
	case runner.UserAudioInbox <- pcm:
	case <-ctx.Done():
		t.Fatalf("send captured audio to session: %v", ctx.Err())
	}
	audioMessage := readDeviceProbeSessionMessage(t, ctx, session.sent)
	if audioMessage.Type != messages.StreamTypeAudioDelta {
		t.Fatalf("session audio message type = %s, want %s", audioMessage.Type, messages.StreamTypeAudioDelta)
	}
	audioValue, ok := audioMessage.Value.(*messages.AudioDeltaValue)
	if !ok {
		t.Fatalf("session audio message value = %T, want *messages.AudioDeltaValue", audioMessage.Value)
	}
	if !bytes.Equal(audioValue.Content, pcm) {
		t.Fatalf("session audio bytes differ from active input track: got %d bytes, want %d", len(audioValue.Content), len(pcm))
	}
	select {
	case runner.UserEventInbox <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd}:
	case <-ctx.Done():
		t.Fatalf("send captured turn boundary to session: %v", ctx.Err())
	}
	turnMessage := readDeviceProbeSessionMessage(t, ctx, session.sent)
	if turnMessage.Type != messages.StreamTypeMessageEnd {
		t.Fatalf("session turn message type = %s, want %s after audio", turnMessage.Type, messages.StreamTypeMessageEnd)
	}
}

// assertDeviceProbeSpeakerEmission writes the session response to the output
// device and requires the virtual loopback to observe the exact frame.
func assertDeviceProbeSpeakerEmission(t *testing.T, ctx context.Context, sink *devicegw.DeviceSink, source *devicegw.DeviceSource, responseSamples []int16, responsePCM []byte) {
	t.Helper()
	outputTap := &deviceProbeOutputTap{sink: sink}
	if err := outputTap.WriteFrame(ctx, responseSamples); err != nil {
		t.Fatalf("write session response to selected output device: %v", err)
	}
	emitted := make([]int16, audio.FrameSize)
	if err := source.ReadFrame(ctx, emitted); err != nil {
		t.Fatalf("tap selected output device emission: %v", err)
	}
	if !bytes.Equal(pcm16ProbeBytes(emitted), responsePCM) {
		t.Fatalf("emitted speaker frame changed: got %d bytes, want %d", len(pcm16ProbeBytes(emitted)), len(responsePCM))
	}
	if err := assertDeviceProbeEnergy("speaker output", emitted); err != nil {
		t.Fatal(err)
	}
	if got := outputTap.LastRMS(); got != pcm16ProbeRMS(emitted) {
		t.Fatalf("speaker tap RMS = %.2f, loopback RMS = %.2f, want equal measurements", got, pcm16ProbeRMS(emitted))
	}
}
