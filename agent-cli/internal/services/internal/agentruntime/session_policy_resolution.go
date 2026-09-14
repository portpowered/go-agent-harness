package agentruntime

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

func resolveSessionInteractiveToolPolicy(opts SessionRunOptions, definitions []messages.ToolDefinition) (runtimeTools.InteractiveToolPolicy, error) {
	if opts.InteractiveToolPolicy != nil {
		policy := opts.InteractiveToolPolicy.Clone()
		if err := policy.Validate(); err != nil {
			return nil, fmt.Errorf("resolve interactive tool policy: %w", err)
		}
		return policy, nil
	}
	settings, err := sessionInteractiveToolSettings(opts)
	if err != nil {
		return nil, err
	}
	return runtimeToolsWire.NewInteractiveToolPolicy().Resolve(runtimeTools.InteractiveToolPolicyRequest{
		Settings: runtimeTools.InteractiveToolPolicySettings{
			FastReadTimeout:          settings.FastReadTimeout,
			LongRunningTimeout:       settings.LongRunningTimeout,
			AcknowledgementThreshold: settings.AcknowledgementThreshold,
		},
		Definitions:     definitions,
		BaseDefinitions: opts.ToolDefinitionBase,
		ExplicitLongRunningNames: []string{
			"webmcp_select_tab", "webmcp_invoke", "webmcp_list_tools", "webmcp_list_tabs",
			"webmcp_get_context", "webmcp_cancel", "webmcp_list_cast_devices", "webmcp_cast_tab",
			"webmcp_stop_casting",
		},
		DynamicLongRunning: opts.BrowserToolsEnabled,
	})
}

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
