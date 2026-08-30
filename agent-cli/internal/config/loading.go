package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
	yamlv3 "gopkg.in/yaml.v3"
)

// ConfigStorage handles configuration file operations with layered loading.
type ConfigStorage struct {
	configPath string
	mu         sync.RWMutex
	cached     *Config
}

type browserConfigValueKind uint8

const (
	browserConfigString browserConfigValueKind = iota
	browserConfigBool
	browserConfigEnum
	browserConfigDuration
	browserConfigSize
	browserConfigStringList
)

type browserConfigFieldSpec struct {
	path    string
	kind    browserConfigValueKind
	allowed []string
}

type interactiveConfigFieldSpec struct {
	path string
}

// browserConfigFieldSpecs is deliberately ordered to keep configuration
// errors deterministic when more than one browser value is invalid.
var browserConfigFieldSpecs = []browserConfigFieldSpec{
	{path: "browser.tools.enabled", kind: browserConfigBool},
	{path: "browser.tools.backend", kind: browserConfigEnum, allowed: []string{BrowserToolsBackendWebMCP}},
	{path: "browser.connection.cdp_url", kind: browserConfigString},
	{path: "browser.connection.ws_endpoint", kind: browserConfigString},
	{path: "browser.connection.user_data_dir", kind: browserConfigString},
	{path: "browser.connection.allow_process_scan", kind: browserConfigBool},
	{path: "browser.connection.allow_remote_cdp", kind: browserConfigBool},
	{path: "browser.selection.browser", kind: browserConfigString},
	{path: "browser.selection.tab", kind: browserConfigString},
	{path: "browser.selection.origin", kind: browserConfigString},
	{path: "browser.selection.auto_select", kind: browserConfigEnum, allowed: []string{BrowserAutoSelectOff, BrowserAutoSelectSingle, BrowserAutoSelectPersisted}},
	{path: "browser.selection.activate_tab", kind: browserConfigBool},
	{path: "browser.selection.persist", kind: browserConfigBool},
	{path: "browser.policy.allowed_origins", kind: browserConfigStringList},
	{path: "browser.policy.denied_origins", kind: browserConfigStringList},
	{path: "browser.policy.approval", kind: browserConfigEnum, allowed: []string{BrowserApprovalAlways, BrowserApprovalWrites, BrowserApprovalNever}},
	{path: "browser.policy.cancel_on_interrupt", kind: browserConfigEnum, allowed: []string{BrowserCancelOnInterruptNever, BrowserCancelOnInterruptReadOnly, BrowserCancelOnInterruptAlways}},
	{path: "browser.limits.invocation_timeout", kind: browserConfigDuration},
	{path: "browser.limits.max_input_bytes", kind: browserConfigSize},
	{path: "browser.limits.max_result_bytes", kind: browserConfigSize},
	{path: "browser.limits.serialize_per_target", kind: browserConfigBool},
	{path: "browser.recording.enabled", kind: browserConfigBool},
	{path: "browser.recording.include_arguments", kind: browserConfigBool},
	{path: "browser.recording.include_results", kind: browserConfigBool},
	{path: "browser.recording.redact_url_query", kind: browserConfigBool},
	{path: "browser.recording.redact_url_fragment", kind: browserConfigBool},
	{path: "browser.replay.path", kind: browserConfigString},
	{path: "browser.replay.strict", kind: browserConfigBool},
}

var interactiveConfigFieldSpecs = []interactiveConfigFieldSpec{
	{path: "tools.interactive.fast_read_timeout"},
	{path: "tools.interactive.long_running_timeout"},
	{path: "tools.interactive.acknowledgement_threshold"},
}

// NewConfigStorage creates a new configuration storage handler.
func NewConfigStorage(configPath string) *ConfigStorage {
	return &ConfigStorage{
		configPath: configPath,
	}
}

// NewDefaultConfigStorage creates a ConfigStorage using the default config directory
// (~/.agent-cli/config.yaml). If configDir is non-empty it is used instead.
func NewDefaultConfigStorage(configDir string) (*ConfigStorage, error) {
	if configDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get user home directory: %w", err)
		}
		configDir = filepath.Join(homeDir, ConfigDirName)
	}

	configDir, err := filepath.Abs(configDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve config directory: %w", err)
	}

	configPath := filepath.Join(configDir, ConfigFileName)
	return NewConfigStorage(configPath), nil
}

// Load loads configuration with hierarchy: defaults -> config file -> environment variables.
// Results are cached in memory to avoid repeated file I/O.
func (s *ConfigStorage) Load() (*Config, error) {
	s.mu.RLock()
	if s.cached != nil {
		cached := s.cached
		s.mu.RUnlock()
		return cached, nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cached != nil {
		return s.cached, nil
	}

	k := koanf.New(".")
	if err := validateInteractiveEnvironment(); err != nil {
		return nil, fmt.Errorf("invalid interactive tool configuration: %w", err)
	}
	if err := validateBrowserEnvironment(); err != nil {
		return nil, fmt.Errorf("invalid browser configuration: %w", err)
	}

	// 1. Load default values using struct provider
	defaultCfg := getDefaultConfig()
	if err := k.Load(structs.Provider(defaultCfg, structTag), nil); err != nil {
		return nil, fmt.Errorf("failed to load default config: %w", err)
	}

	// 2. Validate browser values in the YAML before Koanf's weakly typed
	// unmarshaller gets a chance to coerce them. This keeps values such as
	// "yes", 1, and comma-separated origin lists from being accepted by
	// accident.
	if data, err := os.ReadFile(s.configPath); err == nil {
		if err := validateInteractiveYAML(data); err != nil {
			return nil, fmt.Errorf("invalid interactive tool configuration in %s: %w", s.configPath, err)
		}
		if err := validateBrowserYAML(data); err != nil {
			return nil, fmt.Errorf("invalid browser configuration in %s: %w", s.configPath, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read config file %s: %w", s.configPath, err)
	}

	// 3. Load configuration from YAML file (if exists)
	if err := k.Load(file.Provider(s.configPath), yaml.Parser()); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to load config file %s: %w", s.configPath, err)
		}

		if os.IsNotExist(err) {
			// Create config directory if needed
			if err := os.MkdirAll(filepath.Dir(s.configPath), 0755); err != nil {
				return nil, fmt.Errorf("failed to create config directory for %s: %w", s.configPath, err)
			}
			// Serialize default config to YAML and write the file
			data, err := yamlv3.Marshal(defaultCfg)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal default config: %w", err)
			}
			if err := os.WriteFile(s.configPath, data, 0644); err != nil {
				return nil, fmt.Errorf("failed to create config file %s: %w", s.configPath, err)
			}
		}
	}

	// 4. Load environment variables with AGENT_ prefix (override file and defaults)
	// Use __ for each nesting level: AGENT_MODEL__PROVIDER -> model.provider, AGENT_MODEL__OPENAI__API_KEY -> model.openai.api_key
	if err := k.Load(env.ProviderWithValue(EnvPrefix, ".", browserEnvironmentProviderValue), nil); err != nil {
		return nil, fmt.Errorf("failed to load environment variables: %w", err)
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}
	if err := cfg.Browser.Validate(); err != nil {
		return nil, fmt.Errorf("invalid browser configuration: %w", err)
	}
	if err := cfg.ValidateInteractive(); err != nil {
		return nil, fmt.Errorf("invalid interactive tool configuration: %w", err)
	}

	s.cached = &cfg
	return &cfg, nil
}

func validateInteractiveEnvironment() error {
	for _, spec := range interactiveConfigFieldSpecs {
		envName := configEnvironmentName(spec.path)
		value, ok := os.LookupEnv(envName)
		if !ok {
			continue
		}
		if err := validateInteractiveRawDuration(value, "environment variable "+envName); err != nil {
			return err
		}
	}
	return nil
}

func validateInteractiveYAML(data []byte) error {
	var root map[string]interface{}
	if err := yamlv3.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parse YAML: %w", err)
	}
	for _, spec := range interactiveConfigFieldSpecs {
		value, present, err := lookupYAMLPath(root, spec.path)
		if err != nil {
			return err
		}
		if !present {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("YAML field %s: expected a Go duration such as 5s", spec.path)
		}
		if err := validateInteractiveRawDuration(text, "YAML field "+spec.path); err != nil {
			return err
		}
	}
	return nil
}

func validateInteractiveRawDuration(value, source string) error {
	duration, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("%s: invalid Go duration %q: %w", source, value, err)
	}
	if duration <= 0 {
		return fmt.Errorf("%s: duration must be positive", source)
	}
	return nil
}

// validateBrowserEnvironment validates only the canonical browser environment
// variables. The generic AGENT_ loader remains available for existing model
// and tool settings, while browser values get strict type/encoding checks.
func validateBrowserEnvironment() error {
	for _, spec := range browserConfigFieldSpecs {
		envName := browserEnvironmentName(spec.path)
		value, ok := os.LookupEnv(envName)
		if !ok {
			continue
		}
		if err := validateBrowserRawValue(spec, value, "environment variable "+envName); err != nil {
			return err
		}
	}
	return nil
}

func browserEnvironmentName(path string) string {
	return configEnvironmentName(path)
}

func configEnvironmentName(path string) string {
	return EnvPrefix + strings.ReplaceAll(strings.ToUpper(path), ".", "__")
}

// browserEnvironmentProviderValue converts the two JSON-list environment
// values to []string before Koanf unmarshal. Without this conversion Koanf's
// weak slice conversion would treat the entire JSON document as one origin.
func browserEnvironmentProviderValue(key, value string) (string, interface{}) {
	path := strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(key, EnvPrefix), "__", "."))
	for _, spec := range browserConfigFieldSpecs {
		if spec.path != path {
			continue
		}
		if spec.kind != browserConfigStringList {
			break
		}
		var values []string
		// validateBrowserEnvironment has already checked this exact value, so
		// this decode cannot fail. Keep the original string as a defensive
		// fallback for callers that use this callback independently.
		if err := json.Unmarshal([]byte(value), &values); err == nil {
			return path, values
		}
		break
	}
	return path, value
}

func validateBrowserYAML(data []byte) error {
	var root map[string]interface{}
	if err := yamlv3.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parse YAML: %w", err)
	}
	for _, spec := range browserConfigFieldSpecs {
		value, present, err := lookupYAMLPath(root, spec.path)
		if err != nil {
			return err
		}
		if !present {
			continue
		}
		if err := validateBrowserRawValue(spec, value, "YAML field "+spec.path); err != nil {
			return err
		}
	}
	return nil
}

func lookupYAMLPath(root map[string]interface{}, path string) (interface{}, bool, error) {
	var current interface{} = root
	parts := strings.Split(path, ".")
	for index, part := range parts {
		mapping, ok := current.(map[string]interface{})
		if !ok {
			return nil, false, fmt.Errorf("YAML field %s: expected mapping at %q", path, strings.Join(parts[:index], "."))
		}
		value, present := mapping[part]
		if !present {
			return nil, false, nil
		}
		current = value
	}
	return current, true, nil
}

func validateBrowserRawValue(spec browserConfigFieldSpec, value interface{}, source string) error {
	fromEnvironment := strings.HasPrefix(source, "environment variable ")
	switch spec.kind {
	case browserConfigString:
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s: expected a string", source)
		}
	case browserConfigBool:
		switch typed := value.(type) {
		case bool:
			if fromEnvironment {
				return fmt.Errorf("%s: expected strict boolean true or false", source)
			}
		case string:
			if !fromEnvironment || (typed != "true" && typed != "false") {
				return fmt.Errorf("%s: expected strict boolean true or false", source)
			}
		default:
			return fmt.Errorf("%s: expected strict boolean true or false", source)
		}
	case browserConfigEnum:
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s: expected one of %s", source, strings.Join(spec.allowed, ", "))
		}
		if !containsString(spec.allowed, text) {
			return fmt.Errorf("%s: invalid value %q (want one of %s)", source, text, strings.Join(spec.allowed, ", "))
		}
	case browserConfigDuration:
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s: expected a positive Go duration such as 30s", source)
		}
		duration, err := time.ParseDuration(text)
		if err != nil || duration <= 0 {
			if err != nil {
				return fmt.Errorf("%s: invalid Go duration %q: %w", source, text, err)
			}
			return fmt.Errorf("%s: duration must be positive", source)
		}
	case browserConfigSize:
		if text, ok := value.(string); ok {
			if !fromEnvironment {
				return fmt.Errorf("%s: expected a non-negative decimal integer", source)
			}
			if _, err := parseNonNegativeDecimalSize(text); err != nil {
				return fmt.Errorf("%s: %w", source, err)
			}
			return nil
		}
		if err := validateYAMLInteger(value); err != nil {
			return fmt.Errorf("%s: %w", source, err)
		}
	case browserConfigStringList:
		if text, ok := value.(string); ok {
			if !fromEnvironment {
				return fmt.Errorf("%s: expected a YAML list of strings", source)
			}
			var values []interface{}
			if err := json.Unmarshal([]byte(text), &values); err != nil {
				return fmt.Errorf("%s: expected a JSON array of strings: %w", source, err)
			}
			if values == nil {
				return fmt.Errorf("%s: expected a JSON array of strings", source)
			}
			for index, item := range values {
				if _, ok := item.(string); !ok {
					return fmt.Errorf("%s: item %d must be a string", source, index)
				}
			}
			return nil
		}
		values, ok := value.([]interface{})
		if !ok {
			return fmt.Errorf("%s: expected a YAML list of strings", source)
		}
		for index, item := range values {
			if _, ok := item.(string); !ok {
				return fmt.Errorf("%s: item %d must be a string", source, index)
			}
		}
	default:
		return fmt.Errorf("%s: unsupported browser configuration value", source)
	}
	return nil
}

func parseNonNegativeDecimalSize(value string) (int, error) {
	if value == "" {
		return 0, fmt.Errorf("expected a non-negative decimal integer")
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, fmt.Errorf("invalid size %q: expected a non-negative decimal integer", value)
		}
	}
	parsed, err := strconv.ParseUint(value, 10, strconv.IntSize)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: expected a non-negative decimal integer: %w", value, err)
	}
	return int(parsed), nil
}

func validateYAMLInteger(value interface{}) error {
	maxInt := uint64(^uint(0) >> 1)
	switch typed := value.(type) {
	case int:
		if typed < 0 {
			return fmt.Errorf("expected a non-negative decimal integer")
		}
	case int8:
		if typed < 0 {
			return fmt.Errorf("expected a non-negative decimal integer")
		}
	case int16:
		if typed < 0 {
			return fmt.Errorf("expected a non-negative decimal integer")
		}
	case int32:
		if typed < 0 {
			return fmt.Errorf("expected a non-negative decimal integer")
		}
	case int64:
		if typed < 0 || uint64(typed) > maxInt {
			return fmt.Errorf("expected a non-negative decimal integer within int range")
		}
	case uint:
		if uint64(typed) > maxInt {
			return fmt.Errorf("expected a non-negative decimal integer within int range")
		}
	case uint8:
	case uint16:
	case uint32:
		if uint64(typed) > maxInt {
			return fmt.Errorf("expected a non-negative decimal integer within int range")
		}
	case uint64:
		if typed > maxInt {
			return fmt.Errorf("expected a non-negative decimal integer within int range")
		}
	default:
		return fmt.Errorf("expected a non-negative decimal integer")
	}
	return nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// Validate checks that the selected provider has required fields (e.g. API key).
// Local providers do not require an API key but do require a baseURL.
func (c Config) Validate() error {
	if err := c.ValidateInteractive(); err != nil {
		return fmt.Errorf("invalid interactive tool configuration: %w", err)
	}
	if c.Model.Provider == ProviderFal {
		return c.validateFal()
	}
	if c.Model.Provider == ProviderGrok {
		return fmt.Errorf("model.provider %q is session-only; use agent session --record or --replay for Grok realtime sessions", ProviderGrok)
	}
	active, err := c.ActiveOpenAIConfig()
	if err != nil {
		return err
	}
	if c.Model.Provider == ProviderLocal {
		if active.BaseURL == "" {
			return fmt.Errorf("base URL is required for local provider (use --base-url or configure model.local.base_url in %s)", ConfigFileName)
		}
		return nil
	}
	if active.APIKey == "" {
		return fmt.Errorf("model API key is required for provider %q (set AGENT_MODEL__%s__API_KEY or configure in %s)", c.Model.Provider, strings.ToUpper(c.Model.Provider), ConfigFileName)
	}
	return nil
}

// validateFal checks fal.ai provider config requirements.
func (c Config) validateFal() error {
	if c.Model.Fal == nil {
		return fmt.Errorf("model.provider is fal but model.fal is not set")
	}
	if c.Model.Fal.APIKey == "" {
		return fmt.Errorf("model API key is required for fal provider (set AGENT_MODEL__FAL__API_KEY or configure model.fal.api_key in %s)", ConfigFileName)
	}
	return nil
}

// ActiveGrokConfig returns the Grok realtime session config for model.provider=grok.
func (c Config) ActiveGrokConfig() (*GrokConfig, error) {
	if c.Model.Provider != ProviderGrok {
		return nil, fmt.Errorf("unsupported session model.provider %q (use %s)", c.Model.Provider, ProviderGrok)
	}
	if c.Model.Grok == nil {
		return nil, fmt.Errorf("model.provider is %s but model.grok is not set", ProviderGrok)
	}
	return c.Model.Grok, nil
}

// ValidateGrokSession checks live Grok realtime session requirements.
func (c Config) ValidateGrokSession() error {
	active, err := c.ActiveGrokConfig()
	if err != nil {
		return err
	}
	if active.APIKey == "" {
		return fmt.Errorf("grok API key is required for live session record mode (set AGENT_MODEL__GROK__API_KEY, pass --api-key, or configure model.grok.api_key in %s)", ConfigFileName)
	}
	if active.Model == "" {
		return fmt.Errorf("grok session model is required for live session record mode (set AGENT_MODEL__GROK__MODEL, pass --model, or configure model.grok.model in %s)", ConfigFileName)
	}
	return nil
}

// ActiveOpenAIConfig returns the OpenAI-style config for the selected provider (openai, openrouter, or local).
// Use this when building an OpenAI-compatible gateway. Returns error if provider is not openai/openrouter/local or config is missing.
func (c Config) ActiveOpenAIConfig() (*OpenAIConfig, error) {
	switch c.Model.Provider {
	case ProviderOpenAI:
		if c.Model.OpenAI == nil {
			return nil, fmt.Errorf("model.provider is openai but model.openai is not set")
		}
		return c.Model.OpenAI, nil
	case ProviderOpenRouter:
		if c.Model.OpenRouter == nil {
			return nil, fmt.Errorf("model.provider is openrouter but model.openrouter is not set")
		}
		return c.Model.OpenRouter, nil
	case ProviderLocal:
		if c.Model.Local == nil {
			return nil, fmt.Errorf("model.provider is local but model.local is not set")
		}
		return c.Model.Local, nil
	default:
		return nil, fmt.Errorf("unsupported model.provider %q (use openai, openrouter, or local)", c.Model.Provider)
	}
}

// getDefaultConfig returns the default configuration values.
func getDefaultConfig() *Config {
	return &Config{
		Model: ModelConfig{
			Provider: DefaultModelProvider,
			OpenRouter: &OpenAIConfig{
				Model:   DefaultModelModel,
				BaseURL: "https://openrouter.ai/api/v1",
			},
		},
		Tools: ToolsConfig{
			Interactive: DefaultInteractiveToolConfig(),
			List:        DefaultToolsList(),
		},
		Browser: DefaultBrowserConfig(),
	}
}
