//go:build darwin && live

package mouse

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	display "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/display"
)

func TestS12DarwinRealCapabilities(t *testing.T) {
	assertDarwinLiveScreen(t)
	assertDarwinLiveMouse(t)
}

func assertDarwinLiveScreen(t *testing.T) {
	t.Helper()
	for _, command := range []string{"screencapture", "cliclick"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Fatalf("%s: required live capability unavailable: %s executable", runtime.GOOS, command)
		}
	}
	msgs, err := display.NewScreenTool().Execute(t.Context(), map[string]any{"action": "screenshot"})
	if err != nil {
		t.Fatalf("%s: required live capability unavailable: live screen capture (%v)", runtime.GOOS, err)
	}
	if len(msgs) == 0 || len(msgs[0].ContentParts) < 2 {
		t.Fatalf("live screenshot result = %#v, want image part", msgs)
	}
	part, ok := msgs[0].ContentParts[1].(messages.ImagePart)
	if !ok {
		t.Fatalf("live screenshot content part = %T, want messages.ImagePart", msgs[0].ContentParts[1])
	}
	if part.MediaType != "image/jpeg" || len(part.Bytes) == 0 {
		t.Fatalf("live screenshot did not produce non-empty JPEG: %#v", part)
	}
}

func assertDarwinLiveMouse(t *testing.T) {
	t.Helper()
	bounds := screenDisplayBounds(0)
	if bounds.Dx() < 16 || bounds.Dy() < 16 {
		t.Fatalf("%s: required live capability unavailable: usable display bounds (%v)", runtime.GOOS, bounds)
	}
	originalX, originalY, err := darwinCursorPosition(t.Context())
	if err != nil {
		t.Fatalf("%s: required live capability unavailable: cursor position query (%v)", runtime.GOOS, err)
	}
	driver := newMouseDriver(MouseToolOptions{})
	t.Cleanup(func() {
		if err := driver.buttonUp(originalX, originalY, "left"); err != nil {
			t.Logf("%s: cursor cleanup release failed: %v", runtime.GOOS, err)
		}
		if err := driver.move(originalX, originalY); err != nil {
			t.Logf("%s: cursor cleanup restore failed: %v", runtime.GOOS, err)
		}
	})

	baseX := bounds.Min.X + bounds.Dx()/2
	baseY := bounds.Min.Y + bounds.Dy()/2
	operations := []struct {
		name         string
		wantX, wantY int
		call         func() error
	}{
		{"move", baseX, baseY, func() error { return driver.move(baseX, baseY) }},
		{"click", baseX + 1, baseY + 1, func() error { return driver.click(baseX+1, baseY+1, "left") }},
		{"double-click", baseX + 2, baseY + 2, func() error { return driver.doubleClick(baseX+2, baseY+2, "left") }},
		{"button-down", baseX + 3, baseY + 3, func() error { return driver.buttonDown(baseX+3, baseY+3, "left") }},
		{"button-up", baseX + 4, baseY + 4, func() error { return driver.buttonUp(baseX+4, baseY+4, "left") }},
		{"drag", baseX + 7, baseY + 7, func() error { return driver.drag(baseX+5, baseY+5, baseX+7, baseY+7, "left") }},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			assertDarwinMouseOperation(t, operation)
		})
	}
}

func assertDarwinMouseOperation(t *testing.T, operation struct {
	name         string
	wantX, wantY int
	call         func() error
}) {
	t.Helper()
	if err := operation.call(); err != nil {
		t.Fatalf("%s: required live capability unavailable: cursor input (%v)", runtime.GOOS, err)
	}
	gotX, gotY, err := darwinCursorPosition(t.Context())
	if err != nil {
		t.Fatalf("%s: required live capability unavailable: cursor position query after %s (%v)", runtime.GOOS, operation.name, err)
	}
	if gotX != operation.wantX || gotY != operation.wantY {
		t.Fatalf("cursor after %s = (%d, %d), want (%d, %d)", operation.name, gotX, gotY, operation.wantX, operation.wantY)
	}
}

func darwinCursorPosition(ctx context.Context) (int, int, error) {
	out, err := exec.CommandContext(ctx, "cliclick", "p").CombinedOutput()
	if err != nil {
		return 0, 0, fmt.Errorf("cliclick p: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	output := strings.TrimSpace(string(out))
	if output == "" {
		return 0, 0, fmt.Errorf("cliclick returned an empty position")
	}
	lines := strings.Split(output, "\n")
	parts := strings.Split(strings.TrimSpace(lines[len(lines)-1]), ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("unexpected cliclick position %q", output)
	}
	x, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("parse cliclick x: %w", err)
	}
	y, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("parse cliclick y: %w", err)
	}
	return x, y, nil
}
