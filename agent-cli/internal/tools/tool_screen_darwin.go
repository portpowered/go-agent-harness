//go:build darwin

package tools

import (
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"strings"
)

// screenDisplayCount returns the number of displays by counting "Resolution:"
// entries in system_profiler output.  Falls back to 1 on error.
func screenDisplayCount() int {
	out, err := exec.Command("system_profiler", "SPDisplaysDataType").Output()
	if err != nil {
		return 1
	}
	n := strings.Count(string(out), "Resolution:")
	if n > 0 {
		return n
	}
	return 1
}

// screenDisplayBounds returns the logical (UI) pixel dimensions of the
// requested display by capturing a full-screen PNG and reading its size.
// The logical resolution matches the coordinate space used by screencapture
// and the mouse, including on HiDPI/Retina displays.
// The idx parameter is 0-based; screencapture -D uses 1-based numbering.
func screenDisplayBounds(idx int) image.Rectangle {
	f, err := os.CreateTemp("", "agent-screen-bounds-*.png")
	if err != nil {
		return image.Rect(0, 0, 1920, 1080)
	}
	path := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(path) }()

	args := []string{"-x", path}
	if idx > 0 {
		args = []string{"-x", "-D", fmt.Sprintf("%d", idx+1), path}
	}
	if exec.Command("screencapture", args...).Run() != nil {
		return image.Rect(0, 0, 1920, 1080)
	}

	img, err := loadPNGasRGBA(path)
	if err != nil {
		return image.Rect(0, 0, 1920, 1080)
	}
	return img.Bounds()
}

// screenCapture uses the built-in screencapture command to capture the given
// region.  No external tools need to be installed.
func screenCapture(bounds image.Rectangle) (*image.RGBA, error) {
	f, err := os.CreateTemp("", "agent-screen-*.png")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	path := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(path) }()

	region := fmt.Sprintf("%d,%d,%d,%d", bounds.Min.X, bounds.Min.Y, bounds.Dx(), bounds.Dy())
	out, err := exec.Command("screencapture", "-x", "-R", region, path).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("screencapture -R %s: %w (output: %s)", region, err, string(out))
	}

	return loadPNGasRGBA(path)
}

// loadPNGasRGBA opens path, decodes the PNG, and returns an *image.RGBA.
func loadPNGasRGBA(path string) (*image.RGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open screenshot: %w", err)
	}
	defer func() { _ = f.Close() }()

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
