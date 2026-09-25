//go:build darwin

package mouse

import (
	"errors"
	"fmt"
	"os/exec"
	"time"
)

const (
	mouseDragPause     = 30 * time.Millisecond
	mouseDragStepPause = 10 * time.Millisecond
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
func (d mouseDriver) runCliclick(args ...string) error {
	out, err := d.process.Run("cliclick", args...)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("cliclick not found – install with 'brew install cliclick'")
		}
		return fmt.Errorf("cliclick %v: %w (output: %s)", args, err, string(out))
	}
	return nil
}

func (d mouseDriver) move(x, y int) error {
	return d.runCliclick(fmt.Sprintf("m:%d,%d", x, y))
}

func (d mouseDriver) click(x, y int, button string) error {
	return d.runCliclick(fmt.Sprintf("%s:%d,%d", cliclickAction(button, "c"), x, y))
}

func (d mouseDriver) doubleClick(x, y int, button string) error {
	return d.runCliclick(fmt.Sprintf("%s:%d,%d", cliclickAction(button, "C"), x, y))
}

// buttonDown presses and holds a mouse button.  cliclick's press action
// (p:) only supports the left button; right/middle will return an error.
func (d mouseDriver) buttonDown(x, y int, button string) error {
	if button != mouseButtonLeft && button != "" {
		return fmt.Errorf("cliclick only supports mouse-down for the left button; got %q", button)
	}
	return d.runCliclick(fmt.Sprintf("p:%d,%d", x, y))
}

// buttonUp releases a held mouse button.  Same left-only limitation as
// buttonDown.
func (d mouseDriver) buttonUp(x, y int, button string) error {
	if button != mouseButtonLeft && button != "" {
		return fmt.Errorf("cliclick only supports mouse-up for the left button; got %q", button)
	}
	return d.runCliclick(fmt.Sprintf("r:%d,%d", x, y))
}

// drag presses at (fromX, fromY), moves incrementally to (toX, toY), then
// releases.  Only the left button is supported on macOS via cliclick.
func (d mouseDriver) drag(fromX, fromY, toX, toY int, button string) error {
	if button != mouseButtonLeft && button != "" {
		return fmt.Errorf("cliclick only supports drag for the left button; got %q", button)
	}

	if err := d.runCliclick(fmt.Sprintf("p:%d,%d", fromX, fromY)); err != nil {
		return fmt.Errorf("drag start: %w", err)
	}
	d.sleep(mouseDragPause)

	const steps = 20
	for i := 1; i <= steps; i++ {
		ix := fromX + (toX-fromX)*i/steps
		iy := fromY + (toY-fromY)*i/steps
		if err := d.runCliclick(fmt.Sprintf("m:%d,%d", ix, iy)); err != nil {
			releaseErr := d.runCliclick(fmt.Sprintf("r:%d,%d", ix, iy)) // best-effort release
			return fmt.Errorf("drag step %d: %w", i, errors.Join(err, releaseErr))
		}
		d.sleep(mouseDragStepPause)
	}

	return d.runCliclick(fmt.Sprintf("r:%d,%d", toX, toY))
}
