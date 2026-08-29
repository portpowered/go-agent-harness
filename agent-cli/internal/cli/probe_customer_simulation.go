package cli

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/agent"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

const (
	defaultCustomerSimulationProvider          = config.ProviderOpenAI
	defaultCustomerSimulationModel             = "gpt-realtime-2.1-mini"
	defaultCustomerSimulationValidatorProvider = config.ProviderOpenAI
	defaultCustomerSimulationValidatorModel    = "gpt-4o-mini"
	defaultCustomerSimulationAPIKeyEnv         = "OPENAI_API_KEY"
	defaultCustomerSimulationSecretFile        = "~/.you-agent-factory/secrets/OPENAPI_API_KEY"
)

// CustomerSimulationSuiteRunner is the command's process-runner seam. The
// production constructor installs probe.RunCustomerSimulationSuite; tests can
// replace it with a credential-free fake without touching the live command.
type CustomerSimulationSuiteRunner func(context.Context, probe.CustomerSimulationSuiteOptions) (probe.CustomerSimulationSuiteResult, error)

// CustomerSimulationCommand exposes one explicit opt-in command for the
// billed, process-boundary customer simulation suite.
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

	globalFlags *flags.GlobalFlags
	run         CustomerSimulationSuiteRunner
	validator   probe.CustomerSimulationValidatorAgent
}

// NewCustomerSimulationCommand constructs the opt-in customer simulation
// command. It performs no network or filesystem work until Execute is called
// with --live.
func NewCustomerSimulationCommand(globalFlags *flags.GlobalFlags) *CustomerSimulationCommand {
	return &CustomerSimulationCommand{
		Provider:            defaultCustomerSimulationProvider,
		Model:               defaultCustomerSimulationModel,
		ValidatorProvider:   defaultCustomerSimulationValidatorProvider,
		APIKeyEnv:           defaultCustomerSimulationAPIKeyEnv,
		SecretFile:          defaultCustomerSimulationSecretFile,
		ValidatorModel:      defaultCustomerSimulationValidatorModel,
		ValidatorAPIKeyEnv:  defaultCustomerSimulationAPIKeyEnv,
		ValidatorSecretFile: defaultCustomerSimulationSecretFile,
		ValidatorTimeout:    probe.DefaultCustomerSimulationValidatorTimeout,
		MaxDuration:         probe.DefaultCustomerSimulationMaxDuration,
		FrameDuration:       probe.DefaultCustomerSimulationFrame,
		SilenceDuration:     probe.DefaultCustomerSimulationSilence,
		ShutdownGrace:       probe.DefaultCustomerSimulationShutdown,
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
		Long: "Run selected A/B/C/D/E customer-simulation scenarios through the shipped agent binary. " +
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
	cmd.Flags().StringVar(&c.BinaryPath, "binary", "", "Shipped agent binary; if omitted, locate it or build a temporary copy")
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
	if !c.Live {
		return errors.New("customer simulation is opt-in and may incur provider charges; pass --live to continue")
	}
	if c.MaxDuration <= 0 || c.MaxDuration > probe.DefaultCustomerSimulationMaxDuration {
		return fmt.Errorf("--max-duration must be positive and no greater than %s", probe.DefaultCustomerSimulationMaxDuration)
	}
	if c.FrameDuration <= 0 || c.SilenceDuration < 0 || c.ShutdownGrace <= 0 || c.ValidatorTimeout <= 0 {
		return errors.New("--frame-duration, --shutdown-grace, and --validator-timeout must be positive; --silence-duration must not be negative")
	}

	selectors := customerSimulationSelectors(c.Families)
	scenarioPaths := append([]string(nil), c.ScenarioPaths...)
	scenarioPaths = append(scenarioPaths, positional...)
	if c.Required {
		if len(selectors) > 0 || len(scenarioPaths) > 0 {
			return errors.New("--required cannot be combined with --family or scenario paths")
		}
		selectors = []string{"A", "B", "D-SIGINT", "D-NATURAL"}
	}
	if len(selectors) == 0 && len(scenarioPaths) == 0 {
		return errors.New("no customer simulation selected; pass --family, --required, or scenario paths")
	}

	scenarios, err := customerSimulationLoadScenarios(selectors, scenarioPaths)
	if err != nil {
		return err
	}
	if err := ensureCustomerSimulationRunRootOutsideCheckout(c.RunRoot); err != nil {
		return err
	}

	// Register cleanup for both credential variables before resolving either
	// credential. This covers failures where the primary key is absent but a
	// separately configured validator key was already exported by the caller.
	credentialEnvNames := uniqueNonEmptyStrings(c.APIKeyEnv, c.ValidatorAPIKeyEnv)
	defer cleanupCustomerSimulationEnvironment(credentialEnvNames)
	apiKey, _, err := readCustomerSimulationCredential(c.APIKeyEnv, c.SecretFile)
	if err != nil {
		return err
	}
	validatorAPIKey := apiKey
	if c.ValidatorAPIKeyEnv != c.APIKeyEnv || c.ValidatorSecretFile != c.SecretFile {
		validatorAPIKey, _, err = readCustomerSimulationCredential(c.ValidatorAPIKeyEnv, c.ValidatorSecretFile)
		if err != nil {
			return err
		}
	}

	binaryPath, binaryCleanup, err := locateCustomerSimulationBinary(cmd.Context(), c.BinaryPath)
	if err != nil {
		return err
	}
	defer binaryCleanup()

	runs, err := customerSimulationRunSpecs(scenarios, c.AudioPaths, c.AudioDir, c.PatienceRepromptAudioPath)
	if err != nil {
		return err
	}

	validator := c.validator
	if validator == nil {
		validator, err = buildCustomerSimulationValidator(c.ValidatorProvider, c.ValidatorModel, c.ValidatorBaseURL, validatorAPIKey)
		if err != nil {
			return err
		}
	}
	runner := c.run
	if runner == nil {
		runner = probe.RunCustomerSimulationSuite
	}
	result, runErr := runner(cmd.Context(), probe.CustomerSimulationSuiteOptions{
		BinaryPath: binaryPath, RunRoot: c.RunRoot, Provider: c.Provider, Model: c.Model, BaseURL: c.BaseURL, APIKey: apiKey, SystemPrompt: c.SystemPrompt,
		Runs: runs, Validator: validator, ValidatorTimeout: c.ValidatorTimeout, MaxDuration: c.MaxDuration, FrameDuration: c.FrameDuration, SilenceDuration: c.SilenceDuration, ShutdownGrace: c.ShutdownGrace,
	})
	if writeErr := writeCustomerSimulationReport(cmd, c.ReportPath, result, apiKey, validatorAPIKey); writeErr != nil {
		return writeErr
	}
	passed := 0
	for _, run := range result.Runs {
		if run.Validator.Pass() {
			passed++
		}
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "customer-simulation: %d/%d validator verdicts WORKED; evidence root %s\n", passed, len(result.Runs), result.Root)
	resultErr := validateCustomerSimulationCommandResult(result, scenarios)
	if runErr != nil || resultErr != nil {
		return errors.Join(runErr, resultErr)
	}
	return nil
}

// validateCustomerSimulationCommandResult is the CLI's final fail-closed
// boundary. The production runner already returns an aggregate error, but the
// command must also reject incomplete or contradictory results from any
// runner implementation before it reports success to an operator.
func validateCustomerSimulationCommandResult(result probe.CustomerSimulationSuiteResult, scenarios []probe.CustomerScenario) error {
	var failures []error
	if strings.TrimSpace(result.Root) == "" {
		failures = append(failures, errors.New("customer simulation result has no evidence root"))
	}
	if len(result.Runs) != len(scenarios) {
		failures = append(failures, fmt.Errorf("customer simulation returned %d run results, want %d", len(result.Runs), len(scenarios)))
	}
	expected := make(map[string]probe.CustomerScenario, len(scenarios))
	for _, scenario := range scenarios {
		expected[scenario.ID] = scenario
	}
	seen := make(map[string]struct{}, len(result.Runs))
	for index, run := range result.Runs {
		label := fmt.Sprintf("customer simulation result %d", index+1)
		if strings.TrimSpace(run.RunID) == "" {
			failures = append(failures, fmt.Errorf("%s has no run ID", label))
		}
		scenario, ok := expected[run.ScenarioID]
		if !ok {
			failures = append(failures, fmt.Errorf("%s identifies unexpected scenario %q", label, run.ScenarioID))
			continue
		}
		if _, duplicate := seen[run.ScenarioID]; duplicate {
			failures = append(failures, fmt.Errorf("scenario %q appears more than once in the result", run.ScenarioID))
		}
		seen[run.ScenarioID] = struct{}{}
		if run.Family != scenario.Family || run.Termination != scenario.Termination {
			failures = append(failures, fmt.Errorf("scenario %q returned contradictory family or termination facts", run.ScenarioID))
		}
		if strings.TrimSpace(run.BundleRoot) == "" || strings.TrimSpace(run.RecordRoot) == "" || strings.TrimSpace(run.WorkspaceRoot) == "" {
			failures = append(failures, fmt.Errorf("scenario %q is incomplete: evidence, record, and workspace roots are required", run.ScenarioID))
		}
		if !run.Mechanical.Pass || !run.Validator.Mechanical.Pass || !run.Validator.Pass() {
			status := string(run.Validator.Status)
			if status == "" {
				status = "missing"
			}
			failures = append(failures, fmt.Errorf("scenario %q did not produce an accepted WORKED verdict (status %s)", run.ScenarioID, status))
		}
	}
	for _, scenario := range scenarios {
		if _, ok := seen[scenario.ID]; !ok {
			failures = append(failures, fmt.Errorf("selected scenario %q has no result", scenario.ID))
		}
	}
	return errors.Join(failures...)
}

func customerSimulationSelectors(raw []string) []string {
	var selectors []string
	for _, value := range raw {
		for _, selector := range strings.Split(value, ",") {
			if strings.TrimSpace(selector) != "" {
				selectors = append(selectors, strings.TrimSpace(selector))
			}
		}
	}
	return selectors
}

func customerSimulationLoadScenarios(selectors, paths []string) ([]probe.CustomerScenario, error) {
	var scenarios []probe.CustomerScenario
	if len(selectors) > 0 {
		selected, err := probe.CustomerSimulationScenariosForSelectors(selectors...)
		if err != nil {
			return nil, err
		}
		scenarios = append(scenarios, selected...)
	}
	seen := make(map[string]struct{}, len(scenarios))
	for _, scenario := range scenarios {
		seen[scenario.ID] = struct{}{}
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read customer simulation scenario %q: %w", path, err)
		}
		scenario, err := probe.ParseCustomerScenario(data)
		if err != nil {
			return nil, fmt.Errorf("load customer simulation scenario %q: %w", path, err)
		}
		if _, duplicate := seen[scenario.ID]; duplicate {
			return nil, fmt.Errorf("customer simulation scenario %q was selected more than once", scenario.ID)
		}
		seen[scenario.ID] = struct{}{}
		scenarios = append(scenarios, scenario)
	}
	return scenarios, nil
}

func customerSimulationRunSpecs(scenarios []probe.CustomerScenario, audioPaths []string, audioDir string, patienceRepromptAudioPaths ...string) ([]probe.CustomerSimulationRunSpec, error) {
	if len(audioPaths) > 0 && strings.TrimSpace(audioDir) != "" {
		return nil, errors.New("--audio and --audio-dir cannot be combined")
	}
	if len(patienceRepromptAudioPaths) > 1 {
		return nil, errors.New("only one --patience-reprompt-audio path is supported")
	}
	patienceRepromptAudioPath := ""
	if len(patienceRepromptAudioPaths) == 1 {
		patienceRepromptAudioPath = strings.TrimSpace(patienceRepromptAudioPaths[0])
	}
	hasFamilyE := false
	for _, scenario := range scenarios {
		if scenario.Family == probe.ScenarioFamilyE {
			hasFamilyE = true
			break
		}
	}
	if hasFamilyE && patienceRepromptAudioPath == "" {
		return nil, errors.New("family E requires --patience-reprompt-audio with a natural check-in recording")
	}
	if !hasFamilyE && patienceRepromptAudioPath != "" {
		return nil, errors.New("--patience-reprompt-audio is only valid when Family E is selected")
	}
	var patienceRepromptAudio []byte
	if patienceRepromptAudioPath != "" {
		data, err := readCustomerSimulationPCM16(patienceRepromptAudioPath)
		if err != nil {
			return nil, fmt.Errorf("load Family E patience re-prompt audio: %w", err)
		}
		patienceRepromptAudio = data
	}
	totalTurns := 0
	for _, scenario := range scenarios {
		totalTurns += len(probe.CustomerSimulationScenarioScript(scenario))
	}
	if len(audioPaths) > 0 && len(audioPaths) != totalTurns {
		return nil, fmt.Errorf("--audio needs exactly one file per selected customer turn: got %d, want %d", len(audioPaths), totalTurns)
	}
	runs := make([]probe.CustomerSimulationRunSpec, 0, len(scenarios))
	audioIndex := 0
	for _, scenario := range scenarios {
		script := probe.CustomerSimulationScenarioScript(scenario)
		paths := make([]string, len(script))
		if len(audioPaths) > 0 {
			copy(paths, audioPaths[audioIndex:audioIndex+len(script)])
			audioIndex += len(script)
		} else if strings.TrimSpace(audioDir) != "" {
			resolved, err := resolveCustomerSimulationAudioPaths(audioDir, scenario, script)
			if err != nil {
				return nil, err
			}
			paths = resolved
		} else {
			return nil, fmt.Errorf("audio is required for scenario %q; pass --audio once per turn or --audio-dir", scenario.ID)
		}
		pcm := make([][]byte, len(paths))
		for index, path := range paths {
			data, err := readCustomerSimulationPCM16(path)
			if err != nil {
				return nil, fmt.Errorf("load audio for scenario %q turn %d: %w", scenario.ID, index+1, err)
			}
			pcm[index] = data
		}
		spec := probe.CustomerSimulationRunSpec{Scenario: scenario, Script: script, Audio: pcm}
		if scenario.Family == probe.ScenarioFamilyE {
			spec.PatienceRepromptAudio = append([]byte(nil), patienceRepromptAudio...)
		}
		runs = append(runs, spec)
	}
	return runs, nil
}

func resolveCustomerSimulationAudioPaths(root string, scenario probe.CustomerScenario, script []probe.CustomerScriptTurn) ([]string, error) {
	paths := make([]string, len(script))
	for index, turn := range script {
		candidates := make([]string, 0, 12)
		bases := []string{
			filepath.Join(root, scenario.ID, turn.ActionID),
			filepath.Join(root, scenario.ID, fmt.Sprintf("%02d", index+1)),
			filepath.Join(root, scenario.ID+"-"+turn.ActionID),
			filepath.Join(root, turn.ActionID),
		}
		for _, base := range bases {
			for _, extension := range []string{".wav", ".pcm", ".raw"} {
				candidates = append(candidates, base+extension)
			}
		}
		found := ""
		for _, candidate := range candidates {
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				found = candidate
				break
			}
		}
		if found == "" {
			return nil, fmt.Errorf("audio directory %q has no file for scenario %q turn %q; tried scenario/action .wav/.pcm/.raw names", root, scenario.ID, turn.ActionID)
		}
		paths[index] = found
	}
	return paths, nil
}

func readCustomerSimulationPCM16(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(filepath.Ext(path), ".wav") {
		rate, samples, err := wavio.Read(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		if rate != wavio.Rate16kHz {
			return nil, fmt.Errorf("WAV sample rate is %d Hz; customer simulation requires %d Hz", rate, wavio.Rate16kHz)
		}
		pcm := make([]byte, len(samples)*2)
		for index, sample := range samples {
			binary.LittleEndian.PutUint16(pcm[index*2:], uint16(sample))
		}
		data = pcm
	}
	if len(data) == 0 || len(data)%2 != 0 {
		return nil, errors.New("audio must be non-empty, even-length PCM16")
	}
	return append([]byte(nil), data...), nil
}

func readCustomerSimulationCredential(envName, rawFile string) (string, []string, error) {
	names := uniqueNonEmptyStrings(envName)
	for _, name := range names {
		if value, ok := os.LookupEnv(name); ok {
			value = strings.ReplaceAll(value, "\n", "")
			value = strings.ReplaceAll(value, "\r", "")
			if strings.TrimSpace(value) == "" {
				continue
			}
			return value, names, nil
		}
	}
	files := uniqueNonEmptyStrings(rawFile)
	for _, rawPath := range files {
		path, err := expandCustomerSimulationHome(rawPath)
		if err != nil {
			return "", names, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", names, fmt.Errorf("read customer simulation secret file %q: %w", path, err)
		}
		value := strings.ReplaceAll(string(data), "\n", "")
		value = strings.ReplaceAll(value, "\r", "")
		if strings.TrimSpace(value) != "" {
			return value, names, nil
		}
	}
	return "", names, fmt.Errorf("live customer simulation credentials are required; set %s or provide %s", strings.Join(names, " or "), strings.Join(files, " or "))
}

func uniqueNonEmptyStrings(values ...string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func cleanupCustomerSimulationEnvironment(names []string) {
	for _, name := range names {
		_ = os.Unsetenv(name)
	}
}

func expandCustomerSimulationHome(raw string) (string, error) {
	if !strings.HasPrefix(raw, "~/") && raw != "~" {
		return raw, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve secret home: %w", err)
	}
	if raw == "~" {
		return home, nil
	}
	return filepath.Join(home, strings.TrimPrefix(raw, "~/")), nil
}

func buildCustomerSimulationValidator(providerName, model, baseURL, apiKey string) (probe.CustomerSimulationValidatorAgent, error) {
	providerName = strings.ToLower(strings.TrimSpace(providerName))
	if providerName == config.ProviderGrok {
		return nil, errors.New("--validator-provider grok is unsupported: the independent validator requires a stateless provider")
	}
	if providerName != config.ProviderOpenAI && providerName != config.ProviderOpenRouter && providerName != config.ProviderLocal {
		return nil, fmt.Errorf("unsupported --validator-provider %q; want openai, openrouter, or local", providerName)
	}
	loaded := config.Config{Model: config.ModelConfig{Provider: providerName}}
	loaded = loaded.ApplyOverrides(apiKey, model, providerName, baseURL)
	factory := agent.NewProviderFactory()
	agent.RegisterOpenAIProvider(factory, config.ProviderOpenAI, config.ProviderOpenRouter, config.ProviderLocal)
	built, err := factory.Build(providerName, agent.ProviderBuildContext{LoadedConfig: &loaded, Logger: zap.NewNop(), HTTPClient: http.DefaultClient})
	if err != nil {
		return nil, fmt.Errorf("build independent validator provider: %w", err)
	}
	gw, err := gateway.NewGateway(gateway.WithProvider(built.Provider))
	if err != nil {
		return nil, fmt.Errorf("build independent validator gateway: %w", err)
	}
	return probe.GatewayCustomerSimulationValidator{Gateway: gw, Model: model}, nil
}

func writeCustomerSimulationReport(cmd *cobra.Command, reportPath string, result probe.CustomerSimulationSuiteResult, secrets ...string) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode customer simulation report: %w", err)
	}
	for _, secret := range secrets {
		if secret = strings.TrimSpace(secret); secret != "" {
			data = bytes.ReplaceAll(data, []byte(secret), []byte("<redacted>"))
		}
	}
	data = append(data, '\n')
	if strings.TrimSpace(reportPath) == "" {
		_, err = cmd.OutOrStdout().Write(data)
		if err != nil {
			return fmt.Errorf("write customer simulation report: %w", err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(reportPath), 0o700); err != nil {
		return fmt.Errorf("create customer simulation report directory: %w", err)
	}
	if err := os.WriteFile(reportPath, data, 0o600); err != nil {
		return fmt.Errorf("write customer simulation report %q: %w", reportPath, err)
	}
	return nil
}

func locateCustomerSimulationBinary(ctx context.Context, explicit string) (string, func(), error) {
	if strings.TrimSpace(explicit) != "" {
		path, err := validateCustomerSimulationBinary(explicit)
		return path, func() {}, err
	}
	candidates := []string{}
	if executable, err := os.Executable(); err == nil && filepath.Base(executable) == "agent" {
		candidates = append(candidates, executable)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "agent-cli", "bin", "agent"), filepath.Join(cwd, "bin", "agent"))
		if root, rootErr := customerSimulationRepositoryRoot(cwd); rootErr == nil {
			candidates = append(candidates, filepath.Join(root, "agent-cli", "bin", "agent"), filepath.Join(root, "bin", "agent"))
		}
	}
	for _, candidate := range uniqueNonEmptyStrings(candidates...) {
		if path, err := validateCustomerSimulationBinary(candidate); err == nil {
			return path, func() {}, nil
		}
	}
	root, err := customerSimulationRepositoryRootFromWorkingDirectory()
	if err != nil {
		return "", func() {}, fmt.Errorf("locate shipped agent binary: %w", err)
	}
	temporary, err := os.CreateTemp("", "agent-customer-simulation-binary-")
	if err != nil {
		return "", func() {}, fmt.Errorf("create temporary shipped binary: %w", err)
	}
	path := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(path)
		return "", func() {}, fmt.Errorf("prepare temporary shipped binary: %w", err)
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", path, "./agent-cli/cmd/agent")
	build.Dir = root
	build.Stdout = io.Discard
	build.Stderr = io.Discard
	if err := build.Run(); err != nil {
		_ = os.Remove(path)
		return "", func() {}, fmt.Errorf("build shipped agent binary: %w", err)
	}
	return path, func() { _ = os.Remove(path) }, nil
}

func validateCustomerSimulationBinary(raw string) (string, error) {
	path, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("binary %q is not a regular file", path)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("binary %q is not executable", path)
	}
	return path, nil
}

func ensureCustomerSimulationRunRootOutsideCheckout(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	root, err := filepath.Abs(raw)
	if err != nil {
		return fmt.Errorf("resolve --run-root: %w", err)
	}
	repository, err := customerSimulationRepositoryRootFromWorkingDirectory()
	if err != nil {
		return nil
	}
	relative, err := filepath.Rel(repository, root)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("--run-root %q must be outside checkout %q", root, repository)
	}
	return nil
}

func customerSimulationRepositoryRootFromWorkingDirectory() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return customerSimulationRepositoryRoot(cwd)
}

func customerSimulationRepositoryRoot(start string) (string, error) {
	current, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("repository root not found")
		}
		current = parent
	}
}
