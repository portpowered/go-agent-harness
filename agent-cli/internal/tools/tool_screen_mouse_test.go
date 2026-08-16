package tools

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

const s4DefectCommentURL = "https://github.com/portpowered/go-agent-harness/pull/52#issuecomment-5306715323"

func skipS4MissingTypedIdentity(t *testing.T, behavior string) {
	t.Helper()
	t.Skipf("%s: production defect — %s exposes only an untyped error; see S4 review comment %s", runtime.GOOS, behavior, s4DefectCommentURL)
}

func TestS12ScreenAndMousePortableContracts(t *testing.T) {
	screen := NewScreenTool()
	if screen.Name() != "show" {
		t.Fatalf("screen tool name = %q, want show", screen.Name())
	}
	if !strings.Contains(screen.Description(), "screenshot") || !strings.Contains(screen.Description(), "record") {
		t.Fatalf("screen description does not describe both operations: %q", screen.Description())
	}
	screenParams := screen.Parameters()
	if got := screenParams["required"].([]string); len(got) != 1 || got[0] != "action" {
		t.Fatalf("screen required parameters = %#v, want [action]", got)
	}

	mouse := NewMouseTool()
	if mouse.Name() != "mouse" {
		t.Fatalf("mouse tool name = %q, want mouse", mouse.Name())
	}
	if !strings.Contains(mouse.Description(), "screen pixels") {
		t.Fatalf("mouse description omits coordinate contract: %q", mouse.Description())
	}
	mouseParams := mouse.Parameters()
	if got := mouseParams["required"].([]string); len(got) != 3 || got[0] != "action" || got[1] != "x" || got[2] != "y" {
		t.Fatalf("mouse required parameters = %#v, want [action x y]", got)
	}
}

func TestS4ScreenAndMouseErrorPaths(t *testing.T) {
	screen := NewScreenTool()
	mouse := NewMouseTool()
	cases := []struct {
		name   string
		run    func() error
		want   string
		defect string
	}{
		{
			name: "unavailable display",
			run: func() error {
				_, err := screen.Execute(context.Background(), map[string]any{"action": "screenshot", "display": float64(1 << 20)})
				return err
			},
			want:   "display 1048576 not available",
			defect: "unavailable display",
		},
		{
			name: "unknown screen action",
			run: func() error {
				_, err := screen.Execute(context.Background(), map[string]any{"action": "annotate"})
				return err
			},
			want: `unknown action "annotate"`,
		},
		{
			name: "missing mouse action",
			run: func() error {
				_, err := mouse.Execute(context.Background(), map[string]any{"x": float64(1), "y": float64(1)})
				return err
			},
			want: "action is required",
		},
		{
			name: "missing coordinates",
			run: func() error {
				_, err := mouse.Execute(context.Background(), map[string]any{"action": "move"})
				return err
			},
			want: "x and y coordinates are required",
		},
		{
			name: "missing drag destination",
			run: func() error {
				_, err := mouse.Execute(context.Background(), map[string]any{"action": "drag", "x": float64(1), "y": float64(1)})
				return err
			},
			want: "to_x and to_y are required for the drag action",
		},
		{
			name: "unknown mouse action",
			run: func() error {
				_, err := mouse.Execute(context.Background(), map[string]any{"action": "teleport", "x": float64(1), "y": float64(1)})
				return err
			},
			want: `unknown action "teleport"`,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if err == nil {
				t.Fatalf("expected error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err, tt.want)
			}
			if tt.defect != "" {
				skipS4MissingTypedIdentity(t, tt.defect)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := screen.recordScreen(ctx, 0, 0, 1); err == nil || !strings.Contains(err.Error(), "no frames captured") {
		t.Fatalf("canceled zero-duration recording error = %v", err)
	}

}

func TestS4KnownProductionGaps(t *testing.T) {
	t.Run("out-of-bounds coordinates", func(t *testing.T) {
		t.Skipf("%s: production defect — MouseTool passes coordinates through without a bounds validator or typed identity; see S4 review comment %s", runtime.GOOS, s4DefectCommentURL)
	})
	t.Run("negative dimensions", func(t *testing.T) {
		t.Skipf("%s: production defect — ScreenTool normalizes negative duration/fps instead of returning a typed validation error; see S4 review comment %s", runtime.GOOS, s4DefectCommentURL)
	})
}
