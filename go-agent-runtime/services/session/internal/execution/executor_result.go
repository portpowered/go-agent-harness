package agent

// This file owns execution result handling: turn output presentation, refusal and modality reporting, session persistence, and recorder flushing.

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ExecuteStreamingTurn starts a single agent turn in streaming mode and returns
// the event stream directly. The caller is responsible for draining the stream,
// calling stream.Close() when done, and then calling SaveSession and
// FlushRecorder as needed. Presentation (writing to out) is the caller's
// responsibility.
func (e *Executor) ExecuteStreamingTurn(ctx context.Context, runData *RunData, execInput agentloop.ExecuteInput, cfg *Config) (agentloop.Stream, error) {
	execInput.OutputReasoningStream = cfg.OutputReasoningTokens
	streamResult, err := runData.Loop.ExecuteStreaming(ctx, execInput)
	if err != nil {
		return nil, err
	}
	return streamResult.EventStream, nil
}

// ExecuteOneTurn runs one turn and retains its structured messages. The optional
// writer is an internal text compatibility sink; hosts render typed results.
// A failed tool batch ends the loop's turn with a nonterminal tool-execution
// diagnostic instead of a final answer; ExecuteOneTurn returns that tool error
// rather than reporting an empty successful turn.
func (e *Executor) ExecuteOneTurn(ctx context.Context, runData *RunData, execInput agentloop.ExecuteInput, cfg *Config, out io.Writer) (string, error) {
	execInput.OutputReasoningStream = cfg.OutputReasoningTokens
	result, err := runData.Loop.Execute(ctx, execInput)
	runData.producedMessages = append(runData.producedMessages, result.Messages...)
	if err != nil {
		return "", err
	}
	if toolErr := toolExecutionError(result.Deltas); toolErr != nil {
		return "", toolErr
	}
	text := result.Text()
	if out != nil {
		if _, err := fmt.Fprintln(out, text); err != nil {
			return "", fmt.Errorf("write output: %w", err)
		}
	}
	return text, nil
}

// toolExecutionError returns the error of the turn's tool-execution
// diagnostic, if the turn emitted one.
func toolExecutionError(deltas []messages.StreamMessage) error {
	for _, delta := range deltas {
		if delta.Type != messages.StreamTypeError {
			continue
		}
		value, ok := delta.Value.(*messages.ErrorValue)
		if !ok || value.Classification != messages.ToolExecutionErrorClassification {
			continue
		}
		if value.Err != nil {
			return value.Err
		}
		return errors.New(value.Message)
	}
	return nil
}

// SaveSession saves the conversation history to the session storage.
func (e *Executor) SaveSession(ctx context.Context, runData *RunData) error {
	if runData.SessionID == "" {
		return nil
	}
	history := runData.Loop.GetConversationHistory()
	if len(history) > 0 {
		if err := runData.sessionManager.Save(ctx, runData.SessionID, history); err != nil {
			return fmt.Errorf("save session: %w", err)
		}
	}
	return nil
}

// FlushRecorder flushes the recorder to file if recording is enabled.
func (e *Executor) FlushRecorder(runData *RunData, recordPath string) error {
	if runData.Capture != nil && recordPath != "" {
		if err := runData.Capture.FlushToFile(recordPath); err != nil {
			return fmt.Errorf("failed to flush captures: %w", err)
		}
	}
	return nil
}
