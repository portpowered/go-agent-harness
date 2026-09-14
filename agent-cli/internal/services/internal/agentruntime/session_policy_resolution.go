package agentruntime

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
)

func sessionInteractiveToolSettings(opts SessionRunOptions) (config.InteractiveToolConfig, error) {
	loadedConfig := opts.LoadedConfig
	if loadedConfig == nil && opts.ConfigDir != "" {
		var err error
		loadedConfig, err = loadInteractiveToolConfig(opts.ConfigDir)
		if err != nil {
			return config.InteractiveToolConfig{}, err
		}
	}
	if loadedConfig == nil {
		return config.DefaultInteractiveToolConfig(), nil
	}
	settings, err := loadedConfig.ResolveInteractiveToolConfig()
	if err != nil {
		return config.InteractiveToolConfig{}, fmt.Errorf("resolve interactive tool policy: %w", err)
	}
	return settings, nil
}

func loadInteractiveToolConfig(configDir string) (*config.Config, error) {
	configPath := filepath.Join(configDir, config.ConfigFileName)
	if _, err := os.Stat(configPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("inspect interactive tool configuration: %w", err)
	}
	storage, err := config.NewDefaultConfigStorage(configDir)
	if err != nil {
		return nil, fmt.Errorf("initialize interactive tool configuration: %w", err)
	}
	loaded, err := storage.Load()
	if err != nil {
		return nil, fmt.Errorf("load interactive tool configuration: %w", err)
	}
	return loaded, nil
}
