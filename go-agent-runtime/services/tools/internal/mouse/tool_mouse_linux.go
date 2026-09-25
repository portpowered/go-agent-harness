//go:build linux

package mouse

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

const (
	mouseDragPause        = 30 * time.Millisecond
	mouseDragStepPause    = 10 * time.Millisecond
	mouseDoubleClickPause = 50 * time.Millisecond
)

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
func (d mouseDriver) runXdotool(args ...string) error {
	out, err := d.process.Run("xdotool", args...)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("xdotool not found – install with 'apt install xdotool' or 'dnf install xdotool'")
		}
		return fmt.Errorf("xdotool %v: %w (output: %s)", args, err, string(out))
	}
	return nil
}

func (d mouseDriver) move(x, y int) error {
	return d.runXdotool("mousemove", strconv.Itoa(x), strconv.Itoa(y))
}

func (d mouseDriver) click(x, y int, button string) error {
	return d.runXdotool("mousemove", strconv.Itoa(x), strconv.Itoa(y), "click", xdotoolButton(button))
}

func (d mouseDriver) doubleClick(x, y int, button string) error {
	if err := d.click(x, y, button); err != nil {
		return err
	}
	d.sleep(mouseDoubleClickPause)
	return d.click(x, y, button)
}

func (d mouseDriver) buttonDown(x, y int, button string) error {
	if err := d.runXdotool("mousemove", strconv.Itoa(x), strconv.Itoa(y)); err != nil {
		return err
	}
	return d.runXdotool("mousedown", xdotoolButton(button))
}

func (d mouseDriver) buttonUp(x, y int, button string) error {
	if err := d.runXdotool("mousemove", strconv.Itoa(x), strconv.Itoa(y)); err != nil {
		return err
	}
	return d.runXdotool("mouseup", xdotoolButton(button))
}

func (d mouseDriver) drag(fromX, fromY, toX, toY int, button string) error {
	if err := d.runXdotool("mousemove", strconv.Itoa(fromX), strconv.Itoa(fromY)); err != nil {
		return fmt.Errorf("drag start: %w", err)
	}
	if err := d.runXdotool("mousedown", xdotoolButton(button)); err != nil {
		return fmt.Errorf("drag mousedown: %w", err)
	}
	d.sleep(mouseDragPause)

	const steps = 20
	for i := 1; i <= steps; i++ {
		ix := fromX + (toX-fromX)*i/steps
		iy := fromY + (toY-fromY)*i/steps
		if err := d.runXdotool("mousemove", strconv.Itoa(ix), strconv.Itoa(iy)); err != nil {
			releaseErr := d.runXdotool("mouseup", xdotoolButton(button))
			stepErr := fmt.Errorf("drag step %d: %w", i, err)
			if releaseErr != nil {
				return errors.Join(stepErr, fmt.Errorf("drag release: %w", releaseErr))
			}
			return stepErr
		}
		d.sleep(mouseDragStepPause)
	}

	return d.runXdotool("mouseup", xdotoolButton(button))
}
