package livehost

import (
	"context"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

// The livehost path remains the compatibility-facing name for the stateless
// transport adapter. Its implementation is shared from the parent transport
// package so no policy or mutable state is retained here.
type TurnAdapter = transport.TurnAdapter
type TurnPublicationOptions = transport.TurnPublicationOptions

func NewTurnAdapter(service sessionturn.Service) TurnAdapter {
	return transport.NewTurnAdapter(service)
}

func SessionTurnBrowserRequest(watch func(context.Context) <-chan webmcp.BrokerEvent, refresh func(context.Context) ([]messages.ToolDefinition, error)) sessionturn.BrowserRequest {
	return transport.SessionTurnBrowserRequest(watch, refresh)
}
