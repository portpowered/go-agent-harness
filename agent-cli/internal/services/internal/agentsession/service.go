// Package agentsession contains the private adapter from the thin session
// service contract to the runtime implementation.
package agentsession

import (
	"context"
	"fmt"
	"io"

	runtime "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentruntime"
	public "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

var _ public.Service = (*Service)(nil)

// Service is the small application service boundary. Runtime construction and
// mode dispatch are owned by the private agentruntime implementation.
type Service struct {
	clock   clock.Source
	runtime runtime.Runtime
}

type Dependencies struct {
	Clock   clock.Source
	Runtime runtime.Runtime
}

func New(deps Dependencies) *Service {
	return &Service{clock: deps.Clock, runtime: deps.Runtime}
}

func (s *Service) Run(ctx context.Context, out io.Writer, request public.Request) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if out == nil {
		return fmt.Errorf("session output is required")
	}
	if s == nil || s.clock == nil {
		return fmt.Errorf("session clock is required")
	}
	if s.runtime == nil {
		return fmt.Errorf("session runtime is required")
	}
	return s.runtime.Run(ctx, out, request)
}
