package registry

import (
	"bytes"
	"context"
	"image"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	core "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/composition"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/display"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/filesystem"
)

func TestNewToolRegistryFromConfig_NilConfigAllEnabled(t *testing.T) {
	r := NewToolRegistry()
	names := r.List()
	if len(names) == 0 {
		t.Fatal("expected at least one tool when config is nil")
	}
	_, ok := r.Get("exec")
	if !ok {
		t.Error("exec should be present when config is nil")
	}
	if _, ok := r.Get(filesystem.ReadImageToolID); !ok {
		t.Errorf("%s should be present when config is nil", filesystem.ReadImageToolID)
	}
}

func TestNewToolRegistryFromConfig_DisabledToolExcluded(t *testing.T) {
	r := newToolRegistry(RegistryOptions{
		Selections: []public.ToolSelection{{ID: "exec", Enabled: false}},
	}, display.DisplayCapability{}, nil, false, nil, false, nil, nil)
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
	r := newToolRegistry(RegistryOptions{
		Selections: []public.ToolSelection{{ID: filesystem.ReadImageToolID, Enabled: false}},
	}, display.DisplayCapability{}, nil, false, nil, false, nil, nil)
	if _, ok := r.Get(filesystem.ReadImageToolID); ok {
		t.Fatalf("%s should be excluded when disabled in config", filesystem.ReadImageToolID)
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

func TestRegistryUsesInjectedDiagnosticWriter(t *testing.T) {
	var diagnostics bytes.Buffer
	registry := &ToolRegistry{
		tools:            make(map[string]core.Tool),
		diagnosticWriter: &diagnostics,
	}
	if err := registry.Register(newContractTool("diagnostic")); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Execute(context.Background(), "diagnostic", nil); err != nil {
		t.Fatal(err)
	}
	output := diagnostics.String()
	if !strings.Contains(output, `tool "diagnostic" execution started`) || !strings.Contains(output, `tool "diagnostic" execution completed`) {
		t.Fatalf("diagnostics = %q", output)
	}
}

func TestShowRemainsAdvertisedWhenDisplayIsUnavailable(t *testing.T) {
	registry := NewToolRegistry()
	if _, ok := registry.Get(display.ScreenToolID); !ok {
		t.Fatal("show is not present in the static tool surface")
	}
}

type registryDisplaySurface struct {
	capability display.DisplayCapability
	permission display.DisplayPermission
	rechecks   int
}

func (s *registryDisplaySurface) Probe(context.Context) (display.DisplayCapability, error) {
	return s.capability, nil
}

func (s *registryDisplaySurface) DisplayCount(context.Context) (int, error) {
	return s.capability.DisplayCount, nil
}

func (*registryDisplaySurface) Bounds(context.Context, int) (image.Rectangle, error) {
	return image.Rectangle{}, nil
}

func (*registryDisplaySurface) Capture(context.Context, image.Rectangle) (*image.RGBA, error) {
	return nil, nil
}

func (*registryDisplaySurface) ScreenRecordingPermissionRecheckSupported() bool { return true }

func (s *registryDisplaySurface) RecheckScreenRecordingPermission(context.Context) (display.DisplayPermission, error) {
	s.rechecks++
	return s.permission, nil
}

func TestScreenPermissionRecheckPropagatesThroughRegistryAndComposition(t *testing.T) {
	surface := &registryDisplaySurface{
		capability: display.UsableDisplayCapability(1),
		permission: display.DisplayPermission{State: display.DisplayPermissionDenied, Reason: "recheck denied"},
	}
	tool := display.NewScreenToolWithDisplaySurface(surface)
	registry := NewEmptyToolRegistry()
	if err := registry.Register(tool); err != nil {
		t.Fatalf("register screen tool: %v", err)
	}
	executor := NewRegistryExecutor(registry)
	rechecker, ok := any(executor).(display.ScreenRecordingPermissionRechecker)
	if !ok || !rechecker.ScreenRecordingPermissionRecheckSupported() {
		t.Fatalf("registry executor does not expose screen permission recheck: %T", executor)
	}
	permission, err := rechecker.RecheckScreenRecordingPermission(context.Background())
	if err != nil || permission.State != display.DisplayPermissionDenied {
		t.Fatalf("registry recheck = %#v, %v, want denied permission", permission, err)
	}

	composed, err := composition.ComposeToolSurface(
		executor,
		[]messages.ToolDefinition{{Name: display.ScreenToolID}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("compose screen surface: %v", err)
	}
	composedRechecker, ok := composed.Executor.(display.ScreenRecordingPermissionRechecker)
	if !ok || !composedRechecker.ScreenRecordingPermissionRecheckSupported() {
		t.Fatalf("composed executor does not expose screen permission recheck: %T", composed.Executor)
	}
	permission, err = composedRechecker.RecheckScreenRecordingPermission(context.Background())
	if err != nil || permission.State != display.DisplayPermissionDenied {
		t.Fatalf("composed recheck = %#v, %v, want denied permission", permission, err)
	}
	if surface.rechecks != 2 {
		t.Fatalf("permission checker calls = %d, want two calls through the same surface contract", surface.rechecks)
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
