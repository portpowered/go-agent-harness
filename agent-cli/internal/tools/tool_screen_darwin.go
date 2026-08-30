//go:build darwin

package tools

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"regexp"
	"strings"
)

var darwinDisplayResolutionPattern = regexp.MustCompile(`(?i)([0-9]+)\s*x\s*([0-9]+)`)

func darwinDisplayResolutionsWithContextAndProcess(ctx context.Context, process DisplayProcess) ([]image.Rectangle, error) {
	out, err := process.Run(ctx, "system_profiler", "SPDisplaysDataType")
	if err != nil {
		return nil, fmt.Errorf("system_profiler SPDisplaysDataType: %w", err)
	}
	resolutions := darwinDisplayResolutions(string(out))
	if len(resolutions) == 0 {
		return nil, errors.New("system_profiler reported no usable displays")
	}
	return resolutions, nil
}

// screenDisplayInfoWithContextAndProcess performs one metadata query for the
// admission probe. system_profiler is comparatively expensive on macOS; a
// count query followed by a second geometry query could exceed the bounded
// session-admission budget even when the desktop is healthy.
func screenDisplayInfoWithContextAndProcess(ctx context.Context, process DisplayProcess) (int, image.Rectangle, error) {
	resolutions, err := darwinDisplayResolutionsWithContextAndProcess(ctx, process)
	if err != nil {
		return 0, image.Rectangle{}, err
	}
	return len(resolutions), resolutions[0], nil
}

func screenDisplayCountWithContextAndProcess(ctx context.Context, process DisplayProcess) (int, error) {
	resolutions, err := darwinDisplayResolutionsWithContextAndProcess(ctx, process)
	if err != nil {
		return 0, err
	}
	return len(resolutions), nil
}

// screenDisplayBounds returns the logical (UI) pixel dimensions reported by
// system_profiler. It deliberately does not capture screen content merely to
// decide whether the display tool may be advertised.
func screenDisplayBoundsWithContextAndProcess(ctx context.Context, idx int, process DisplayProcess) (image.Rectangle, error) {
	resolutions, err := darwinDisplayResolutionsWithContextAndProcess(ctx, process)
	if err != nil {
		return image.Rectangle{}, err
	}
	if idx < 0 || idx >= len(resolutions) {
		return image.Rectangle{}, fmt.Errorf("display %d not available (only %d display(s) found)", idx, len(resolutions))
	}
	return resolutions[idx], nil
}

func darwinDisplayResolutions(output string) []image.Rectangle {
	lines := strings.Split(output, "\n")
	resolutions := make([]image.Rectangle, 0)
	for _, line := range lines {
		if !strings.Contains(strings.ToLower(line), "resolution:") {
			continue
		}
		match := darwinDisplayResolutionPattern.FindStringSubmatch(line)
		if len(match) != 3 {
			continue
		}
		var width, height int
		if _, err := fmt.Sscanf(match[1], "%d", &width); err != nil {
			continue
		}
		if _, err := fmt.Sscanf(match[2], "%d", &height); err != nil {
			continue
		}
		if width > 0 && height > 0 {
			resolutions = append(resolutions, image.Rect(0, 0, width, height))
		}
	}
	return resolutions
}

func screenCapturePrerequisitesWithContextAndProcess(ctx context.Context, process DisplayProcess) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := process.LookPath("screencapture"); err != nil {
		return fmt.Errorf("screencapture not found: %w", err)
	}
	return nil
}

// screenCapture uses the built-in screencapture command to capture the given
// region. No external tools need to be installed.
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
	defer func() { _ = os.Remove(path) }()

	region := fmt.Sprintf("%d,%d,%d,%d", bounds.Min.X, bounds.Min.Y, bounds.Dx(), bounds.Dy())
	out, err := process.Run(ctx, "screencapture", "-x", "-R", region, path)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
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
