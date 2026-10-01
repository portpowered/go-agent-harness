package mouse

import (
	"context"
	"strings"
	"testing"

	display "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/display"
)

func TestS4ScreenAndMouseErrorPaths(t *testing.T) {
	// Keep the display-index error path independent from the host desktop. The
	// session admission path intentionally fails closed when the host has no
	// usable display, so a host-backed tool cannot reach this validation branch
	// deterministically in CI.
	screen := display.NewScreenToolWithDisplaySurface(&portableDisplaySurface{
		capability: display.UsableDisplayCapability(1),
	})
	mouse := NewMouseTool()
	cases := []struct {
		name    string
		run     func() error
		wantAny []string
		want    string
	}{
		{
			name: "unavailable display",
			run: func() error {
				_, err := screen.Execute(context.Background(), map[string]any{"action": "screenshot", "display": float64(1 << 20)})
				return err
			},
			// Displayless CI environments fail at discovery before the index
			// check; both shapes are honest capability denials.
			wantAny: []string{"display 1048576 not available", "display unavailable for show"},
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
			matched := tt.want != "" && strings.Contains(err.Error(), tt.want)
			for _, alt := range tt.wantAny {
				if strings.Contains(err.Error(), alt) {
					matched = true
				}
			}
			if !matched {
				t.Fatalf("error = %q, want substring %q or one of %q", err, tt.want, tt.wantAny)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := screen.Execute(ctx, map[string]any{"action": "record", "duration": float64(1), "fps": float64(1)}); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("canceled recording error = %v", err)
	}

}
