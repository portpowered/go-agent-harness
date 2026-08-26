package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/spf13/cobra"
)

func TestDeviceProbeSkipIsStructuredAndExitsSuccessfully(t *testing.T) {
	root := newDeviceProbeTestRoot(&deviceProbeRegistry{})
	root.SetArgs([]string{"probe", "run", "s2s-v9-webrtc-device-roundtrip-001", "--devices", "real", "--json"})
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

func TestDeviceProbeWithDevicesDoesNotTakeSkipPath(t *testing.T) {
	input, err := audio.NewDevice(audio.VirtualBackendName, "input", "Microphone", audio.DirectionInput)
	if err != nil {
		t.Fatal(err)
	}
	output, err := audio.NewDevice(audio.VirtualBackendName, "output", "Speaker", audio.DirectionOutput)
	if err != nil {
		t.Fatal(err)
	}
	root := newDeviceProbeTestRoot(&deviceProbeRegistry{devices: []audio.Device{input, output}})
	root.SetArgs([]string{"probe", "run", "s2s-v9-webrtc-device-roundtrip-001", "--devices", "real"})
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)

	err = root.Execute()
	if err == nil || !strings.Contains(err.Error(), "--replay") {
		t.Fatalf("device-present execution error = %v, want the next-stage replay requirement", err)
	}
	if strings.Contains(stdout.String()+stderr.String(), `"status":"skip"`) {
		t.Fatalf("device-present probe took skip path: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func newDeviceProbeTestRoot(registry audio.DeviceRegistry) *cobra.Command {
	root := &cobra.Command{Use: "agent", SilenceUsage: true, SilenceErrors: true}
	probe := NewProbeCommand().Generate()
	probe.AddCommand(NewProbeRunCommand(registry).Generate())
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
