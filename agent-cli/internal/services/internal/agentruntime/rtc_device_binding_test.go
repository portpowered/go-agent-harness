package agentruntime_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	serviceDevices "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"
	agentruntime "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime"
	servicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli"
	runtimedevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	sessionclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

func TestDeviceServiceBindRTCUsesRegistryDefaultsAndClosesExactlyOnce(t *testing.T) {
	registry := virtualRTCRegistry(t)
	binding, err := newTestDeviceService(registry).BindRTC(context.Background(), runtimedevices.RTCBindingRequest{InputPresent: true, OutputPresent: true})
	if err != nil {
		t.Fatalf("BindRTC: %v", err)
	}
	if binding == nil || binding.Inferencer() == nil {
		t.Fatalf("binding = %#v, want public inferencer", binding)
	}
	if got := registry.Observations(); got.OpenCount != 2 || got.ReleaseCount != 0 {
		t.Fatalf("before close = %+v", got)
	}
	if err := binding.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := binding.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if got := registry.Observations(); got.OpenCount != 2 || got.ReleaseCount != 2 {
		t.Fatalf("after close = %+v", got)
	}
}

func TestDeviceServiceBindRTCPublishesCaptureSnapshotAfterClose(t *testing.T) {
	registry, err := devicegw.NewSimulatedDuplexRegistry(devicegw.DuplexScenario{
		Render:       devicegw.ClockSpec{NominalRate: 48000, Quanta: []int{480}},
		Capture:      devicegw.ClockSpec{NominalRate: 48000, Quanta: []int{480}},
		CaptureQueue: devicegw.QueueSpec{LatencyNanos: 1, DropPolicy: "drop_oldest"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got audio.CaptureQueueStats
	calls := 0
	binding, err := newTestDeviceService(registry).BindRTC(context.Background(), runtimedevices.RTCBindingRequest{
		InputPresent: true, InputSampleRate: 48000,
		CaptureObserver: func(_ devicegw.DeviceID, stats audio.CaptureQueueStats) { got = stats; calls++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Advance(2); err != nil {
		t.Fatal(err)
	}
	if err := binding.Close(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || got.DropPolicy != "drop_oldest" || got.DroppedSamples == 0 || got.SequenceGaps == 0 {
		t.Fatalf("capture observation = %+v, calls=%d", got, calls)
	}
}

func TestDeviceServiceBindRTCAcceptsDefaultKeywordAndExactIDs(t *testing.T) {
	registry := virtualRTCRegistry(t)
	binding, err := newTestDeviceService(registry).BindRTC(context.Background(), runtimedevices.RTCBindingRequest{InputDevice: "DeFaUlT", OutputDevice: "virtual:output"})
	if err != nil {
		t.Fatalf("BindRTC: %v", err)
	}
	if binding == nil {
		t.Fatal("BindRTC returned nil binding")
	}
	defer binding.Close()
}

func TestDeviceServiceBindRTCPreservesTypedRegistryErrors(t *testing.T) {
	cases := []struct {
		name    string
		request runtimedevices.RTCBindingRequest
		want    error
	}{
		{name: "missing input", request: runtimedevices.RTCBindingRequest{InputDevice: "virtual:missing", InputPresent: true}, want: devicegw.ErrDeviceNotFound},
		{name: "wrong direction", request: runtimedevices.RTCBindingRequest{InputDevice: "virtual:output", InputPresent: true}, want: devicegw.ErrDeviceDirectionMismatch},
		{name: "nil registry", request: runtimedevices.RTCBindingRequest{InputDevice: "virtual:input", InputPresent: true}, want: devicegw.ErrNilDeviceRegistry},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var service runtimedevices.Service
			if tc.name != "nil registry" {
				service = newTestDeviceService(virtualRTCRegistry(t))
			} else {
				service = newTestDeviceService(nil)
			}
			binding, err := service.BindRTC(context.Background(), tc.request)
			if err == nil || !errors.Is(err, tc.want) {
				if binding != nil {
					if closeErr := binding.Close(); closeErr != nil {
						t.Errorf("close failed binding: %v", closeErr)
					}
				}
				t.Fatalf("BindRTC error = %v, want errors.Is(%v)", err, tc.want)
			}
		})
	}
}

func TestDeviceServiceBindRTCReleasesInputWhenOutputOpenFails(t *testing.T) {
	registry := virtualRTCRegistry(t)
	held, err := registry.Open("virtual:exclusive")
	if err != nil {
		t.Fatalf("hold exclusive output: %v", err)
	}
	defer held.Close()
	_, err = newTestDeviceService(registry).BindRTC(context.Background(), runtimedevices.RTCBindingRequest{InputPresent: true, OutputDevice: "virtual:exclusive", OutputPresent: true})
	if err == nil || !errors.Is(err, devicegw.ErrDeviceInUse) {
		t.Fatalf("BindRTC error = %v, want device-in-use", err)
	}
	if got := registry.Observations(); got.OpenCount != 2 || got.ReleaseCount != 1 {
		t.Fatalf("partial failure = %+v", got)
	}
}

func TestDeviceServiceBindRTCNoSelectionDoesNotTouchRegistry(t *testing.T) {
	binding, err := newTestDeviceService(nil).BindRTC(context.Background(), runtimedevices.RTCBindingRequest{})
	if err != nil || binding != nil {
		t.Fatalf("no-selection BindRTC = binding:%v err:%v", binding, err)
	}
}

func TestValidateSessionAudioDeviceConflictsPreservesInputErrorAndAllowsIndependentOutputs(t *testing.T) {
	inputErr := serviceDevices.ValidateSessionAudioDeviceConflicts(true, false, true, false)
	if inputErr == nil || !errors.Is(inputErr, serviceDevices.ErrSessionAudioInputConflict) || !errors.Is(inputErr, devicegw.ErrDeviceSelectionConflict) {
		t.Fatalf("input conflict = %v", inputErr)
	}
	if outputErr := serviceDevices.ValidateSessionAudioDeviceConflicts(false, true, false, true); outputErr != nil {
		t.Fatalf("independent file/device outputs = %v", outputErr)
	}
}

func TestSessionCommandWiresBothRTCDeviceSelectorsBeforeProviderConnect(t *testing.T) {
	registry := virtualRTCRegistry(t)
	inferencer := &countingSessionInferencer{}
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), newInjectedSessionService(servicewire.SessionDependencies{Clock: sessionclock.Real{}, SessionInferencer: inferencer, DeviceRegistry: registry}), nil).Generate()
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{"--replay", "synthetic.json", "--audio-in-device", "virtual:input", "--audio-out-device", "virtual:output"})
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "provider connection should not be attempted") {
		t.Fatalf("command error = %v", err)
	}
	if inferencer.connects != 1 {
		t.Fatalf("provider connects = %d, want one", inferencer.connects)
	}
	if got := registry.Observations(); got.OpenCount != 2 || got.ReleaseCount != 2 {
		t.Fatalf("device observations = %+v", got)
	}
}

func TestRunSessionRTCDevicePreflightHappensBeforeProviderConnect(t *testing.T) {
	registry := virtualRTCRegistry(t)
	inferencer := &countingSessionInferencer{}
	err := agentruntime.RunSession(context.Background(), io.Discard, agentruntime.SessionRunOptions{ModelCatalog: testModelCatalog(), ReplayPath: "synthetic.json", SessionInferencer: inferencer, DeviceService: newTestDeviceService(registry), RTCBinding: runtimedevices.RTCBindingRequest{InputDevice: "virtual:missing", InputPresent: true}})
	if err == nil || !errors.Is(err, devicegw.ErrDeviceNotFound) {
		t.Fatalf("session error = %v, want typed preflight not-found", err)
	}
	if inferencer.connects != 0 {
		t.Fatalf("provider connects = %d, want zero", inferencer.connects)
	}
	if got := registry.Observations(); got.OpenCount != 0 || got.ReleaseCount != 0 {
		t.Fatalf("device observations = %+v, want no acquisition", got)
	}
}

func virtualRTCRegistry(t *testing.T) *devicegw.VirtualRegistry {
	t.Helper()
	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("new virtual registry: %v", err)
	}
	return registry
}
