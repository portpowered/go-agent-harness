//go:build linux || darwin

package display

import (
	"context"
	"errors"
	"runtime"
	"testing"
)

// The display admission process seam exists only on command-based platforms.
func TestDisplaySurfaceProbeUsesContextAndDoesNotCapture(t *testing.T) {
	var calls []string
	process := DisplayProcessAdapter{
		RunFunc: func(ctx context.Context, name string, _ ...string) ([]byte, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			calls = append(calls, name)
			switch name {
			case "system_profiler":
				return []byte("Resolution: 16 x 10\n"), nil
			case "xrandr":
				return []byte("Monitors: 1\n"), nil
			case "xdotool":
				if runtime.GOOS == "linux" {
					return []byte("8 6\n"), nil
				}
				return nil, errors.New("unexpected process")
			default:
				return nil, errors.New("unexpected process")
			}
		},
		LookPathFunc: func(name string) (string, error) {
			calls = append(calls, "lookpath:"+name)
			return name, nil
		},
	}
	capability, err := NewHostDisplaySurfaceWithOptions(HostDisplaySurfaceOptions{
		Process: process,
		PermissionChecker: DisplayPermissionCheckerFunc(func(context.Context) (DisplayPermission, error) {
			return DisplayPermission{State: DisplayPermissionGranted}, nil
		}),
	}).Probe(context.Background())
	if err != nil || !capability.Usable() {
		t.Fatalf("display probe = %#v, err = %v", capability, err)
	}
	for _, call := range calls {
		if call == screenCaptureCommand || call == "scrot" {
			t.Fatalf("probe attempted image capture through %q", call)
		}
	}
}
