package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	serviceDevices "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"
	"github.com/spf13/cobra"
)

func TestDevicesListCommandJSONIDRoundTripsThroughSelection(t *testing.T) {
	result := executeDevicesList(t, newDevicesTestService(), "--json")
	if result.err != nil {
		t.Fatalf("list error = %v", result.err)
	}
	var response deviceListResponse
	if err := json.Unmarshal([]byte(result.stdout), &response); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	var inputID string
	for _, device := range response.Devices {
		if device.Direction == serviceDevices.DeviceDirectionInput {
			inputID = device.ID
			break
		}
	}
	if inputID == "" {
		t.Fatal("JSON returned no input device ID")
	}
	if inputID != "virtual:input-a" {
		t.Fatalf("JSON input ID = %q, want stable service ID", inputID)
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
			result := executeDevicesList(t, newDevicesTestService(), tt.args...)
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
	service := &devicesTestService{}
	result := executeDevicesList(t, service)
	if result.err != nil || result.stdout != "INPUT\nOUTPUT\nNo audio devices found.\n" {
		t.Fatalf("empty table result = (%q, %v), want successful headings and note", result.stdout, result.err)
	}
	if service.enumerateCalls != 1 {
		t.Fatalf("service observations = %+v, want one enumeration", service)
	}

	result = executeDevicesList(t, service, "--json")
	if result.err != nil || result.stdout != "{\"devices\":[]}\n" {
		t.Fatalf("empty JSON result = (%q, %v), want empty schema", result.stdout, result.err)
	}
}

func TestDevicesListCommandErrorsDoNotWritePartialOutput(t *testing.T) {
	listErr := errors.New("enumerator unavailable")
	result := executeDevicesList(t, &devicesTestService{enumerateErr: listErr}, "--json")
	if result.err == nil || !strings.Contains(result.err.Error(), listErr.Error()) || result.stdout != "" {
		t.Fatalf("list failure = (%q, %v), want actionable error and empty stdout", result.stdout, result.err)
	}

	result = executeDevicesList(t, &devicesTestService{enumerateErr: errors.New("default lookup unavailable")})
	if result.err == nil || !strings.Contains(result.err.Error(), "default lookup unavailable") || result.stdout != "" {
		t.Fatalf("service failure = (%q, %v), want actionable error and empty stdout", result.stdout, result.err)
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

func executeDevicesList(t *testing.T, service serviceDevices.DeviceService, args ...string) devicesCommandResult {
	t.Helper()
	root := &cobra.Command{Use: "agent", SilenceUsage: true, SilenceErrors: true}
	devices := NewDevicesCommand().Generate()
	devices.AddCommand(NewDevicesListCommand(service).Generate())
	root.AddCommand(devices)
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"devices", "list"}, args...))
	err := root.Execute()
	return devicesCommandResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

type devicesTestService struct {
	devices        []serviceDevices.Device
	enumerateErr   error
	enumerateCalls int
}

func newDevicesTestService() *devicesTestService {
	return &devicesTestService{devices: []serviceDevices.Device{
		{ID: "virtual:input-a", Name: "Desk Microphone", Direction: serviceDevices.DeviceDirectionInput, Default: true},
		{ID: "virtual:input-b", Name: "Room Microphone", Direction: serviceDevices.DeviceDirectionInput},
		{ID: "virtual:output-a", Name: "Desk Speaker", Direction: serviceDevices.DeviceDirectionOutput, Default: true},
		{ID: "virtual:output-b", Name: "Room Speaker", Direction: serviceDevices.DeviceDirectionOutput},
	}}
}

func (s *devicesTestService) Enumerate(context.Context) (serviceDevices.DeviceList, error) {
	s.enumerateCalls++
	if s.enumerateErr != nil {
		return serviceDevices.DeviceList{}, s.enumerateErr
	}
	return serviceDevices.DeviceList{Devices: append([]serviceDevices.Device(nil), s.devices...)}, nil
}

func (*devicesTestService) Select(context.Context, serviceDevices.DeviceSelectionRequest) (serviceDevices.DeviceSelection, error) {
	return serviceDevices.DeviceSelection{}, errors.New("unused in list transport test")
}

func (*devicesTestService) ProbeAvailability(context.Context) (serviceDevices.DeviceProbeAvailability, error) {
	return serviceDevices.DeviceProbeAvailability{
		Status:     serviceDevices.DeviceProbeStatusSkip,
		ReasonCode: serviceDevices.DeviceProbeSkipNoDevices,
		Reason:     "no audio input or output device",
	}, nil
}
