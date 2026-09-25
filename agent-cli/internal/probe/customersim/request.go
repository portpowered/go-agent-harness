// Package customersim prepares and admits one opt-in, billed customer
// simulation run: it validates the operator request, selects scenarios,
// resolves credentials and turn audio, locates the shipped binary, runs the
// suite through an injected runner, and applies the fail-closed result
// boundary. The CLI transport owns flags and output streams only.
package customersim

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe"
)

// Operator defaults for a customer simulation run.
const (
	DefaultProvider          = config.ProviderOpenAI
	DefaultModel             = "gpt-realtime-2.1-mini"
	DefaultValidatorProvider = config.ProviderOpenAI
	DefaultValidatorModel    = "gpt-4o-mini"
	DefaultAPIKeyEnv         = "OPENAI_API_KEY"
	DefaultSecretFile        = "~/.you-agent-factory/secrets/OPENAPI_API_KEY"
)

// Request is one operator's customer simulation invocation.
type Request struct {
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
}

// DefaultRequest returns a request carrying the operator defaults.
func DefaultRequest() Request {
	return Request{
		Provider:            DefaultProvider,
		Model:               DefaultModel,
		ValidatorProvider:   DefaultValidatorProvider,
		APIKeyEnv:           DefaultAPIKeyEnv,
		SecretFile:          DefaultSecretFile,
		ValidatorModel:      DefaultValidatorModel,
		ValidatorAPIKeyEnv:  DefaultAPIKeyEnv,
		ValidatorSecretFile: DefaultSecretFile,
		ValidatorTimeout:    probe.DefaultCustomerSimulationValidatorTimeout,
		MaxDuration:         probe.DefaultCustomerSimulationMaxDuration,
		FrameDuration:       probe.DefaultCustomerSimulationFrame,
		SilenceDuration:     probe.DefaultCustomerSimulationSilence,
		ShutdownGrace:       probe.DefaultCustomerSimulationShutdown,
	}
}

// validate admits only an explicitly acknowledged, bounded run.
func (r Request) validate() error {
	if !r.Live {
		return errors.New("customer simulation is opt-in and may incur provider charges; pass --live to continue")
	}
	if r.MaxDuration <= 0 || r.MaxDuration > probe.DefaultCustomerSimulationMaxDuration {
		return fmt.Errorf("--max-duration must be positive and no greater than %s", probe.DefaultCustomerSimulationMaxDuration)
	}
	if r.FrameDuration <= 0 || r.SilenceDuration < 0 || r.ShutdownGrace <= 0 || r.ValidatorTimeout <= 0 {
		return errors.New("--frame-duration, --shutdown-grace, and --validator-timeout must be positive; --silence-duration must not be negative")
	}
	return nil
}

// selection returns the family selectors and scenario paths to load;
// --required expands to the required evidence set.
func (r Request) selection(positional []string) ([]string, []string, error) {
	selectors := splitSelectors(r.Families)
	scenarioPaths := append([]string(nil), r.ScenarioPaths...)
	scenarioPaths = append(scenarioPaths, positional...)
	if r.Required {
		if len(selectors) > 0 || len(scenarioPaths) > 0 {
			return nil, nil, errors.New("--required cannot be combined with --family or scenario paths")
		}
		selectors = []string{"A", "B", "D-SIGINT", "D-NATURAL"}
	}
	if len(selectors) == 0 && len(scenarioPaths) == 0 {
		return nil, nil, errors.New("no customer simulation selected; pass --family, --required, or scenario paths")
	}
	return selectors, scenarioPaths, nil
}

func splitSelectors(raw []string) []string {
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

// credentialEnvNames lists both credential variables so both are cleared
// even when resolving the primary key fails.
func (r Request) credentialEnvNames() []string {
	return uniqueNonEmpty(r.APIKeyEnv, r.ValidatorAPIKeyEnv)
}

func (r Request) sharesValidatorCredential() bool {
	return r.ValidatorAPIKeyEnv == r.APIKeyEnv && r.ValidatorSecretFile == r.SecretFile
}

func uniqueNonEmpty(values ...string) []string {
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
