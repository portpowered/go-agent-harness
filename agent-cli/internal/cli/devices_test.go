package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/spf13/cobra"
)

func TestDevicesListCommandTableAndJSONGoldens(t *testing.T) {
	registry := newDevicesTestRegistry(t)
	got := executeDevicesList(t, registry)
	if got.err != nil || got.stdout != devicesTableGolden || got.stderr != "" {
		t.Fatalf("table result = (%q, %q, %v), want (%q, %q, nil)", got.stdout, got.stderr, got.err, devicesTableGolden, "")
	}
	if registry.listCalls != 1 || registry.defaultCalls[audio.DirectionInput] != 1 || registry.defaultCalls[audio.DirectionOutput] != 1 || registry.openCalls != 0 {
		t.Fatalf("registry observations = %+v, want one list/default lookup and no opens", registry)
	}

	registry = newDevicesTestRegistry(t)
	got = executeDevicesList(t, registry, "--json")
	if got.err != nil || got.stdout != devicesJSONGolden || got.stderr != "" {
		t.Fatalf("JSON result = (%q, %q, %v), want (%q, %q, nil)", got.stdout, got.stderr, got.err, devicesJSONGolden, "")
	}
	var decoded deviceListResponse
	if err := json.Unmarshal([]byte(got.stdout), &decoded); err != nil {
		t.Fatalf("JSON is invalid: %v", err)
	}
	if len(decoded.Devices) != 4 || decoded.Devices[0].ID != "virtual:input-a" || decoded.Devices[2].Direction != audio.DirectionOutput {
		t.Fatalf("decoded devices = %#v, want four canonically ordered entries", decoded.Devices)
	}
	if registry.openCalls != 0 {
		t.Fatalf("JSON listing opened %d devices", registry.openCalls)
	}
}

func TestDevicesListCommandJSONIDRoundTripsThroughSelection(t *testing.T) {
	registry := newDevicesTestRegistry(t)
	result := executeDevicesList(t, registry, "--json")
	if result.err != nil {
		t.Fatalf("list error = %v", result.err)
	}
	var response deviceListResponse
	if err := json.Unmarshal([]byte(result.stdout), &response); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	var inputID audio.DeviceID
	for _, device := range response.Devices {
		if device.Direction == audio.DirectionInput {
			inputID = device.ID
			break
		}
	}
	if inputID == "" {
		t.Fatal("JSON returned no input device ID")
	}
	selection, err := audio.ResolveDeviceSelection(registry, audio.DeviceSelectionRequest{InputSelector: inputID, OutputSelector: "virtual:output-a"})
	if err != nil {
		t.Fatalf("selection for JSON input ID %q: %v", inputID, err)
	}
	if selection.Input.ID != inputID || selection.Input.Direction != audio.DirectionInput {
		t.Fatalf("selected input = %#v, want ID %q and input direction", selection.Input, inputID)
	}
	if registry.openCalls != 0 {
		t.Fatalf("resolve-only selection opened %d devices", registry.openCalls)
	}
}

func TestDevicesListCommandFlagMatrix(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		stdout  string
		wantErr string
	}{
		{name: "table", stdout: devicesTableGolden},
		{name: "json", args: []string{"--json"}, stdout: devicesJSONGolden},
		{name: "explicit false", args: []string{"--json=false"}, stdout: devicesTableGolden},
		{name: "unknown flag", args: []string{"--unknown"}, wantErr: "unknown flag: --unknown"},
		{name: "unexpected argument", args: []string{"extra"}, wantErr: "unknown command \"extra\" for \"agent devices list\""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := executeDevicesList(t, newDevicesTestRegistry(t), tt.args...)
			if tt.wantErr == "" {
				if result.err != nil || result.stdout != tt.stdout || result.stderr != "" {
					t.Fatalf("result = (%q, %q, %v), want stdout %q and no error", result.stdout, result.stderr, result.err, tt.stdout)
				}
				return
			}
			if result.err == nil || result.err.Error() != tt.wantErr || result.stdout != "" {
				t.Fatalf("error result = (%q, %q, %v), want empty stdout and error %q", result.stdout, result.stderr, result.err, tt.wantErr)
			}
		})
	}
}

func TestDevicesListCommandEmptyRegistry(t *testing.T) {
	registry := &devicesTestRegistry{defaultCalls: map[audio.Direction]int{}}
	result := executeDevicesList(t, registry)
	if result.err != nil || result.stdout != "INPUT\nOUTPUT\nNo audio devices found.\n" {
		t.Fatalf("empty table result = (%q, %v), want successful headings and note", result.stdout, result.err)
	}
	if registry.listCalls != 1 || len(registry.defaultCalls) != 0 {
		t.Fatalf("empty registry observations = %+v, want one list and no default lookups", registry)
	}

	registry = &devicesTestRegistry{defaultCalls: map[audio.Direction]int{}}
	result = executeDevicesList(t, registry, "--json")
	if result.err != nil || result.stdout != "{\"devices\":[]}\n" {
		t.Fatalf("empty JSON result = (%q, %v), want empty schema", result.stdout, result.err)
	}
}

func TestDevicesListCommandErrorsDoNotWritePartialOutput(t *testing.T) {
	listErr := errors.New("enumerator unavailable")
	result := executeDevicesList(t, &devicesTestRegistry{listErr: listErr, defaultCalls: map[audio.Direction]int{}}, "--json")
	if result.err == nil || !strings.Contains(result.err.Error(), listErr.Error()) || result.stdout != "" {
		t.Fatalf("list failure = (%q, %v), want actionable error and empty stdout", result.stdout, result.err)
	}

	defaultErr := errors.New("default lookup unavailable")
	registry := newDevicesTestRegistry(t)
	registry.defaultErr = defaultErr
	result = executeDevicesList(t, registry)
	if result.err == nil || !strings.Contains(result.err.Error(), defaultErr.Error()) || result.stdout != "" {
		t.Fatalf("default failure = (%q, %v), want actionable error and empty stdout", result.stdout, result.err)
	}
}

const devicesTableGolden = "INPUT\n" +
	"  default id=virtual:input-a \"Desk Microphone\"\n" +
	"          id=virtual:input-b \"Room Microphone\"\n" +
	"OUTPUT\n" +
	"  default id=virtual:output-a \"Desk Speaker\"\n" +
	"          id=virtual:output-b \"Room Speaker\"\n"

const devicesJSONGolden = `{"devices":[{"id":"virtual:input-a","name":"Desk Microphone","direction":"input","default":true},{"id":"virtual:input-b","name":"Room Microphone","direction":"input","default":false},{"id":"virtual:output-a","name":"Desk Speaker","direction":"output","default":true},{"id":"virtual:output-b","name":"Room Speaker","direction":"output","default":false}]}` + "\n"

type devicesCommandResult struct {
	stdout, stderr string
	err            error
}

func executeDevicesList(t *testing.T, registry audio.DeviceRegistry, args ...string) devicesCommandResult {
	t.Helper()
	root := &cobra.Command{Use: "agent", SilenceUsage: true, SilenceErrors: true}
	devices := NewDevicesCommand().Generate()
	devices.AddCommand(NewDevicesListCommand(registry).Generate())
	root.AddCommand(devices)
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"devices", "list"}, args...))
	err := root.Execute()
	return devicesCommandResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

type devicesTestRegistry struct {
	devices      []audio.Device
	defaults     map[audio.Direction]audio.Device
	listErr      error
	defaultErr   error
	listCalls    int
	defaultCalls map[audio.Direction]int
	openCalls    int
}

func newDevicesTestRegistry(t *testing.T) *devicesTestRegistry {
	t.Helper()
	inputA := newDevicesTestDevice(t, "input-a", "Desk Microphone", audio.DirectionInput)
	inputB := newDevicesTestDevice(t, "input-b", "Room Microphone", audio.DirectionInput)
	outputA := newDevicesTestDevice(t, "output-a", "Desk Speaker", audio.DirectionOutput)
	outputB := newDevicesTestDevice(t, "output-b", "Room Speaker", audio.DirectionOutput)
	return &devicesTestRegistry{
		devices:      []audio.Device{outputB, inputB, outputA, inputA},
		defaults:     map[audio.Direction]audio.Device{audio.DirectionInput: inputA, audio.DirectionOutput: outputA},
		defaultCalls: map[audio.Direction]int{},
	}
}

func newDevicesTestDevice(t *testing.T, id, name string, direction audio.Direction) audio.Device {
	t.Helper()
	device, err := audio.NewDevice(audio.VirtualBackendName, id, name, direction)
	if err != nil {
		t.Fatalf("new test device: %v", err)
	}
	return device
}

func (r *devicesTestRegistry) List() ([]audio.Device, error) {
	r.listCalls++
	if r.listErr != nil {
		return nil, r.listErr
	}
	return append([]audio.Device(nil), r.devices...), nil
}

func (r *devicesTestRegistry) Default(direction audio.Direction) (audio.Device, error) {
	r.defaultCalls[direction]++
	if r.defaultErr != nil {
		return audio.Device{}, r.defaultErr
	}
	device, ok := r.defaults[direction]
	if !ok {
		return audio.Device{}, audio.NewNoDefaultDeviceError(direction)
	}
	return device, nil
}

func (r *devicesTestRegistry) Open(audio.DeviceID) (audio.OpenedDevice, error) {
	r.openCalls++
	return nil, fmt.Errorf("device listing must not open devices")
}
