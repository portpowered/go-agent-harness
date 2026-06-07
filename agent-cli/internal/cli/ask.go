package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/portpowered/agent-cli/internal/agent"
	"github.com/portpowered/agent-cli/internal/flags"
	"github.com/portpowered/agent-cli/internal/input"
	"github.com/portpowered/agent-cli/internal/services"
	"github.com/spf13/cobra"
)

// isStdinPiped returns true when stdin has been redirected/piped rather than
// connected to a terminal. In tests this is triggered by calling SetIn on the
// root command; in production it is triggered by a shell pipe or redirect.
func isStdinPiped(cmd *cobra.Command) bool {
	in := cmd.InOrStdin()
	if in != os.Stdin {
		// SetIn was called (e.g. in tests) — treat as piped.
		return true
	}
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) == 0
}

// AskCommand wraps the ask subcommand for one-shot queries.
type AskCommand struct {
	executor    *agent.Executor
	askFlags    *flags.AskFlags
	loopFlags   *flags.LoopFlags
	globalFlags *flags.GlobalFlags
}

// NewAskCommand creates the AskCommand with the given dependencies.
func NewAskCommand(executor *agent.Executor, askFlags *flags.AskFlags, loopFlags *flags.LoopFlags, globalFlags *flags.GlobalFlags) *AskCommand {
	return &AskCommand{executor: executor, askFlags: askFlags, loopFlags: loopFlags, globalFlags: globalFlags}
}

// Generate returns the cobra command for ask.
func (c *AskCommand) Generate() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ask [prompt] [files...]",
		Short: "Ask the agent a question and get a response",
		Long:  "One-shot queries. Pass a prompt and optional file paths for multimodal input.\nText can also be piped via stdin: echo \"question\" | agent ask",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			argPrompt, filePaths := input.ParseAskArgs(args)

			var stdinReader io.Reader
			if isStdinPiped(cmd) {
				stdinReader = cmd.InOrStdin()
			}

			execInput, err := services.BuildExecuteInput(stdinReader, argPrompt, filePaths)
			if err != nil {
				return err
			}

			cfg := services.BuildAgentConfigFromFlags(c.globalFlags, c.askFlags, nil, "")
			cfg.StderrWriter = cmd.ErrOrStderr()

			if c.loopFlags.Loop {
				maxIter := c.loopFlags.MaxIterations
				if maxIter <= 0 {
					maxIter = 5
				}
				loopCfg := agent.IterativeLoopConfig{
					MaxIterations:            maxIter,
					StopWord:                 c.loopFlags.StopWord,
					ContextPressureThreshold: c.loopFlags.ContextPressureThreshold,
					ContextPressureMessage:   c.loopFlags.ContextPressureMessage,
					TraceID:                  c.loopFlags.TraceID,
				}
				result, loopErr := c.executor.RunIterativeLoop(cmd.Context(), cfg, loopCfg, execInput, cmd.OutOrStdout())
				if loopErr != nil {
					return fmt.Errorf("loop execution failed: %w", loopErr)
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "\n[Loop complete: %d iteration(s), completed: %v, trace: %s]\n",
					len(result.Iterations), result.Completed, result.TraceID); err != nil {
					return fmt.Errorf("write loop summary: %w", err)
				}
				return nil
			}

			_, err = c.executor.RunAsk(cmd.Context(), cfg, execInput, cmd.OutOrStdout())
			if err != nil {
				return fmt.Errorf("execution failed: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&c.askFlags.Stream, "stream", false, "Stream response tokens")
	cmd.Flags().StringVar(&c.askFlags.SystemPrompt, "system-prompt", "", "Path to system prompt file")
	cmd.Flags().BoolVar(&c.askFlags.ContinueLastSession, "continue-last-session", false, "Continue from the most recent session")
	cmd.Flags().StringVar(&c.askFlags.SessionID, "session-id", "", "Specific session ID to continue")
	cmd.Flags().StringVar(&c.askFlags.Model, "model", "", "Model ID (overrides config)")
	cmd.Flags().StringVar(&c.askFlags.Provider, "provider", "", "Provider ID (overrides config)")
	cmd.Flags().BoolVar(&c.askFlags.ShowToolUse, "show-tool-use", false, "Show tool invocations in output")
	cmd.Flags().StringVar(&c.askFlags.APIKey, "api-key", "", "API key (overrides config)")
	cmd.Flags().StringVar(&c.askFlags.BaseURL, "base-url", "", "Base URL for the provider (required with --provider local if no local config exists)")
	cmd.Flags().StringVar(&c.askFlags.RecordCapturePath, "record", "", "Record LLM HTTP traffic to file for replay")
	cmd.Flags().StringVar(&c.askFlags.ReplayCapturePath, "replay", "", "Replay LLM requests from a captured file (no live API)")
	cmd.Flags().BoolVar(&c.askFlags.OutputReasoningTokens, "output-reasoning-tokens", false, "Emit reasoning/thinking tokens to stdout when streaming")
	cmd.Flags().BoolVar(&c.askFlags.NoSystemInformation, "no-system-information", false, "Disable injection of runtime system info (OS, cwd, model, date) into the system prompt")
	cmd.Flags().BoolVar(&c.askFlags.OutputJSON, "output-json", false, "Output response as JSON; with --stream emits one NDJSON delta event per line")
	cmd.Flags().StringVar(&c.askFlags.OutputModality, "output-modality", "", "Output modality: text, image, audio, video, embedding (raw binary output for non-text modalities)")
	cmd.Flags().StringVar(&c.askFlags.ModelConfig, "model-config", "", "Model-specific config as JSON string (e.g. '{\"aspect_ratio\":\"16:9\",\"duration\":6}')")

	cmd.Flags().BoolVar(&c.loopFlags.Loop, "loop", false, "Enable iterative loop mode (re-instantiates fresh sessions up to --max-iterations)")
	cmd.Flags().IntVar(&c.loopFlags.MaxIterations, "max-iterations", 5, "Maximum number of loop iterations (requires --loop)")
	cmd.Flags().StringVar(&c.loopFlags.StopWord, "stop-word", "", "Stop the loop when this word appears in the response (requires --loop)")
	cmd.Flags().Float64Var(&c.loopFlags.ContextPressureThreshold, "context-pressure-threshold", 0.8, "Context pressure threshold 0-1 that triggers a context-full warning (requires --loop)")
	cmd.Flags().StringVar(&c.loopFlags.ContextPressureMessage, "context-pressure-message", "", "Custom warning message when context pressure threshold is exceeded (requires --loop)")
	cmd.Flags().StringVar(&c.loopFlags.TraceID, "trace-id", "", "Resume an existing loop run by trace ID (requires --loop)")

	return cmd
}
