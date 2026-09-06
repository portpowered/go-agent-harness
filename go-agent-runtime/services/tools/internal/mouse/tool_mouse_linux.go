//go:build linux

package mouse

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

const mouseDragPause = 30 * time.Millisecond

// xdotoolButton maps a button name to the xdotool button number.
func xdotoolButton(button string) string {
	switch button {
	case "right":
		return "3"
	case "middle":
		return "2"
	default: // "left"
		return "1"
	}
}

// runXdotool executes xdotool with the given arguments.
func runXdotool(args ...string) error {
	out, err := exec.Command("xdotool", args...).CombinedOutput()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("xdotool not found – install with 'apt install xdotool' or 'dnf install xdotool'")
		}
		return fmt.Errorf("xdotool %v: %w (output: %s)", args, err, string(out))
	}
	return nil
}

func mouseMove(x, y int) error {
	return runXdotool("mousemove", strconv.Itoa(x), strconv.Itoa(y))
}

func mouseClick(x, y int, button string) error {
	return runXdotool("mousemove", strconv.Itoa(x), strconv.Itoa(y), "click", xdotoolButton(button))
}

func mouseDoubleClick(x, y int, button string) error {
	if err := mouseClick(x, y, button); err != nil {
		return err
	}
	time.Sleep(50 * time.Millisecond)
	return mouseClick(x, y, button)
}

func mouseButtonDown(x, y int, button string) error {
	if err := runXdotool("mousemove", strconv.Itoa(x), strconv.Itoa(y)); err != nil {
		return err
	}
	return runXdotool("mousedown", xdotoolButton(button))
}

func mouseButtonUp(x, y int, button string) error {
	if err := runXdotool("mousemove", strconv.Itoa(x), strconv.Itoa(y)); err != nil {
		return err
	}
	return runXdotool("mouseup", xdotoolButton(button))
}

func mouseDrag(fromX, fromY, toX, toY int, button string) error {
	if err := runXdotool("mousemove", strconv.Itoa(fromX), strconv.Itoa(fromY)); err != nil {
		return fmt.Errorf("drag start: %w", err)
	}
	if err := runXdotool("mousedown", xdotoolButton(button)); err != nil {
		return fmt.Errorf("drag mousedown: %w", err)
	}
	time.Sleep(mouseDragPause)

	const steps = 20
	for i := 1; i <= steps; i++ {
		ix := fromX + (toX-fromX)*i/steps
		iy := fromY + (toY-fromY)*i/steps
		if err := runXdotool("mousemove", strconv.Itoa(ix), strconv.Itoa(iy)); err != nil {
			_ = runXdotool("mouseup", xdotoolButton(button)) // best-effort release
			return fmt.Errorf("drag step %d: %w", i, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	return runXdotool("mouseup", xdotoolButton(button))
}
