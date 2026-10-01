package grok

import (
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/internal/realtime"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// NewDefaultWebSocketDialer returns the live Grok WebSocket dialer.
func NewDefaultWebSocketDialer() transport.Dialer {
	return realtime.NewDialer()
}
