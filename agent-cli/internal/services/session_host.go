package services

// This file is the CLI host adapter for the embeddable session service. It is
// deliberately kept outside go-agent-runtime: config files, AGENTS.md, and
// the CLI session directory are host concerns and must be resolved before a
// neutral session.Request reaches the reusable runtime.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// NewSessionResolverWithStoreFactory creates the CLI host resolver with an
// explicitly composed runtime persistence factory. The resolver remains the
// owner of config, prompt, and workspace discovery; the store factory owns
// only the durable session/trace codec.
func NewSessionResolverWithStoreFactory(globalFlags *flags.GlobalFlags, storeFactory session.FileStoreFactory) session.Resolver {
	return &sessionHostResolver{globalFlags: globalFlags, storeFactory: storeFactory}
}

type sessionHostResolver struct {
	globalFlags  *flags.GlobalFlags
	storeFactory session.FileStoreFactory
}

func (h *sessionHostResolver) Resolve(ctx context.Context, request session.Request) (session.Resolution, error) {
	if h.storeFactory == nil {
		return session.Resolution{}, fmt.Errorf("session file store factory is required")
	}
	if err := ctx.Err(); err != nil {
		return session.Resolution{}, err
	}
	configDir, err := cliConfigDir(h.globalFlags)
	if err != nil {
		return session.Resolution{}, err
	}
	workDir, err := cliWorkDir(h.globalFlags)
	if err != nil {
		return session.Resolution{}, err
	}

	configStorage := config.NewConfigStorage(filepath.Join(configDir, config.ConfigFileName))
	loaded, err := configStorage.Load()
	if err != nil {
		return session.Resolution{}, fmt.Errorf("load CLI config: %w", err)
	}
	effective := loaded.ApplyOverrides(request.APIKey, request.Model, request.Provider, request.BaseURL)
	provider, err := resolvedProvider(effective)
	if err != nil {
		return session.Resolution{}, err
	}

	models, err := cliModelCatalog(configDir)
	if err != nil {
		return session.Resolution{}, err
	}
	prompt, err := resolveCLIRequestPrompt(request, workDir)
	if err != nil {
		return session.Resolution{}, err
	}
	store, err := h.storeFactory.Open(session.FileStoreOptions{Directory: configDir, WorkspaceDirectory: workDir})
	if err != nil {
		return session.Resolution{}, fmt.Errorf("open CLI session store: %w", err)
	}

	return session.Resolution{
		Provider:             provider,
		SystemPrompt:         prompt,
		SystemPromptResolved: true,
		WorkspaceDir:         workDir,
		AllowPaths:           globalAllowPaths(h.globalFlags, workDir),
		SkillRoots: []runtimeTools.SkillRoot{
			{Directory: filepath.Join(workDir, "skills")},
			{Directory: filepath.Join(configDir, "skills")},
		},
		Models:                   models,
		Store:                    store,
		TraceStore:               store,
		ContinuationNudgeEnabled: effective.Model.ContinuationNudgeEnabled,
		ContinuationNudgeMessage: effective.Model.ContinuationNudgeMessage,
		RepetitionPenalty:        effective.Model.RepetitionPenalty,
	}, nil
}

// NewSessionStoreWithFactory opens the canonical runtime-managed store for a
// CLI host. The factory is supplied by the outer application wire so command
// transports never construct a private persistence implementation.
func NewSessionStoreWithFactory(globalFlags *flags.GlobalFlags, storeFactory session.FileStoreFactory) (session.ManagedStore, error) {
	if storeFactory == nil {
		return nil, fmt.Errorf("session file store factory is required")
	}
	configDir, err := cliConfigDir(globalFlags)
	if err != nil {
		return nil, err
	}
	workDir, err := cliWorkDir(globalFlags)
	if err != nil {
		return nil, err
	}
	managed, err := storeFactory.Open(session.FileStoreOptions{Directory: configDir, WorkspaceDirectory: workDir})
	if err != nil {
		return nil, fmt.Errorf("open CLI session store: %w", err)
	}
	return managed, nil
}

func cliConfigDir(globalFlags *flags.GlobalFlags) (string, error) {
	storage, err := config.NewDefaultConfigStorage(globalFlags.ConfigDir())
	if err != nil {
		return "", fmt.Errorf("resolve CLI config directory: %w", err)
	}
	return filepath.Dir(storage.Path()), nil
}

func cliWorkDir(globalFlags *flags.GlobalFlags) (string, error) {
	workDir := globalFlags.WorkDir()
	if workDir == "" {
		var err error
		workDir, err = os.Getwd() //nolint:forbidigo // This CLI host boundary resolves process state before injecting runtime values.
		if err != nil {
			return "", fmt.Errorf("resolve CLI work directory: %w", err)
		}
	}
	workDir, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("resolve CLI work directory %q: %w", workDir, err)
	}
	if info, err := os.Stat(workDir); err != nil {
		return "", fmt.Errorf("invalid CLI work directory %q: %w", workDir, err)
	} else if !info.IsDir() {
		return "", fmt.Errorf("invalid CLI work directory %q: not a directory", workDir)
	}
	return filepath.Clean(workDir), nil
}

// ResolveCLIWorkDir resolves the command's effective workspace before a
// request reaches a reusable service. In particular, an omitted --workdir is
// resolved once at this host boundary and then injected into every request;
// runtime services must never rediscover process cwd themselves.
func ResolveCLIWorkDir(globalFlags *flags.GlobalFlags) (string, error) {
	return cliWorkDir(globalFlags)
}

func globalAllowPaths(globalFlags *flags.GlobalFlags, workDir string) []string {
	paths := globalFlags.AllowPaths()
	for i, path := range paths {
		if !filepath.IsAbs(path) {
			path = filepath.Join(workDir, path)
		}
		paths[i] = filepath.Clean(path)
	}
	return paths
}

func resolvedProvider(cfg config.Config) (session.ProviderConfig, error) {
	provider := session.ProviderConfig{Provider: cfg.Model.Provider}
	switch cfg.Model.Provider {
	case config.ProviderOpenAI:
		active, err := cfg.ActiveOpenAIConfig()
		if err != nil {
			return session.ProviderConfig{}, err
		}
		provider.Model, provider.APIKey, provider.BaseURL = active.Model, active.APIKey, active.BaseURL
	case config.ProviderOpenRouter:
		active, err := cfg.ActiveOpenAIConfig()
		if err != nil {
			return session.ProviderConfig{}, err
		}
		provider.Model, provider.APIKey, provider.BaseURL = active.Model, active.APIKey, active.BaseURL
	case config.ProviderLocal:
		active, err := cfg.ActiveOpenAIConfig()
		if err != nil {
			return session.ProviderConfig{}, err
		}
		provider.Model, provider.APIKey, provider.BaseURL = active.Model, active.APIKey, active.BaseURL
	case config.ProviderFal:
		if cfg.Model.Fal == nil {
			return session.ProviderConfig{}, fmt.Errorf("model.provider is fal but model.fal is not set")
		}
		provider.Model, provider.APIKey, provider.BaseURL = cfg.Model.Fal.Model, cfg.Model.Fal.APIKey, cfg.Model.Fal.BaseURL
		provider.Fal = &session.FalProviderConfig{Model: provider.Model, APIKey: provider.APIKey, BaseURL: provider.BaseURL}
	case config.ProviderGrok:
		if cfg.Model.Grok == nil {
			return session.ProviderConfig{}, fmt.Errorf("model.provider is grok but model.grok is not set")
		}
		provider.Model, provider.APIKey, provider.BaseURL = cfg.Model.Grok.Model, cfg.Model.Grok.APIKey, cfg.Model.Grok.BaseURL
	default:
		return session.ProviderConfig{}, fmt.Errorf("unsupported model.provider %q", cfg.Model.Provider)
	}
	return provider, nil
}

func cliModelCatalog(configDir string) ([]session.ModelInfo, error) {
	storage, err := config.NewModelsConfigStorage(configDir)
	if err != nil {
		return nil, fmt.Errorf("initialize CLI model catalog: %w", err)
	}
	catalog, err := storage.Load()
	if err != nil {
		return nil, fmt.Errorf("load CLI model catalog: %w", err)
	}
	models := make([]session.ModelInfo, 0, len(catalog.Models))
	for _, model := range catalog.Models {
		models = append(models, session.ModelInfo{
			Name:                    model.Name,
			Aliases:                 append([]string(nil), model.Aliases...),
			Providers:               append([]string(nil), model.Providers...),
			InputModalities:         append([]string(nil), model.InputModalities...),
			OutputModalities:        append([]string(nil), model.OutputModalities...),
			SupportedInputMimeTypes: append([]string(nil), model.SupportedInputMimeTypes...),
		})
	}
	return models, nil
}

func resolveCLIPrompt(value, workDir string) (string, error) {
	if value == "none" {
		return "", nil
	}
	if value != "" {
		if _, err := os.Stat(value); err == nil {
			data, readErr := os.ReadFile(value)
			if readErr != nil {
				return "", fmt.Errorf("read system prompt %s: %w", value, readErr)
			}
			return string(data), nil
		}
		return value, nil
	}
	agentsPath := filepath.Join(workDir, "AGENTS.md")
	data, err := os.ReadFile(agentsPath)
	if err == nil {
		return string(data), nil
	}
	if os.IsNotExist(err) {
		return "", nil
	}
	return "", fmt.Errorf("read AGENTS.md %s: %w", agentsPath, err)
}

func resolveCLIRequestPrompt(request session.Request, workDir string) (string, error) {
	// Existing history already owns its system prompt. Host discovery must not
	// turn a resume request into a conflicting explicit prompt override.
	if request.SystemPrompt == "" && (request.SessionID != "" || request.ContinueLastSession) {
		return "", nil
	}
	return resolveCLIPrompt(request.SystemPrompt, workDir)
}
