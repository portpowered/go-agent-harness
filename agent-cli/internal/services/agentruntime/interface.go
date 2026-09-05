// Package agentruntime defines the service-facing session runtime seam.
// Concrete provider gateways and runtime factories stay behind the services
// composition root.
package agentruntime

import (
	"context"
	"io"

	agentsession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
)

// Runtime executes one admitted session request.
type Runtime interface {
	Run(context.Context, io.Writer, agentsession.Request) error
}

// Service is the concise provider spelling.
type Service = Runtime
