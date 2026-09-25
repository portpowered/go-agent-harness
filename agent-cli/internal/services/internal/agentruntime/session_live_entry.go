package agentruntime

import (
	"context"
	"io"
	"time"

	sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

// SessionTextSeed carries the value and presence of an explicit --prompt.
type SessionTextSeed = sessionturn.Seed

// RunSession validates and runs the session inference command surface.
func RunSession(ctx context.Context, out io.Writer, opts SessionRunOptions) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() { closeSessionCapabilityIfNeeded(coordinator, &runErr) }()
	//nolint:contextcheck // Option validation has no context-aware seam; planning below honors ctx.
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	plan, err := planSessionRuntimeContext(ctx, opts)
	if err != nil {
		return err
	}
	return plan.run(ctx, out)
}

// RunSessionWithInstructions resolves the system-prompt contract and applies
// it to the realtime session before the first user turn. A pure replay has no
// provider session to configure and keeps its captured outbound sequence.
func RunSessionWithInstructions(ctx context.Context, out io.Writer, opts SessionRunOptions, systemPrompt string) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() { closeSessionCapabilityIfNeeded(coordinator, &runErr) }()
	if opts.ReplayPath != "" && opts.SessionInferencer == nil {
		return RunSession(ctx, out, opts)
	}
	plan, err := planSessionWithInstructions(ctx, opts, systemPrompt)
	if err != nil {
		return err
	}
	return plan.run(ctx, out)
}

// RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration preserves the
// session command's text-seed and duration behavior while carrying the
// selected workspace instructions into provider construction.
func RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, audioPath string, maxDuration time.Duration, seed SessionTextSeed, systemPrompt string) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() { closeSessionCapabilityIfNeeded(coordinator, &runErr) }()
	if err := sessioncontract.ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	if audioPath != "" {
		return ErrLegacyAudioRuntimeRetired
	}
	if opts.ReplayPath != "" && opts.SessionInferencer == nil {
		return RunSessionWithTextSeedAndMaxDuration(ctx, out, opts, maxDuration, seed)
	}
	opts = withSessionTextSeed(opts, seed)
	plan, err := planSessionWithInstructions(ctx, opts, systemPrompt)
	if err != nil {
		return err
	}
	return runSessionPlanWithSeed(ctx, out, plan, maxDuration, seed, "")
}

// RunSessionWithMaxDuration runs a session with an optional graceful bound.
func RunSessionWithMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration) error {
	return RunSessionWithMaxDurationClock(ctx, out, opts, maxDuration, nil)
}

// RunSessionWithMaxDurationClock is the deterministic-clock duration seam.
func RunSessionWithMaxDurationClock(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration, clock sessionduration.TimerScheduler) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() { closeSessionCapabilityIfNeeded(coordinator, &runErr) }()
	if err := sessioncontract.ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	plan, err := planBoundedSession(ctx, opts)
	if err != nil {
		return err
	}
	return runSessionPlanWithDuration(ctx, out, plan, maxDuration, clock, nil)
}

// RunSessionWithTextSeed runs the explicit text seed when present, otherwise
// the positional prompt.
func RunSessionWithTextSeed(ctx context.Context, out io.Writer, opts SessionRunOptions, seed SessionTextSeed) error {
	return RunSessionWithTextSeedAndMaxDuration(ctx, out, opts, 0, seed)
}

// RunSessionWithTextSeedAndMaxDuration applies the duration admission boundary
// around the seed wrapper; a zero duration runs unbounded.
func RunSessionWithTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration, seed SessionTextSeed) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() { closeSessionCapabilityIfNeeded(coordinator, &runErr) }()
	if err := sessioncontract.ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	if !seed.Present {
		if maxDuration == 0 {
			return RunSession(ctx, out, opts)
		}
		return RunSessionWithMaxDuration(ctx, out, opts, maxDuration)
	}
	plan, err := planBoundedSession(ctx, withSessionTextSeed(opts, seed))
	if err != nil {
		return err
	}
	return runSessionPlanWithSeed(ctx, out, plan, maxDuration, seed, "")
}

func withSessionTextSeed(opts SessionRunOptions, seed SessionTextSeed) SessionRunOptions {
	if seed.Present {
		opts.Prompt, opts.PromptProvided = seed.Value, true
	}
	return opts
}

// planSessionWithInstructions validates, resolves instructions, and plans.
func planSessionWithInstructions(ctx context.Context, opts SessionRunOptions, systemPrompt string) (sessionRuntimePlan, error) {
	//nolint:contextcheck // Option validation has no context-aware seam; resolution below honors ctx.
	if err := validateSessionRunOptions(opts); err != nil {
		return sessionRuntimePlan{}, err
	}
	instructions, err := resolveSessionInstructions(ctx, opts, systemPrompt)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	return planSessionWithResolvedInstructionsContext(ctx, opts, instructions)
}

// planBoundedSession validates and plans a session for a duration entrypoint.
func planBoundedSession(ctx context.Context, opts SessionRunOptions) (sessionRuntimePlan, error) {
	//nolint:contextcheck // Option validation has no context-aware seam; planning below honors ctx.
	if err := validateSessionRunOptions(opts); err != nil {
		return sessionRuntimePlan{}, err
	}
	return planSessionRuntimeContext(ctx, opts)
}

// SessionImageRunOptions adds initial images to a session run.
type SessionImageRunOptions struct {
	SessionRunOptions
	ImagePaths   []string
	AudioOutPath string
	MaxDuration  time.Duration
	TextSeed     SessionTextSeed
	SystemPrompt string
}

// RunSessionWithImages validates the images before any provider connection,
// stages them for read_image, and sends them with the first user turn.
func RunSessionWithImages(ctx context.Context, out io.Writer, opts SessionImageRunOptions) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts.SessionRunOptions, coordinator = prepareSessionCapabilityCoordinator(opts.SessionRunOptions)
	defer func() { closeSessionCapabilityIfNeeded(coordinator, &runErr) }()
	paths := append([]string(nil), opts.ImagePaths...)
	if len(paths) == 0 {
		return RunSession(ctx, out, opts.SessionRunOptions)
	}
	if err := sessioncontract.ValidateSessionMaxDuration(opts.MaxDuration); err != nil {
		return err
	}
	if opts.AudioOutPath != "" {
		return ErrLegacyAudioRuntimeRetired
	}
	//nolint:contextcheck // Option validation and image admission have no context-aware seam.
	parts, err := prepareSessionImages(&opts.SessionRunOptions, paths)
	if err != nil {
		return err
	}
	var cleanup func()
	opts.SessionRunOptions, cleanup, err = prepareSessionImageToolAccess(ctx, withSessionTextSeed(opts.SessionRunOptions, opts.TextSeed), paths, parts)
	if err != nil {
		return err
	}
	defer cleanup()
	plan, wirePrompt, err := planSessionImageRuntime(ctx, opts.SessionRunOptions, parts, opts.TextSeed, opts.SystemPrompt, false)
	if err != nil {
		return err
	}
	return runSessionPlanWithSeed(ctx, out, plan, opts.MaxDuration, opts.TextSeed, wirePrompt)
}

// prepareSessionImages resolves the capability snapshot once for the session
// and validates every image before provider construction.
func prepareSessionImages(opts *SessionRunOptions, paths []string) ([]messages.ImagePart, error) {
	if err := validateSessionRunOptions(*opts); err != nil {
		return nil, err
	}
	capabilities, err := resolveSessionImageCapabilities(*opts)
	if err != nil {
		return nil, err
	}
	opts.sessionImageCapabilities = &capabilities
	return prepareSessionImageParts(paths, capabilities)
}

func planSessionImageRuntime(ctx context.Context, opts SessionRunOptions, parts []messages.ImagePart, seed SessionTextSeed, systemPrompt string, deferResponse bool) (sessionRuntimePlan, string, error) {
	var (
		plan sessionRuntimePlan
		err  error
	)
	if opts.ReplayPath != "" {
		plan, err = planSessionRuntimeContext(ctx, opts)
	} else {
		var instructions string
		instructions, err = resolveSessionInstructions(ctx, opts, systemPrompt)
		if err == nil {
			plan, err = planSessionWithResolvedInstructionsContext(ctx, opts, instructions)
		}
	}
	if err != nil {
		return sessionRuntimePlan{}, "", err
	}
	attachment, err := newTurnService().AttachImages(sessionturn.ImageAttachRequest{
		Inferencer: plan.inferencer, Parts: parts, Seed: seed, Prompt: opts.Prompt, DeferResponse: deferResponse,
	})
	if err != nil {
		return sessionRuntimePlan{}, "", err
	}
	plan.inferencer, plan.loop.awaitFirstTurn = attachment.Inferencer, attachment.FirstTurn
	if attachment.Prompt != "" {
		plan.loop.Prompt = attachment.Prompt
	}
	return plan, attachment.WirePrompt, nil
}
