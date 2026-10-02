//go:build linux && e2e

package mouse

import (
	"os"
	"os/exec"
	"runtime"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	display "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/display"
)

func TestLinuxRealScreenAndMouse(t *testing.T) {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Fatalf("%s: required live capability unavailable: display server (DISPLAY/WAYLAND_DISPLAY)", runtime.GOOS)
	}
	for _, command := range []string{"scrot", "xdotool"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Fatalf("%s: required live capability unavailable: %s executable", runtime.GOOS, command)
		}
	}
	tool := display.NewScreenTool()
	msgs, err := tool.Execute(t.Context(), map[string]any{"action": "screenshot"})
	if err != nil {
		t.Fatalf("%s: required live capability unavailable: live screen capture (%v)", runtime.GOOS, err)
	}
	part, ok := msgs[0].ContentParts[1].(messages.ImagePart)
	if !ok {
		t.Fatalf("live screenshot content part = %T, want messages.ImagePart", msgs[0].ContentParts[1])
	}
	if part.MediaType != "image/jpeg" || len(part.Bytes) == 0 {
		t.Fatalf("live screenshot did not produce non-empty JPEG: %#v", part)
	}
}
