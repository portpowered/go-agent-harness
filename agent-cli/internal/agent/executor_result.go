package agent

// This file owns execution result handling: turn output presentation, refusal and modality reporting, session persistence, and recorder flushing.

import (
	"context"
	"fmt"
	"io"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/execctx"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/output"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ExecuteStreamingTurn starts a single agent turn in streaming mode and returns
// the event stream directly. The caller is responsible for draining the stream,
// calling stream.Close() when done, and then calling SaveSession, FlushRecorder,
// and CloseLogger as needed. Presentation (writing to out) is the caller's responsibility.
func (e *Executor) ExecuteStreamingTurn(ctx context.Context, runData *RunData, execInput agentloop.ExecuteInput, cfg *Config) (agentloop.Stream, error) {
	ctx = execctx.WithWorkspaceDir(ctx, runData.SessionManager.WorkspaceDir())
	ctx = execctx.WithConfigDir(ctx, runData.ConfigDir)
	execInput.OutputReasoningStream = cfg.OutputReasoningTokens
	streamResult, err := runData.Loop.ExecuteStreaming(ctx, execInput)
	if err != nil {
		return nil, err
	}
	return streamResult.EventStream, nil
}

// ExecuteOneTurn runs a single agent turn and writes the result to out.
// When cfg.Stream is true, it uses ExecuteStreamingTurn and handles presentation
// by draining the stream to out (or JSON lines when cfg.OutputJSON is set).
func (e *Executor) ExecuteOneTurn(ctx context.Context, runData *RunData, execInput agentloop.ExecuteInput, cfg *Config, out io.Writer) (result string, execErr error) {
	ctx = execctx.WithWorkspaceDir(ctx, runData.SessionManager.WorkspaceDir())
	ctx = execctx.WithConfigDir(ctx, runData.ConfigDir)
	streaming := cfg.Stream
	outputJSON := cfg.OutputJSON
	execInput.OutputReasoningStream = cfg.OutputReasoningTokens

	binaryModality := cfg.OutputModality != "" && cfg.OutputModality != "text"

	if streaming {
		stream, err := e.ExecuteStreamingTurn(ctx, runData, execInput, cfg)
		if err != nil {
			return "", err
		}
		defer func() { _ = stream.Close() }()
		if binaryModality && out != nil {
			n, writeErr := output.WriteBinaryModalityStream(out, stream, cfg.OutputModality)
			if writeErr != nil {
				return "", writeErr
			}
			if n == 0 {
				if _, err := fmt.Fprintf(cfg.Stderr(), "no %s content in response\n", cfg.OutputModality); err != nil {
					return "", fmt.Errorf("write binary modality warning: %w", err)
				}
			}
		} else if outputJSON {
			for stream.HasNext() {
				evt := stream.Response()
				// Route refusal events to stderr as structured JSON, not to stdout NDJSON stream.
				if evt.Type == messages.StreamTypeRefusal {
					if v, ok := evt.Value.(*messages.RefusalValue); ok && v.Message != "" {
						if writeErr := output.WriteRefusalJSON(cfg.Stderr(), v.Message, cfg.Model); writeErr != nil {
							return "", writeErr
						}
					}
					continue
				}
				if writeErr := output.WriteStreamEventJSON(out, evt); writeErr != nil {
					execErr = writeErr
					return "", execErr
				}
			}
		} else if out != nil {
			execErr = output.WriteEventStreamToWriter(out, cfg.Stderr(), stream)
		}
		return "", execErr
	}

	execResult, err := runData.Loop.Execute(ctx, execInput)
	if err != nil {
		return "", err
	}

	// Surface any refusals from assistant messages to stderr.
	for _, m := range execResult.Messages {
		if m.Role == messages.RoleAssistant && m.Refusal != "" {
			if outputJSON {
				if writeErr := output.WriteRefusalJSON(cfg.Stderr(), m.Refusal, cfg.Model); writeErr != nil {
					return "", writeErr
				}
			} else {
				output.WriteRefusal(cfg.Stderr(), m.Refusal)
			}
		}
	}

	if binaryModality && out != nil {
		n, writeErr := output.WriteBinaryModalityMessages(out, execResult.Messages, cfg.OutputModality)
		if writeErr != nil {
			return "", writeErr
		}
		if n == 0 {
			if _, err := fmt.Fprintf(cfg.Stderr(), "no %s content in response\n", cfg.OutputModality); err != nil {
				return "", fmt.Errorf("write binary modality warning: %w", err)
			}
		}
		return "", nil
	}
	if outputJSON {
		return "", output.WriteMessagesJSON(out, execResult.Messages)
	}
	result = execResult.Text()
	if out != nil {
		if _, err := fmt.Fprintln(out, result); err != nil {
			return "", fmt.Errorf("write output: %w", err)
		}
	}
	return result, nil
}

// SaveSession saves the conversation history to the session storage.
func (e *Executor) SaveSession(runData *RunData) error {
	if runData.SessionID == "" {
		return nil
	}
	history := runData.Loop.GetConversationHistory()
	if len(history) > 0 {
		if err := runData.SessionManager.Save(runData.SessionID, history); err != nil {
			return fmt.Errorf("save session: %w", err)
		}
	}
	return nil
}

// FlushRecorder flushes the recorder to file if recording is enabled.
func (e *Executor) FlushRecorder(runData *RunData, recordPath string) error {
	if runData.Recorder != nil && recordPath != "" {
		if err := runData.Recorder.FlushToFile(recordPath); err != nil {
			return fmt.Errorf("failed to flush captures: %w", err)
		}
	}
	return nil
}
