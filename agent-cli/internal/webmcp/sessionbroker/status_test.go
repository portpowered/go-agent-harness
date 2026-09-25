package sessionbroker

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

func TestInitialBrowserState(t *testing.T) {
	if got := initialBrowserState(nil); got != webmcp.BrowserCapabilityUnavailable {
		t.Fatalf("nil broker state = %q", got)
	}
	nested := readyBroker(&baseBroker{}, nil)
	if got := initialBrowserState(nested); got != webmcp.BrowserCapabilityInitializing {
		t.Fatalf("nested initializer state = %q", got)
	}
	if got := initialBrowserState(&baseBroker{}); got != webmcp.BrowserCapabilityConnectedUnselected {
		t.Fatalf("unselected state = %q", got)
	}
	disconnected := &discovery.DiscoveryError{Code: discovery.CodeBrowserDisconnected, Message: "gone"}
	if got := initialBrowserState(&baseBroker{selectedErr: disconnected}); got != webmcp.BrowserCapabilityDisconnected {
		t.Fatalf("disconnected discovery state = %q", got)
	}
	if got := browserStateForError(errors.New("other")); got != webmcp.BrowserCapabilityUnavailable {
		t.Fatalf("generic error state = %q", got)
	}
}

func TestBrokerIgnoresEmptyBrowserStateUpdates(t *testing.T) {
	broker := readyBroker(&baseBroker{}, func(context.Context) error { return nil })
	broker.setBrowserCapabilityState("")
	var nilBroker *Broker
	nilBroker.setBrowserCapabilityState(webmcp.BrowserCapabilitySelected)
	if got := broker.SessionCapabilityStatus().BrowserCapabilityState; got != webmcp.BrowserCapabilityInitializing {
		t.Fatalf("browser state = %q, want unchanged initializing", got)
	}
	zero := &Broker{Broker: &baseBroker{}}
	if status := zero.SessionCapabilityStatus(); status.State != StateInitializing {
		t.Fatalf("zero-value broker status = %+v, want initializing", status)
	}
	if err := zero.InitializeSession(context.Background()); err != nil {
		t.Fatalf("zero-value broker without bootstrap: %v", err)
	}
}
