//go:build !windows && !linux && !darwin

package tools

import (
	"context"
	"errors"
	"fmt"
	"image"
)

func screenDisplayInfoWithContextAndProcess(ctx context.Context, process DisplayProcess) (int, image.Rectangle, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return 0, image.Rectangle{}, err
		}
	}
	return 0, image.Rectangle{}, errors.New("display discovery is not supported on this platform")
}

func screenDisplayCountWithContextAndProcess(ctx context.Context, _ DisplayProcess) (int, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
	}
	return 0, errors.New("display discovery is not supported on this platform")
}

func screenDisplayBoundsWithContextAndProcess(ctx context.Context, _ int, _ DisplayProcess) (image.Rectangle, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return image.Rectangle{}, err
		}
	}
	return image.Rectangle{}, errors.New("display geometry is not supported on this platform")
}

func screenCapturePrerequisitesWithContextAndProcess(ctx context.Context, _ DisplayProcess) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return errors.New("screen capture is not yet supported on this platform")
}

func screenCaptureWithContextAndProcess(ctx context.Context, bounds image.Rectangle, process DisplayProcess) (*image.RGBA, error) {
	return screenCaptureDisplayWithContextAndProcess(ctx, 0, bounds, process)
}

func screenCaptureDisplayWithContextAndProcess(ctx context.Context, _ int, _ image.Rectangle, _ DisplayProcess) (*image.RGBA, error) {
	if err := screenCapturePrerequisitesWithContextAndProcess(ctx, nil); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("screen capture is not yet supported on this platform")
}
