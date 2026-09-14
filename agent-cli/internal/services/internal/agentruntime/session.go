package agentruntime

import (
	"context"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionupdate"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionupdate/wire"
)

// RunSession validates and runs the session inference command surface.
func RunSession(ctx context.Context, out io.Writer, opts SessionRunOptions) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	defer func() { _ = claim.release() }()
	plan, err := planSessionRuntime(opts)
	if err != nil {
		return err
	}
	return plan.run(ctx, out)
}

// Deprecated: the planner keeps this adapter for CLI compatibility while
// session-update ownership lives in go-agent-runtime/services/sessionupdate.
type sessionInstructionsInferencer struct {
	inner   messages.SessionInferencer
	service sessionupdate.Service
	config  sessionupdate.Config
}

var _ messages.SessionInferencer = (*sessionInstructionsInferencer)(nil)

func newSessionInstructionsInferencer(inner messages.SessionInferencer, instructions string, toolDefinitions []messages.ToolDefinition) messages.SessionInferencer {
	return &sessionInstructionsInferencer{
		inner:   inner,
		service: wire.NewService(),
		config: sessionupdate.Config{
			Instructions:    instructions,
			ToolDefinitions: toolDefinitions,
		},
	}
}

func (i *sessionInstructionsInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	inner, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	return &sessionInstructionsSession{
		DecoratedSession: i.service.DecorateSession(ctx, inner, i.config),
		inner:            inner,
	}, nil
}

type sessionInstructionsSession struct {
	sessionupdate.DecoratedSession
	inner messages.Session
}

var _ messages.Session = (*sessionInstructionsSession)(nil)

func (s *sessionInstructionsSession) rtcMedia() (RTCMediaEndpoints, bool) {
	return rtcMediaFromSession(s.inner)
}
