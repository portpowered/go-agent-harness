//go:build darwin

package display

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

const darwinDisplayResolutionCaptureGroups = 3

var darwinDisplayResolutionPattern = regexp.MustCompile(`(?i)resolution:\s*([0-9]+)\s*x\s*([0-9]+)`)

func screenDisplayInfoWithContextAndProcess(ctx context.Context, process DisplayProcess) (int, image.Rectangle, error) {
	resolutions, err := darwinDisplayResolutionsWithContextAndProcess(ctx, process)
	if err != nil {
		return 0, image.Rectangle{}, err
	}
	return len(resolutions), resolutions[0], nil
}

func darwinDisplayResolutionsWithContextAndProcess(ctx context.Context, process DisplayProcess) ([]image.Rectangle, error) {
	process = normalizeDisplayProcess(process)
	out, err := process.Run(ctx, "system_profiler", "SPDisplaysDataType")
	if err != nil {
		if ctx != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
		}
		return nil, fmt.Errorf("system_profiler SPDisplaysDataType: %w", err)
	}
	resolutions := darwinDisplayResolutions(string(out))
	if len(resolutions) == 0 {
		return nil, errors.New("system_profiler reported no displays")
	}
	return resolutions, nil
}

func screenDisplayCountWithContextAndProcess(ctx context.Context, process DisplayProcess) (int, error) {
	resolutions, err := darwinDisplayResolutionsWithContextAndProcess(ctx, process)
	if err != nil {
		return 0, err
	}
	return len(resolutions), nil
}

// screenDisplayBoundsWithContextAndProcess reads the display's reported
// resolution. It deliberately does not capture a frame to discover bounds.
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
	resolutions := make([]image.Rectangle, 0, len(lines))
	for _, line := range lines {
		match := darwinDisplayResolutionPattern.FindStringSubmatch(line)
		if len(match) != darwinDisplayResolutionCaptureGroups {
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
	if ctx == nil {
		return errors.New("screen capture context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	process = normalizeDisplayProcess(process)
	if _, err := process.LookPath(screenCaptureCommand); err != nil {
		return fmt.Errorf("%s not found: %w", screenCaptureCommand, err)
	}
	return nil
}

func isScreenRecordingPermissionDenied(output []byte, err error) bool {
	if errors.Is(err, ErrScreenRecordingPermissionDenied) {
		return true
	}
	text := strings.TrimSpace(string(output))
	if err != nil {
		text = strings.TrimSpace(strings.Join([]string{text, err.Error()}, " "))
	}
	return screenRecordingPermissionText(text)
}

func screenCaptureDisplayWithContextAndProcess(ctx context.Context, display int, _ image.Rectangle, process DisplayProcess) (*image.RGBA, error) {
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
	if err := f.Close(); err != nil {
		removeScreenCaptureTempFile(path)
		return nil, fmt.Errorf("close temp file: %w", err)
	}
	defer removeScreenCaptureTempFile(path)

	args := []string{"-x", "-D", fmt.Sprintf("%d", display+1), path}
	out, err := process.Run(ctx, screenCaptureCommand, args...)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if isScreenRecordingPermissionDenied(out, err) {
			return nil, &ScreenRecordingPermissionError{Detail: strings.TrimSpace(string(out)), Cause: err}
		}
		return nil, fmt.Errorf("screencapture %s: %w (output: %s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
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

func loadPNGasRGBAWithContext(ctx context.Context, path string) (*image.RGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open screenshot: %w", err)
	}
	defer closeScreenCaptureFile(f)

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

func removeScreenCaptureTempFile(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return
	}
}

func closeScreenCaptureFile(file *os.File) {
	if file == nil {
		return
	}
	if err := file.Close(); err != nil {
		return
	}
}
