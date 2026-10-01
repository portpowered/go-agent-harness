//go:build linux

package mouse

import (
	"context"
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
func (d mouseDriver) runXdotool(ctx context.Context, args ...string) error {
	out, err := d.process.Run(ctx, "xdotool", args...)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("xdotool not found – install with 'apt install xdotool' or 'dnf install xdotool'")
		}
		return fmt.Errorf("xdotool %v: %w (output: %s)", args, err, string(out))
	}
	return nil
}

func (d mouseDriver) move(ctx context.Context, x, y int) error {
	return d.runXdotool(ctx, "mousemove", strconv.Itoa(x), strconv.Itoa(y))
}

func (d mouseDriver) click(ctx context.Context, x, y int, button string) error {
	return d.runXdotool(ctx, "mousemove", strconv.Itoa(x), strconv.Itoa(y), "click", xdotoolButton(button))
}

func (d mouseDriver) doubleClick(ctx context.Context, x, y int, button string) error {
	if err := d.click(ctx, x, y, button); err != nil {
		return err
	}
	if err := d.sleep(ctx, mouseDoubleClickPause); err != nil {
		return err
	}
	return d.click(ctx, x, y, button)
}

func (d mouseDriver) buttonDown(ctx context.Context, x, y int, button string) error {
	if err := d.runXdotool(ctx, "mousemove", strconv.Itoa(x), strconv.Itoa(y)); err != nil {
		return err
	}
	return d.runXdotool(ctx, "mousedown", xdotoolButton(button))
}

func (d mouseDriver) buttonUp(ctx context.Context, x, y int, button string) error {
	if err := d.runXdotool(ctx, "mousemove", strconv.Itoa(x), strconv.Itoa(y)); err != nil {
		return err
	}
	return d.runXdotool(ctx, "mouseup", xdotoolButton(button))
}

func (d mouseDriver) drag(ctx context.Context, fromX, fromY, toX, toY int, button string) error {
	if err := d.runXdotool(ctx, "mousemove", strconv.Itoa(fromX), strconv.Itoa(fromY)); err != nil {
		return fmt.Errorf("drag start: %w", err)
	}
	if err := d.runXdotool(ctx, "mousedown", xdotoolButton(button)); err != nil {
		return fmt.Errorf("drag mousedown: %w", err)
	}
	if err := d.sleep(ctx, mouseDragPause); err != nil {
		return err
	}

	const steps = 20
	for i := 1; i <= steps; i++ {
		ix := fromX + (toX-fromX)*i/steps
		iy := fromY + (toY-fromY)*i/steps
		if err := d.runXdotool(ctx, "mousemove", strconv.Itoa(ix), strconv.Itoa(iy)); err != nil {
			releaseErr := d.runXdotool(ctx, "mouseup", xdotoolButton(button))
			stepErr := fmt.Errorf("drag step %d: %w", i, err)
			if releaseErr != nil {
				return errors.Join(stepErr, fmt.Errorf("drag release: %w", releaseErr))
			}
			return stepErr
		}
		if err := d.sleep(ctx, mouseDragStepPause); err != nil {
			return err
		}
	}

	return d.runXdotool(ctx, "mouseup", xdotoolButton(button))
}
