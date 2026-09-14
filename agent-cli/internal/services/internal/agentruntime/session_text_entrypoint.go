package agentruntime

import (
	"context"
	"errors"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

// SessionTextSeed is the CLI command's value view of the service-owned
// session-turn seed contract.
type SessionTextSeed = sessionturn.Seed

// RunSessionWithTextSeed runs a session using the explicit text seed when it
// is present, otherwise preserving the existing positional Prompt behavior.
func RunSessionWithTextSeed(ctx context.Context, out io.Writer, opts SessionRunOptions, seed SessionTextSeed) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() { closeSessionCapabilityIfNeeded(coordinator, &runErr) }()

	if !seed.Present {
		return RunSession(ctx, out, opts)
	}

	opts.Prompt = seed.Value
	opts.PromptProvided = true
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	defer func() { releaseSessionClaim(claim, &runErr) }()
	plan, err := planSessionRuntimeWithContext(ctx, opts)
	if err != nil {
		return err
	}

	turnRuntime, err := prepareSessionTurnSeed(ctx, &plan, seed)
	if err != nil {
		return err
	}
	if turnRuntime == nil {
		return plan.run(ctx, out)
	}
	output := turnRuntime.NewOutput(out)
	return errors.Join(plan.run(ctx, output), output.Err())
}
