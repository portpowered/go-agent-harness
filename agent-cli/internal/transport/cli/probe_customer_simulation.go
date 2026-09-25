package cli

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/customersim"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/spf13/cobra"
)

// CustomerSimulationSuiteRunner is the process seam; tests may replace the
// production suite runner with a credential-free fake.
type CustomerSimulationSuiteRunner = customersim.SuiteRunner

// CustomerSimulationCommand exposes the opt-in billed process-boundary suite.
type CustomerSimulationCommand struct {
	Live                      bool
	Required                  bool
	Families                  []string
	ScenarioPaths             []string
	AudioPaths                []string
	AudioDir                  string
	PatienceRepromptAudioPath string
	BinaryPath                string
	RunRoot                   string
	Provider                  string
	Model                     string
	BaseURL                   string
	SystemPrompt              string
	APIKeyEnv                 string
	SecretFile                string

	ValidatorProvider   string
	ValidatorModel      string
	ValidatorBaseURL    string
	ValidatorAPIKeyEnv  string
	ValidatorSecretFile string
	ValidatorTimeout    time.Duration
	MaxDuration         time.Duration
	FrameDuration       time.Duration
	SilenceDuration     time.Duration
	ShutdownGrace       time.Duration
	ReportPath          string

	globalFlags   *flags.GlobalFlags
	run           CustomerSimulationSuiteRunner
	validator     probe.CustomerSimulationValidatorAgent
	ReplayService runtimeReplay.StreamMessageCodec
}

// NewCustomerSimulationCommand constructs the opt-in command without I/O before
// Execute is called with --live.
func NewCustomerSimulationCommand(globalFlags *flags.GlobalFlags) *CustomerSimulationCommand {
	defaults := customersim.DefaultRequest()
	return &CustomerSimulationCommand{
		Provider:            defaults.Provider,
		Model:               defaults.Model,
		ValidatorProvider:   defaults.ValidatorProvider,
		APIKeyEnv:           defaults.APIKeyEnv,
		SecretFile:          defaults.SecretFile,
		ValidatorModel:      defaults.ValidatorModel,
		ValidatorAPIKeyEnv:  defaults.ValidatorAPIKeyEnv,
		ValidatorSecretFile: defaults.ValidatorSecretFile,
		ValidatorTimeout:    defaults.ValidatorTimeout,
		MaxDuration:         defaults.MaxDuration,
		FrameDuration:       defaults.FrameDuration,
		SilenceDuration:     defaults.SilenceDuration,
		ShutdownGrace:       defaults.ShutdownGrace,
		globalFlags:         globalFlags,
		run:                 probe.RunCustomerSimulationSuite,
	}
}

// SetRunner replaces the process runner for hermetic command tests.
func (c *CustomerSimulationCommand) SetRunner(runner CustomerSimulationSuiteRunner) {
	if c != nil && runner != nil {
		c.run = runner
	}
}

// SetValidator replaces the independent validator for hermetic command tests.
func (c *CustomerSimulationCommand) SetValidator(validator probe.CustomerSimulationValidatorAgent) {
	if c != nil {
		c.validator = validator
	}
}

// Generate returns the explicit live customer simulation command.
func (c *CustomerSimulationCommand) Generate() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "customer-simulation [scenario-path...]",
		Short: "Run the opt-in conversational customer simulation suite",
		Long: "Run selected A/B/C/D/E customer-simulation scenarios through the shipped Yui binary. " +
			"This is a billed live-provider command: it requires --live, an audio script, and credentials. " +
			"Family E additionally requires a natural check-in recording via --patience-reprompt-audio. " +
			"Every run is isolated outside the checkout and leaves a hash-verified evidence bundle plus a JSON report. " +
			"The command exits non-zero for BROKEN, invalid, incomplete, inconclusive, or unavailable runs.",
		Args:         cobra.ArbitraryArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.runCommand(cmd, args)
		},
	}
	cmd.Flags().BoolVar(&c.Live, "live", false, "Acknowledge that selected runs use a billed live provider")
	cmd.Flags().BoolVar(&c.Required, "required", false, "Select the required A, B, D-SIGINT, and D-natural evidence set")
	cmd.Flags().StringArrayVar(&c.Families, "family", nil, "Select a built-in family: A, B, C, D, D-SIGINT, D-NATURAL, or E (repeatable)")
	cmd.Flags().StringArrayVar(&c.ScenarioPaths, "scenario", nil, "Load a versioned customer-simulation scenario JSON file (repeatable)")
	cmd.Flags().StringArrayVar(&c.AudioPaths, "audio", nil, "Ordered 16 kHz PCM16/WAV customer turn audio (repeatable)")
	cmd.Flags().StringVar(&c.AudioDir, "audio-dir", "", "Directory containing <scenario-id>/<action-id>.wav (or .pcm/.raw) turn files")
	cmd.Flags().StringVar(&c.PatienceRepromptAudioPath, "patience-reprompt-audio", "", "Family E 16 kHz PCM16/WAV check-in recording sent after the patience threshold")
	cmd.Flags().StringVar(&c.BinaryPath, "binary", "", "Shipped yui binary; if omitted, locate it or build a temporary copy")
	cmd.Flags().StringVar(&c.RunRoot, "run-root", "", "Fresh evidence parent outside the checkout (default: an OS temporary directory)")
	cmd.Flags().StringVar(&c.Provider, "provider", c.Provider, "Live realtime provider: openai or grok")
	cmd.Flags().StringVar(&c.Model, "model", c.Model, "Live realtime model")
	cmd.Flags().StringVar(&c.BaseURL, "base-url", "", "Optional live realtime base URL override")
	cmd.Flags().StringVar(&c.SystemPrompt, "system-prompt", "", "Optional literal or file path system prompt passed to the shipped session")
	cmd.Flags().StringVar(&c.ValidatorProvider, "validator-provider", c.ValidatorProvider, "Independent validator provider: openai, openrouter, or local")
	cmd.Flags().StringVar(&c.ValidatorModel, "validator-model", c.ValidatorModel, "Independent validator model")
	cmd.Flags().StringVar(&c.ValidatorBaseURL, "validator-base-url", "", "Optional independent validator base URL override")
	cmd.Flags().StringVar(&c.APIKeyEnv, "api-key-env", c.APIKeyEnv, "Environment variable containing the live key; never printed or passed in argv")
	cmd.Flags().StringVar(&c.SecretFile, "secret-file", c.SecretFile, "Fallback key file; only its newline-stripped contents are used")
	cmd.Flags().StringVar(&c.ValidatorAPIKeyEnv, "validator-api-key-env", c.ValidatorAPIKeyEnv, "Environment variable for the independent validator key")
	cmd.Flags().StringVar(&c.ValidatorSecretFile, "validator-secret-file", c.ValidatorSecretFile, "Fallback key file for the independent validator")
	cmd.Flags().DurationVar(&c.ValidatorTimeout, "validator-timeout", c.ValidatorTimeout, "Independent validator deadline")
	cmd.Flags().DurationVar(&c.MaxDuration, "max-duration", c.MaxDuration, "Per-scenario shipped-session deadline")
	cmd.Flags().DurationVar(&c.FrameDuration, "frame-duration", c.FrameDuration, "PCM frame pacing duration")
	cmd.Flags().DurationVar(&c.SilenceDuration, "silence-duration", c.SilenceDuration, "Digital silence appended after each customer turn")
	cmd.Flags().DurationVar(&c.ShutdownGrace, "shutdown-grace", c.ShutdownGrace, "Bounded child shutdown grace")
	cmd.Flags().StringVar(&c.ReportPath, "report", "", "Write the JSON report to this path instead of stdout")
	return cmd
}

func (c *CustomerSimulationCommand) runCommand(cmd *cobra.Command, positional []string) error {
	if c == nil {
		return errors.New("customer simulation command is not configured")
	}
	outcome, err := customersim.Run(cmd.Context(), c.request(), positional, customersim.Dependencies{
		Host:          customerSimulationHost(),
		Runner:        c.run,
		Validator:     c.validator,
		ReplayService: c.ReplayService,
	})
	if err != nil {
		return err
	}
	report, err := customersim.EncodeReport(outcome.Result, outcome.Secrets...)
	if err != nil {
		return err
	}
	if err := customersim.WriteReport(cmd.OutOrStdout(), c.ReportPath, report); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "customer-simulation: %d/%d validator verdicts WORKED; evidence root %s\n", customersim.WorkedCount(outcome.Result), len(outcome.Result.Runs), outcome.Result.Root); err != nil {
		return errors.Join(fmt.Errorf("write customer simulation summary: %w", err), outcome.Err())
	}
	return outcome.Err()
}

func (c *CustomerSimulationCommand) request() customersim.Request {
	return customersim.Request{
		Live: c.Live, Required: c.Required, Families: c.Families, ScenarioPaths: c.ScenarioPaths,
		AudioPaths: c.AudioPaths, AudioDir: c.AudioDir, PatienceRepromptAudioPath: c.PatienceRepromptAudioPath,
		BinaryPath: c.BinaryPath, RunRoot: c.RunRoot, Provider: c.Provider, Model: c.Model, BaseURL: c.BaseURL,
		SystemPrompt: c.SystemPrompt, APIKeyEnv: c.APIKeyEnv, SecretFile: c.SecretFile,
		ValidatorProvider: c.ValidatorProvider, ValidatorModel: c.ValidatorModel, ValidatorBaseURL: c.ValidatorBaseURL,
		ValidatorAPIKeyEnv: c.ValidatorAPIKeyEnv, ValidatorSecretFile: c.ValidatorSecretFile, ValidatorTimeout: c.ValidatorTimeout,
		MaxDuration: c.MaxDuration, FrameDuration: c.FrameDuration, SilenceDuration: c.SilenceDuration, ShutdownGrace: c.ShutdownGrace,
	}
}

// customerSimulationHost injects the process working directory, home
// directory, and executable path into the customer simulation run.
func customerSimulationHost() customersim.Host {
	return customersim.Host{
		WorkingDir: os.Getwd,       //nolint:forbidigo // The CLI transport is the host boundary that injects the working directory.
		HomeDir:    os.UserHomeDir, //nolint:forbidigo // The CLI transport is the host boundary that injects the home directory.
		Executable: os.Executable,
	}
}
