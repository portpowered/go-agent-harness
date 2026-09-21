package agentruntime

import (
	"context"
	"errors"
	"io"

	duration "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	durationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"time"
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

// RunSessionWithTextSeedAndMaxDuration preserves explicit prompt-seed behavior
// while applying duration admission before the seed adapter reaches a provider.
func RunSessionWithTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration, seed SessionTextSeed) (runErr error) {
	service := durationwire.NewService()
	if err := service.ValidateDuration(maxDuration); err != nil {
		return err
	}
	if !seed.Present {
		return RunSessionWithMaxDuration(ctx, out, opts, maxDuration)
	}
	if maxDuration == 0 {
		return RunSessionWithTextSeed(ctx, out, opts, seed)
	}
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() { closeSessionCapabilityIfNeeded(coordinator, &runErr) }()
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
	durationCtx, err := service.PrepareArtifacts(ctx)
	if err != nil {
		return err
	}
	turnRuntime, err := prepareSessionTurnSeed(ctx, &plan, seed)
	if err != nil {
		return err
	}
	var output sessionturn.Output
	if turnRuntime != nil {
		output = turnRuntime.NewOutput(out)
	}
	var admitted duration.AdmissionInferencer
	if plan.inferencer != nil {
		admitted = service.NewAdmissionInferencer(plan.inferencer, service.NewEventAdmission(), make(chan struct{}))
		plan.inferencer = admitted
	}
	var writer io.Writer = out
	if output != nil {
		writer = output
	}
	err = runSessionDurationPlanWithAdmission(durationCtx, writer, plan, maxDuration, nil, admitted)
	if output != nil {
		err = errors.Join(err, output.Err())
	}
	return err
}
