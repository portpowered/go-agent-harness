package cli

import (
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
)

var sessionTerminalToolIDs = []string{"exec", "read_file", "read_image", "write_file", "edit_file", "append_file", "list_dir"}
var sessionExperimentalToolIDs = []string{"load_skill", "sleep", "web_fetch", "web_search"}

func validateSessionModelOptions(voice, reasoningEffort string) error {
	if err := services.ValidateOpenAIRealtimeVoice(voice); err != nil {
		return err
	}
	return services.ValidateOpenAIRealtimeReasoningEffort(reasoningEffort)
}

// applySessionToolVisibility makes the CLI flags authoritative at the tool
// composition edge. The loaded config is request-scoped, but copy the top
// level and list so callers that reuse a config snapshot cannot observe the
// overrides.
func applySessionToolVisibility(cfg *config.Config, computerUse, experimentalTools, noTerminalTools bool) *config.Config {
	if cfg == nil {
		cfg = &config.Config{}
	}
	copyCfg := *cfg
	copyCfg.Tools.List = append([]config.ToolEntry(nil), cfg.Tools.List...)
	setEnabled := func(id string, enabled bool) {
		for i := range copyCfg.Tools.List {
			if copyCfg.Tools.List[i].ID == id {
				copyCfg.Tools.List[i].Enabled = enabled
				return
			}
		}
		copyCfg.Tools.List = append(copyCfg.Tools.List, config.ToolEntry{ID: id, Enabled: enabled})
	}
	if !computerUse {
		setEnabled("show", false)
		setEnabled("mouse", false)
	}
	if !experimentalTools {
		for _, id := range sessionExperimentalToolIDs {
			setEnabled(id, false)
		}
	}
	if noTerminalTools {
		for _, id := range sessionTerminalToolIDs {
			setEnabled(id, false)
		}
	}
	return &copyCfg
}
