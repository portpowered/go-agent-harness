package tools

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
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

func TestToolRegistryDefinitionsAreCanonicalAndMapDerivedParametersAreOrdered(t *testing.T) {
	registry := NewEmptyToolRegistry()
	definitions := []struct {
		name  string
		props map[string]any
	}{
		{
			name: "zeta",
			props: map[string]any{
				"z": map[string]any{"type": "string"},
				"a": map[string]any{"type": "boolean"},
			},
		},
		{
			name: "alpha",
			props: map[string]any{
				"value": map[string]any{"type": "number"},
			},
		},
	}
	for _, definition := range definitions {
		if err := registry.Register(&canonicalRegistryTestTool{
			name: definition.name,
			params: map[string]any{
				"type":       "object",
				"properties": definition.props,
				"required":   []string{"z", "a"},
			},
		}); err != nil {
			t.Fatalf("register %q: %v", definition.name, err)
		}
	}

	got := registry.ToAgentLoopDefs()
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Fatalf("registry definitions = %#v, want alpha then zeta", got)
	}
	if len(got[1].Parameters) != 2 || got[1].Parameters[0].Name != "a" || got[1].Parameters[1].Name != "z" {
		t.Fatalf("map-derived parameters = %#v, want a then z", got[1].Parameters)
	}
	if got[1].Parameters[0].Required != true || got[1].Parameters[1].Required != true {
		t.Fatalf("required parameter flags changed: %#v", got[1].Parameters)
	}

	if names := registry.List(); len(names) != 2 || names[0] != "alpha" || names[1] != "zeta" {
		t.Fatalf("registry List = %#v, want canonical order", names)
	}
}

type canonicalRegistryTestTool struct {
	name   string
	params map[string]any
}

func (t *canonicalRegistryTestTool) Name() string               { return t.name }
func (t *canonicalRegistryTestTool) Description() string        { return "canonical test tool" }
func (t *canonicalRegistryTestTool) Parameters() map[string]any { return t.params }
func (t *canonicalRegistryTestTool) Execute(context.Context, map[string]any) ([]messages.Message, error) {
	return []messages.Message{messages.NewTextMessage(messages.RoleTool, "ok")}, nil
}
