package tools

import (
	"testing"

	"github.com/portpowered/agent-cli/internal/config"
)

func TestNewToolRegistryFromConfig_NilConfigAllEnabled(t *testing.T) {
	r := NewToolRegistryFromConfig(nil)
	names := r.List()
	if len(names) == 0 {
		t.Fatal("expected at least one tool when config is nil")
	}
	_, ok := r.Get("exec")
	if !ok {
		t.Error("exec should be present when config is nil")
	}
}

func TestNewToolRegistryFromConfig_DisabledToolExcluded(t *testing.T) {
	cfg := &config.Config{
		Tools: config.ToolsConfig{
			List: []config.ToolEntry{
				{ID: "exec", Enabled: false},
			},
		},
	}
	r := NewToolRegistryFromConfig(cfg)
	_, ok := r.Get("exec")
	if ok {
		t.Error("exec should be excluded when disabled in config")
	}
	_, ok = r.Get("read_file")
	if !ok {
		t.Error("read_file should be present when not in list (default enabled)")
	}
}
