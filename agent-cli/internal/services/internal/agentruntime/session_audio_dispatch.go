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
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioninstructions"
)

func sendEventDrivenAudioInput(ctx context.Context, loop *agentloop.AgentLoop, opts sessionLoopOptions, input ScheduledAudioInput) error {
	if len(input.PCM) == 0 {
		return errors.New("event-driven audio input is empty")
	}
	pcm, err := convertSessionAudioPCM(input.PCM, input.SourceSampleRate, opts.InputAudioSampleRate)
	if err != nil {
		return fmt.Errorf("convert event-driven audio input: %w", err)
	}
	if err := loop.SendAudioInput(ctx, pcm); err != nil {
		return fmt.Errorf("send event-driven audio input: %w", err)
	}
	if opts.observer != nil {
		opts.observer.account(metrics.DirectionInput, metrics.ModalityAudio, len(pcm))
	}
	if !input.EndOfTurn {
		return nil
	}
	if err := loop.SendSessionEvent(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd}); err != nil {
		return fmt.Errorf("send event-driven audio input end-of-turn: %w", err)
	}
	if opts.observer != nil {
		opts.observer.armProviderProgress()
	}
	return nil
}

// RunSessionWithInstructions resolves the ask-path system-prompt contract and
// applies the result to the realtime session before the first user turn.
//
// This compatibility entry point remains for the CLI command layer; embedders
// should use the session runtime service directly.
func RunSessionWithInstructions(ctx context.Context, out io.Writer, opts SessionRunOptions, systemPrompt string) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() { closeSessionCapabilityIfNeeded(coordinator, &runErr) }()
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
	defer func() {
		if releaseErr := claim.release(); releaseErr != nil {
			runErr = errors.Join(runErr, releaseErr)
		}
	}()
	instructions, err := resolveSessionInstructionsWithContext(ctx, opts, systemPrompt)
	if err != nil {
		return err
	}
	plan, err := planSessionWithResolvedInstructionsWithContext(ctx, opts, instructions)
	if err != nil {
		return err
	}
	return plan.run(ctx, out)
}

// RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration preserves the
// session command's audio, explicit text-seed, and duration behavior while
// carrying selected workspace instructions into provider construction.
//
// This compatibility entry point remains for the CLI command layer; embedders
// should use the session runtime service directly.
func RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, audioPath string, maxDuration time.Duration, seed SessionTextSeed, systemPrompt string) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() { closeSessionCapabilityIfNeeded(coordinator, &runErr) }()
	if err := sessioncontract.ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	if opts.ReplayPath != "" && opts.SessionInferencer == nil {
		return RunSessionWithAudioOutAndTextSeedAndMaxDuration(ctx, out, opts, audioPath, maxDuration, seed)
	}
	if seed.Present {
		opts.Prompt = seed.Value
		opts.PromptProvided = true
	}
	if audioPath != "" {
		opts.AudioOutputRequested = true
	}
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := claim.release(); releaseErr != nil {
			runErr = errors.Join(runErr, releaseErr)
		}
	}()
	instructions, err := resolveSessionInstructionsWithContext(ctx, opts, systemPrompt)
	if err != nil {
		return err
	}
	plan, err := planSessionWithResolvedInstructionsWithContext(ctx, opts, instructions)
	if err != nil {
		return err
	}
	return runSessionWithInstructionPlan(ctx, out, &plan, audioPath, maxDuration, seed)
}

func runSessionWithInstructionPlan(ctx context.Context, out io.Writer, plan *sessionRuntimePlan, audioPath string, maxDuration time.Duration, seed SessionTextSeed) (runErr error) {
	if audioPath == "" {
		return runSessionInstructionTextPlan(ctx, out, plan, maxDuration, seed)
	}
	return runSessionInstructionAudioPlan(ctx, out, plan, audioPath, maxDuration, seed)
}

func runSessionInstructionTextPlan(ctx context.Context, out io.Writer, plan *sessionRuntimePlan, maxDuration time.Duration, seed SessionTextSeed) error {
	if seed.Present {
		return runSessionInstructionTextSeedPlan(ctx, out, plan, maxDuration, seed)
	}
	return runSessionPlanWithDuration(ctx, out, plan, maxDuration)
}

func runSessionInstructionTextSeedPlan(ctx context.Context, out io.Writer, plan *sessionRuntimePlan, maxDuration time.Duration, seed SessionTextSeed) (runErr error) {
	wirePrompt := nextSessionTextWirePrompt()
	plan.loop.Prompt = wirePrompt
	output := &sessionTextOutput{writer: out}
	if maxDuration == 0 {
		if plan.inferencer != nil {
			plan.inferencer = &sessionTextSeedInferencer{inner: plan.inferencer, wirePrompt: wirePrompt, value: seed.Value}
		}
		return errors.Join(plan.run(ctx, output), output.errorValue())
	}
	durationCtx, err := prepareSessionDurationArtifacts(ctx)
	if err != nil {
		return err
	}
	admission := newSessionDurationAdmission()
	admittedInferencer := installSessionTextSeedAdmission(plan, admission, wirePrompt, seed.Value)
	runErr = runSessionDurationPlanWithAdmission(durationCtx, output, *plan, maxDuration, realSessionDurationClock{}, admittedInferencer)
	return errors.Join(runErr, output.errorValue())
}

func installSessionTextSeedAdmission(plan *sessionRuntimePlan, admission *sessionDurationAdmission, wirePrompt, seed string) *sessionDurationAdmissionInferencer {
	if plan.inferencer == nil {
		return nil
	}
	admittedInner := &sessionTextSeedInferencer{inner: plan.inferencer, wirePrompt: wirePrompt, value: seed}
	admitted := &sessionDurationAdmissionInferencer{inner: admittedInner, admission: admission, closeDone: make(chan struct{})}
	plan.inferencer = admitted
	return admitted
}

func runSessionInstructionAudioPlan(ctx context.Context, out io.Writer, plan *sessionRuntimePlan, audioPath string, maxDuration time.Duration, seed SessionTextSeed) (runErr error) {
	if seed.Present {
		plan.loop.Prompt = nextSessionTextWirePrompt()
	}
	audioOut, err := newSessionAudioOutputForPlan(plan, audioPath, out, nil)
	if err != nil {
		return fmt.Errorf("--audio-out %q: %w", audioPath, err)
	}
	defer func() {
		if closeErr := audioOut.close(); closeErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", audioPath, closeErr))
		}
	}()
	sessionOut := out
	if audioPath == "-" {
		sessionOut = io.Discard
	}
	if plan.inferencer == nil {
		return runSessionPlanWithDuration(ctx, sessionOut, plan, maxDuration)
	}
	wirePrompt := ""
	if seed.Present {
		wirePrompt = plan.loop.Prompt
	}
	wrapped := newSessionAudioOutputInferencer(plan.inferencer, audioOut, wirePrompt, seed.Value)
	plan.inferencer = wrapped
	runErr = runSessionPlanWithDuration(ctx, sessionOut, plan, maxDuration)
	wrapped.wait()
	if outputErr := wrapped.err(); outputErr != nil {
		runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", audioPath, outputErr))
	}
	return runErr
}

func runSessionPlanWithDuration(ctx context.Context, out io.Writer, plan *sessionRuntimePlan, maxDuration time.Duration) error {
	if maxDuration == 0 {
		return plan.run(ctx, out)
	}
	durationCtx, err := prepareSessionDurationArtifacts(ctx)
	if err != nil {
		return err
	}
	return runSessionDurationPlan(durationCtx, out, *plan, maxDuration, realSessionDurationClock{})
}

type sessionInstructionLoader struct {
	workspaceDir string
	configDir    string
}

func (l sessionInstructionLoader) Stat(path string) error { _, err := os.Stat(path); return err }

func (l sessionInstructionLoader) ReadFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(sessioninstructions.MaxInstructionBytes)+1))
	closeErr := file.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return data, nil
}

func (l sessionInstructionLoader) SkillsSummary() (string, error) {
	return skills.NewLoader(l.workspaceDir, l.configDir).BuildSummary()
}
