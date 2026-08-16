package cli

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/spf13/cobra"
	yamlv3 "gopkg.in/yaml.v3"
)

const redactedAPIKey = "<redacted>"

// ConfigCommand is the config group (parent command); subcommands are wired in routes.go.
type ConfigCommand struct{}

// NewConfigCommand returns the config group command constructor.
func NewConfigCommand() *ConfigCommand {
	return &ConfigCommand{}
}

// Generate returns the cobra command for the config group.
func (c *ConfigCommand) Generate() *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Configuration management commands",
		Long:  "Commands to manage agent CLI configuration.",
	}
}

// ConfigAddLocalCommand wraps the config add-local subcommand.
type ConfigAddLocalCommand struct {
	globalFlags *flags.GlobalFlags
	baseURL     string
	model       string
}

// NewConfigAddLocalCommand creates the ConfigAddLocalCommand with the given dependencies.
func NewConfigAddLocalCommand(globalFlags *flags.GlobalFlags) *ConfigAddLocalCommand {
	return &ConfigAddLocalCommand{globalFlags: globalFlags}
}

// Generate returns the cobra command for config add-local.
func (c *ConfigAddLocalCommand) Generate() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add-local",
		Short: "Add a local inference provider to config",
		Long:  "Add a local inference provider entry to ~/.agent-cli/config.yaml.\nThe local provider connects to an OpenAI-compatible server (Ollama, llama.cpp, LM Studio, vLLM) without requiring an API key.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.run(cmd)
		},
	}

	cmd.Flags().StringVar(&c.baseURL, "base-url", "", "Base URL of the local inference server (e.g. http://localhost:11434/v1)")
	cmd.Flags().StringVar(&c.model, "model", "", "Model name to use (e.g. llama3, mistral)")

	_ = cmd.MarkFlagRequired("base-url")
	_ = cmd.MarkFlagRequired("model")

	return cmd
}

func (c *ConfigAddLocalCommand) run(cmd *cobra.Command) error {
	// Resolve config path
	configDir := c.globalFlags.ConfigDir()
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("get home directory: %w", err)
		}
		configDir = filepath.Join(home, config.ConfigDirName)
	}
	configPath := filepath.Join(configDir, config.ConfigFileName)

	// Load existing config (or create default)
	storage, err := config.NewDefaultConfigStorage(configDir)
	if err != nil {
		return fmt.Errorf("init config: %w", err)
	}
	cfg, err := storage.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Probe the server URL to check reachability
	c.probeServer(cmd, c.baseURL)

	// Update the config with the local provider
	cfg.Model.Local = &config.OpenAIConfig{
		Model:   c.model,
		BaseURL: c.baseURL,
	}
	cfg.Model.Provider = "local"

	// Write back to YAML
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	redactEnvironmentAPIKeys(cfg)
	data, err := yamlv3.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Local provider added to %s\n", configPath); err != nil {
		return fmt.Errorf("write config summary: %w", err)
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "  provider: local\n"); err != nil {
		return fmt.Errorf("write config summary: %w", err)
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "  base_url: %s\n", c.baseURL); err != nil {
		return fmt.Errorf("write config summary: %w", err)
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "  model: %s\n", c.model); err != nil {
		return fmt.Errorf("write config summary: %w", err)
	}
	return nil
}

func redactEnvironmentAPIKeys(cfg *config.Config) {
	if cfg == nil {
		return
	}

	redact := func(envName string, apiKey *string) {
		value, ok := os.LookupEnv(envName)
		if ok && value != "" && apiKey != nil && *apiKey == value {
			*apiKey = redactedAPIKey
		}
	}

	if cfg.Model.OpenAI != nil {
		redact("AGENT_MODEL__OPENAI__API_KEY", &cfg.Model.OpenAI.APIKey)
	}
	if cfg.Model.Claude != nil {
		redact("AGENT_MODEL__CLAUDE__API_KEY", &cfg.Model.Claude.APIKey)
	}
	if cfg.Model.OpenRouter != nil {
		redact("AGENT_MODEL__OPENROUTER__API_KEY", &cfg.Model.OpenRouter.APIKey)
	}
	if cfg.Model.Local != nil {
		redact("AGENT_MODEL__LOCAL__API_KEY", &cfg.Model.Local.APIKey)
	}
	if cfg.Model.Fal != nil {
		redact("AGENT_MODEL__FAL__API_KEY", &cfg.Model.Fal.APIKey)
	}
	if cfg.Model.Grok != nil {
		redact("AGENT_MODEL__GROK__API_KEY", &cfg.Model.Grok.APIKey)
	}
}

// probeServer attempts to reach the inference server's models endpoint.
// Prints a warning if unreachable but does not fail — the server may not be running yet.
func (c *ConfigAddLocalCommand) probeServer(cmd *cobra.Command, baseURL string) {
	// Try /v1/models and /models endpoints
	urls := []string{
		strings.TrimRight(baseURL, "/") + "/models",
	}
	// If baseURL doesn't end with /v1, also try with /v1/models
	if !strings.HasSuffix(strings.TrimRight(baseURL, "/"), "/v1") {
		urls = append(urls, strings.TrimRight(baseURL, "/")+"/v1/models")
	}

	client := &http.Client{Timeout: 5 * time.Second}
	for _, url := range urls {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Server reachable at %s\n", url)
				return
			}
		}
	}

	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not reach server at %s (server may not be running yet)\n", baseURL)
}
