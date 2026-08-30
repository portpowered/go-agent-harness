package tools

import (
	"context"
	"errors"
	"image"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
)

type scriptedDisplaySurface struct {
	capability DisplayCapability
	probeErr   error
	count      int
	bounds     image.Rectangle
	image      *image.RGBA
	boundErr   error
	captureErr error
	probes     int
	captures   int
}

func (s *scriptedDisplaySurface) Probe(context.Context) (DisplayCapability, error) {
	s.probes++
	return s.capability, s.probeErr
}

func (s *scriptedDisplaySurface) DisplayCount(context.Context) (int, error) {
	return s.count, nil
}

func (s *scriptedDisplaySurface) Bounds(context.Context, int) (image.Rectangle, error) {
	return s.bounds, s.boundErr
}

func (s *scriptedDisplaySurface) Capture(context.Context, image.Rectangle) (*image.RGBA, error) {
	s.captures++
	return s.image, s.captureErr
}

func TestScreenToolWithDisplaySurfaceCapturesAndRechecksAdmission(t *testing.T) {
	surface := &scriptedDisplaySurface{
		capability: UsableDisplayCapability(1),
		count:      1,
		bounds:     image.Rect(0, 0, 2, 2),
		image:      image.NewRGBA(image.Rect(0, 0, 2, 2)),
	}
	tool := NewScreenToolWithDisplaySurface(surface)

	msgs, err := tool.Execute(context.Background(), map[string]any{"action": "screenshot"})
	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0].TextContent(), "Screenshot: display 0 (2x2 px)") {
		t.Fatalf("screenshot messages = %#v", msgs)
	}
	if surface.probes != 1 || surface.captures != 1 {
		t.Fatalf("surface calls = probes:%d captures:%d, want one each", surface.probes, surface.captures)
	}

	surface.capability = UnavailableDisplayCapability("the desktop disappeared")
	msgs, err = tool.Execute(context.Background(), map[string]any{"action": "screenshot"})
	var unavailable *DisplayUnavailableError
	if err == nil || !errors.As(err, &unavailable) || !errors.Is(err, ErrDisplayUnavailable) || msgs != nil {
		t.Fatalf("lost display result = %#v, err = %v", msgs, err)
	}
	if !strings.Contains(err.Error(), "the desktop disappeared") {
		t.Fatalf("lost display error = %q, want actionable reason", err)
	}
	if surface.captures != 1 {
		t.Fatalf("capture ran after failed admission: %d calls", surface.captures)
	}
}

func TestScreenToolWithDisplaySurfaceHonorsCanceledContext(t *testing.T) {
	surface := &scriptedDisplaySurface{capability: UsableDisplayCapability(1), count: 1}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	msgs, err := NewScreenToolWithDisplaySurface(surface).Execute(ctx, map[string]any{"action": "screenshot"})
	if err == nil || !errors.Is(err, ErrDisplayUnavailable) || !errors.Is(err, context.Canceled) || msgs != nil {
		t.Fatalf("canceled screenshot result = %#v, err = %v", msgs, err)
	}
	if surface.captures != 0 {
		t.Fatalf("canceled screenshot captured %d frames", surface.captures)
	}
}

func TestSessionRegistryDisplayAdmissionKeepsDefinitionsAndRoutesAligned(t *testing.T) {
	cfg := &config.Config{Tools: config.ToolsConfig{List: []config.ToolEntry{
		{ID: "show", Enabled: true},
		{ID: "mouse", Enabled: true},
		{ID: "read_file", Enabled: true},
	}}}

	headless := NewToolRegistryFromConfigWithDisplayCapability(cfg, UnavailableDisplayCapability("headless session"), nil)
	if _, ok := headless.Get("show"); ok {
		t.Fatal("headless session retained show route")
	}
	if _, ok := headless.Get("mouse"); ok {
		t.Fatal("headless session retained mouse route")
	}
	if _, ok := headless.Get("read_file"); !ok {
		t.Fatal("headless session dropped unrelated read_file route")
	}

	usable := NewToolRegistryFromConfigWithDisplayCapability(cfg, UsableDisplayCapability(1), &scriptedDisplaySurface{
		capability: UsableDisplayCapability(1),
		count:      1,
		bounds:     image.Rect(0, 0, 1, 1),
		image:      image.NewRGBA(image.Rect(0, 0, 1, 1)),
	})
	if _, ok := usable.Get("show"); !ok {
		t.Fatal("usable session omitted show route")
	}
	if _, ok := usable.Get("mouse"); !ok {
		t.Fatal("usable session omitted mouse route")
	}
}

func TestHostDisplaySurfaceProbeDoesNotCapture(t *testing.T) {
	var calls []string
	process := DisplayProcessAdapter{
		RunFunc: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			calls = append(calls, name)
			return nil, errors.New("platform commands are not part of this portable test")
		},
		LookPathFunc: func(string) (string, error) {
			calls = append(calls, "lookpath")
			return "capture", nil
		},
	}
	_, _ = NewHostDisplaySurface(process).Probe(context.Background())
	for _, call := range calls {
		if call == "screencapture" || call == "scrot" {
			t.Fatalf("admission probe captured screen through %q", call)
		}
	}
}
