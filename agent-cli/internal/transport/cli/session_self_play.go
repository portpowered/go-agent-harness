package cli

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	serviceSelfPlay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
	"github.com/spf13/cobra"
)

// SessionSelfPlayCommand translates CLI input into the runtime-owned request.
type SessionSelfPlayCommand struct {
	globalFlags *flags.GlobalFlags
	service     serviceSelfPlay.Service
}

// NewSessionSelfPlayCommand creates the transport adapter for the self-play service.
func NewSessionSelfPlayCommand(globalFlags *flags.GlobalFlags, service serviceSelfPlay.Service) *SessionSelfPlayCommand {
	return &SessionSelfPlayCommand{globalFlags: globalFlags, service: service}
}

// Generate returns the cobra command for the bounded self-play runner.
func (c *SessionSelfPlayCommand) Generate() *cobra.Command {
	var apiKey string
	var provider string
	var model string
	var baseURL string
	var outputDir string
	var maxDuration string
	var maxTurns string

	cmd := &cobra.Command{
		Use:   "self-play",
		Short: "Run two fixed-persona live agents through the self-play service",
		Long: "Run a bounded live self-play conversation through the runtime service.\n\n" +
			"Only emitted raw PCM16 audio crosses between agents; tools and transcript/text bridging are disabled.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if c == nil || c.service == nil {
				return errors.New("self-play service is required")
			}
			duration, err := parseSelfPlayDuration(maxDuration)
			if err != nil {
				return err
			}
			turns, err := parseSelfPlayTurns(maxTurns)
			if err != nil {
				return err
			}
			configDir := ""
			if c.globalFlags != nil {
				configDir = c.globalFlags.ConfigDir()
			}
			resolvedAPIKey, resolvedBaseURL, err := resolveSelfPlayProviderInputs(configDir, apiKey, baseURL)
			if err != nil {
				return err
			}
			result, runErr := c.service.Run(cmd.Context(), serviceSelfPlay.Request{
				APIKey:      resolvedAPIKey,
				OutputDir:   outputDir,
				Provider:    provider,
				Model:       model,
				BaseURL:     resolvedBaseURL,
				MaxDuration: duration,
				MaxTurns:    turns,
			})
			if result.StopReason == "" {
				return runErr
			}
			_, writeErr := fmt.Fprintf(cmd.OutOrStdout(), "self-play stopped: reason=%s customer_turns=%d assistant_turns=%d\n", result.StopReason, result.Customer.CompletedTurns, result.Assistant.CompletedTurns)
			return errors.Join(runErr, writeErr)
		},
	}
	cmd.Flags().StringVar(&apiKey, "api-key", "", "OpenAI Realtime API key; configuration is used when omitted")
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "Empty directory for this self-play run (required)")
	cmd.Flags().StringVar(&provider, "provider", "", "Realtime provider (default: service-defined)")
	cmd.Flags().StringVar(&model, "model", "", "Realtime model (default: service-defined)")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "Optional Realtime endpoint override; configuration is used when omitted")
	cmd.Flags().StringVar(&maxDuration, "max-duration", "", "Positive maximum run duration (default: service-defined)")
	cmd.Flags().StringVar(&maxTurns, "max-turns", "", "Positive completed-turn target per side (default: service-defined)")
	return cmd
}

func resolveSelfPlayProviderInputs(configDir, apiKey, baseURL string) (string, string, error) {
	if apiKey != "" && baseURL != "" {
		return apiKey, baseURL, nil
	}
	storage, err := config.NewDefaultConfigStorage(configDir)
	if err != nil {
		return "", "", fmt.Errorf("self-play live session configuration: initialize config: %w", err)
	}
	loaded, err := storage.Load()
	if err != nil {
		return "", "", fmt.Errorf("self-play live session configuration: load config: %w", err)
	}
	if openAI := loaded.Model.OpenAI; openAI != nil {
		if apiKey == "" {
			apiKey = openAI.APIKey
		}
		if baseURL == "" {
			baseURL = openAI.BaseURL
		}
	}
	return apiKey, baseURL, nil
}

func parseSelfPlayDuration(value string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid --max-duration %q: %w", value, err)
	}
	return duration, nil
}

func parseSelfPlayTurns(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	turns, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid --max-turns %q: %w", value, err)
	}
	return turns, nil
}
