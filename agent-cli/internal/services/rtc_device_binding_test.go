package services_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
)

func TestPrepareRTCDeviceBindingsUsesRegistryDefaultsAndClosesExactlyOnce(t *testing.T) {
	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("new virtual registry: %v", err)
	}

	binding, err := services.PrepareRTCDeviceBindings(services.RTCDeviceBindingRequest{
		Registry:      registry,
		InputPresent:  true,
		OutputPresent: true,
	})
	if err != nil {
		t.Fatalf("prepare device bindings: %v", err)
	}
	if binding == nil || binding.Source == nil || binding.Sink == nil {
		t.Fatalf("binding = %#v, want both directional endpoints", binding)
	}
	if binding.Source.DeviceID() != "virtual:input" {
		t.Fatalf("source device = %q, want virtual:input", binding.Source.DeviceID())
	}
	if binding.Sink.DeviceID() != "virtual:output" {
		t.Fatalf("sink device = %q, want virtual:output", binding.Sink.DeviceID())
	}
	if got := registry.Observations(); got.OpenCount != 2 || got.ReleaseCount != 0 {
		t.Fatalf("observations before close = %+v, want two opens and no releases", got)
	}

	if err := binding.Close(); err != nil {
		t.Fatalf("first binding close: %v", err)
	}
	if err := binding.Close(); err != nil {
		t.Fatalf("second binding close: %v", err)
	}
	if got := registry.Observations(); got.OpenCount != 2 || got.ReleaseCount != 2 {
		t.Fatalf("observations after close = %+v, want two opens and two releases", got)
	}
}

func TestPrepareRTCDeviceBindingsAcceptsDefaultKeywordAndExactIDs(t *testing.T) {
	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("new virtual registry: %v", err)
	}

	binding, err := services.PrepareRTCDeviceBindings(services.RTCDeviceBindingRequest{
		Registry:     registry,
		InputDevice:  "DeFaUlT",
		OutputDevice: "virtual:output",
	})
	if err != nil {
		t.Fatalf("prepare exact/default device bindings: %v", err)
	}
	defer binding.Close()
	if binding.Source.DeviceID() != "virtual:input" || binding.Sink.DeviceID() != "virtual:output" {
		t.Fatalf("resolved devices = input:%q output:%q, want virtual defaults", binding.Source.DeviceID(), binding.Sink.DeviceID())
	}
}

func TestPrepareRTCDeviceBindingsPreservesTypedRegistryErrors(t *testing.T) {
	cases := []struct {
		name     string
		request  services.RTCDeviceBindingRequest
		want     error
		wantFlag string
		wantID   audio.DeviceID
	}{
		{
			name: "missing exact input",
			request: services.RTCDeviceBindingRequest{
				Registry:     virtualRTCRegistry(t),
				InputDevice:  "virtual:missing",
				InputPresent: true,
			},
			want:     audio.ErrDeviceNotFound,
			wantFlag: "--audio-in-device",
			wantID:   "virtual:missing",
		},
		{
			name: "wrong input direction",
			request: services.RTCDeviceBindingRequest{
				Registry:     virtualRTCRegistry(t),
				InputDevice:  "virtual:output",
				InputPresent: true,
			},
			want:     audio.ErrDeviceDirectionMismatch,
			wantFlag: "--audio-in-device",
			wantID:   "virtual:output",
		},
		{
			name: "nil registry",
			request: services.RTCDeviceBindingRequest{
				InputDevice:  "virtual:input",
				InputPresent: true,
			},
			want:     audio.ErrNilDeviceRegistry,
			wantFlag: "--audio-in-device",
			wantID:   "virtual:input",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			binding, err := services.PrepareRTCDeviceBindings(tc.request)
			if err == nil {
				if binding != nil {
					binding.Close()
				}
				t.Fatal("expected typed registry error")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want errors.Is(%v)", err, tc.want)
			}
			var bindingErr *services.RTCDeviceBindingError
			if !errors.As(err, &bindingErr) {
				t.Fatalf("error = %v, want RTCDeviceBindingError", err)
			}
			if bindingErr.Flag != tc.wantFlag || bindingErr.DeviceID != tc.wantID {
				t.Fatalf("binding error = %+v, want flag %q and ID %q", bindingErr, tc.wantFlag, tc.wantID)
			}
		})
	}
}

func TestPrepareRTCDeviceBindingsReleasesInputWhenOutputOpenFails(t *testing.T) {
	registry := virtualRTCRegistry(t)
	held, err := registry.Open("virtual:exclusive")
	if err != nil {
		t.Fatalf("hold exclusive output: %v", err)
	}
	defer held.Close()

	_, err = services.PrepareRTCDeviceBindings(services.RTCDeviceBindingRequest{
		Registry:      registry,
		InputPresent:  true,
		OutputDevice:  "virtual:exclusive",
		OutputPresent: true,
	})
	if err == nil || !errors.Is(err, audio.ErrDeviceInUse) {
		t.Fatalf("prepare error = %v, want device-in-use error", err)
	}
	var bindingErr *services.RTCDeviceBindingError
	if !errors.As(err, &bindingErr) || bindingErr.Flag != "--audio-out-device" {
		t.Fatalf("error = %v, want typed output binding error", err)
	}
	if got := registry.Observations(); got.OpenCount != 2 || got.ReleaseCount != 1 {
		t.Fatalf("observations after partial failure = %+v, want held+input opens and input release", got)
	}
}

func TestPrepareRTCDeviceBindingsNoSelectionDoesNotTouchRegistry(t *testing.T) {
	binding, err := services.PrepareRTCDeviceBindings(services.RTCDeviceBindingRequest{})
	if err != nil {
		t.Fatalf("no-selection preparation: %v", err)
	}
	if binding != nil {
		t.Fatalf("binding = %#v, want nil when no device flag is present", binding)
	}
}

func TestValidateSessionAudioDeviceConflictsPreservesSharedAndSessionErrors(t *testing.T) {
	inputErr := services.ValidateSessionAudioDeviceConflicts(true, false, true, false)
	if inputErr == nil || !errors.Is(inputErr, services.ErrSessionAudioInputConflict) || !errors.Is(inputErr, audio.ErrDeviceSelectionConflict) {
		t.Fatalf("input conflict = %v, want session and shared conflict identities", inputErr)
	}
	var sharedErr *audio.DeviceSelectionConflictError
	if !errors.As(inputErr, &sharedErr) {
		t.Fatalf("input conflict = %v, want typed shared conflict", inputErr)
	}

	outputErr := services.ValidateSessionAudioDeviceConflicts(false, true, false, true)
	if outputErr == nil || !errors.Is(outputErr, services.ErrSessionAudioOutputConflict) || !errors.Is(outputErr, audio.ErrDeviceSelectionConflict) {
		t.Fatalf("output conflict = %v, want session and shared conflict identities", outputErr)
	}
}

func TestSessionCommandWiresBothRTCDeviceSelectorsBeforeProviderConnect(t *testing.T) {
	registry := virtualRTCRegistry(t)
	inferencer := &countingSessionInferencer{}
	cmd := cli.NewSessionCommandWithDeviceRegistry(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, inferencer, registry).Generate()
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{
		"--replay", "synthetic.json",
		"--audio-in-device", "virtual:input",
		"--audio-out-device", "virtual:output",
	})

	err := cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "provider connection should not be attempted") {
		t.Fatalf("command error = %v, want injected provider connection error", err)
	}
	if inferencer.connects != 1 {
		t.Fatalf("provider connects = %d, want one after device preflight", inferencer.connects)
	}
	if got := registry.Observations(); got.OpenCount != 2 || got.ReleaseCount != 2 {
		t.Fatalf("device observations = %+v, want both devices opened and released", got)
	}
}

func TestSessionCommandRejectsAudioOutputFileAndDeviceConflictBeforeProviderConnect(t *testing.T) {
	inferencer := &countingSessionInferencer{}
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, inferencer).Generate()
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{
		"--replay", "synthetic.json",
		"--audio-out", "response.raw",
		"--audio-out-device", "virtual:output",
	})

	err := cmd.ExecuteContext(context.Background())
	if err == nil || !errors.Is(err, services.ErrSessionAudioOutputConflict) || !errors.Is(err, audio.ErrDeviceSelectionConflict) {
		t.Fatalf("command error = %v, want typed output selection conflict", err)
	}
	if inferencer.connects != 0 {
		t.Fatalf("provider connects = %d, want zero for an early output conflict", inferencer.connects)
	}
}

func TestRunSessionRTCDevicePreflightHappensBeforeProviderConnect(t *testing.T) {
	registry := virtualRTCRegistry(t)
	inferencer := &countingSessionInferencer{}
	err := services.RunSession(context.Background(), io.Discard, services.SessionRunOptions{
		ReplayPath:        "synthetic.json",
		SessionInferencer: inferencer,
		RTCDeviceBinding: services.RTCDeviceBindingRequest{
			Registry:     registry,
			InputDevice:  "virtual:missing",
			InputPresent: true,
		},
	})
	if err == nil || !errors.Is(err, audio.ErrDeviceNotFound) {
		t.Fatalf("session error = %v, want typed preflight not-found error", err)
	}
	if inferencer.connects != 0 {
		t.Fatalf("provider connects = %d, want zero before failed device preflight", inferencer.connects)
	}
	if got := registry.Observations(); got.OpenCount != 0 || got.ReleaseCount != 0 {
		t.Fatalf("device observations = %+v, want no acquisition on failed lookup", got)
	}
}

func virtualRTCRegistry(t *testing.T) *audio.VirtualRegistry {
	t.Helper()
	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("new virtual registry: %v", err)
	}
	return registry
}
