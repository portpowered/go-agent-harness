//go:build darwin

package tools

import (
	"context"
	"errors"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDarwinDisplayBoundsUseDiscoveryWithoutCapturing(t *testing.T) {
	var calls []string
	process := DisplayProcessAdapter{
		RunFunc: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			calls = append(calls, name)
			if name != "system_profiler" {
				return nil, errors.New("unexpected capture command")
			}
			return []byte("Resolution: 1440 x 900 Retina\nResolution: 1920 x 1080\n"), nil
		},
		LookPathFunc: func(string) (string, error) { return "screencapture", nil },
	}

	count, err := screenDisplayCountWithContextAndProcess(context.Background(), process)
	if err != nil || count != 2 {
		t.Fatalf("display count = %d, err = %v", count, err)
	}
	bounds, err := screenDisplayBoundsWithContextAndProcess(context.Background(), 1, process)
	if err != nil || bounds != image.Rect(0, 0, 1920, 1080) {
		t.Fatalf("display bounds = %v, err = %v", bounds, err)
	}
	for _, call := range calls {
		if call == "screencapture" {
			t.Fatalf("display discovery invoked %q", call)
		}
	}
}

func TestDarwinCaptureCleansTemporaryArtifactOnSuccess(t *testing.T) {
	fixture := filepath.Join(t.TempDir(), "fixture.png")
	if err := os.WriteFile(fixture, []byte("not a real PNG"), 0o600); err != nil {
		t.Fatal(err)
	}
	var outputPath string
	process := DisplayProcessAdapter{
		RunFunc: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "screencapture" {
				return []byte("Resolution: 2 x 2\n"), nil
			}
			outputPath = args[len(args)-1]
			data, err := os.ReadFile(fixture)
			if err != nil {
				return nil, err
			}
			return nil, os.WriteFile(outputPath, data, 0o600)
		},
		LookPathFunc: func(string) (string, error) { return "screencapture", nil },
	}
	_, err := screenCaptureDisplayWithContextAndProcess(context.Background(), 0, image.Rect(0, 0, 2, 2), process)
	if err == nil || !strings.Contains(err.Error(), "decode screenshot") {
		t.Fatalf("invalid capture result err = %v", err)
	}
	if outputPath == "" {
		t.Fatal("capture process did not receive a temporary output path")
	}
	if _, statErr := os.Stat(outputPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("temporary capture artifact stat error = %v, want removed", statErr)
	}
}

func TestDarwinCaptureClassifiesPermissionDenialAndCancellation(t *testing.T) {
	permissionProcess := DisplayProcessAdapter{
		RunFunc: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("screencapture: Screen Recording permission denied"), errors.New("exit status 1")
		},
		LookPathFunc: func(string) (string, error) { return "screencapture", nil },
	}
	_, err := screenCaptureDisplayWithContextAndProcess(context.Background(), 0, image.Rect(0, 0, 2, 2), permissionProcess)
	if err == nil || !errors.Is(err, ErrScreenRecordingPermissionDenied) {
		t.Fatalf("permission capture err = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	cancelProcess := DisplayProcessAdapter{
		RunFunc: func(context.Context, string, ...string) ([]byte, error) {
			called = true
			return nil, errors.New("must not run")
		},
		LookPathFunc: func(string) (string, error) { return "screencapture", nil },
	}
	_, err = screenCaptureDisplayWithContextAndProcess(ctx, 0, image.Rect(0, 0, 2, 2), cancelProcess)
	if err == nil || !errors.Is(err, context.Canceled) || called {
		t.Fatalf("canceled capture err = %v, process called = %v", err, called)
	}
}
