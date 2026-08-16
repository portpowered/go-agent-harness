//go:build !windows && !linux && !darwin

package tools

import (
	"context"
	"strings"
	"testing"
)

func TestS4UnsupportedPlatformIdentities(t *testing.T) {
	msgs, err := NewScreenTool().Execute(context.Background(), map[string]any{"action": "screenshot"})
	if err == nil || !strings.Contains(err.Error(), "screen capture is not yet supported on this platform") || msgs != nil {
		t.Fatalf("unsupported screen result = %#v, err = %v", msgs, err)
	}
	msgs, err = NewMouseTool().Execute(context.Background(), map[string]any{"action": "move", "x": float64(1), "y": float64(1)})
	if err == nil || err.Error() != platformMouseErr || msgs != nil {
		t.Fatalf("unsupported mouse result = %#v, err = %v", msgs, err)
	}
}
