package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/portpowered/agent-cli/internal/agent"
	"github.com/portpowered/agent-cli/internal/audio"
	"github.com/portpowered/agent-cli/internal/flags"
	"github.com/portpowered/agent-cli/internal/services"
	"github.com/portpowered/agent-cli/internal/session"
	"github.com/portpowered/go-agent-loop/pkg/agentloop"
	"github.com/spf13/cobra"
)

// ChatCommand wraps the chat subcommand for interactive conversations.
type ChatCommand struct {
	executor    *agent.Executor
	askFlags    *flags.AskFlags
	loopFlags   *flags.LoopFlags
	chatFlags   *flags.ChatFlags
	globalFlags *flags.GlobalFlags
}

// NewChatCommand creates the ChatCommand with the given dependencies.
func NewChatCommand(executor *agent.Executor, askFlags *flags.AskFlags, loopFlags *flags.LoopFlags, chatFlags *flags.ChatFlags, globalFlags *flags.GlobalFlags) *ChatCommand {
	return &ChatCommand{executor: executor, askFlags: askFlags, loopFlags: loopFlags, chatFlags: chatFlags, globalFlags: globalFlags}
}

// Generate returns the cobra command for chat.
func (c *ChatCommand) Generate() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "chat",
		Short: "Start an interactive chat session with the agent",
		Long:  "Interactive multi-turn conversation. Type 'exit' or 'quit' to leave.\nWith --activate-audio-in the agent listens on the default microphone instead of stdin.\nWith --loop, runs in iterative mode with user steering between iterations.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if c.loopFlags.Loop {
				return c.runLoopChat(cmd)
			}
			if c.chatFlags.ActivateAudioIn {
				src, err := audio.NewMicrophoneSource()
				if err != nil {
					return fmt.Errorf("open microphone: %w", err)
				}
				return RunChatWithAudio(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), c.executor, c.globalFlags, c.askFlags, src)
			}
			chatService := services.NewChatService(c.executor, c.globalFlags, c.askFlags)
			return chatService.Run(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}

	cmd.Flags().BoolVar(&c.chatFlags.ActivateAudioIn, "activate-audio-in", false, "Enable audio input from the default microphone")
	cmd.Flags().BoolVar(&c.chatFlags.ActivateAudioOut, "activate-audio-out", false, "Enable audio output")

	cmd.Flags().BoolVar(&c.loopFlags.Loop, "loop", false, "Enable iterative loop mode (re-instantiates fresh sessions up to --max-iterations)")
	cmd.Flags().IntVar(&c.loopFlags.MaxIterations, "max-iterations", 5, "Maximum number of loop iterations (requires --loop)")
	cmd.Flags().StringVar(&c.loopFlags.StopWord, "stop-word", "", "Stop the loop when this word appears in the response (requires --loop)")
	cmd.Flags().Float64Var(&c.loopFlags.ContextPressureThreshold, "context-pressure-threshold", 0.8, "Context pressure threshold 0-1 that triggers a context-full warning (requires --loop)")
	cmd.Flags().StringVar(&c.loopFlags.ContextPressureMessage, "context-pressure-message", "", "Custom warning message when context pressure threshold is exceeded (requires --loop)")
	cmd.Flags().StringVar(&c.loopFlags.TraceID, "trace-id", "", "Resume an existing loop run by trace ID (requires --loop)")

	return cmd
}

// runLoopChat runs an interactive loop chat session. After each iteration the user
// can provide steering input (or press Enter to continue with the same task).
//
// Ctrl+C during an iteration cancels that iteration and drops back to the interactive
// prompt — the user can steer and continue, or type "exit" to exit (saving the trace
// for later resume via --trace-id).
func (c *ChatCommand) runLoopChat(cmd *cobra.Command) error {
	maxIter := c.loopFlags.MaxIterations
	if maxIter <= 0 {
		maxIter = 5
	}

	out := cmd.OutOrStdout()
	in := cmd.InOrStdin()
	ctx := cmd.Context()

	fmt.Fprintf(out, "Port OS Agent Loop Chat (up to %d iterations)\n", maxIter)
	fmt.Fprintln(out, "---")

	cfg := services.BuildAgentConfigFromFlags(c.globalFlags, c.askFlags, nil, "")

	// Get session storage for trace management.
	sessionStorage, err := c.executor.GetSessionStorage(cfg)
	if err != nil {
		return fmt.Errorf("get session storage: %w", err)
	}

	// Create or load trace record.
	var trace session.TraceRecord
	if c.loopFlags.TraceID != "" {
		existing, loadErr := sessionStorage.LoadTrace(c.loopFlags.TraceID)
		if loadErr != nil {
			return fmt.Errorf("load trace %s: %w", c.loopFlags.TraceID, loadErr)
		}
		if existing != nil {
			trace = *existing
		}
	}

	scanner := bufio.NewScanner(in)

	// Compute start iteration and restore config when resuming an existing trace.
	startIter := 1
	var currentPrompt string
	if trace.TraceID != "" {
		// Restore original loop config from the trace so resume uses the same parameters.
		maxIter = trace.Config.MaxIterations
		c.loopFlags.StopWord = trace.Config.StopWord
		currentPrompt = trace.Config.Prompt

		if len(trace.Iterations) > 0 {
			lastIter := trace.Iterations[len(trace.Iterations)-1]
			if lastIter.Status == session.IterationStatusInterrupted {
				// Restart the interrupted iteration fresh — discard its partial record.
				startIter = lastIter.Iteration
				trace.Iterations = trace.Iterations[:len(trace.Iterations)-1]
			} else {
				startIter = lastIter.Iteration + 1
			}
		}
		fmt.Fprintf(out, "[Resuming trace %s from iteration %d/%d]\n", trace.TraceID, startIter, maxIter)
	} else {
		// Get initial task prompt from user.
		fmt.Fprint(out, "Enter your task: ")
		if !scanner.Scan() {
			return nil
		}
		currentPrompt = strings.TrimSpace(scanner.Text())
		if currentPrompt == "" {
			return fmt.Errorf("no task provided")
		}

		trace = session.TraceRecord{
			TraceID: sessionStorage.NewTraceID(),
			Status:  session.TraceStatusRunning,
			Config: session.TraceConfig{
				MaxIterations: maxIter,
				StopWord:      c.loopFlags.StopWord,
				Prompt:        currentPrompt,
			},
		}
		if saveErr := sessionStorage.SaveTrace(trace); saveErr != nil {
			return fmt.Errorf("save trace: %w", saveErr)
		}
	}
	fmt.Fprintf(out, "Trace ID: %s\n", trace.TraceID)

	for i := startIter; i <= maxIter; i++ {
		fmt.Fprintf(out, "\n--- Iteration %d/%d ---\n", i, maxIter)

		iterCfg := *cfg
		iterCfg.SessionID = ""
		iterCfg.ContinueLastSession = false
		iterCfg.InitialHistory = nil
		iterCfg.SystemPromptSuffix = agent.BuildIterationAnnotation(i, maxIter, c.loopFlags.StopWord)

		execInput := agentloop.NewExecuteInput(currentPrompt)

		// Per-iteration signal context: captures Ctrl+C and cancels only this iteration.
		iterCtx, iterCancel := signal.NotifyContext(ctx, os.Interrupt)
		text, runErr := c.executor.RunAsk(iterCtx, &iterCfg, execInput, out)
		// Check for interrupt BEFORE calling iterCancel so iterCtx.Err() is accurate.
		interrupted := iterCtx.Err() != nil
		iterCancel() // restore default signal behaviour between iterations

		var iterStatus session.IterationStatus
		if interrupted {
			iterStatus = session.IterationStatusInterrupted
		} else if runErr != nil {
			iterStatus = session.IterationStatusFailed
		} else {
			iterStatus = session.IterationStatusCompleted
		}
		trace.CurrentIteration = i
		trace.Iterations = append(trace.Iterations, session.IterationTrace{
			Iteration: i,
			Status:    iterStatus,
		})
		_ = sessionStorage.SaveTrace(trace)

		if interrupted {
			// Drop back to the interactive prompt so the user can steer the next iteration
			// or exit and resume later.
			fmt.Fprintf(out, "\n[Iteration %d interrupted. Enter steering for next iteration, or 'exit' to quit (resume later with --trace-id %s)]: ", i, trace.TraceID)
			if !scanner.Scan() {
				// EOF — mark trace interrupted and exit.
				trace.Status = session.TraceStatusInterrupted
				_ = sessionStorage.SaveTrace(trace)
				return nil
			}
			input := strings.TrimSpace(scanner.Text())
			if strings.ToLower(input) == "exit" {
				trace.Status = session.TraceStatusInterrupted
				_ = sessionStorage.SaveTrace(trace)
				fmt.Fprintf(out, "[Loop interrupted. Resume with: --loop --trace-id %s]\n", trace.TraceID)
				return nil
			}
			if input != "" {
				currentPrompt = input
			}
			// Continue to the next iteration (interrupted iteration is not retried here;
			// use --trace-id resume to restart it fresh).
			continue
		}

		if runErr != nil {
			fmt.Fprintf(out, "\n[Iteration %d error: %v]\n", i, runErr)
		}

		// Check for stop word.
		if runErr == nil && c.loopFlags.StopWord != "" && strings.Contains(text, c.loopFlags.StopWord) {
			fmt.Fprintf(out, "\n[Completion detected in iteration %d]\n", i)
			trace.Status = session.TraceStatusCompleted
			_ = sessionStorage.SaveTrace(trace)
			return nil
		}

		if i == maxIter {
			break
		}

		// Prompt user for steering input for the next iteration.
		fmt.Fprintf(out, "\n[Iteration %d complete. Enter steering for iteration %d (or press Enter to continue with same task)]: ", i, i+1)
		if scanner.Scan() {
			if steering := strings.TrimSpace(scanner.Text()); steering != "" {
				currentPrompt = steering
			}
		}
	}

	trace.Status = session.TraceStatusCompleted
	_ = sessionStorage.SaveTrace(trace)

	fmt.Fprintf(out, "\n[Loop complete: %d iteration(s), trace: %s]\n", maxIter, trace.TraceID)
	return nil
}

// RunChatWithAudio runs an interactive audio chat session.
//
// It captures audio from src, uses energy-based VAD to detect utterances, and
// sends each complete utterance to the agent as a single PCM audio chunk.
// The loop exits when the context is cancelled, the source signals EOF, or an
// unrecoverable error occurs.
//
// This function is exported so it can be exercised directly in tests by
// injecting a mock AudioSource and agent.Executor.
func RunChatWithAudio(ctx context.Context, out, errOut io.Writer, executor *agent.Executor, globalFlags *flags.GlobalFlags, askFlags *flags.AskFlags, src audio.AudioSource) error {
	cfg := services.BuildAgentConfigFromFlags(globalFlags, askFlags, nil, "")
	sessionID, err := executor.NewChatSessionID(cfg)
	if err != nil {
		return fmt.Errorf("create chat session: %w", err)
	}
	defer src.Close()

	vad := audio.NewVAD(audio.DefaultVADConfig)
	pipeline := audio.NewPipeline(src, vad, audio.DefaultPipelineConfig)

	fmt.Fprintln(out, "Port OS Agent Chat - Audio Mode (Ctrl+C to exit)")
	fmt.Fprintln(out, "---")

	for {
		fmt.Fprintln(out, "\nListening...")
		samples, err := pipeline.ReadUtterance(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				fmt.Fprintln(out, "Goodbye!")
				return nil
			}
			if errors.Is(err, io.EOF) {
				fmt.Fprintln(out, "Goodbye!")
				return nil
			}
			fmt.Fprintf(errOut, "Audio pipeline error: %v\n", err)
			continue
		}

		fmt.Fprintln(out, "(speech detected, processing...)")

		execInput := agentloop.NewExecuteInput("")
		execInput.Audio = &agentloop.Audio{
			Samples:    samples,
			SampleRate: audio.SampleRate,
			Channels:   audio.Channels,
		}

		cfg := services.BuildAgentConfigFromFlags(globalFlags, askFlags, nil, sessionID)
		if _, err := executor.RunAskWithSession(ctx, sessionID, cfg, execInput, out); err != nil {
			fmt.Fprintf(errOut, "Error: %v\n", err)
		}
	}
}
