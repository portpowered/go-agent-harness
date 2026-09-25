package cli

import (
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/scenariov2"
)

func TestProbeScenarioV2BrowserExecutorDefaultsToHermetic(t *testing.T) {
	command := NewProbeRunCommandWithDeviceService(newDevicesTestService(), nil, nil, newReplayRuntimeServiceForTest())
	if command.BrowserExecutorMode != ProbeScenarioV2BrowserExecutorHermetic {
		t.Fatalf("default browser executor = %q, want hermetic", command.BrowserExecutorMode)
	}
	options, err := command.probeScenarioV2BrowserExecutorOptions(command.Generate())
	if err != nil {
		t.Fatalf("resolve default browser executor options: %v", err)
	}
	if options.Mode != scenariov2.BrowserExecutorHermetic || options.ConfigError != nil {
		t.Fatalf("default browser executor options = %+v, want hermetic without config", options)
	}
}

func TestProbeRunCommandBrowserExecutorFlagIsExplicitAndTyped(t *testing.T) {
	command := NewProbeRunCommandWithDeviceService(newDevicesTestService(), nil, nil, newReplayRuntimeServiceForTest())
	generated := command.Generate()
	if err := generated.Flags().Set("browser-executor", "real"); err != nil {
		t.Fatalf("set real browser executor: %v", err)
	}
	if command.BrowserExecutorMode != ProbeScenarioV2BrowserExecutorReal {
		t.Fatalf("selected browser executor = %q, want real", command.BrowserExecutorMode)
	}
	if err := generated.Flags().Set("browser-executor", "network"); err == nil {
		t.Fatal("invalid browser executor mode was accepted")
	}
}

func TestProbeScenarioV2RealRuntimeFactoryClosesDoctorRuntime(t *testing.T) {
	if probeScenarioV2RealRuntimeFactory(nil) != nil {
		t.Fatal("nil doctor factory adapted to a non-nil real runtime factory")
	}
	closeCalls := 0
	factoryErr := errors.New("endpoint unreachable")
	adapted := probeScenarioV2RealRuntimeFactory(func(config.BrowserConfig) (WebMCPDoctorRuntime, error) {
		return WebMCPDoctorRuntime{Close: func() error {
			closeCalls++
			return nil
		}}, factoryErr
	})
	runtime, err := adapted(config.BrowserConfig{})
	if !errors.Is(err, factoryErr) {
		t.Fatalf("adapted factory error = %v, want %v", err, factoryErr)
	}
	if runtime.Broker != nil {
		t.Fatalf("adapted runtime broker = %v, want nil", runtime.Broker)
	}
	if closeErr := runtime.Close(); closeErr != nil || closeCalls != 1 {
		t.Fatalf("adapted close = %v after %d calls, want doctor runtime closed once", closeErr, closeCalls)
	}
}
