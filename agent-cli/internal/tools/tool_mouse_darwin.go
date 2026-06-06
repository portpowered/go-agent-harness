//go:build darwin

package tools

import (
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// cliclickAction builds the cliclick action string for a given button and kind.
// kind: "c" = single click, "C" = double-click, "p" = press (down), "r" = release (up).
// Prefix: "" = left, "r" = right, "m" = middle.
func cliclickAction(button, kind string) string {
	switch button {
	case "right":
		return "r" + kind
	case "middle":
		return "m" + kind
	default: // "left"
		return kind
	}
}

// runCliclick executes cliclick with the given action arguments.
func runCliclick(args ...string) error {
	out, err := exec.Command("cliclick", args...).CombinedOutput()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("cliclick not found – install with 'brew install cliclick'")
		}
		return fmt.Errorf("cliclick %v: %w (output: %s)", args, err, string(out))
	}
	return nil
}

func mouseMove(x, y int) error {
	return runCliclick(fmt.Sprintf("m:%d,%d", x, y))
}

func mouseClick(x, y int, button string) error {
	return runCliclick(fmt.Sprintf("%s:%d,%d", cliclickAction(button, "c"), x, y))
}

func mouseDoubleClick(x, y int, button string) error {
	return runCliclick(fmt.Sprintf("%s:%d,%d", cliclickAction(button, "C"), x, y))
}

// mouseButtonDown presses and holds a mouse button.  cliclick's press action
// (p:) only supports the left button; right/middle will return an error.
func mouseButtonDown(x, y int, button string) error {
	if button != "left" && button != "" {
		return fmt.Errorf("cliclick only supports mouse-down for the left button; got %q", button)
	}
	return runCliclick(fmt.Sprintf("p:%d,%d", x, y))
}

// mouseButtonUp releases a held mouse button.  Same left-only limitation as
// mouseButtonDown.
func mouseButtonUp(x, y int, button string) error {
	if button != "left" && button != "" {
		return fmt.Errorf("cliclick only supports mouse-up for the left button; got %q", button)
	}
	return runCliclick(fmt.Sprintf("r:%d,%d", x, y))
}

// mouseDrag presses at (fromX, fromY), moves incrementally to (toX, toY), then
// releases.  Only the left button is supported on macOS via cliclick.
func mouseDrag(fromX, fromY, toX, toY int, button string) error {
	if button != "left" && button != "" {
		return fmt.Errorf("cliclick only supports drag for the left button; got %q", button)
	}

	if err := runCliclick(fmt.Sprintf("p:%d,%d", fromX, fromY)); err != nil {
		return fmt.Errorf("drag start: %w", err)
	}
	time.Sleep(30 * time.Millisecond)

	const steps = 20
	for i := 1; i <= steps; i++ {
		ix := fromX + (toX-fromX)*i/steps
		iy := fromY + (toY-fromY)*i/steps
		if err := runCliclick(fmt.Sprintf("m:%d,%d", ix, iy)); err != nil {
			_ = runCliclick(fmt.Sprintf("r:%d,%d", ix, iy)) // best-effort release
			return fmt.Errorf("drag step %d: %w", i, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	return runCliclick(fmt.Sprintf("r:%d,%d", toX, toY))
}
