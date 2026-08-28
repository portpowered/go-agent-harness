package agent

// This file owns request preparation: configuration and session loading, prompt resolution metadata, and system-prompt construction before execution.

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/session"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/skills"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/sysinfo"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/workspace"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// PromptResolutionSource describes an input consulted while building a system
// prompt. Path is set for filesystem-backed sources.
type PromptResolutionSource struct {
	Kind string
	Path string
}

// PromptResolutionSideEffect names IO, environment, or runtime side effects
// owned by the CLI prompt resolution path.
type PromptResolutionSideEffect string

const (
	PromptSideEffectReadPromptFile     PromptResolutionSideEffect = "read_prompt_file"
	PromptSideEffectCreateAgentsMD     PromptResolutionSideEffect = "create_agents_md"
	PromptSideEffectReadAgentsMD       PromptResolutionSideEffect = "read_agents_md"
	PromptSideEffectLoadConfig         PromptResolutionSideEffect = "load_config"
	PromptSideEffectCollectSystemInfo  PromptResolutionSideEffect = "collect_system_info"
	PromptSideEffectReadSkillsMetadata PromptResolutionSideEffect = "read_skills_metadata"
	PromptSideEffectAppendPromptSuffix PromptResolutionSideEffect = "append_prompt_suffix"
)

const (
	PromptSourceKindLiteralPrompt = "literal_prompt"
	PromptSourceKindPromptFile    = "prompt_file"
	PromptSourceKindAgentsMD      = "agents_md"
	PromptSourceKindConfig        = "config"
	PromptSourceKindSystemInfo    = "system_info"
	PromptSourceKindSkills        = "skills"
	PromptSourceKindSuffix        = "suffix"
)

// PromptResolutionDetails exposes the sources and side effects involved in
// resolving a system prompt.
type PromptResolutionDetails struct {
	Sources     []PromptResolutionSource
	SideEffects []PromptResolutionSideEffect
}

func (d *PromptResolutionDetails) addSource(kind, path string) {
	if d != nil {
		d.Sources = append(d.Sources, PromptResolutionSource{Kind: kind, Path: path})
	}
}

func (d *PromptResolutionDetails) addSideEffect(effect PromptResolutionSideEffect) {
	if d != nil {
		d.SideEffects = append(d.SideEffects, effect)
	}
}

// loadConfig loads the configuration from disk, applying CLI overrides and
// validating provider requirements for real executions.
func (e *Executor) loadConfig(cfg *Config) (*config.Config, error) {
	return e.loadConfigWithOptions(cfg, true)
}

// loadConfigAllowingInferencerOverride loads config even when provider
// credentials are intentionally absent because a test inferencer override will
// satisfy execution.
func (e *Executor) loadConfigAllowingInferencerOverride(cfg *Config) (*config.Config, error) {
	return e.loadConfigWithOptions(cfg, e.inferencerOverride == nil)
}

func (e *Executor) loadConfigWithOptions(cfg *Config, validate bool) (*config.Config, error) {
	configDir := cfg.ConfigDir
	storage, err := config.NewDefaultConfigStorage(configDir)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize config: %w", err)
	}

	loadedCfg, err := storage.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	// Apply CLI flag overrides (--api-key, --model, --provider, --base-url)
	if cfg.APIKey != "" || cfg.Model != "" || cfg.Provider != "" || cfg.BaseURL != "" {
		data := loadedCfg.ApplyOverrides(cfg.APIKey, cfg.Model, cfg.Provider, cfg.BaseURL)
		loadedCfg = &data
	}

	shouldValidate := validate && !e.relaxModelValidation
	if e.inferencerOverride != nil && cfg.ConfigDir == "" {
		shouldValidate = false
	}
	if shouldValidate {
		if err := loadedCfg.Validate(); err != nil {
			return nil, err
		}
	}

	return loadedCfg, nil
}

// GetSessionStorage returns a session storage instance.
func (e *Executor) GetSessionStorage(cfg *Config) (*session.Storage, error) {
	return e.getSessionStorage(cfg)
}

// getSessionStorage returns a session storage instance.
func (e *Executor) getSessionStorage(cfg *Config) (*session.Storage, error) {
	configDir := cfg.ConfigDir
	workspaceDir := configDir
	if workspaceDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("get workspace dir: %w", err)
		}
		workspaceDir = filepath.Join(home, config.ConfigDirName)
	}
	workspaceDir, err := filepath.Abs(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("get workspace dir: %w", err)
	}
	return session.NewStorage(workspaceDir), nil
}

// getInitialHistory resolves the session ID and loads initial history based on config.
func (e *Executor) getInitialHistory(cfg *Config, sessionStorage *session.Storage) ([]messages.Message, string, error) {
	var initialHistory []messages.Message
	var sessionID string
	var err error

	if cfg.SessionID != "" {
		sessionID = cfg.SessionID
		initialHistory, err = sessionStorage.Load(sessionID)
		if err != nil {
			return nil, "", fmt.Errorf("load session %s: %w", sessionID, err)
		}
	} else if cfg.ContinueLastSession {
		sessionID, err = sessionStorage.Latest()
		if err != nil {
			return nil, "", fmt.Errorf("find latest session: %w", err)
		}
		if sessionID == "" {
			return nil, "", fmt.Errorf("no previous session to continue (use --session-id or run an ask first)")
		}
		initialHistory, err = sessionStorage.Load(sessionID)
		if err != nil {
			return nil, "", fmt.Errorf("load latest session: %w", err)
		}
	} else if len(cfg.InitialHistory) > 0 {
		// Use provided initial history (for chat with explicit session)
		initialHistory = cfg.InitialHistory
		sessionID = cfg.SessionID
		if sessionID == "" {
			sessionID = sessionStorage.NewSessionID()
		}
	} else {
		sessionID = sessionStorage.NewSessionID()
	}

	return initialHistory, sessionID, nil
}

// LoadSystemPrompt loads and resolves the system prompt from config or file.
// It is exported so callers (e.g. /system command) can display the resolved prompt.
func (e *Executor) LoadSystemPrompt(cfg *Config, workspaceDir string, toolDefs []messages.ToolDefinition) (string, error) {
	prompt, _, err := e.LoadSystemPromptWithDetails(cfg, workspaceDir, toolDefs)
	return prompt, err
}

// LoadSystemPromptWithDetails loads and resolves the system prompt and reports
// the prompt sources and CLI-owned side effects consulted along the way.
func (e *Executor) LoadSystemPromptWithDetails(cfg *Config, workspaceDir string, toolDefs []messages.ToolDefinition) (string, PromptResolutionDetails, error) {
	var details PromptResolutionDetails
	prompt, err := e.loadSystemPrompt(cfg, workspaceDir, toolDefs, &details)
	return prompt, details, err
}

// loadSystemPrompt loads the system prompt from config or file.
func (e *Executor) loadSystemPrompt(cfg *Config, workspaceDir string, toolDefs []messages.ToolDefinition, details *PromptResolutionDetails) (string, error) {
	systemPrompt := ""
	if cfg.SystemPrompt == "none" {
		// Explicitly no system prompt
	} else if cfg.SystemPrompt != "" {
		if _, err := os.Stat(cfg.SystemPrompt); err == nil {
			details.addSource(PromptSourceKindPromptFile, cfg.SystemPrompt)
			details.addSideEffect(PromptSideEffectReadPromptFile)
			data, err := os.ReadFile(cfg.SystemPrompt)
			if err != nil {
				return "", fmt.Errorf("read system prompt %s: %w", cfg.SystemPrompt, err)
			}
			systemPrompt = string(data)
		} else {
			// Stat is only an existence probe. A value that cannot be probed as
			// a path (for example, long prose or a path containing invalid
			// syntax) is still valid literal prompt text. Once Stat succeeds,
			// ReadFile above owns errors for the selected existing entry.
			details.addSource(PromptSourceKindLiteralPrompt, "")
			systemPrompt = cfg.SystemPrompt
		}
	} else {
		// Default: use AGENTS.md from workspace
		agentsPath := filepath.Join(workspaceDir, "AGENTS.md")
		if _, err := os.Stat(agentsPath); os.IsNotExist(err) {
			details.addSideEffect(PromptSideEffectCreateAgentsMD)
		}
		if err := workspace.EnsureAgentsMD(workspaceDir, toolDefs); err != nil {
			return "", fmt.Errorf("initialize AGENTS.md: %w", err)
		}
		details.addSource(PromptSourceKindAgentsMD, agentsPath)
		details.addSideEffect(PromptSideEffectReadAgentsMD)
		if data, err := os.ReadFile(agentsPath); err == nil {
			systemPrompt = string(data)
		}
	}

	// Prepend dynamic system info unless explicitly disabled.
	if !cfg.NoSystemInformation {
		var model, provider string
		details.addSource(PromptSourceKindConfig, cfg.ConfigDir)
		details.addSideEffect(PromptSideEffectLoadConfig)
		loadedCfg, err := e.loadConfigAllowingInferencerOverride(cfg)
		if err == nil {
			provider = loadedCfg.Model.Provider
			if provider == "fal" && loadedCfg.Model.Fal != nil {
				model = loadedCfg.Model.Fal.Model
			} else if active, err := loadedCfg.ActiveOpenAIConfig(); err == nil {
				model = active.Model
			}
		}
		details.addSource(PromptSourceKindSystemInfo, "")
		details.addSideEffect(PromptSideEffectCollectSystemInfo)
		systemPrompt = sysinfo.Collect(model, provider).Format() + systemPrompt
	}

	// Append skills metadata (name + description) so the model knows available skills.
	if workspaceDir != "" {
		configSkillsDir := cfg.ConfigDir
		loader := skills.NewLoader(workspaceDir, configSkillsDir)
		details.addSource(PromptSourceKindSkills, filepath.Join(workspaceDir, "skills"))
		if configSkillsDir != "" {
			details.addSource(PromptSourceKindSkills, filepath.Join(configSkillsDir, "skills"))
		}
		details.addSideEffect(PromptSideEffectReadSkillsMetadata)
		summary, err := loader.BuildSummary()
		if err == nil && summary != "" {
			systemPrompt = systemPrompt + "\n\n---\n\n" + summary
		}
	}

	// Append iteration-specific suffix (for loop mode).
	if cfg.SystemPromptSuffix != "" {
		details.addSource(PromptSourceKindSuffix, "")
		details.addSideEffect(PromptSideEffectAppendPromptSuffix)
		if systemPrompt != "" {
			systemPrompt = systemPrompt + "\n\n" + cfg.SystemPromptSuffix
		} else {
			systemPrompt = cfg.SystemPromptSuffix
		}
	}

	return systemPrompt, nil
}

// NewChatSessionID returns a new session ID in the workspace (for chat).
func (e *Executor) NewChatSessionID(cfg *Config) (string, error) {
	storage, err := e.getSessionStorage(cfg)
	if err != nil {
		return "", err
	}
	return storage.NewSessionID(), nil
}
