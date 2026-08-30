//go:build linux

package tools

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"strconv"
	"strings"
)

// screenDisplayCountWithContextAndProcess returns only displays positively
// reported by xrandr. A discovery failure is unavailable, never one fake
// primary display.
func screenDisplayCountWithContextAndProcess(ctx context.Context, process DisplayProcess) (int, error) {
	out, err := process.Run(ctx, "xrandr", "--listmonitors")
	if err != nil {
		return 0, fmt.Errorf("xrandr --listmonitors: %w", err)
	}
	// First line: "Monitors: N"
	first := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	parts := strings.Fields(first)
	if len(parts) >= 2 {
		if n, err := strconv.Atoi(parts[len(parts)-1]); err == nil && n > 0 {
			return n, nil
		}
	}
	return 0, errors.New("xrandr reported no monitors")
}

// screenDisplayBoundsWithContextAndProcess returns the pixel dimensions of
// the primary display using xdotool. The idx parameter is currently unused,
// matching the existing Linux capture implementation.
func screenDisplayBoundsWithContextAndProcess(ctx context.Context, _ int, process DisplayProcess) (image.Rectangle, error) {
	out, err := process.Run(ctx, "xdotool", "getdisplaygeometry")
	if err != nil {
		return image.Rectangle{}, fmt.Errorf("xdotool getdisplaygeometry: %w", err)
	}
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) == 2 {
		w, we := strconv.Atoi(parts[0])
		h, he := strconv.Atoi(parts[1])
		if we == nil && he == nil && w > 0 && h > 0 {
			return image.Rect(0, 0, w, h), nil
		}
	}
	return image.Rectangle{}, errors.New("xdotool reported invalid display geometry")
}

func screenCapturePrerequisitesWithContextAndProcess(ctx context.Context, process DisplayProcess) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := process.LookPath("scrot"); err != nil {
		return fmt.Errorf("scrot not found – install with 'apt install scrot' or 'dnf install scrot': %w", err)
	}
	return nil
}

// screenCapture uses scrot to capture the given screen region.
// Install scrot with: apt install scrot  OR  dnf install scrot
func screenCaptureWithContextAndProcess(ctx context.Context, bounds image.Rectangle, process DisplayProcess) (*image.RGBA, error) {
	if err := screenCapturePrerequisitesWithContextAndProcess(ctx, process); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp("", "agent-screen-*.png")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	path := f.Name()
	_ = f.Close()
	defer func() {
		_ = os.Remove(path)
	}()

	area := fmt.Sprintf("%d,%d,%d,%d", bounds.Min.X, bounds.Min.Y, bounds.Dx(), bounds.Dy())
	out, err := process.Run(ctx, "scrot", "-a", area, path)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("scrot -a %s: %w (output: %s)", area, err, string(out))
	}

	return loadPNGasRGBA(path)
}

// loadPNGasRGBA opens path, decodes the PNG, and returns an *image.RGBA.
func loadPNGasRGBA(path string) (*image.RGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open screenshot: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	img, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode screenshot: %w", err)
	}

	if rgba, ok := img.(*image.RGBA); ok {
		return rgba, nil
	}
	rgba := image.NewRGBA(img.Bounds())
	draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
	return rgba, nil
}
