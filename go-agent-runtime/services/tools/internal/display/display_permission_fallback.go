//go:build (darwin && !cgo) || !darwin

package display

import (
	"context"
	"errors"
	"runtime"
)

// darwinDisplayPermissionChecker is unavailable in cgo-disabled builds. The
// explicit unavailable result keeps that build deterministic and avoids
// pretending that Screen Recording access was granted or denied.
type darwinDisplayPermissionChecker struct{}

func (darwinDisplayPermissionChecker) Check(ctx context.Context) (DisplayPermission, error) {
	if ctx == nil {
		return DisplayPermission{}, errors.New("display permission context is required")
	}
	if err := ctx.Err(); err != nil {
		return DisplayPermission{}, err
	}
	return DisplayPermission{
		State:  DisplayPermissionUnavailable,
		Reason: "macOS Screen Recording preflight is unavailable because cgo is disabled",
	}, nil
}

func defaultDisplayPermissionChecker() DisplayPermissionChecker {
	if runtime.GOOS == "darwin" {
		return darwinDisplayPermissionChecker{}
	}
	return nil
}

func screenRecordingPermissionRecheckSupported() bool { return runtime.GOOS == "darwin" }
