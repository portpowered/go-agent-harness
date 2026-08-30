//go:build darwin && cgo

package tools

/*
#cgo darwin LDFLAGS: -framework CoreGraphics
#include <CoreGraphics/CoreGraphics.h>

static int go_agent_harness_preflight_screen_capture_access(void) {
	return CGPreflightScreenCaptureAccess() ? 1 : 0;
}
*/
import "C"

import "context"

// darwinDisplayPermissionChecker is deliberately a tiny native boundary:
// CGPreflightScreenCaptureAccess is non-prompting, and unlike screencapture it
// does not create a temporary file or start a capture process.
type darwinDisplayPermissionChecker struct{}

func (darwinDisplayPermissionChecker) Check(ctx context.Context) (DisplayPermission, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return DisplayPermission{}, err
	}
	if C.go_agent_harness_preflight_screen_capture_access() == 0 {
		return DisplayPermission{
			State:  DisplayPermissionDenied,
			Reason: "CGPreflightScreenCaptureAccess reported that Screen Recording access is denied",
		}, nil
	}
	if err := ctx.Err(); err != nil {
		return DisplayPermission{}, err
	}
	return DisplayPermission{State: DisplayPermissionGranted}, nil
}

func defaultDisplayPermissionChecker() DisplayPermissionChecker {
	return darwinDisplayPermissionChecker{}
}
