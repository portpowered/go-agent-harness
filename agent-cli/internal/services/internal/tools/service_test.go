package tools

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
)

func browserEnabledConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Browser = config.DefaultBrowserConfig()
	cfg.Browser.Tools.Enabled = true
	return cfg
}

func TestServiceResolvesWithoutBrowserCapability(t *testing.T) {
	service := New(nil, nil, nil, nil)
	cfg := &config.Config{}
	cfg.Browser = config.DefaultBrowserConfig()
	capabilities, err := service.Resolve(cfg)
	if err != nil {
		t.Fatalf("resolve static capabilities: %v", err)
	}
	if capabilities.BrowserCapabilityState != "disabled" {
		t.Fatalf("browser capability state = %q, want disabled", capabilities.BrowserCapabilityState)
	}
	if capabilities.Executor == nil {
		t.Fatal("static capability executor is nil")
	}
	var _ serviceTools.Service = service
}

func TestServiceClosesPartiallyConstructedBrowserWhenFactoryFails(t *testing.T) {
	wantErr := errors.New("broker startup failed")
	var closes atomic.Int32
	service := New(nil, func(config.BrowserConfig, string) (serviceTools.BrowserCapability, error) {
		return serviceTools.BrowserCapability{Close: func() error {
			closes.Add(1)
			return nil
		}}, wantErr
	}, nil, nil)

	_, err := service.Resolve(browserEnabledConfig())
	if !errors.Is(err, wantErr) {
		t.Fatalf("resolve error = %v, want wrapped factory error", err)
	}
	if got := closes.Load(); got != 1 {
		t.Fatalf("close calls = %d, want one for partially constructed browser", got)
	}
}

func TestServiceClosesBrowserWhenFactoryReturnsNilBroker(t *testing.T) {
	var closes atomic.Int32
	service := New(nil, func(config.BrowserConfig, string) (serviceTools.BrowserCapability, error) {
		return serviceTools.BrowserCapability{Close: func() error {
			closes.Add(1)
			return nil
		}}, nil
	}, nil, nil)

	_, err := service.Resolve(browserEnabledConfig())
	if err == nil {
		t.Fatal("resolve error = nil, want nil broker error")
	}
	if got := closes.Load(); got != 1 {
		t.Fatalf("close calls = %d, want one for nil broker", got)
	}
}
