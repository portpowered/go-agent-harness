package agentruntime

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	durationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/wire"
)

type SessionDurationArtifactLifecycle = sessionduration.ArtifactLifecycle

func sessionDurationArtifactsFromContext(ctx context.Context) SessionDurationArtifactLifecycle {
	return durationwire.NewService().ArtifactsFromContext(ctx)
}

func withSessionDurationTerminalRecorder(ctx context.Context, recorder sessionduration.TerminalRecorder) context.Context {
	return durationwire.NewService().WithTerminalRecorder(ctx, recorder)
}

func prepareSessionDurationArtifacts(ctx context.Context) (context.Context, error) {
	return durationwire.NewService().PrepareArtifacts(ctx)
}

func finalizeSessionDurationArtifacts(artifacts SessionDurationArtifactLifecycle) error {
	return durationwire.NewService().FinalizeArtifacts(artifacts)
}
