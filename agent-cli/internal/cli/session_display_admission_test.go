package cli

import (
	"context"
	"errors"
	"image"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type sessionDisplaySurfaceFake struct {
	capability tools.DisplayCapability
	probes     int
	captures   int
}

func (s *sessionDisplaySurfaceFake) Probe(context.Context) (tools.DisplayCapability, error) {
	s.probes++
	return s.capability, nil
}

func (*sessionDisplaySurfaceFake) DisplayCount(context.Context) (int, error) { return 1, nil }

func (*sessionDisplaySurfaceFake) Bounds(context.Context, int) (image.Rectangle, error) {
	return image.Rect(0, 0, 1, 1), nil
}

func (s *sessionDisplaySurfaceFake) Capture(context.Context, image.Rectangle) (*image.RGBA, error) {
	s.captures++
	return image.NewRGBA(image.Rect(0, 0, 1, 1)), nil
}

func displayAdmissionConfig() *config.Config {
	browser := config.DefaultBrowserConfig()
	return &config.Config{
		Browser: browser,
		Tools: config.ToolsConfig{List: []config.ToolEntry{
			{ID: "show", Enabled: true},
			{ID: "mouse", Enabled: true},
			{ID: "read_file", Enabled: true},
		}},
	}
}

func TestSessionToolCapabilitiesFactoryOmitsDisplayToolsOnHeadlessProbe(t *testing.T) {
	surface := &sessionDisplaySurfaceFake{capability: tools.UnavailableDisplayCapability("no desktop session")}
	factory := NewSessionToolCapabilitiesFactoryWithDisplaySurface(nil, nil, surface)

	capabilities, err := factory(displayAdmissionConfig())
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if capabilities.DisplayCapability.Usable() || capabilities.DisplayCapability.State != tools.DisplayCapabilityUnavailable {
		t.Fatalf("display capability = %+v, want explicit unavailable state", capabilities.DisplayCapability)
	}
	for _, definition := range capabilities.Definitions {
		if definition.Name == "show" || definition.Name == "mouse" {
			t.Fatalf("headless definition retained display tool %q", definition.Name)
		}
	}
	if _, err := capabilities.Executor.Execute(context.Background(), messages.ToolCall{ID: "headless-show", Name: "show"}); err == nil || !errors.Is(err, tools.ErrToolNotFound) {
		t.Fatalf("headless show route result = %v, want absent route", err)
	}
	if _, ok := findSessionDefinition(capabilities.Definitions, "read_file"); !ok {
		t.Fatal("headless admission dropped unrelated read_file")
	}
	if surface.probes != 1 || surface.captures != 0 {
		t.Fatalf("admission surface calls = probes:%d captures:%d, want probe only", surface.probes, surface.captures)
	}
}

func TestSessionToolCapabilitiesFactoryRetainsShowOnUsableProbe(t *testing.T) {
	surface := &sessionDisplaySurfaceFake{capability: tools.UsableDisplayCapability(1)}
	factory := NewSessionToolCapabilitiesFactoryWithDisplaySurface(nil, nil, surface)

	capabilities, err := factory(displayAdmissionConfig())
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if !capabilities.DisplayCapability.Usable() {
		t.Fatalf("display capability = %+v, want usable", capabilities.DisplayCapability)
	}
	if _, ok := findSessionDefinition(capabilities.Definitions, "show"); !ok {
		t.Fatal("usable admission omitted show")
	}
	if _, ok := findSessionDefinition(capabilities.Definitions, "mouse"); !ok {
		t.Fatal("usable admission omitted mouse")
	}
	if surface.probes != 1 || surface.captures != 0 {
		t.Fatalf("admission surface calls = probes:%d captures:%d, want probe only", surface.probes, surface.captures)
	}
}

func TestSessionDisplayAdmissionProbeIsBoundedAndFailsClosed(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	probe := tools.DisplayCapabilityProbeFunc(func(context.Context) (tools.DisplayCapability, error) {
		close(started)
		<-release
		return tools.UnavailableDisplayCapability("released after timeout"), nil
	})
	t.Cleanup(func() { close(release) })
	factory := NewSessionToolCapabilitiesFactoryWithDisplayProbe(nil, nil, probe)
	startedAt := time.Now()
	capabilities, err := factory(displayAdmissionConfig())
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed >= 2*sessionDisplayCapabilityProbeTimeout {
		t.Fatalf("bounded probe took %s", elapsed)
	}
	select {
	case <-started:
	default:
		t.Fatal("display probe was not started")
	}
	if capabilities.DisplayCapability.Usable() {
		t.Fatalf("timed-out probe admitted display tools: %+v", capabilities.DisplayCapability)
	}
	if _, ok := findSessionDefinition(capabilities.Definitions, "show"); ok {
		t.Fatal("timed-out probe retained show")
	}
}

func findSessionDefinition(definitions []messages.ToolDefinition, name string) (messages.ToolDefinition, bool) {
	for _, definition := range definitions {
		if definition.Name == name {
			return definition, true
		}
	}
	return messages.ToolDefinition{}, false
}

var _ tools.DisplaySurface = (*sessionDisplaySurfaceFake)(nil)
