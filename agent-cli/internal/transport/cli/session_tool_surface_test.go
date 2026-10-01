package cli

import (
	"context"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type resolvedSessionToolSurface struct {
	initializeErr     error
	executor          messages.ToolExecutor
	definitions       []messages.ToolDefinition
	base              []messages.ToolDefinition
	browserState      webmcp.BrowserCapabilityState
	refresh           func(context.Context) ([]messages.ToolDefinition, error)
	browserWatch      func(context.Context) <-chan webmcp.BrokerEvent
	browserEventWatch func(context.Context) <-chan webmcp.BrowserEvent
	capabilityClose   func() error
}

func resolveSessionToolSurface(ctx context.Context, capabilities SessionToolCapabilities) resolvedSessionToolSurface {
	var initializeErr error
	if capabilities.Initialize != nil {
		// Initialization is deliberately completed before the provider receives
		// the executor. The command checks initializeErr before provider startup.
		initializeErr = capabilities.Initialize(ctx)
	}
	result := resolvedSessionToolSurface{
		initializeErr:     initializeErr,
		executor:          capabilities.Executor,
		definitions:       append([]messages.ToolDefinition(nil), capabilities.Definitions...),
		base:              append([]messages.ToolDefinition(nil), capabilities.Definitions...),
		browserState:      capabilities.BrowserCapabilityState,
		refresh:           capabilities.RefreshDefinitionsWithError,
		browserWatch:      capabilities.BrowserWatch,
		browserEventWatch: capabilities.BrowserEventWatch,
	}
	if capabilities.Close != nil {
		// Ownership transfers to the command as soon as initialization has run,
		// including its failure path.
		result.capabilityClose = capabilities.Close
	}
	if initializeErr != nil {
		return result
	}
	if capabilities.Status != nil {
		status := capabilities.Status()
		if status.BrowserCapabilityState != "" {
			result.browserState = status.BrowserCapabilityState
		}
	}
	if result.refresh == nil && capabilities.RefreshDefinitions != nil {
		result.refresh = func(ctx context.Context) ([]messages.ToolDefinition, error) {
			return capabilities.RefreshDefinitions(ctx), nil
		}
	}
	if capabilities.RefreshDefinitions != nil {
		result.definitions = capabilities.RefreshDefinitions(ctx)
	}
	return result
}

func ambiguousSessionLaneTarget(target webmcp.Target, toolCount int) discovery.Target {
	return discovery.Target{
		BrowserID:             string(target.BrowserID),
		ID:                    string(target.ID),
		Type:                  target.Type,
		Title:                 target.Title,
		URL:                   target.URL,
		Origin:                target.Origin,
		Generation:            target.Generation,
		WebSocketPresent:      true,
		WebMCP:                true,
		WebMCPKnown:           true,
		WebMCPDomainSupported: true,
		WebMCPDomainKnown:     true,
		PageToolsReady:        true,
		PageToolsKnown:        true,
		ToolCount:             toolCount,
		ToolCountKnown:        true,
		Eligible:              true,
	}
}
