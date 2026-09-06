package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/spf13/cobra"
)

// validateLoopFlagRanges rejects an explicit --max-iterations or
// --context-pressure-threshold value that --loop mode cannot act on
// meaningfully, instead of silently discarding it.
//
// Before this check: `--max-iterations -5` (or 0) fell into
// RunIterativeLoop's "maxIter <= 0 -> default to 5" fallback with no
// warning at all, so the value the user typed was thrown away in favor of
// the default they never asked for. `--context-pressure-threshold -3` is
// likewise indistinguishable from never having passed the flag: the
// context-pressure notifier only wires up when the threshold is > 0, so a
// negative value silently disables the feature the user just tried to
// configure. `--context-pressure-threshold 5000` is accepted just as
// silently in the other direction: the loop compares this value against a
// 0-1 fraction of context used, so anything above 1 can never trigger --
// the notifier is wired up (since 5000 > 0) but permanently inert. In every
// case the user believed they configured something; they hadn't.
func validateLoopFlagRanges(loopFlags *flags.LoopFlags) error {
	if loopFlags.MaxIterations <= 0 {
		return fmt.Errorf("--max-iterations must be a positive integer, got %d", loopFlags.MaxIterations)
	}
	if loopFlags.ContextPressureThreshold <= 0 || loopFlags.ContextPressureThreshold > 1 {
		return fmt.Errorf("--context-pressure-threshold must be greater than 0 and at most 1 (a fraction of the context window), got %g", loopFlags.ContextPressureThreshold)
	}
	return nil
}

const defaultLoopMaxIterations = 5

// loopChatRunner is the terminal adapter for the runtime iterative service.
// Trace persistence and iteration state stay in the service; this value only
// owns scanner input, presentation, and the signal-to-context bridge.
type loopChatRunner struct {
	out           io.Writer
	scanner       *bufio.Scanner
	traceID       string
	max           int
	done          bool
	stoppedByWord bool
	interrupted   bool
}

// runLoopChat runs an interactive loop chat session. The runtime service
// drives fresh iterations and durable traces through the typed interaction
// port; this command supplies terminal input and renders events.
func (c *ChatCommand) runLoopChat(cmd *cobra.Command) error {
	maxIterations := normalizedLoopMaxIterations(c.loopFlags.MaxIterations)
	out := cmd.OutOrStdout()
	if err := writeLoopHeader(out, maxIterations); err != nil {
		return err
	}
	traceStore, err := c.loopTraceStore()
	if err != nil {
		return err
	}
	if c.service == nil {
		return fmt.Errorf("session service is not configured")
	}
	runner := &loopChatRunner{
		out:     out,
		scanner: bufio.NewScanner(cmd.InOrStdin()),
		max:     maxIterations,
	}
	request := services.BuildAgentConfigFromFlags(c.globalFlags, c.askFlags, nil, "")
	interaction := &session.IterativeInteraction{
		InitialPrompt:    runner.InitialPrompt,
		TraceReady:       runner.TraceReady,
		IterationContext: runner.IterationContext,
		OnIteration:      runner.OnIteration,
	}
	result, err := c.service.RunIterative(cmd.Context(), *request, session.IterativeRequest{
		MaxIterations:            maxIterations,
		StopWord:                 c.loopFlags.StopWord,
		ContextPressureThreshold: c.loopFlags.ContextPressureThreshold,
		ContextPressureMessage:   c.loopFlags.ContextPressureMessage,
		TraceID:                  c.loopFlags.TraceID,
		TraceStore:               traceStore,
		Interaction:              interaction,
	})
	if err != nil {
		return err
	}
	if runner.done || runner.interrupted {
		return nil
	}
	return runner.writeLoopComplete(result)
}

func normalizedLoopMaxIterations(value int) int {
	if value <= 0 {
		return defaultLoopMaxIterations
	}
	return value
}

func writeLoopHeader(out io.Writer, maxIterations int) error {
	if _, err := fmt.Fprintf(out, "Port OS Agent Loop Chat (up to %d iterations)\n", maxIterations); err != nil {
		return fmt.Errorf("write chat header: %w", err)
	}
	if _, err := fmt.Fprintln(out, "---"); err != nil {
		return fmt.Errorf("write chat header separator: %w", err)
	}
	return nil
}

func (c *ChatCommand) loopTraceStore() (session.TraceStore, error) {
	if c.storeFactory == nil {
		return nil, fmt.Errorf("session file store factory is required")
	}
	managed, err := services.NewSessionStoreWithFactory(c.globalFlags, c.storeFactory)
	if err != nil {
		return nil, fmt.Errorf("get session storage: %w", err)
	}
	return managed, nil
}

func (r *loopChatRunner) InitialPrompt(_ context.Context) (string, bool, error) {
	if _, err := fmt.Fprint(r.out, "Enter your task: "); err != nil {
		return "", false, fmt.Errorf("write task prompt: %w", err)
	}
	if !r.scanner.Scan() {
		r.done = true
		return "", true, nil
	}
	prompt := strings.TrimSpace(r.scanner.Text())
	if prompt == "" {
		return "", false, fmt.Errorf("no task provided")
	}
	return prompt, false, nil
}

func (r *loopChatRunner) TraceReady(_ context.Context, trace session.IterativeTrace) error {
	r.traceID = trace.TraceID
	r.max = trace.MaxIterations
	if trace.Resumed {
		if _, err := fmt.Fprintf(r.out, "[Resuming trace %s from iteration %d/%d]\n", trace.TraceID, trace.StartIteration, trace.MaxIterations); err != nil {
			return fmt.Errorf("write resume trace banner: %w", err)
		}
	}
	if _, err := fmt.Fprintf(r.out, "Trace ID: %s\n", trace.TraceID); err != nil {
		return fmt.Errorf("write trace ID: %w", err)
	}
	return nil
}

func (r *loopChatRunner) IterationContext(ctx context.Context, _ int) (context.Context, func()) {
	return signal.NotifyContext(ctx, os.Interrupt)
}

func (r *loopChatRunner) OnIteration(_ context.Context, iteration session.IterationResult) (session.IterativeDecision, error) {
	if err := r.writeIterationHeader(iteration.Iteration); err != nil {
		return session.IterativeDecision{}, err
	}
	if iteration.Interrupted {
		return r.handleInterrupted(iteration.Iteration)
	}
	if iteration.Err != nil {
		if _, err := fmt.Fprintf(r.out, "\n[Iteration %d error: %v]\n", iteration.Iteration, iteration.Err); err != nil {
			return session.IterativeDecision{}, fmt.Errorf("write iteration error: %w", err)
		}
	} else if _, err := fmt.Fprintln(r.out, iteration.Text); err != nil {
		return session.IterativeDecision{}, fmt.Errorf("write iteration output: %w", err)
	}
	if iteration.StopWordMatched {
		if _, err := fmt.Fprintf(r.out, "\n[Completion detected in iteration %d]\n", iteration.Iteration); err != nil {
			return session.IterativeDecision{}, fmt.Errorf("write completion banner: %w", err)
		}
		r.stoppedByWord = true
		return session.IterativeDecision{Action: session.IterativeStop}, nil
	}
	if iteration.Iteration == r.max {
		return session.IterativeDecision{Action: session.IterativeContinue}, nil
	}
	return r.readSteering(iteration.Iteration)
}

func (r *loopChatRunner) writeIterationHeader(iteration int) error {
	if _, err := fmt.Fprintf(r.out, "\n--- Iteration %d/%d ---\n", iteration, r.max); err != nil {
		return fmt.Errorf("write iteration header: %w", err)
	}
	return nil
}

func (r *loopChatRunner) handleInterrupted(iteration int) (session.IterativeDecision, error) {
	if _, err := fmt.Fprintf(r.out, "\n[Iteration %d interrupted. Enter steering for next iteration, or 'exit' to quit (resume later with --trace-id %s)]: ", iteration, r.traceID); err != nil {
		return session.IterativeDecision{}, fmt.Errorf("write interrupted prompt: %w", err)
	}
	if !r.scanner.Scan() {
		r.interrupted = true
		return session.IterativeDecision{Action: session.IterativeStop}, nil
	}
	input := strings.TrimSpace(r.scanner.Text())
	if strings.EqualFold(input, "exit") {
		r.interrupted = true
		if _, err := fmt.Fprintf(r.out, "[Loop interrupted. Resume with: --loop --trace-id %s]\n", r.traceID); err != nil {
			return session.IterativeDecision{}, fmt.Errorf("write interrupted resume banner: %w", err)
		}
		return session.IterativeDecision{Action: session.IterativeStop}, nil
	}
	return session.IterativeDecision{Action: session.IterativeContinue, Prompt: input}, nil
}

func (r *loopChatRunner) readSteering(iteration int) (session.IterativeDecision, error) {
	if _, err := fmt.Fprintf(r.out, "\n[Iteration %d complete. Enter steering for iteration %d (or press Enter to continue with same task)]: ", iteration, iteration+1); err != nil {
		return session.IterativeDecision{}, fmt.Errorf("write steering prompt: %w", err)
	}
	if !r.scanner.Scan() {
		return session.IterativeDecision{Action: session.IterativeContinue}, nil
	}
	return session.IterativeDecision{Action: session.IterativeContinue, Prompt: strings.TrimSpace(r.scanner.Text())}, nil
}

func (r *loopChatRunner) writeLoopComplete(result session.IterativeResult) error {
	traceID := result.TraceID
	if traceID == "" {
		traceID = r.traceID
	}
	if _, err := fmt.Fprintf(r.out, "\n[Loop complete: %d iteration(s), trace: %s]\n", r.max, traceID); err != nil {
		return fmt.Errorf("write loop completion banner: %w", err)
	}
	return nil
}
