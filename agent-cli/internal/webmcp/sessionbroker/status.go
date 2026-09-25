package sessionbroker

import (
	"context"
	"errors"

	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

// State is the lifecycle state of a request-scoped session capability.
// Browser capabilities begin initializing, then become ready or retain a
// classified failure for every subsequent tool call. It is the tool service
// contract's state, so the session and the service share one definition.
type State = serviceTools.CapabilityState

const (
	StateInitializing = serviceTools.CapabilityInitializing
	StateReady        = serviceTools.CapabilityReady
	StateFailed       = serviceTools.CapabilityFailed
)

// Status is a read-only snapshot of capability setup.
type Status = serviceTools.CapabilityStatus

// Initializer is the optional lifecycle seam exposed by a browser-backed
// capability set. It is deliberately separate from the frozen
// messages.ToolExecutor interface.
type Initializer interface {
	InitializeSession(context.Context) error
	SessionCapabilityStatus() Status
}

// initialBrowserState derives the browser capability state of a delegate
// after a successful bootstrap that did not record a state of its own.
func initialBrowserState(broker webmcp.Broker) webmcp.BrowserCapabilityState {
	if broker == nil {
		return webmcp.BrowserCapabilityUnavailable
	}
	if _, initializer := broker.(Initializer); initializer {
		return webmcp.BrowserCapabilityInitializing
	}
	selected, err := broker.Selected(context.Background())
	if err == nil && selected.Connected && selected.Key.BrowserID != "" && selected.Key.TargetID != "" {
		return webmcp.BrowserCapabilitySelected
	}
	if err != nil {
		return browserStateForError(err)
	}
	return webmcp.BrowserCapabilityConnectedUnselected
}

// browserStateForError maps a bootstrap failure to the browser state the
// model is grounded with: a disconnect is distinguishable from any other
// unavailability.
func browserStateForError(err error) webmcp.BrowserCapabilityState {
	var classified *webmcp.ClassifiedError
	if errors.As(err, &classified) && classified != nil && classified.Code == webmcp.ErrorBrowserDisconnected {
		return webmcp.BrowserCapabilityDisconnected
	}
	var discoveryErr *discovery.DiscoveryError
	if errors.As(err, &discoveryErr) && discoveryErr != nil && discoveryErr.Code == discovery.CodeBrowserDisconnected {
		return webmcp.BrowserCapabilityDisconnected
	}
	return webmcp.BrowserCapabilityUnavailable
}
