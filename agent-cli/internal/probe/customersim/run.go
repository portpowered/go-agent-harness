package customersim

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	providerswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/wire"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
)

// SuiteRunner is the process seam; tests may replace the production suite
// runner with a credential-free fake.
type SuiteRunner func(context.Context, probe.CustomerSimulationSuiteOptions) (probe.CustomerSimulationSuiteResult, error)

// Dependencies are the injected seams of one run. A nil Runner selects the
// production suite runner and a nil Validator builds the independent
// validator from the request.
type Dependencies struct {
	Host          Host
	Runner        SuiteRunner
	Validator     probe.CustomerSimulationValidatorAgent
	ReplayService runtimeReplay.StreamMessageCodec
}

// Outcome is an admitted run: the suite runner was invoked and its result
// must be reported even when it failed.
type Outcome struct {
	Result    probe.CustomerSimulationSuiteResult
	Scenarios []probe.CustomerScenario
	// Secrets are the resolved credentials a report must redact.
	Secrets []string
	RunErr  error
}

// Err joins the runner error with the fail-closed result boundary.
func (o Outcome) Err() error {
	return errors.Join(o.RunErr, ValidateResult(o.Result, o.Scenarios))
}

// credentials are the resolved live and independent validator keys.
type credentials struct {
	apiKey          string
	validatorAPIKey string
}

// Run admits, prepares, and executes one customer simulation. A returned
// error means the suite runner was never invoked.
func Run(ctx context.Context, request Request, positional []string, deps Dependencies) (Outcome, error) {
	if err := request.validate(); err != nil {
		return Outcome{}, err
	}
	selectors, scenarioPaths, err := request.selection(positional)
	if err != nil {
		return Outcome{}, err
	}
	scenarios, err := loadScenarios(selectors, scenarioPaths)
	if err != nil {
		return Outcome{}, err
	}
	if err := deps.Host.ensureRunRootOutsideCheckout(request.RunRoot); err != nil {
		return Outcome{}, err
	}
	// Register cleanup for both credential variables before resolving either
	// credential. This covers failures where the primary key is absent but a
	// separately configured validator key was already exported by the caller.
	defer clearEnvironment(request.credentialEnvNames())
	keys, err := deps.Host.resolveCredentials(request)
	if err != nil {
		return Outcome{}, err
	}
	binaryPath, binaryCleanup, err := deps.Host.locateBinary(ctx, request.BinaryPath)
	if err != nil {
		return Outcome{}, err
	}
	defer binaryCleanup()
	options, err := suiteOptions(ctx, request, deps, scenarios, keys)
	if err != nil {
		return Outcome{}, err
	}
	options.BinaryPath = binaryPath
	runner := deps.Runner
	if runner == nil {
		runner = probe.RunCustomerSimulationSuite
	}
	result, runErr := runner(ctx, options)
	return Outcome{Result: result, Scenarios: scenarios, Secrets: []string{keys.apiKey, keys.validatorAPIKey}, RunErr: runErr}, nil
}

func (h Host) resolveCredentials(request Request) (credentials, error) {
	apiKey, err := h.readCredential(request.APIKeyEnv, request.SecretFile)
	if err != nil {
		return credentials{}, err
	}
	keys := credentials{apiKey: apiKey, validatorAPIKey: apiKey}
	if !request.sharesValidatorCredential() {
		keys.validatorAPIKey, err = h.readCredential(request.ValidatorAPIKeyEnv, request.ValidatorSecretFile)
		if err != nil {
			return credentials{}, err
		}
	}
	return keys, nil
}

func suiteOptions(ctx context.Context, request Request, deps Dependencies, scenarios []probe.CustomerScenario, keys credentials) (probe.CustomerSimulationSuiteOptions, error) {
	runs, err := RunSpecs(scenarios, request.AudioPaths, request.AudioDir, request.PatienceRepromptAudioPath)
	if err != nil {
		return probe.CustomerSimulationSuiteOptions{}, err
	}
	validator := deps.Validator
	if validator == nil {
		validator, err = buildValidator(ctx, request.ValidatorProvider, request.ValidatorModel, request.ValidatorBaseURL, keys.validatorAPIKey)
		if err != nil {
			return probe.CustomerSimulationSuiteOptions{}, err
		}
	}
	return probe.CustomerSimulationSuiteOptions{
		RunRoot: request.RunRoot, Provider: request.Provider, Model: request.Model, BaseURL: request.BaseURL, APIKey: keys.apiKey, SystemPrompt: request.SystemPrompt,
		Runs: runs, Validator: validator, ValidatorTimeout: request.ValidatorTimeout, MaxDuration: request.MaxDuration, FrameDuration: request.FrameDuration,
		SilenceDuration: request.SilenceDuration, ShutdownGrace: request.ShutdownGrace, ReplayService: deps.ReplayService,
	}, nil
}

// loadScenarios selects built-in families first, then versioned scenario
// files; a scenario selected twice is rejected.
func loadScenarios(selectors, paths []string) ([]probe.CustomerScenario, error) {
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
		scenario, err := loadScenarioFile(path)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[scenario.ID]; duplicate {
			return nil, fmt.Errorf("customer simulation scenario %q was selected more than once", scenario.ID)
		}
		seen[scenario.ID] = struct{}{}
		scenarios = append(scenarios, scenario)
	}
	return scenarios, nil
}

func loadScenarioFile(path string) (probe.CustomerScenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return probe.CustomerScenario{}, fmt.Errorf("read customer simulation scenario %q: %w", path, err)
	}
	scenario, err := probe.ParseCustomerScenario(data)
	if err != nil {
		return probe.CustomerScenario{}, fmt.Errorf("load customer simulation scenario %q: %w", path, err)
	}
	return scenario, nil
}

// buildValidator composes the independent, stateless validator gateway.
func buildValidator(ctx context.Context, providerName, model, baseURL, apiKey string) (probe.CustomerSimulationValidatorAgent, error) {
	providerName = strings.ToLower(strings.TrimSpace(providerName))
	if providerName == config.ProviderGrok {
		return nil, errors.New("--validator-provider grok is unsupported: the independent validator requires a stateless provider")
	}
	if providerName != config.ProviderOpenAI && providerName != config.ProviderOpenRouter && providerName != config.ProviderLocal {
		return nil, fmt.Errorf("unsupported --validator-provider %q; want openai, openrouter, or local", providerName)
	}
	providerService := providerswire.NewService(providerswire.Dependencies{HTTPClient: http.DefaultClient})
	providerConfig := providers.Config{Provider: providerName, Model: model, APIKey: apiKey, BaseURL: baseURL}
	built, err := providerService.Build(ctx, providerConfig)
	if err != nil {
		return nil, fmt.Errorf("build independent validator provider: %w", err)
	}
	gw, err := gateway.NewGateway(gateway.WithProvider(built))
	if err != nil {
		return nil, fmt.Errorf("build independent validator gateway: %w", err)
	}
	return probe.GatewayCustomerSimulationValidator{Gateway: gw, Model: model}, nil
}
