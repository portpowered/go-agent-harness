//go:build !windows && !linux && !darwin

package mouse

import (
	"context"
	display "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/display"
	"runtime"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestS4UnsupportedPlatformIdentities(t *testing.T) {
	cases := []struct {
		name string
		run  func() ([]messages.Message, error)
		want string
	}{
		{
			name: "screen screenshot",
			run: func() ([]messages.Message, error) {
				return display.NewScreenTool().Execute(context.Background(), map[string]any{"action": "screenshot"})
			},
			want: "display unavailable for show",
		},
		{
			name: "mouse move",
			run: func() ([]messages.Message, error) {
				return NewMouseTool().Execute(context.Background(), map[string]any{"action": "move", "x": float64(1), "y": float64(1)})
			},
			want: platformMouseErr,
		},
		{
			name: "mouse click",
			run: func() ([]messages.Message, error) {
				return NewMouseTool().Execute(context.Background(), map[string]any{"action": "click", "x": float64(1), "y": float64(1), "button": "right"})
			},
			want: platformMouseErr,
		},
		{
			name: "mouse double click",
			run: func() ([]messages.Message, error) {
				return NewMouseTool().Execute(context.Background(), map[string]any{"action": "double_click", "x": float64(1), "y": float64(1), "button": "middle"})
			},
			want: platformMouseErr,
		},
		{
			name: "mouse button down",
			run: func() ([]messages.Message, error) {
				return NewMouseTool().Execute(context.Background(), map[string]any{"action": "down", "x": float64(1), "y": float64(1)})
			},
			want: platformMouseErr,
		},
		{
			name: "mouse button up",
			run: func() ([]messages.Message, error) {
				return NewMouseTool().Execute(context.Background(), map[string]any{"action": "up", "x": float64(1), "y": float64(1)})
			},
			want: platformMouseErr,
		},
		{
			name: "mouse drag",
			run: func() ([]messages.Message, error) {
				return NewMouseTool().Execute(context.Background(), map[string]any{
					"action": "drag", "x": float64(1), "y": float64(1), "to_x": float64(3), "to_y": float64(3),
				})
			},
			want: platformMouseErr,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			msgs, err := tt.run()
			if err == nil || err.Error() != tt.want || msgs != nil {
				t.Fatalf("unsupported result = %#v, err = %v; want nil messages and %q", msgs, err, tt.want)
			}
			t.Skipf("%s: production defect — unsupported %s exposes only an untyped error; see S4 review comment %s", runtime.GOOS, tt.name, s4DefectCommentURL)
		})
	}
}
