package tools

import (
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
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
	if _, ok := r.Get(ReadImageToolID); !ok {
		t.Errorf("%s should be present when config is nil", ReadImageToolID)
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

func TestNewToolRegistryFromConfig_DisabledReadImageExcluded(t *testing.T) {
	cfg := &config.Config{
		Tools: config.ToolsConfig{
			List: []config.ToolEntry{{ID: ReadImageToolID, Enabled: false}},
		},
	}
	r := NewToolRegistryFromConfig(cfg)
	if _, ok := r.Get(ReadImageToolID); ok {
		t.Fatalf("%s should be excluded when disabled in config", ReadImageToolID)
	}
	if _, ok := r.Get("read_file"); !ok {
		t.Fatal("read_file should remain enabled when read_image is disabled")
	}
}
