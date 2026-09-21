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
	duration "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	durationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/wire"
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
	instructions, err := resolveSessionInstructions(opts, systemPrompt)
	if err != nil {
		return err
	}

	plan, err := planSessionWithResolvedInstructions(opts, instructions)
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
	if opts.ReplayPath != "" && opts.SessionInferencer == nil {
		if maxDuration == 0 {
			return RunSessionWithTextSeed(ctx, out, opts, seed)
		}
		return RunSessionWithTextSeedAndMaxDuration(ctx, out, opts, maxDuration, seed)
	}
	if seed.Present {
		opts.Prompt = seed.Value
		opts.PromptProvided = true
	}
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	instructions, err := resolveSessionInstructions(opts, systemPrompt)
	if err != nil {
		return err
	}
	plan, err := planSessionWithResolvedInstructions(opts, instructions)
	if err != nil {
		return err
	}

	{
		if seed.Present {
			wirePrompt := nextSessionTextWirePrompt()
			plan.loop.Prompt = wirePrompt
			output := &sessionTextOutput{writer: out}
			if maxDuration == 0 {
				if plan.inferencer != nil {
					plan.inferencer = &sessionTextSeedInferencer{
						inner:      plan.inferencer,
						wirePrompt: wirePrompt,
						value:      seed.Value,
					}
				}
				return errors.Join(plan.run(ctx, output), output.errorValue())
			}
			durationService := durationwire.NewService()
			durationCtx, err := durationService.PrepareArtifacts(ctx)
			if err != nil {
				return err
			}
			return plan.withLiveEvidence(durationCtx, func(runCtx context.Context, prepared sessionRuntimePlan) error {
				admission := durationService.NewEventAdmission()
				// The seed substitution wrapper must sit INSIDE the admission
				// boundary: the duration runner connects through
				// admittedInferencer, so any wrapper composed outside it never
				// observes the session and the sentinel prompt would leak onto
				// the live wire.
				var admittedInner messages.SessionInferencer
				if prepared.inferencer != nil {
					admittedInner = &sessionTextSeedInferencer{
						inner:      prepared.inferencer,
						wirePrompt: wirePrompt,
						value:      seed.Value,
					}
				}
				if admittedInner != nil {
					prepared.inferencer = durationService.NewAdmissionInferencer(admittedInner, admission, make(chan struct{}))
				}
				var admittedInferencer duration.AdmissionInferencer
				if admitted, ok := prepared.inferencer.(duration.AdmissionInferencer); ok {
					admittedInferencer = admitted
				}
				runErr = runSessionDurationPlanWithAdmission(runCtx, output, prepared, maxDuration, nil, admittedInferencer)
				return errors.Join(runErr, output.errorValue())
			})
		}
		if maxDuration == 0 {
			return plan.run(ctx, out)
		}
		durationCtx, err := durationwire.NewService().PrepareArtifacts(ctx)
		if err != nil {
			return err
		}
		return plan.withLiveEvidence(durationCtx, func(runCtx context.Context, prepared sessionRuntimePlan) error {
			return runSessionDurationPlan(runCtx, out, prepared, maxDuration, nil)
		})
	}
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
