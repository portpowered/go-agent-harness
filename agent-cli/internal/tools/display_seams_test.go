package tools

import (
	"context"
	"errors"
	"fmt"
	"image"
)

type DisplayPermissionCheckerFunc func(context.Context) (DisplayPermission, error)

func (f DisplayPermissionCheckerFunc) Check(ctx context.Context) (DisplayPermission, error) {
	if f == nil {
		return DisplayPermission{State: DisplayPermissionGranted}, nil
	}
	return f(ctx)
}

// DisplayProcessAdapter is a deterministic process seam for platform tests.
type DisplayProcessAdapter struct {
	RunFunc      func(context.Context, string, ...string) ([]byte, error)
	LookPathFunc func(string) (string, error)
}

func (p DisplayProcessAdapter) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if p.RunFunc == nil {
		return nil, fmt.Errorf("display process runner is not configured")
	}
	return p.RunFunc(ctx, name, args...)
}

func (p DisplayProcessAdapter) LookPath(file string) (string, error) {
	if p.LookPathFunc == nil {
		return file, nil
	}
	return p.LookPathFunc(file)
}

type DisplayCapturerFunc func(context.Context, int, image.Rectangle) (*image.RGBA, error)

func (f DisplayCapturerFunc) Capture(ctx context.Context, display int, bounds image.Rectangle) (*image.RGBA, error) {
	if f == nil {
		return nil, errors.New("display capturer is not configured")
	}
	return f(ctx, display, bounds)
}
