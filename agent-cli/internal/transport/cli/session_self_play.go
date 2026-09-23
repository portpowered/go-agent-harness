package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	serviceSelfPlay "github.com/portpowered/go-agent-harness/agent-cli/internal/services/selfplay"
	runtimeSelfPlay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
	"github.com/spf13/cobra"
)

// SessionSelfPlayCommand exposes the bounded Phase 1 live two-agent audio
// conversation under `yui session self-play`.
type SessionSelfPlayCommand struct {
	globalFlags *flags.GlobalFlags
	run         serviceSelfPlay.Service
}

// NewSessionSelfPlayCommand creates the self-play command from its transport
// service. A nil service is useful for parser tests that install a runner.
func NewSessionSelfPlayCommand(globalFlags *flags.GlobalFlags, service serviceSelfPlay.Service) *SessionSelfPlayCommand {
	return &SessionSelfPlayCommand{globalFlags: globalFlags, run: service}
}

// NewSelfPlayServiceAdapter translates CLI values and presentation to the
// runtime-owned self-play contract.
func NewSelfPlayServiceAdapter(service runtimeSelfPlay.Service) serviceSelfPlay.Service {
	return selfPlayServiceAdapter{service: service}
}

type selfPlayServiceAdapter struct {
	service runtimeSelfPlay.Service
}

func (adapter selfPlayServiceAdapter) Run(ctx context.Context, out io.Writer, options serviceSelfPlay.RunOptions) error {
	if adapter.service == nil {
		return errors.New("self-play service is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if out == nil {
		out = io.Discard
	}
	request, err := selfPlayRequest(options)
	if err != nil {
		return err
	}
	result, runErr := adapter.service.Run(ctx, request)
	if _, err := fmt.Fprintf(out, "self-play stopped: reason=%s customer_turns=%d assistant_turns=%d\n", result.StopReason, result.Customer.CompletedTurns, result.Assistant.CompletedTurns); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("write self-play result: %w", err))
	}
	return runErr
}

func selfPlayRequest(options serviceSelfPlay.RunOptions) (runtimeSelfPlay.Request, error) {
	request := runtimeSelfPlay.Request{
		Provider:    options.Provider,
		Model:       options.Model,
		OutputDir:   options.OutputDir,
		MaxDuration: options.MaxDuration,
		MaxTurns:    options.MaxTurns,
	}
	provider := strings.ToLower(strings.TrimSpace(options.Provider))
	if provider == "" {
		provider = runtimeSelfPlay.DefaultProvider
	}
	if provider != runtimeSelfPlay.DefaultProvider {
		return request, nil
	}
	storage, err := config.NewDefaultConfigStorage(options.ConfigDir)
	if err != nil {
		return runtimeSelfPlay.Request{}, fmt.Errorf("self-play live session configuration: %w", err)
	}
	loaded, err := storage.Load()
	if err != nil {
		return runtimeSelfPlay.Request{}, fmt.Errorf("self-play live session configuration: %w", err)
	}
	effective := loaded.ApplyOverrides(options.APIKey, options.Model, provider, options.BaseURL)
	active, err := effective.ActiveOpenAIConfig()
	if err != nil {
		return runtimeSelfPlay.Request{}, fmt.Errorf("self-play live session configuration: %w", err)
	}
	request.APIKey = active.APIKey
	request.BaseURL = active.BaseURL
	return request, nil
}

// SetRunner replaces the service runner used by this command for hermetic
// parser tests.
func (c *SessionSelfPlayCommand) SetRunner(runner func(context.Context, io.Writer, serviceSelfPlay.RunOptions) error) {
	if c != nil && runner != nil {
		c.run = serviceSelfPlay.RunFunc(runner)
	}
}

// Generate returns the cobra command for the bounded self-play runner.
func (c *SessionSelfPlayCommand) Generate() *cobra.Command {
	apiKey := ""
	provider := runtimeSelfPlay.DefaultProvider
	model := runtimeSelfPlay.DefaultModel
	baseURL := ""
	outputDir := ""
	maxDuration := runtimeSelfPlay.DefaultMaxDuration
	maxTurns := runtimeSelfPlay.DefaultTurnTarget

	cmd := &cobra.Command{
		Use:   "self-play",
		Short: "Run a bounded two-agent live audio conversation",
		Long: "Run the bounded self-play harness with two continuously open realtime sessions.\n\n" +
			"Only emitted raw PCM16 audio crosses between agents; tools and transcript/text bridging are disabled.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if c == nil || c.run == nil {
				return errors.New("self-play service is required")
			}
			configDir := ""
			if c.globalFlags != nil {
				configDir = c.globalFlags.ConfigDir()
			}
			return c.run.Run(cmd.Context(), cmd.OutOrStdout(), serviceSelfPlay.RunOptions{
				APIKey:      apiKey,
				OutputDir:   outputDir,
				Provider:    provider,
				Model:       model,
				BaseURL:     baseURL,
				ConfigDir:   configDir,
				MaxDuration: maxDuration,
				MaxTurns:    maxTurns,
			})
		},
	}
	cmd.Flags().StringVar(&apiKey, "api-key", "", "OpenAI API key; may also come from the configured AGENT_MODEL__OPENAI__API_KEY")
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "Empty directory for this self-play run (required)")
	cmd.Flags().StringVar(&provider, "provider", provider, "Realtime provider (openai only)")
	cmd.Flags().StringVar(&model, "model", model, "OpenAI Realtime model")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "Optional OpenAI Realtime WebSocket endpoint override")
	cmd.Flags().DurationVar(&maxDuration, "max-duration", maxDuration, "Positive maximum run duration")
	cmd.Flags().IntVar(&maxTurns, "max-turns", maxTurns, "Positive completed-turn target per side")
	return cmd
}
