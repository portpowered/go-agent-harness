//go:build darwin && !cgo

package tools

import "context"

// darwinDisplayPermissionChecker is unavailable in cgo-disabled builds. The
// explicit unavailable result keeps the build/link contract deterministic and
// avoids pretending that Screen Recording access was granted or denied.
type darwinDisplayPermissionChecker struct{}

func (darwinDisplayPermissionChecker) Check(ctx context.Context) (DisplayPermission, error) {
	if ctx == nil {
		ctx = context.Background()
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
	return darwinDisplayPermissionChecker{}
}

// The macOS contract remains present in cgo-disabled builds, but its explicit
// checker result is unavailable rather than a fabricated grant or denial.
func screenRecordingPermissionRecheckSupported() bool { return true }
