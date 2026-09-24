package agentruntime

import sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/skills"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	runtimeSessionWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
)

// RunSessionWithInstructions resolves the ask-path system-prompt contract and
// applies the result to the realtime session before the first user turn.
//
// Session instructions intentionally disable dynamic system information. A
// realtime session's instructions are the configured workspace or explicit
// prompt content, while the provider/session runtime continues to own its
// model configuration.
func RunSessionWithInstructions(ctx context.Context, out io.Writer, opts SessionRunOptions, systemPrompt string) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	// A pure replay has no provider session to configure. Preserve its captured
	// outbound sequence; injected replay sessions remain configurable for tests
	// and caller-owned session seams.
	if opts.ReplayPath != "" && opts.SessionInferencer == nil {
		return RunSession(ctx, out, opts)
	}
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	defer func() { _ = claim.release() }()

	instructions, err := resolveSessionInstructionsContext(ctx, opts, systemPrompt)
	if err != nil {
		return err
	}

	plan, err := planSessionWithResolvedInstructionsContext(ctx, opts, instructions)
	if err != nil {
		return err
	}
	return plan.run(ctx, out)
}

// RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration preserves the
// session command's audio, explicit text-seed, and duration behavior while
// carrying the selected or default workspace instructions into provider
// construction.
func RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, audioPath string, maxDuration time.Duration, seed SessionTextSeed, systemPrompt string) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if err := sessioncontract.ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	if audioPath != "" {
		return ErrLegacyAudioRuntimeRetired
	}
	if opts.ReplayPath != "" && opts.SessionInferencer == nil && maxDuration == 0 {
		return RunSessionWithTextSeed(ctx, out, opts, seed)
	}
	if seed.Present {
		opts.Prompt = seed.Value
		opts.PromptProvided = true
	}
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	defer func() { _ = claim.release() }()
	var plan sessionRuntimePlan
	if opts.ReplayPath != "" && opts.SessionInferencer == nil {
		plan, err = planSessionRuntimeWithContext(ctx, opts)
	} else {
		var instructions string
		instructions, err = resolveSessionInstructionsContext(ctx, opts, systemPrompt)
		if err == nil {
			plan, err = planSessionWithResolvedInstructionsContext(ctx, opts, instructions)
		}
	}
	if err != nil {
		return err
	}
	return runSessionPlanWithDuration(ctx, out, plan, maxDuration, seed, "")
}

func runSessionPlanWithDuration(ctx context.Context, out io.Writer, plan sessionRuntimePlan, maxDuration time.Duration, seed SessionTextSeed, wirePrompt string) error {
	plan.loop.MaxDuration = maxDuration
	if err := prepareSessionDurationClock(&plan, maxDuration); err != nil {
		return err
	}
	if !seed.Present {
		return plan.run(ctx, out)
	}
	if wirePrompt == "" {
		wirePrompt = nextSessionTextWirePrompt()
	}
	plan.loop.Prompt = wirePrompt
	if plan.inferencer != nil {
		plan.inferencer = &sessionTextSeedInferencer{inner: plan.inferencer, wirePrompt: wirePrompt, value: seed.Value}
	}
	output := &sessionTextOutput{writer: out}
	return errors.Join(plan.run(ctx, output), output.errorValue())
}

func prepareSessionDurationClock(plan *sessionRuntimePlan, maxDuration time.Duration) error {
	if maxDuration <= 0 || plan.inferencer == nil {
		return nil
	}
	if plan.loop.audioService == nil {
		return errors.New("audio service is required for session duration timing")
	}
	clock, err := plan.loop.audioService.NewClock(plan.loop.clockSource)
	if err != nil {
		return err
	}
	plan.loop.durationClock = clock
	return nil
}

// resolveSessionInstructions is a compatibility adapter around the reusable
// session instruction service. Filesystem policy normalization remains at the
// CLI host edge; prompt selection, skills ordering, scope formatting and all
// model-facing policy decisions live behind the runtime contract.
func resolveSessionInstructions(opts SessionRunOptions, systemPrompt string) (string, error) {
	return resolveSessionInstructionsContext(context.Background(), opts, systemPrompt)
}

func resolveSessionInstructionsContext(ctx context.Context, opts SessionRunOptions, systemPrompt string) (string, error) {
	request, err := newSessionInstructionRequest(opts, systemPrompt)
	if err != nil {
		return "", err
	}
	result, err := runtimeSessionWire.NewInstructionService().Resolve(ctx, request)
	if err != nil {
		return "", err
	}
	return result.Instructions, nil
}

func newSessionInstructionRequest(opts SessionRunOptions, systemPrompt string) (runtimeSession.InstructionRequest, error) {
	workDir := opts.WorkDir
	if workDir == "" && opts.FilesystemPolicy == nil {
		// Preserve the direct service API's historical workspace behavior. CLI
		// sessions always supply the launch-captured policy explicitly.
		workDir = opts.ConfigDir
	}
	if workDir != "" && opts.FilesystemPolicy == nil {
		// Validate the host-selected workspace before attempting prompt
		// discovery. A missing workspace is a startup/configuration error, not
		// an empty prompt, and must prevent provider/session admission.
		policy, policyErr := cliTools.ResolveFilesystemPolicy(workDir, opts.AllowPaths...)
		if policyErr != nil {
			return runtimeSession.InstructionRequest{}, fmt.Errorf("resolve filesystem scope: %w", policyErr)
		}
		workDir = policy.PrimaryRoot()
	}
	request := runtimeSession.InstructionRequest{
		Prompt:       systemPrompt,
		WorkspaceDir: workDir,
		Loader:       sessionInstructionLoader{workspaceDir: workDir, configDir: opts.ConfigDir},
	}
	if opts.FilesystemPolicy != nil {
		request.FilesystemScopeSet = true
		request.FilesystemScopeDescription = opts.FilesystemPolicy.ScopeDescription()
	}
	return request, nil
}

type sessionInstructionLoader struct {
	workspaceDir string
	configDir    string
}

func (l sessionInstructionLoader) Stat(path string) error { _, err := os.Stat(path); return err }

func (l sessionInstructionLoader) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (l sessionInstructionLoader) SkillsSummary() (string, error) {
	return skills.NewLoader(l.workspaceDir, l.configDir).BuildSummary()
}

// composeSessionInstructions is a compatibility adapter around the runtime
// service. Keeping this callable helper preserves the existing planner seams
// without retaining a second policy implementation in the CLI.
func composeSessionInstructions(opts SessionRunOptions, instructions string) string {
	return runtimeSessionWire.NewInstructionService().Compose(runtimeSession.InstructionComposition{
		Instructions:           instructions,
		ToolDefinitions:        append([]messages.ToolDefinition(nil), opts.ToolDefinitions...),
		BrowserCapabilityState: runtimeSession.BrowserCapabilityState(string(opts.BrowserCapabilityState)),
		BrowserToolsEnabled:    opts.BrowserToolsEnabled,
		PageSightToolID:        cliTools.PageSightToolID,
	})
}
