//go:build linux

package display

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

func screenDisplayInfoWithContextAndProcess(ctx context.Context, process DisplayProcess) (int, image.Rectangle, error) {
	count, err := screenDisplayCountWithContextAndProcess(ctx, process)
	if err != nil {
		return 0, image.Rectangle{}, err
	}
	bounds, err := screenDisplayBoundsWithContextAndProcess(ctx, 0, process)
	if err != nil {
		return 0, image.Rectangle{}, err
	}
	return count, bounds, nil
}

// screenDisplayCountWithContextAndProcess returns only displays positively
// reported by xrandr. A discovery failure is unavailable, never one fake
// primary display.
func screenDisplayCountWithContextAndProcess(ctx context.Context, process DisplayProcess) (int, error) {
	process = normalizeDisplayProcess(process)
	out, err := process.Run(ctx, "xrandr", "--listmonitors")
	if err != nil {
		if ctx != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return 0, ctxErr
			}
		}
		return 0, fmt.Errorf("xrandr --listmonitors: %w", err)
	}
	// First line: "Monitors: N".
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
// the primary display using xdotool. The idx parameter remains unused to
// match the existing Linux capture implementation.
func screenDisplayBoundsWithContextAndProcess(ctx context.Context, _ int, process DisplayProcess) (image.Rectangle, error) {
	process = normalizeDisplayProcess(process)
	out, err := process.Run(ctx, "xdotool", "getdisplaygeometry")
	if err != nil {
		if ctx != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return image.Rectangle{}, ctxErr
			}
		}
		return image.Rectangle{}, fmt.Errorf("xdotool getdisplaygeometry: %w", err)
	}
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) == 2 {
		w, widthErr := strconv.Atoi(parts[0])
		h, heightErr := strconv.Atoi(parts[1])
		if widthErr == nil && heightErr == nil && w > 0 && h > 0 {
			return image.Rect(0, 0, w, h), nil
		}
	}
	return image.Rectangle{}, errors.New("xdotool reported invalid display geometry")
}

func screenCapturePrerequisitesWithContextAndProcess(ctx context.Context, process DisplayProcess) error {
	if ctx == nil {
		return errors.New("screen capture context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	process = normalizeDisplayProcess(process)
	if _, err := process.LookPath("scrot"); err != nil {
		return fmt.Errorf("scrot not found – install with 'apt install scrot' or 'dnf install scrot': %w", err)
	}
	return nil
}

// screenCaptureDisplayWithContextAndProcess uses scrot to capture the given
// screen region. Linux currently has one geometry surface, so display is
// intentionally ignored while the index remains part of the seam.
func screenCaptureDisplayWithContextAndProcess(ctx context.Context, _ int, bounds image.Rectangle, process DisplayProcess) (result *image.RGBA, resultErr error) {
	if ctx == nil {
		return nil, errors.New("screen capture context is required")
	}
	if err := screenCapturePrerequisitesWithContextAndProcess(ctx, process); err != nil {
		return nil, err
	}
	process = normalizeDisplayProcess(process)

	f, err := os.CreateTemp("", "agent-screen-*.png")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	path := f.Name()
	if closeErr := f.Close(); closeErr != nil {
		return nil, closeScreenCaptureTempFileAfterCloseFailure(path, closeErr)
	}
	defer func() { result, resultErr = finishScreenCapture(result, resultErr, path) }()

	area := fmt.Sprintf("%d,%d,%d,%d", bounds.Min.X, bounds.Min.Y, bounds.Dx(), bounds.Dy())
	args := []string{"-a", area, path}
	out, err := process.Run(ctx, "scrot", args...)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("scrot %s: %w (output: %s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}

	img, err := loadPNGasRGBAWithContext(ctx, path)
	if err != nil {
		return nil, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	return img, nil
}

func loadPNGasRGBAWithContext(ctx context.Context, path string) (result *image.RGBA, resultErr error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open screenshot: %w", err)
	}
	defer func() { result, resultErr = finishScreenshotFile(result, resultErr, f) }()

	img, err := png.Decode(contextReader{ctx: ctx, r: f})
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

func cleanupScreenCaptureTempFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func closeScreenCaptureTempFileAfterCloseFailure(path string, closeErr error) error {
	removeErr := cleanupScreenCaptureTempFile(path)
	if removeErr == nil {
		return fmt.Errorf("close temp file: %w", closeErr)
	}
	return errors.Join(
		fmt.Errorf("close temp file: %w", closeErr),
		fmt.Errorf("remove temp file: %w", removeErr),
	)
}

func finishScreenCapture(result *image.RGBA, resultErr error, path string) (*image.RGBA, error) {
	removeErr := cleanupScreenCaptureTempFile(path)
	if removeErr == nil {
		return result, resultErr
	}
	return nil, errors.Join(resultErr, fmt.Errorf("remove screenshot temp file: %w", removeErr))
}

func finishScreenshotFile(result *image.RGBA, resultErr error, file *os.File) (*image.RGBA, error) {
	if closeErr := file.Close(); closeErr != nil {
		return nil, errors.Join(resultErr, fmt.Errorf("close screenshot file: %w", closeErr))
	}
	return result, resultErr
}
