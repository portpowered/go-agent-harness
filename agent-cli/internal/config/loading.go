package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

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

	// 1. Load default values using struct provider
	defaultCfg := getDefaultConfig()
	if err := k.Load(structs.Provider(defaultCfg, structTag), nil); err != nil {
		return nil, fmt.Errorf("failed to load default config: %w", err)
	}

	// 2. Load configuration from YAML file (if exists)
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

	// 3. Load environment variables with AGENT_ prefix (override file and defaults)
	// Use __ for each nesting level: AGENT_MODEL__PROVIDER -> model.provider, AGENT_MODEL__OPENAI__API_KEY -> model.openai.api_key
	if err := k.Load(env.Provider(EnvPrefix, ".", func(s string) string {
		s = strings.TrimPrefix(s, EnvPrefix)
		s = strings.ToLower(s)
		s = strings.ReplaceAll(s, "__", ".")
		return s
	}), nil); err != nil {
		return nil, fmt.Errorf("failed to load environment variables: %w", err)
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	s.cached = &cfg
	return &cfg, nil
}

// Validate checks that the selected provider has required fields (e.g. API key).
// Local providers do not require an API key but do require a baseURL.
func (c Config) Validate() error {
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
			List: DefaultToolsList(),
		},
	}
}
