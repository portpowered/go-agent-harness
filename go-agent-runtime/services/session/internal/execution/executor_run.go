package agent

import (
	"context"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"io"
	"strings"
)

// DefaultMaxContinuationDepth is the maximum number of TODO queue re-invocations
// per turn when MaxContinuationDepth is not explicitly set.
const DefaultMaxContinuationDepth = 3

// DefaultContinuationNudgeMessage is the message enqueued in the TODO queue when
// the model stops early and continuationNudgeEnabled is true.
const DefaultContinuationNudgeMessage = "Please continue where you left off."

// executeWithContinuation runs ExecuteOneTurn and then drains the TODO queue,
// re-invoking inference for each dequeued message up to maxContinuationDepth.
// The stopWord, if non-empty, causes the loop to stop early when found in the
// result text. When the model config has ContinuationNudgeEnabled set, a nudge
// message is auto-enqueued whenever the model stops without a tool call or
// stop-word. It returns the final result text and any error.
func (e *Executor) executeWithContinuation(
	ctx context.Context,
	runData *RunData,
	input agentloop.ExecuteInput,
	cfg *Config,
	out io.Writer,
	stopWord string,
) (string, error) {
	maxDepth := cfg.MaxContinuationDepth
	if maxDepth <= 0 {
		maxDepth = DefaultMaxContinuationDepth
	}

	// Load continuation nudge settings from model config.
	nudgeEnabled, nudgeMsg := e.loadContinuationNudgeConfig()

	result, err := e.ExecuteOneTurn(ctx, runData, input, cfg, out)
	if err != nil {
		return result, err
	}

	// Check for stop word — if found, no continuation needed.
	if stopWord != "" && strings.Contains(result, stopWord) {
		return result, nil
	}

	// Auto-enqueue a continuation nudge if the model stopped early.
	if nudgeEnabled {
		maybeEnqueueContinuationNudge(runData, stopWord, nudgeMsg)
	}

	// Drain the TODO queue, re-invoking inference for each dequeued message.
	for depth := 0; depth < maxDepth; depth++ {
		msg, ok := runData.Loop.DequeueTodo()
		if !ok {
			break
		}

		continuationInput := agentloop.ExecuteInput{
			Message: msg,
		}
		result, err = e.ExecuteOneTurn(ctx, runData, continuationInput, cfg, out)
		if err != nil {
			return result, err
		}

		if stopWord != "" && strings.Contains(result, stopWord) {
			break
		}

		// Auto-enqueue again if the model still stopped early.
		if nudgeEnabled {
			maybeEnqueueContinuationNudge(runData, stopWord, nudgeMsg)
		}
	}

	return result, nil
}

func buildInferenceDefaultsForPenalty(rp float64) *messages.InferenceDefaults {
	if rp == 0 || rp == 1.0 {
		return nil
	}
	return &messages.InferenceDefaults{
		FrequencyPenalty: &rp,
	}
}

// continuationNudge returns the host-resolved continuation policy. It accepts
// no config or filesystem inputs so a resolved invocation cannot rediscover
// model behavior while running.
func (e *Executor) loadContinuationNudgeConfig() (bool, string) {
	if e == nil || !e.resolved || !e.resolvedModelPolicy.ContinuationNudgeEnabled {
		return false, ""
	}
	msg := e.resolvedModelPolicy.ContinuationNudgeMessage
	if msg == "" {
		msg = DefaultContinuationNudgeMessage
	}
	return true, msg
}

// maybeEnqueueContinuationNudge checks the last assistant message in the
// conversation history. If the message has no tool calls and the result does
// not contain the stop-word, it enqueues the nudge message in the TODO queue.
func maybeEnqueueContinuationNudge(runData *RunData, stopWord string, nudgeMsg string) {
	history := runData.Loop.GetConversationHistory()

	// Find the last assistant message.
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		if m.Role != messages.RoleAssistant {
			continue
		}
		// If the assistant used tool calls, this is not an early stop.
		if len(m.ToolCalls) > 0 {
			return
		}
		// If the stop-word appears in the assistant text, this is not an early stop.
		if stopWord != "" && strings.Contains(m.TextContent(), stopWord) {
			return
		}
		// Early stop detected — enqueue nudge.
		runData.Loop.EnqueueTodo(nudgeMsg)
		return
	}
}

// RunAsk runs a one-shot ask: builds the loop from cfg, executes one turn, saves session.
// When streaming, out receives all tokens (reasoning then text when cfg.OutputReasoningTokens is set).
func (e *Executor) RunAsk(ctx context.Context, cfg *Config, input agentloop.ExecuteInput, out io.Writer) (string, error) {
	text, _, err := e.RunAskDetailed(ctx, cfg, input, out)
	return text, err
}

// RunAskDetailed retains typed content so an embedding host can render any
// supported modality without the runtime choosing a presentation format.
func (e *Executor) RunAskDetailed(ctx context.Context, cfg *Config, input agentloop.ExecuteInput, out io.Writer) (string, []messages.Message, error) {
	if err := cfg.Validate(); err != nil {
		return "", nil, err
	}

	runData, err := e.BuildLoop(ctx, cfg)
	if err != nil {
		return "", nil, err
	}
	if err := e.validateOutputModality(cfg, runData); err != nil {
		return "", nil, err
	}
	if err := e.validateInputMimeTypes(cfg, runData, input); err != nil {
		return "", nil, err
	}

	result, execErr := e.executeWithContinuation(ctx, runData, input, cfg, out, "")

	if flushErr := e.FlushRecorder(runData, cfg.RecordCapturePath); flushErr != nil {
		if execErr != nil {
			execErr = fmt.Errorf("%w (flush: %w)", execErr, flushErr)
		} else {
			execErr = flushErr
		}
	}

	if execErr == nil {
		if saveErr := e.SaveSession(runData); saveErr != nil {
			return result, append([]messages.Message(nil), runData.producedMessages...), saveErr
		}
	}

	return result, append([]messages.Message(nil), runData.producedMessages...), execErr
}

// RunAskWithSession runs one turn using the given session ID: loads existing history, runs the loop, saves.
// Use for multi-turn chat. Pass empty sessionID to use normal ask behavior (session-id, continue-last-session, or new).
func (e *Executor) RunAskWithSession(ctx context.Context, sessionID string, cfg *Config, input agentloop.ExecuteInput, out io.Writer) (string, error) {
	if sessionID == "" {
		return e.RunAsk(ctx, cfg, input, out)
	}

	storage, err := e.getSessionStorage()
	if err != nil {
		return "", err
	}
	initialHistory, err := storage.Load(sessionID)
	if err != nil {
		return "", fmt.Errorf("load session %s: %w", sessionID, err)
	}
	if initialHistory == nil {
		initialHistory = []messages.Message{}
	}

	cfgWithHistory := *cfg
	cfgWithHistory.SessionID = sessionID
	cfgWithHistory.InitialHistory = initialHistory
	if err := cfgWithHistory.Validate(); err != nil {
		return "", err
	}

	runData, err := e.BuildLoop(ctx, &cfgWithHistory)
	if err != nil {
		return "", err
	}
	if err := e.validateInputMimeTypes(&cfgWithHistory, runData, input); err != nil {
		return "", err
	}

	result, execErr := e.executeWithContinuation(ctx, runData, input, &cfgWithHistory, out, "")

	if flushErr := e.FlushRecorder(runData, cfg.RecordCapturePath); flushErr != nil {
		if execErr != nil {
			execErr = fmt.Errorf("%w (flush: %w)", execErr, flushErr)
		} else {
			execErr = flushErr
		}
	}

	if execErr == nil {
		if saveErr := e.SaveSession(runData); saveErr != nil {
			return result, saveErr
		}
	}

	return result, execErr
}
