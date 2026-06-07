//go:build linux

package tools

import (
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// screenDisplayCount returns the number of connected displays reported by
// xrandr.  Falls back to 1 if xrandr is unavailable.
func screenDisplayCount() int {
	out, err := exec.Command("xrandr", "--listmonitors").Output()
	if err != nil {
		return 1
	}
	// First line: "Monitors: N"
	first := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	parts := strings.Fields(first)
	if len(parts) >= 2 {
		if n, err := strconv.Atoi(parts[len(parts)-1]); err == nil && n > 0 {
			return n
		}
	}
	return 1
}

// screenDisplayBounds returns the pixel dimensions of the primary display
// using xdotool.  The idx parameter is currently unused (matching the
// Windows implementation which also only queries the primary screen).
func screenDisplayBounds(_ int) image.Rectangle {
	out, err := exec.Command("xdotool", "getdisplaygeometry").Output()
	if err != nil {
		return image.Rect(0, 0, 1920, 1080)
	}
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) == 2 {
		w, we := strconv.Atoi(parts[0])
		h, he := strconv.Atoi(parts[1])
		if we == nil && he == nil {
			return image.Rect(0, 0, w, h)
		}
	}
	return image.Rect(0, 0, 1920, 1080)
}

// screenCapture uses scrot to capture the given screen region.
// Install scrot with: apt install scrot  OR  dnf install scrot
func screenCapture(bounds image.Rectangle) (*image.RGBA, error) {
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
	out, err := exec.Command("scrot", "-a", area, path).CombinedOutput()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("scrot not found – install with 'apt install scrot' or 'dnf install scrot'")
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
