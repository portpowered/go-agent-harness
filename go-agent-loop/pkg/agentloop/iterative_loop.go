package agentloop

import (
	"context"
	"fmt"
	"strings"
)

// IterativeConfig holds iteration-specific configuration for IterativeLoop.
type IterativeConfig struct {
	// MaxIterations is the maximum number of iterations to run.
	// Defaults to 5 if not set (or <= 0).
	MaxIterations int

	// StopWord is a case-sensitive string that, when found in the last assistant
	// response, signals completion and stops the loop early.
	// If empty, the loop always runs MaxIterations times.
	StopWord string

	// PressureThreshold, if > 0, wires the ContextPressureNotifier subsystem for
	// each iteration's AgentLoop. Requires WithTokenCounter to be set in the base
	// options; otherwise the option is silently ignored.
	PressureThreshold float64

	// PressureMessage is the custom warning message injected when context pressure
	// exceeds PressureThreshold. Uses the default message if empty.
	PressureMessage string
}

// IterationResult holds the result of a single iteration.
type IterationResult struct {
	// Iteration is the 1-based iteration number.
	Iteration int

	// Text is the final text response produced by the agent in this iteration.
	Text string

	// Err is non-nil if the iteration failed.
	Err error
}

// IterativeResult holds the combined result of all iterations.
type IterativeResult struct {
	// Iterations contains the result of each iteration that ran, in order.
	Iterations []IterationResult

	// Completed is true if the stop word was found in an iteration's response,
	// signalling that the agent has finished its work.
	Completed bool
}

// IterativeLoop runs an AgentLoop repeatedly, creating a fresh isolated session per
// iteration. It injects the current iteration count into the system prompt and stops
// early when a configured stop word is detected in the assistant's final response.
//
// Each iteration has no shared history with previous iterations — the loop starts
// from a clean slate each time, with only the (augmented) system prompt and the same
// initial user input.
type IterativeLoop struct {
	opts    []Option
	iterCfg IterativeConfig
}

// NewIterativeLoop creates a new IterativeLoop.
//
// opts are the same AgentLoop options (inferencer, tools, system prompt, etc.)
// used to build each iteration's AgentLoop. The system prompt, if any, will be
// augmented per-iteration with the current iteration count and stop word instruction.
//
// If IterativeConfig.MaxIterations is 0, it defaults to 5.
func NewIterativeLoop(iterCfg IterativeConfig, opts ...Option) *IterativeLoop {
	return &IterativeLoop{
		opts:    opts,
		iterCfg: iterCfg,
	}
}

// Execute runs the agent loop iteratively. Each iteration creates a brand new
// AgentLoop with fresh context, executes a single turn with the given input, and
// records the result. The loop stops when MaxIterations is reached or the stop word
// is found in the response.
//
// Errors from individual iterations are recorded in IterationResult.Err and the
// loop continues to the next iteration rather than aborting.
func (il *IterativeLoop) Execute(ctx context.Context, input ExecuteInput) (IterativeResult, error) {
	result := IterativeResult{}

	maxIter := il.iterCfg.MaxIterations
	if maxIter <= 0 {
		maxIter = 5
	}

	// Extract the base system prompt from the provided options so we can augment
	// it per-iteration. The last WithSystemPrompt wins, matching agentloop.New behaviour.
	tmpCfg := AgentLoopConfig{}
	for _, opt := range il.opts {
		opt(&tmpCfg)
	}
	baseSystemPrompt := tmpCfg.SystemPrompt

	for i := 1; i <= maxIter; i++ {
		// Build the iteration-specific system prompt.
		iterPrompt := il.buildIterationPrompt(i, maxIter, baseSystemPrompt)

		// Build options: copy all original opts then append the iteration-specific
		// system prompt (overrides any WithSystemPrompt in il.opts) and, if
		// configured, the context pressure notifier.
		iterOpts := make([]Option, 0, len(il.opts)+2)
		iterOpts = append(iterOpts, il.opts...)
		iterOpts = append(iterOpts, WithSystemPrompt(iterPrompt))
		if il.iterCfg.PressureThreshold > 0 {
			iterOpts = append(iterOpts, WithContextPressureNotifier(
				il.iterCfg.PressureThreshold, il.iterCfg.PressureMessage))
		}

		// Create a fresh AgentLoop — no shared history with previous iterations.
		loop, err := New(iterOpts...)
		if err != nil {
			return result, fmt.Errorf("iteration %d: failed to create loop: %w", i, err)
		}

		execResult, execErr := loop.Execute(ctx, input)
		iterResult := IterationResult{
			Iteration: i,
			Text:      execResult.Text(),
			Err:       execErr,
		}
		result.Iterations = append(result.Iterations, iterResult)

		if execErr != nil {
			// Record the error and continue to the next iteration.
			continue
		}

		// Check for stop word completion (case-sensitive containment).
		if il.iterCfg.StopWord != "" && strings.Contains(execResult.Text(), il.iterCfg.StopWord) {
			result.Completed = true
			break
		}
	}

	return result, nil
}

// buildIterationPrompt appends the iteration annotation (and optional stop word
// instruction) to the base system prompt.
func (il *IterativeLoop) buildIterationPrompt(iter, maxIter int, basePrompt string) string {
	annotation := fmt.Sprintf("You are on iteration %d of %d.", iter, maxIter)
	if il.iterCfg.StopWord != "" {
		annotation += fmt.Sprintf(" When you are finished, end your response with %s.", il.iterCfg.StopWord)
	}
	if basePrompt == "" {
		return annotation
	}
	return basePrompt + "\n\n" + annotation
}
