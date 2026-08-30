//go:build darwin

package tools

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func writeDarwinCommand(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nset -eu\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func fakeDarwinDesktop(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	fixture := filepath.Join(dir, "fixture.png")
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GO_AGENT_HARNESS_SCREEN_FIXTURE", fixture)
	writeDarwinCommand(t, dir, "system_profiler", `printf 'Resolution: 2x2\nResolution: 2x2\n'`)
	writeDarwinCommand(t, dir, "screencapture", `last=""; for arg in "$@"; do last="$arg"; done; cp "$GO_AGENT_HARNESS_SCREEN_FIXTURE" "$last"`)
	writeDarwinCommand(t, dir, "cliclick", `printf '%s\n' "$*" >> "$GO_AGENT_HARNESS_CLICLICK_LOG"`)
	t.Setenv("GO_AGENT_HARNESS_CLICLICK_LOG", filepath.Join(dir, "cliclick.log"))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func assertDarwinCliclickLog(t *testing.T, path string, want []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	gotText := strings.TrimSpace(string(data))
	wantText := strings.Join(want, "\n")
	if gotText != wantText {
		t.Fatalf("cliclick log = %q, want %q", gotText, wantText)
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

func TestS12DarwinFakeScreenAndMouseOperations(t *testing.T) {
	dir := fakeDarwinDesktop(t)
	logPath := filepath.Join(dir, "cliclick.log")
	tool := NewScreenTool()
	if got := screenDisplayCount(); got != 2 {
		t.Fatalf("screenDisplayCount = %d, want 2", got)
	}
	if got := screenDisplayBounds(1); got.Dx() != 2 || got.Dy() != 2 {
		t.Fatalf("screenDisplayBounds = %v", got)
	}
	msgs, err := tool.Execute(context.Background(), map[string]any{"display": float64(1)})
	if err != nil {
		t.Fatal(err)
	}
	assertScreenResult(t, msgs[0], "image/jpeg", 2, 2)

	msgs, err = tool.Execute(context.Background(), map[string]any{"action": "record", "duration": float64(1), "fps": float64(1)})
	if err != nil || msgs[0].ContentParts[1].(messages.ImagePart).MediaType != "image/gif" {
		t.Fatalf("recording result = %#v, err = %v", msgs, err)
	}
	mousetool := NewMouseTool()
	for _, tt := range []struct {
		name    string
		args    map[string]any
		want    string
		wantLog []string
	}{
		{"move", map[string]any{"action": "move", "x": float64(1), "y": float64(2)}, "Mouse moved to (1, 2)", []string{"m:1,2"}},
		{"click", map[string]any{"action": "click", "x": float64(1), "y": float64(2), "button": "right"}, "right click at (1, 2)", []string{"rc:1,2"}},
		{"double", map[string]any{"action": "double_click", "x": float64(1), "y": float64(2), "button": "middle"}, "middle double-click at (1, 2)", []string{"mC:1,2"}},
		{"down", map[string]any{"action": "down", "x": float64(1), "y": float64(2)}, "left button held at (1, 2)", []string{"p:1,2"}},
		{"up", map[string]any{"action": "up", "x": float64(1), "y": float64(2)}, "left button released at (1, 2)", []string{"r:1,2"}},
		{"drag", map[string]any{"action": "drag", "x": float64(1), "y": float64(2), "to_x": float64(3), "to_y": float64(4)}, "left drag from (1, 2) to (3, 4)", expectedDarwinDragLog(1, 2, 3, 4)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(logPath, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			msgs, err := mousetool.Execute(context.Background(), tt.args)
			if err != nil || len(msgs) != 1 || msgs[0].TextContent() != tt.want {
				t.Fatalf("mouse result = %#v, err = %v; want %q", msgs, err, tt.want)
			}
			assertDarwinCliclickLog(t, logPath, tt.wantLog)
		})
	}

	bad := filepath.Join(dir, "bad.png")
	if err := os.WriteFile(bad, []byte("bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPNGasRGBA(bad); err == nil || !strings.Contains(err.Error(), "decode screenshot") {
		t.Fatalf("invalid PNG error = %v", err)
	}
}

func TestDarwinHostDisplaySurfaceProbeUsesOneMetadataQuery(t *testing.T) {
	runCalls := 0
	process := DisplayProcessAdapter{
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

	capability, err := NewHostDisplaySurface(process).Probe(context.Background())
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
	for _, call := range []func() error{
		func() error { return mouseButtonDown(1, 2, "right") },
		func() error { return mouseButtonUp(1, 2, "middle") },
		func() error { return mouseDrag(1, 2, 3, 4, "right") },
	} {
		err := call()
		if err == nil || !strings.Contains(err.Error(), "only supports") {
			t.Fatalf("unsupported button error = %v", err)
		}
	}
	if cliclickAction("left", "c") != "c" || cliclickAction("right", "c") != "rc" || cliclickAction("middle", "c") != "mc" || cliclickAction("other", "c") != "c" {
		t.Fatal("cliclick button mapping is incorrect")
	}
}

func TestS4DarwinCliclickErrors(t *testing.T) {
	failDir := t.TempDir()
	writeDarwinCommand(t, failDir, "cliclick", `printf 'command failed\n' >&2; exit 7`)
	t.Setenv("PATH", failDir)
	if err := mouseClick(1, 2, "left"); err == nil || !strings.Contains(err.Error(), "cliclick [c:1,2]") || !strings.Contains(err.Error(), "command failed") {
		t.Fatalf("cliclick command error = %v", err)
	}
	if err := mouseDrag(1, 2, 3, 4, "left"); err == nil || !strings.Contains(err.Error(), "drag start") {
		t.Fatalf("drag start error = %v", err)
	}

	stepDir := t.TempDir()
	stepLog := filepath.Join(stepDir, "cliclick.log")
	t.Setenv("GO_AGENT_HARNESS_CLICLICK_LOG", stepLog)
	writeDarwinCommand(t, stepDir, "cliclick", `printf '%s\n' "$*" >> "$GO_AGENT_HARNESS_CLICLICK_LOG"; case "$1" in p:*) exit 0 ;; *) exit 7 ;; esac`)
	t.Setenv("PATH", stepDir)
	if err := mouseDrag(1, 2, 3, 4, "left"); err == nil || !strings.Contains(err.Error(), "drag step 1") {
		t.Fatalf("drag step error = %v", err)
	}

	t.Setenv("PATH", t.TempDir())
	if err := mouseMove(1, 2); err == nil || !strings.Contains(err.Error(), "cliclick not found") {
		t.Fatalf("missing cliclick error = %v", err)
	}
}

func TestS12DarwinRealCapabilities(t *testing.T) {
	for _, command := range []string{"screencapture", "cliclick"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Skipf("%s: unavailable capability: %s executable", runtime.GOOS, command)
		}
	}
	msgs, err := NewScreenTool().Execute(context.Background(), map[string]any{"action": "screenshot"})
	if err != nil {
		t.Skipf("%s: unavailable capability: live screen capture (%v)", runtime.GOOS, err)
	}
	part := msgs[0].ContentParts[1].(messages.ImagePart)
	if part.MediaType != "image/jpeg" || len(part.Bytes) == 0 {
		t.Fatalf("live screenshot did not produce non-empty JPEG: %#v", part)
	}

	bounds := screenDisplayBounds(0)
	if bounds.Dx() < 16 || bounds.Dy() < 16 {
		t.Skipf("%s: unavailable capability: usable display bounds (%v)", runtime.GOOS, bounds)
	}
	originalX, originalY, err := darwinCursorPosition()
	if err != nil {
		t.Skipf("%s: unavailable capability: cursor position query (%v)", runtime.GOOS, err)
	}
	t.Cleanup(func() {
		if err := mouseButtonUp(originalX, originalY, "left"); err != nil {
			t.Logf("%s: cursor cleanup release failed: %v", runtime.GOOS, err)
		}
		if err := mouseMove(originalX, originalY); err != nil {
			t.Logf("%s: cursor cleanup restore failed: %v", runtime.GOOS, err)
		}
	})

	baseX := bounds.Min.X + bounds.Dx()/2
	baseY := bounds.Min.Y + bounds.Dy()/2
	operations := []struct {
		name         string
		wantX, wantY int
		call         func() error
	}{
		{"move", baseX, baseY, func() error { return mouseMove(baseX, baseY) }},
		{"click", baseX + 1, baseY + 1, func() error { return mouseClick(baseX+1, baseY+1, "left") }},
		{"double-click", baseX + 2, baseY + 2, func() error { return mouseDoubleClick(baseX+2, baseY+2, "left") }},
		{"button-down", baseX + 3, baseY + 3, func() error { return mouseButtonDown(baseX+3, baseY+3, "left") }},
		{"button-up", baseX + 4, baseY + 4, func() error { return mouseButtonUp(baseX+4, baseY+4, "left") }},
		{"drag", baseX + 7, baseY + 7, func() error { return mouseDrag(baseX+5, baseY+5, baseX+7, baseY+7, "left") }},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.call(); err != nil {
				t.Skipf("%s: unavailable capability: cursor input (%v)", runtime.GOOS, err)
			}
			gotX, gotY, err := darwinCursorPosition()
			if err != nil {
				t.Skipf("%s: unavailable capability: cursor position query after %s (%v)", runtime.GOOS, operation.name, err)
			}
			if gotX != operation.wantX || gotY != operation.wantY {
				t.Fatalf("cursor after %s = (%d, %d), want (%d, %d)", operation.name, gotX, gotY, operation.wantX, operation.wantY)
			}
		})
	}
}

func darwinCursorPosition() (int, int, error) {
	out, err := exec.Command("cliclick", "p").CombinedOutput()
	if err != nil {
		return 0, 0, fmt.Errorf("cliclick p: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	output := strings.TrimSpace(string(out))
	if output == "" {
		return 0, 0, fmt.Errorf("cliclick returned an empty position")
	}
	lines := strings.Split(output, "\n")
	parts := strings.Split(strings.TrimSpace(lines[len(lines)-1]), ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("unexpected cliclick position %q", output)
	}
	x, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("parse cliclick x: %w", err)
	}
	y, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("parse cliclick y: %w", err)
	}
	return x, y, nil
}
