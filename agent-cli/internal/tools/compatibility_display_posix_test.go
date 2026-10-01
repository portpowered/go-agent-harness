//go:build !windows

package tools

import (
	"context"
	"errors"
	"testing"
)

func TestDisplaySurfaceReportsUnavailableDiscovery(t *testing.T) {
	process := DisplayProcessAdapter{
		RunFunc: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "xrandr" || name == "system_profiler" {
				return nil, errors.New("discovery failed")
			}
			return nil, nil
		},
		LookPathFunc: func(string) (string, error) { return "", errors.New("missing capture command") },
	}
	surface := NewHostDisplaySurfaceWithOptions(HostDisplaySurfaceOptions{
		Process: process,
		PermissionChecker: DisplayPermissionCheckerFunc(func(context.Context) (DisplayPermission, error) {
			return DisplayPermission{State: DisplayPermissionGranted}, nil
		}),
	})
	capability, err := surface.Probe(context.Background())
	if err == nil || capability.State != ScreenCaptureUnavailable {
		t.Fatalf("failed discovery Probe = %+v, %v", capability, err)
	}
}
