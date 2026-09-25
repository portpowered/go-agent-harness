package sessionbroker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	runtime "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation"
)

func TestServiceCapabilityExposesSessionLifecycle(t *testing.T) {
	if capability := ServiceCapability(nil); capability.Broker != nil || capability.Status != nil {
		t.Fatalf("nil broker capability = %+v, want empty", capability)
	}
	delegate := &capabilityBroker{events: make(chan webmcp.BrowserEvent, 1)}
	broker := readyBroker(delegate, func(context.Context) error { return nil })
	capability := ServiceCapability(broker)
	if err := capability.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if status := capability.Status(); status.State != StateReady {
		t.Fatalf("status = %+v, want ready", status)
	}
	delegate.events <- webmcp.BrowserEvent{Type: "tool_invoked", ToolName: "read_state"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if event := <-capability.BrowserEventWatch(ctx); event.ToolName != "read_state" {
		t.Fatalf("projected event = %+v", event)
	}
	if err := capability.Close(); err != nil || delegate.closeCalls != 1 {
		t.Fatalf("close = %v calls=%d", err, delegate.closeCalls)
	}

	plain := ServiceCapability(&baseBroker{})
	if plain.Initialize != nil || plain.BrowserEventWatch != nil || plain.Status().BrowserCapabilityState != webmcp.BrowserCapabilityInitializing {
		t.Fatalf("plain broker capability = %+v", plain)
	}
}

type resolverFunc func(*config.Config) (serviceTools.Capabilities, error)

func (f resolverFunc) Resolve(cfg *config.Config) (serviceTools.Capabilities, error) { return f(cfg) }

func TestFactoryRoundTripsServiceCapabilities(t *testing.T) {
	if _, err := FactoryFromService(nil)(&config.Config{}); err == nil {
		t.Fatal("missing service must fail")
	}
	resolveErr := errors.New("resolve failed")
	if _, err := FactoryFromService(resolverFunc(func(*config.Config) (serviceTools.Capabilities, error) {
		return serviceTools.Capabilities{}, resolveErr
	}))(&config.Config{}); !errors.Is(err, resolveErr) {
		t.Fatalf("resolve error = %v", err)
	}

	source := make(chan runtime.BrowserEvent, 1)
	source <- runtime.BrowserEvent{Type: "catalog_changed", Tools: []runtime.BrowserToolDescriptor{{Name: "read_state", InputSchema: json.RawMessage(`{}`)}}}
	service := resolverFunc(func(*config.Config) (serviceTools.Capabilities, error) {
		return serviceTools.Capabilities{
			BrowserCapabilityState: webmcp.BrowserCapabilitySelected,
			Status:                 func() Status { return Status{State: StateReady} },
			BrowserEventWatch:      func(context.Context) <-chan runtime.BrowserEvent { return source },
		}, nil
	})
	capabilities, err := FactoryFromService(service)(&config.Config{})
	if err != nil || capabilities.BrowserCapabilityState != webmcp.BrowserCapabilitySelected || capabilities.Status().State != StateReady {
		t.Fatalf("session capabilities = %+v err=%v", capabilities, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if event := <-capabilities.BrowserEventWatch(ctx); len(event.Tools) != 1 || event.Tools[0].Name != "read_state" {
		t.Fatalf("webmcp event = %+v", event)
	}

	resolved, err := ToolCapabilitiesFactory(FactoryFromService(service)).Resolve(&config.Config{})
	if err != nil || resolved.Status().State != StateReady {
		t.Fatalf("resolved = %+v err=%v", resolved, err)
	}
}

func TestFactoryResolveHandlesMissingHooks(t *testing.T) {
	var missing ToolCapabilitiesFactory
	if _, err := missing.Resolve(&config.Config{}); err == nil {
		t.Fatal("nil factory must fail")
	}
	factoryErr := errors.New("factory failed")
	if _, err := ToolCapabilitiesFactory(func(*config.Config) (ToolCapabilities, error) { return ToolCapabilities{}, factoryErr }).Resolve(&config.Config{}); !errors.Is(err, factoryErr) {
		t.Fatalf("factory error = %v", err)
	}
	resolved, err := ToolCapabilitiesFactory(func(*config.Config) (ToolCapabilities, error) { return ToolCapabilities{}, nil }).Resolve(&config.Config{})
	if err != nil || resolved.Status() != (Status{}) || resolved.BrowserEventWatch(context.Background()) != nil {
		t.Fatalf("resolved = %+v err=%v, want empty status and no event stream", resolved, err)
	}
	empty := fromService(serviceTools.Capabilities{})
	if empty.BrowserEventWatch(context.Background()) != nil || empty.Status != nil {
		t.Fatal("service capabilities without hooks must stay inert")
	}
}

func TestEventProjectionStopsWithContextOrSource(t *testing.T) {
	if ToRuntimeEvents(context.Background(), nil) != nil || ToWebMCPEvents(context.Background(), nil) != nil {
		t.Fatal("a nil source must yield a nil stream")
	}
	source := make(chan webmcp.BrowserEvent)
	close(source)
	if _, open := <-ToRuntimeEvents(context.Background(), source); open {
		t.Fatal("a closed source must close the projection")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pending := make(chan webmcp.BrowserEvent)
	if _, open := <-ToRuntimeEvents(ctx, pending); open {
		t.Fatal("a canceled context must close the projection")
	}
	buffered := make(chan webmcp.BrowserEvent, 1)
	buffered <- webmcp.BrowserEvent{Tools: []webmcp.ToolDescriptor{{Name: "read_state"}}}
	blocked, stop := context.WithCancel(context.Background())
	projected := ToRuntimeEvents(blocked, buffered)
	stop()
	for range projected {
	}
}
