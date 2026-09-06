package agent

import (
	"context"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/persistence"
	"io"
	"strings"
)

// IterativeLoopConfig holds transport-neutral configuration for an iterative loop run.
type IterativeLoopConfig struct {
	// MaxIterations is the maximum number of loop iterations. Defaults to 5 if <= 0.
	MaxIterations int

	// StopWord is a case-sensitive string that, when found in the last assistant
	// response, signals completion and stops the loop early.
	StopWord string

	// ContextPressureThreshold enables the ContextPressureNotifier when > 0.
	// Ignored here (handled by IterativeLoop when token counting is configured).
	ContextPressureThreshold float64

	// ContextPressureMessage is the custom warning injected at pressure threshold.
	ContextPressureMessage string

	// TraceID, if set, loads an existing trace record and resumes from it.
	TraceID string
}

// IterativeAction is the host's decision after an interactive iteration.
type IterativeAction string

const (
	IterativeContinue IterativeAction = "continue"
	IterativeStop     IterativeAction = "stop"
)

// IterativeDecision carries an optional prompt for the next fresh iteration.
type IterativeDecision struct {
	Action IterativeAction
	Prompt string
}

// IterativeTrace is the runtime-owned trace state exposed at the host
// interaction boundary before iteration execution begins.
type IterativeTrace struct {
	TraceID        string
	StartIteration int
	MaxIterations  int
	Resumed        bool
}

// IterativeInteraction keeps terminal and UI policy outside the runtime while
// leaving trace and iteration state in this package.
type IterativeInteraction struct {
	InitialPrompt    func(context.Context) (prompt string, done bool, err error)
	TraceReady       func(context.Context, IterativeTrace) error
	IterationContext func(context.Context, int) (context.Context, func())
	OnIteration      func(context.Context, IterationRunResult) (IterativeDecision, error)
}

// IterationRunResult holds the result of a single loop iteration.
type IterationRunResult struct {
	Iteration       int
	SessionID       string
	Text            string
	Err             error
	Interrupted     bool
	StopWordMatched bool
}

// IterativeRunResult holds the combined result of a loop run.
type IterativeRunResult struct {
	TraceID    string
	Iterations []IterationRunResult
	Completed  bool
}

// BuildIterationAnnotation returns the iteration-specific annotation appended to the system prompt.
func BuildIterationAnnotation(i, maxIter int, stopWord string) string {
	annotation := fmt.Sprintf("You are on iteration %d of %d.", i, maxIter)
	if stopWord != "" {
		annotation += fmt.Sprintf(" When you are finished, end your response with %s.", stopWord)
	}
	return annotation
}

// RunIterativeLoop runs the agent iteratively, printing iteration banners, managing the trace record,
// and saving each iteration's session. Each iteration runs with a fresh context window.
//
// Ctrl+C (SIGINT) cancels the current iteration, updates the trace record as interrupted,
// prints a resume message, and returns. The loop can be resumed by passing the printed trace ID
// via IterativeLoopConfig.TraceID.
//
// Errors from individual iterations are recorded in IterativeRunResult and the loop continues;
// only setup errors (trace load, session storage) are returned as hard failures.
func (e *Executor) RunIterativeLoop(
	ctx context.Context,
	cfg *Config,
	loopCfg IterativeLoopConfig,
	input agentloop.ExecuteInput,
	out io.Writer,
) (IterativeRunResult, error) {
	return e.runIterativeLoop(ctx, cfg, loopCfg, input, out, nil)
}

// RunIterativeLoopWithInteraction runs the same runtime-owned loop while
// delegating prompt acquisition, presentation, and per-iteration cancellation
// to a host interaction port.
func (e *Executor) RunIterativeLoopWithInteraction(
	ctx context.Context,
	cfg *Config,
	loopCfg IterativeLoopConfig,
	input agentloop.ExecuteInput,
	out io.Writer,
	interaction *IterativeInteraction,
) (IterativeRunResult, error) {
	return e.runIterativeLoop(ctx, cfg, loopCfg, input, out, interaction)
}

func (e *Executor) runIterativeLoop(
	ctx context.Context,
	cfg *Config,
	loopCfg IterativeLoopConfig,
	input agentloop.ExecuteInput,
	out io.Writer,
	interaction *IterativeInteraction,
) (IterativeRunResult, error) {
	state, err := e.prepareIterations(ctx, cfg, loopCfg, input, out, interaction)
	if err != nil {
		return IterativeRunResult{}, err
	}
	if state.done {
		return IterativeRunResult{}, nil
	}
	trace, sessionStorage := state.trace, state.storage
	startIter, maxIter := state.start, state.maximum
	loopCfg.StopWord, input = state.stopWord, state.input
	result := IterativeRunResult{TraceID: trace.TraceID}
	if err := announceIterativeTrace(ctx, out, interaction, trace, startIter, maxIter, state.resumed); err != nil {
		return result, err
	}
	if interaction == nil || interaction.OnIteration == nil {
		return e.runNonInteractiveIterations(ctx, cfg, loopCfg, input, &trace, sessionStorage, startIter, maxIter, result, out)
	}
	return e.runInteractiveIterations(ctx, cfg, loopCfg, &input, trace, sessionStorage, startIter, maxIter, result, interaction)
}

func announceIterativeTrace(ctx context.Context, out io.Writer, interaction *IterativeInteraction, trace session.TraceRecord, startIter, maxIter int, resumed bool) error {
	if interaction != nil && interaction.TraceReady != nil {
		return interaction.TraceReady(ctx, IterativeTrace{TraceID: trace.TraceID, StartIteration: startIter, MaxIterations: maxIter, Resumed: resumed})
	}
	if _, err := fmt.Fprintf(out, "Trace ID: %s\n", trace.TraceID); err != nil {
		return fmt.Errorf("write trace ID: %w", err)
	}
	return nil
}

func (e *Executor) runNonInteractiveIterations(ctx context.Context, cfg *Config, loopCfg IterativeLoopConfig, input agentloop.ExecuteInput, trace *session.TraceRecord, storage Storage, startIter, maxIter int, result IterativeRunResult, out io.Writer) (IterativeRunResult, error) {
	for i := startIter; i <= maxIter; i++ {
		iteration, interrupted, err := e.runNonInteractiveIteration(ctx, cfg, loopCfg.StopWord, input, trace, storage, i, maxIter, out)
		if err != nil {
			return result, err
		}
		if interrupted {
			return finishInterruptedIteration(storage, *trace, result, iteration, out)
		}
		result.Iterations = append(result.Iterations, iteration)
		if iteration.StopWordMatched {
			result.Completed = true
			break
		}
	}
	return finishIterativeTrace(storage, *trace, result)
}

func (e *Executor) runNonInteractiveIteration(ctx context.Context, cfg *Config, stopWord string, input agentloop.ExecuteInput, trace *session.TraceRecord, storage Storage, iteration, maxIter int, out io.Writer) (IterationRunResult, bool, error) {
	if _, err := fmt.Fprintf(out, "\n--- Iteration %d/%d ---\n", iteration, maxIter); err != nil {
		return IterationRunResult{}, false, fmt.Errorf("write iteration header: %w", err)
	}
	iterCtx, iterCancel := iterativeContext(ctx, nil, iteration)
	result := e.runIteration(iterCtx, cfg, iteration, maxIter, stopWord, input, out)
	interrupted := iterationContextInterrupted(iterCtx)
	iterCancel()
	result.Interrupted = interrupted
	if interrupted {
		result.Err = context.Canceled
	}
	appendIterationTrace(trace, result)
	if interrupted {
		return result, true, nil
	}
	if err := storage.SaveTrace(*trace); err != nil {
		return result, false, fmt.Errorf("save trace after iteration %d: %w", iteration, err)
	}
	result.StopWordMatched = hasIterationStopWord(result, stopWord)
	return result, false, nil
}

func (e *Executor) runInteractiveIterations(ctx context.Context, cfg *Config, loopCfg IterativeLoopConfig, input *agentloop.ExecuteInput, trace session.TraceRecord, storage Storage, startIter, maxIter int, result IterativeRunResult, interaction *IterativeInteraction) (IterativeRunResult, error) {
	for i := startIter; i <= maxIter; i++ {
		iteration, err := e.runInteractiveIteration(ctx, cfg, loopCfg.StopWord, *input, &trace, storage, i, maxIter, interaction)
		if err != nil {
			return result, err
		}
		result.Iterations = append(result.Iterations, iteration)
		action, err := e.applyInteractiveDecision(ctx, interaction, iteration, &trace, storage, input)
		if err != nil {
			return result, err
		}
		if action == iterativeLoopInterrupted {
			return result, nil
		}
		if action == iterativeLoopComplete {
			result.Completed = true
			break
		}
	}
	return finishIterativeTrace(storage, trace, result)
}

func (e *Executor) runInteractiveIteration(ctx context.Context, cfg *Config, stopWord string, input agentloop.ExecuteInput, trace *session.TraceRecord, storage Storage, iteration, maxIter int, interaction *IterativeInteraction) (IterationRunResult, error) {
	iterCtx, iterCancel := iterativeContext(ctx, interaction, iteration)
	result := e.runIteration(iterCtx, cfg, iteration, maxIter, stopWord, input, io.Discard)
	interrupted := iterationContextInterrupted(iterCtx)
	iterCancel()
	result.Interrupted = interrupted
	if interrupted {
		result.Err = context.Canceled
	}
	appendIterationTrace(trace, result)
	trace.Status = session.TraceStatusRunning
	if err := storage.SaveTrace(*trace); err != nil {
		return result, fmt.Errorf("save trace after iteration %d: %w", iteration, err)
	}
	result.StopWordMatched = hasIterationStopWord(result, stopWord)
	return result, nil
}

type iterativeLoopAction uint8

const (
	iterativeLoopContinue iterativeLoopAction = iota
	iterativeLoopComplete
	iterativeLoopInterrupted
)

func (e *Executor) applyInteractiveDecision(ctx context.Context, interaction *IterativeInteraction, iteration IterationRunResult, trace *session.TraceRecord, storage Storage, input *agentloop.ExecuteInput) (iterativeLoopAction, error) {
	decision, err := interaction.OnIteration(ctx, iteration)
	if err != nil {
		return iterativeLoopContinue, err
	}
	if iteration.Interrupted {
		if ctx.Err() != nil || decision.Action != IterativeContinue {
			trace.Status = session.TraceStatusInterrupted
			if err := storage.SaveTrace(*trace); err != nil {
				return iterativeLoopContinue, fmt.Errorf("save interrupted trace: %w", err)
			}
			return iterativeLoopInterrupted, nil
		}
	}
	if iteration.StopWordMatched || decision.Action == IterativeStop {
		return iterativeLoopComplete, nil
	}
	if decision.Prompt != "" {
		input.Message = decision.Prompt
		trace.Config.Prompt = decision.Prompt
		if err := storage.SaveTrace(*trace); err != nil {
			return iterativeLoopContinue, fmt.Errorf("save steering prompt after iteration %d: %w", iteration.Iteration, err)
		}
	}
	return iterativeLoopContinue, nil
}

func appendIterationTrace(trace *session.TraceRecord, iteration IterationRunResult) {
	trace.CurrentIteration = iteration.Iteration
	trace.Iterations = append(trace.Iterations, session.IterationTrace{
		Iteration: iteration.Iteration,
		SessionID: iteration.SessionID,
		Status:    iterationStatus(iteration.Interrupted, iteration.Err),
	})
}

func hasIterationStopWord(iteration IterationRunResult, stopWord string) bool {
	return !iteration.Interrupted && iteration.Err == nil && stopWord != "" && strings.Contains(iteration.Text, stopWord)
}

func iterationContextInterrupted(ctx context.Context) bool {
	return ctx != nil && ctx.Err() != nil
}

func finishIterativeTrace(storage Storage, trace session.TraceRecord, result IterativeRunResult) (IterativeRunResult, error) {
	trace.Status = session.TraceStatusCompleted
	if err := storage.SaveTrace(trace); err != nil {
		return result, fmt.Errorf("save completed trace: %w", err)
	}
	return result, nil
}

func iterativeContext(ctx context.Context, interaction *IterativeInteraction, iteration int) (context.Context, func()) {
	if interaction != nil && interaction.IterationContext != nil {
		iterCtx, cancel := interaction.IterationContext(ctx, iteration)
		if iterCtx == nil {
			iterCtx = ctx
		}
		if cancel == nil {
			cancel = func() {}
		}
		return iterCtx, cancel
	}
	return ctx, func() {}
}

func (e *Executor) runIteration(ctx context.Context, cfg *Config, iteration, maximum int, stopWord string, input agentloop.ExecuteInput, out io.Writer) IterationRunResult {
	// Build iteration config: fresh session, iteration-specific annotation appended to system prompt.
	iterCfg := *cfg
	iterCfg.SessionID = ""
	iterCfg.ContinueLastSession = false
	iterCfg.InitialHistory = nil
	iterCfg.SystemPromptSuffix = BuildIterationAnnotation(iteration, maximum, stopWord)

	// Build and run this iteration using the caller-owned context. Host
	// adapters translate signals into context cancellation before invoking
	// the runtime; the reusable service must not install process-global
	// signal handlers of its own.
	runData, buildErr := e.BuildLoop(ctx, &iterCfg)
	var text string
	var execErr error
	var sessionID string

	if buildErr != nil {
		execErr = buildErr
	} else if mimeErr := e.validateInputMimeTypes(&iterCfg, runData, input); mimeErr != nil {
		execErr = mimeErr
	} else {
		sessionID = runData.SessionID
		text, execErr = e.executeWithContinuation(ctx, runData, input, &iterCfg, out, stopWord)
		if execErr == nil {
			if saveErr := e.SaveSession(runData); saveErr != nil {
				execErr = saveErr
			}
		}
	}

	return IterationRunResult{Iteration: iteration, SessionID: sessionID, Text: text, Err: execErr}
}

func iterationStatus(interrupted bool, err error) session.IterationStatus {
	if interrupted {
		return session.IterationStatusInterrupted
	}
	if err != nil {
		return session.IterationStatusFailed
	}
	return session.IterationStatusCompleted
}

func finishInterruptedIteration(storage Storage, trace session.TraceRecord, result IterativeRunResult, iteration IterationRunResult, out io.Writer) (IterativeRunResult, error) {
	trace.Status = session.TraceStatusInterrupted
	if saveErr := storage.SaveTrace(trace); saveErr != nil {
		return result, fmt.Errorf("save interrupted trace: %w", saveErr)
	}
	if _, err := fmt.Fprintf(out, "\n[Interrupted. Resume with: --loop --trace-id %s]\n", trace.TraceID); err != nil {
		return result, fmt.Errorf("write interrupted trace banner: %w", err)
	}
	result.Iterations = append(result.Iterations, IterationRunResult{
		Iteration: iteration.Iteration,
		SessionID: iteration.SessionID,
		Text:      iteration.Text,
		Err:       context.Canceled,
	})
	return result, nil
}
