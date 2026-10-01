//go:build darwin

package mouse

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	display "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/display"
)

func darwinFixturePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeDarwinDisplayProcess answers system_profiler with two 2x2 displays and
// writes the fixture PNG to the screencapture output path.
func fakeDarwinDisplayProcess(fixture []byte) display.DisplayProcessAdapter {
	return display.DisplayProcessAdapter{
		RunFunc: func(_ context.Context, name string, args ...string) ([]byte, error) {
			switch name {
			case "system_profiler":
				return []byte("Resolution: 2x2\nResolution: 2x2\n"), nil
			case "screencapture":
				return nil, os.WriteFile(args[len(args)-1], fixture, 0o600)
			default:
				return nil, fmt.Errorf("unexpected display command %q", name)
			}
		},
	}
}

func expectedDarwinDragLog(fromX, fromY, toX, toY int) []string {
	lines := []string{fmt.Sprintf("p:%d,%d", fromX, fromY)}
	const steps = 20
	for i := 1; i <= steps; i++ {
		ix := fromX + (toX-fromX)*i/steps
		iy := fromY + (toY-fromY)*i/steps
		lines = append(lines, fmt.Sprintf("m:%d,%d", ix, iy))
	}
	return append(lines, fmt.Sprintf("r:%d,%d", toX, toY))
}

func TestS12DarwinFakeScreenOperations(t *testing.T) {
	surface := display.NewHostDisplaySurfaceWithOptions(display.HostDisplaySurfaceOptions{
		Process: fakeDarwinDisplayProcess(darwinFixturePNG(t)),
		PermissionChecker: display.DisplayPermissionCheckerFunc(func(context.Context) (display.DisplayPermission, error) {
			return display.DisplayPermission{State: display.DisplayPermissionGranted}, nil
		}),
	})
	if got, err := surface.DisplayCount(context.Background()); err != nil || got != 2 {
		t.Fatalf("DisplayCount = %d, %v; want 2", got, err)
	}
	if got, err := surface.Bounds(context.Background(), 1); err != nil || got.Dx() != 2 || got.Dy() != 2 {
		t.Fatalf("Bounds = %v, %v; want 2x2", got, err)
	}
	tool := display.NewScreenToolWithDisplaySurface(surface)
	msgs, err := tool.Execute(context.Background(), map[string]any{"display": float64(1)})
	if err != nil {
		t.Fatal(err)
	}
	assertScreenResult(t, msgs[0], "image/jpeg", 2, 2)

	msgs, err = tool.Execute(context.Background(), map[string]any{"action": "record", "duration": float64(1), "fps": float64(1)})
	if err != nil {
		t.Fatalf("recording result = %#v, err = %v", msgs, err)
	}
	if len(msgs) == 0 || len(msgs[0].ContentParts) < 2 {
		t.Fatalf("recording result = %#v, want image part", msgs)
	}
	imagePart, ok := msgs[0].ContentParts[1].(messages.ImagePart)
	if !ok || imagePart.MediaType != "image/gif" {
		t.Fatalf("recording image part = %#v, want GIF", msgs[0].ContentParts[1])
	}

	bad := filepath.Join(t.TempDir(), "bad.png")
	if err := os.WriteFile(bad, []byte("bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPNGasRGBA(bad); err == nil || !strings.Contains(err.Error(), "decode screenshot") {
		t.Fatalf("invalid PNG error = %v", err)
	}
}

func TestS12DarwinFakeMouseOperations(t *testing.T) {
	dragSleeps := append([]time.Duration{mouseDragPause}, repeatDuration(mouseDragStepPause, 20)...)
	for _, tt := range []struct {
		name       string
		args       map[string]any
		want       string
		wantLog    []string
		wantSleeps []time.Duration
	}{
		{"move", map[string]any{"action": "move", "x": float64(1), "y": float64(2)}, "Mouse moved to (1, 2)", []string{"m:1,2"}, nil},
		{"click", map[string]any{"action": "click", "x": float64(1), "y": float64(2), "button": "right"}, "right click at (1, 2)", []string{"rc:1,2"}, nil},
		{"double", map[string]any{"action": "double_click", "x": float64(1), "y": float64(2), "button": "middle"}, "middle double-click at (1, 2)", []string{"mC:1,2"}, nil},
		{"down", map[string]any{"action": "down", "x": float64(1), "y": float64(2)}, "left button held at (1, 2)", []string{"p:1,2"}, nil},
		{"up", map[string]any{"action": "up", "x": float64(1), "y": float64(2)}, "left button released at (1, 2)", []string{"r:1,2"}, nil},
		{"drag", map[string]any{"action": "drag", "x": float64(1), "y": float64(2), "to_x": float64(3), "to_y": float64(4)}, "left drag from (1, 2) to (3, 4)", expectedDarwinDragLog(1, 2, 3, 4), dragSleeps},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			process := &fakeMouseProcess{}
			var sleeps recordedSleeps
			tool := NewMouseToolWithOptions(MouseToolOptions{Process: process, Sleep: sleeps.sleep})
			msgs, err := tool.Execute(context.Background(), tt.args)
			if err != nil || len(msgs) != 1 || msgs[0].TextContent() != tt.want {
				t.Fatalf("mouse result = %#v, err = %v; want %q", msgs, err, tt.want)
			}
			assertMouseCalls(t, process, "cliclick", tt.wantLog)
			if fmt.Sprint([]time.Duration(sleeps)) != fmt.Sprint(tt.wantSleeps) {
				t.Fatalf("sleeps = %v, want %v", sleeps, tt.wantSleeps)
			}
		})
	}
}

func repeatDuration(duration time.Duration, count int) []time.Duration {
	durations := make([]time.Duration, count)
	for i := range durations {
		durations[i] = duration
	}
	return durations
}

// TestS12DarwinMouseToolRunsCliclickSubprocess keeps the production process
// runner covered end to end with one fake cliclick executable on PATH.
func TestS12DarwinMouseToolRunsCliclickSubprocess(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "cliclick.log")
	writeFakeCommand(t, dir, "cliclick", `printf '%s\n' "$*" >> "$GO_AGENT_HARNESS_CLICLICK_LOG"`)
	t.Setenv("GO_AGENT_HARNESS_CLICLICK_LOG", logPath)
	t.Setenv("PATH", dir)
	msgs, err := NewMouseTool().Execute(context.Background(), map[string]any{"action": "move", "x": float64(1), "y": float64(2)})
	if err != nil || len(msgs) != 1 || msgs[0].TextContent() != "Mouse moved to (1, 2)" {
		t.Fatalf("mouse result = %#v, err = %v", msgs, err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != "m:1,2" {
		t.Fatalf("cliclick log = %q, want %q", got, "m:1,2")
	}

	t.Setenv("PATH", t.TempDir())
	if err := newMouseDriver(MouseToolOptions{}).move(1, 2); err == nil || !strings.Contains(err.Error(), "cliclick not found") {
		t.Fatalf("missing cliclick error = %v", err)
	}
}

func TestDarwinHostDisplaySurfaceProbeUsesOneMetadataQuery(t *testing.T) {
	runCalls := 0
	process := display.DisplayProcessAdapter{
		RunFunc: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			runCalls++
			if name != "system_profiler" {
				t.Fatalf("probe ran unexpected command %q", name)
			}
			return []byte("Resolution: 2x2\n"), nil
		},
		LookPathFunc: func(file string) (string, error) {
			if file != "screencapture" {
				t.Fatalf("probe checked unexpected executable %q", file)
			}
			return file, nil
		},
	}

	capability, err := display.NewHostDisplaySurfaceWithOptions(display.HostDisplaySurfaceOptions{
		Process: process,
		PermissionChecker: display.DisplayPermissionCheckerFunc(func(context.Context) (display.DisplayPermission, error) {
			return display.DisplayPermission{State: display.DisplayPermissionGranted}, nil
		}),
	}).Probe(context.Background())
	if err != nil {
		t.Fatalf("display probe: %v", err)
	}
	if !capability.Usable() || capability.DisplayCount != 1 {
		t.Fatalf("display capability = %+v, want one usable display", capability)
	}
	if runCalls != 1 {
		t.Fatalf("system_profiler calls = %d, want one metadata query", runCalls)
	}
}

func TestS4DarwinUnsupportedMouseButtons(t *testing.T) {
	process := &fakeMouseProcess{}
	var sleeps recordedSleeps
	driver := newFakeMouseDriver(process, &sleeps)
	for _, call := range []func() error{
		func() error { return driver.buttonDown(1, 2, "right") },
		func() error { return driver.buttonUp(1, 2, "middle") },
		func() error { return driver.drag(1, 2, 3, 4, "right") },
	} {
		err := call()
		if err == nil || !strings.Contains(err.Error(), "only supports") {
			t.Fatalf("unsupported button error = %v", err)
		}
	}
	if len(process.calls) != 0 {
		t.Fatalf("unsupported buttons ran cliclick: %q", process.calls)
	}
	if cliclickAction("left", "c") != "c" || cliclickAction("right", "c") != "rc" || cliclickAction("middle", "c") != "mc" || cliclickAction("other", "c") != "c" {
		t.Fatal("cliclick button mapping is incorrect")
	}
}

func TestS4DarwinCliclickErrors(t *testing.T) {
	var sleeps recordedSleeps
	failing := &fakeMouseProcess{run: func([]string) ([]byte, error) {
		return []byte("command failed\n"), errors.New("exit status 7")
	}}
	driver := newFakeMouseDriver(failing, &sleeps)
	if err := driver.click(1, 2, "left"); err == nil || !strings.Contains(err.Error(), "cliclick [c:1,2]") || !strings.Contains(err.Error(), "command failed") {
		t.Fatalf("cliclick command error = %v", err)
	}
	if err := driver.drag(1, 2, 3, 4, "left"); err == nil || !strings.Contains(err.Error(), "drag start") {
		t.Fatalf("drag start error = %v", err)
	}

	stepFailure := &fakeMouseProcess{run: func(args []string) ([]byte, error) {
		if strings.HasPrefix(args[0], "p:") {
			return nil, nil
		}
		return nil, errors.New("exit status 7")
	}}
	err := newFakeMouseDriver(stepFailure, &sleeps).drag(1, 2, 3, 4, "left")
	if err == nil || !strings.Contains(err.Error(), "drag step 1") {
		t.Fatalf("drag step error = %v", err)
	}
	assertMouseCalls(t, stepFailure, "cliclick", []string{"p:1,2", "m:1,2", "r:1,2"})

	missing := &fakeMouseProcess{run: func([]string) ([]byte, error) { return nil, helperNotFound("cliclick") }}
	if err := newFakeMouseDriver(missing, &sleeps).move(1, 2); err == nil || !strings.Contains(err.Error(), "cliclick not found") {
		t.Fatalf("missing cliclick error = %v", err)
	}
	if sleeps.total() != mouseDragPause {
		t.Fatalf("sleeps = %v, want only the drag press pause before the failing step", sleeps)
	}
}
