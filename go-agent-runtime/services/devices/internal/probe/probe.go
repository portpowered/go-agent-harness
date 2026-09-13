package deviceprobe

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/participants"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

const (
	deviceProbeDefaultCaptureDuration = 5 * time.Second
	deviceProbeProviderSampleRate     = wavio.Rate24kHz
	deviceProbeInputSampleRate        = audio.SampleRate
	deviceProbeFrameDuration          = 20 * time.Millisecond
	deviceProbeInputFrameSamples      = deviceProbeInputSampleRate / 50
	deviceProbeProviderFrameSamples   = deviceProbeProviderSampleRate / 50
)

type deviceProbeRuntimeOptions = runtimeDevices.ProbeRequest

// deviceProbeInputPlan is the explicit hardware-input contract carried by a
// device-tier scenario. The corpus ID identifies the authored utterance used
// by the corresponding offline lane; Utterance tells the operator exactly
// what must be spoken into the selected physical microphone. The device path
// never injects the corpus WAV into the session.
type deviceProbeInputPlan struct {
	CorpusID  string
	Utterance string
}

func runDeviceProbeScenario(ctx context.Context, scenario probe.Scenario, availability devicegw.DeviceProbeAvailability, registry devicegw.DeviceRegistry, opts deviceProbeRuntimeOptions, sessionFactory runtimeDevices.ProbeSessionFactory) (observation probe.ObservationSnapshot, runErr error) {
	if ctx == nil {
		return observation, errors.New("device probe context is required")
	}
	inputPlan, err := scenarioDeviceProbeInput(scenario)
	if err != nil {
		return observation, err
	}
	if err := validateProbeAvailability(availability); err != nil {
		return observation, err
	}
	inputDevice := selectLiveDeviceProbeDevice(registry, availability.InputDevices, devicegw.DirectionInput)
	outputDevice := selectLiveDeviceProbeDevice(registry, availability.OutputDevices, devicegw.DirectionOutput)
	resources, err := openLiveDeviceProbeResources(registry, inputDevice, outputDevice)
	if err != nil {
		return observation, err
	}
	defer func() { runErr = errors.Join(runErr, resources.Close()) }()
	_, inferencer, err := resolveProbeSession(inputPlan, scenario, opts, sessionFactory)
	if err != nil {
		return observation, err
	}
	runner, bridge, runnerCancel, stopSession, runnerFinished := startLiveDeviceProbeSession(ctx, inferencer, resources)
	defer stopSession()
	if err := bridge.waitOpened(ctx); err != nil {
		return observation, err
	}
	if err := captureAndValidateProbeInput(ctx, opts.CaptureTime, resources, runner); err != nil {
		return observation, err
	}
	select {
	case runner.UserEventInbox <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd}:
	case <-ctx.Done():
		return observation, ctx.Err()
	}
	if err := bridge.waitResponse(ctx); err != nil {
		return observation, err
	}
	if err := waitProbeRunnerStop(runnerCancel, runnerFinished); err != nil {
		return observation, errors.New("session runner did not stop after response completion")
	}
	if err := bridge.errorValue(nil); err != nil {
		return observation, err
	}
	return bridge.snapshot(), nil
}

func validateProbeAvailability(availability devicegw.DeviceProbeAvailability) error {
	if availability.Status != devicegw.DeviceProbeStatusReady {
		return fmt.Errorf("device probe cannot run with availability status %q", availability.Status)
	}
	if len(availability.InputDevices) == 0 || len(availability.OutputDevices) == 0 {
		return fmt.Errorf("device probe ready snapshot has no input/output device")
	}
	return nil
}

func resolveProbeSession(input deviceProbeInputPlan, scenario probe.Scenario, opts deviceProbeRuntimeOptions, sessionFactory runtimeDevices.ProbeSessionFactory) (string, messages.SessionInferencer, error) {
	instructions := opts.Instructions
	if strings.TrimSpace(instructions) == "" {
		instructions = deviceProbeInstructionsForInput(input, scenarioDeviceProbeTranscript(scenario))
	}
	if opts.InstructionsObserved != nil {
		opts.InstructionsObserved(instructions)
	}
	if opts.SessionInferencer != nil {
		return instructions, opts.SessionInferencer, nil
	}
	if sessionFactory == nil {
		return instructions, nil, errors.New("device probe session factory is required")
	}
	inferencer, model, err := sessionFactory(opts, instructions)
	if err != nil {
		return instructions, nil, fmt.Errorf("create live realtime session (%s): %w", model, err)
	}
	return instructions, inferencer, nil
}

func captureAndValidateProbeInput(ctx context.Context, captureDuration time.Duration, resources liveDeviceProbeResources, runner *participants.ModelRunner) error {
	if captureDuration <= 0 {
		captureDuration = deviceProbeDefaultCaptureDuration
	}
	captureContext, captureCancel := context.WithTimeout(ctx, captureDuration)
	inputFrames, inputRMS, err := captureLiveDeviceProbeInput(captureContext, resources.source, resources.inputLink, runner)
	captureCancel()
	if err != nil {
		return err
	}
	if inputFrames == 0 {
		return fmt.Errorf("selected microphone produced no complete 20 ms frames")
	}
	if inputRMS <= audio.DefaultVADConfig.EnergyThreshold {
		return fmt.Errorf("selected microphone RMS = %.2f, want > %.2f (silence threshold)", inputRMS, audio.DefaultVADConfig.EnergyThreshold)
	}
	return nil
}

func waitProbeRunnerStop(cancel context.CancelFunc, finished <-chan struct{}) error {
	cancel()
	select {
	case <-finished:
		return nil
	case <-time.After(time.Second):
		return context.DeadlineExceeded
	}
}

func selectLiveDeviceProbeDevice(registry devicegw.DeviceRegistry, candidates []devicegw.Device, direction devicegw.Direction) devicegw.Device {
	if registry != nil {
		if defaultDevice, err := registry.Default(direction); err == nil {
			for _, candidate := range candidates {
				if candidate.ID == defaultDevice.ID {
					return candidate
				}
			}
		}
	}
	return candidates[0]
}

func closeDeviceProbeResource(name string, close func() error) error {
	if err := close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	return nil
}
