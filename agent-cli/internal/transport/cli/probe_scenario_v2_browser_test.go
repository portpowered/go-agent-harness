package cli

import (
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
)

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
