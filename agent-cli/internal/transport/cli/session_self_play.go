package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	runtimeSelfPlay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
	"github.com/spf13/cobra"
)

// SessionSelfPlayCommand exposes the self-play transport and presentation
// surface under `yui session self-play`. Runtime policy and mutable state stay
// behind the service contract.
type SessionSelfPlayCommand struct {
	globalFlags *flags.GlobalFlags
	run         runtimeSelfPlay.Service
}

type sessionSelfPlayRunnerFunc func(context.Context, runtimeSelfPlay.Request) (runtimeSelfPlay.Result, error)

func (f sessionSelfPlayRunnerFunc) Run(ctx context.Context, request runtimeSelfPlay.Request) (runtimeSelfPlay.Result, error) {
	return f(ctx, request)
}

// NewSessionSelfPlayCommand creates the self-play command from the runtime
// service contract. A nil service is useful for parser tests that install a
// runner with SetRunner.
func NewSessionSelfPlayCommand(globalFlags *flags.GlobalFlags, service runtimeSelfPlay.Service) *SessionSelfPlayCommand {
	return &SessionSelfPlayCommand{globalFlags: globalFlags, run: service}
}

// SetRunner replaces the service runner used by this command. It is intended
// for hermetic command tests and does not change the production default.
func (c *SessionSelfPlayCommand) SetRunner(runner func(context.Context, io.Writer, runtimeSelfPlay.Request) error) {
	if c != nil && runner != nil {
		c.run = sessionSelfPlayRunnerFunc(func(ctx context.Context, request runtimeSelfPlay.Request) (runtimeSelfPlay.Result, error) {
			return runtimeSelfPlay.Result{}, runner(ctx, io.Discard, request)
		})
	}
}

// Generate returns the cobra command for the bounded self-play runner.
func (c *SessionSelfPlayCommand) Generate() *cobra.Command {
	var apiKey string
	var provider string
	var model string
	var baseURL string
	var outputDir string
	var maxDuration time.Duration
	var maxTurns int

	cmd := &cobra.Command{
		Use:   "self-play",
		Short: "Run two fixed-persona live agents through a PCM16 audio bridge",
		Long: "Run the self-play harness with two continuously open realtime sessions.\n\n" +
			"The service owns fixed personas, the opening seed, admission, turn bounds, and evidence.\n" +
			"Only emitted raw PCM16 audio crosses between agents; tools and transcript/text bridging are disabled.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if c == nil || c.run == nil {
				return errors.New("self-play service is required")
			}
			resolvedKey, err := c.resolveAPIKey(apiKey)
			if err != nil {
				return err
			}
			result, runErr := c.run.Run(cmd.Context(), runtimeSelfPlay.Request{
				APIKey:      resolvedKey,
				OutputDir:   outputDir,
				Provider:    provider,
				Model:       model,
				BaseURL:     baseURL,
				MaxDuration: maxDuration,
				MaxTurns:    maxTurns,
			})
			if result.StopReason != "" {
				if _, writeErr := fmt.Fprintf(cmd.OutOrStdout(), "self-play stopped: reason=%s customer_turns=%d assistant_turns=%d\n", result.StopReason, result.Customer.CompletedTurns, result.Assistant.CompletedTurns); writeErr != nil {
					runErr = errors.Join(runErr, fmt.Errorf("write self-play result: %w", writeErr))
				}
			}
			return runErr
		},
	}
	cmd.Flags().StringVar(&apiKey, "api-key", "", "OpenAI API key; may also come from the configured AGENT_MODEL__OPENAI__API_KEY")
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "Empty directory for this self-play run (required)")
	cmd.Flags().StringVar(&provider, "provider", "", "Realtime provider (service default applies)")
	cmd.Flags().StringVar(&model, "model", "", "Realtime model (service default applies)")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "Optional realtime WebSocket endpoint override")
	cmd.Flags().DurationVar(&maxDuration, "max-duration", 0, "Positive maximum run duration (service default applies)")
	cmd.Flags().IntVar(&maxTurns, "max-turns", 0, "Positive completed-turn target per side (service default applies)")
	return cmd
}

func (c *SessionSelfPlayCommand) resolveAPIKey(explicit string) (string, error) {
	if value := strings.TrimSpace(explicit); value != "" {
		return value, nil
	}
	if value := strings.TrimSpace(os.Getenv("AGENT_MODEL__OPENAI__API_KEY")); value != "" {
		return value, nil
	}
	configDir := ""
	if c != nil && c.globalFlags != nil {
		configDir = c.globalFlags.ConfigDir()
	}
	storage, err := config.NewDefaultConfigStorage(configDir)
	if err != nil {
		return "", fmt.Errorf("self-play config: %w", err)
	}
	loaded, err := storage.Load()
	if err != nil {
		return "", fmt.Errorf("self-play config: %w", err)
	}
	if loaded.Model.OpenAI == nil {
		return "", nil
	}
	return strings.TrimSpace(loaded.Model.OpenAI.APIKey), nil
}
