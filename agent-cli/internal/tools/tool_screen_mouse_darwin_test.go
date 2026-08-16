//go:build darwin

package tools

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func TestS12DarwinFakeScreenAndMouseOperations(t *testing.T) {
	dir := fakeDarwinDesktop(t)
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
	if got := msgs[0].TextContent(); got != "Screenshot: display 1 (2x2 px)" {
		t.Fatalf("screenshot text = %q", got)
	}
	part := msgs[0].ContentParts[1].(messages.ImagePart)
	if part.MediaType != "image/jpeg" || len(part.Bytes) == 0 {
		t.Fatalf("screenshot part = %#v", part)
	}

	msgs, err = tool.Execute(context.Background(), map[string]any{"action": "record", "duration": float64(1), "fps": float64(1)})
	if err != nil || msgs[0].ContentParts[1].(messages.ImagePart).MediaType != "image/gif" {
		t.Fatalf("recording result = %#v, err = %v", msgs, err)
	}
	for _, tt := range []struct {
		name string
		call func() error
	}{
		{"move", func() error { return mouseMove(1, 2) }},
		{"click", func() error { return mouseClick(1, 2, "right") }},
		{"double", func() error { return mouseDoubleClick(1, 2, "middle") }},
		{"down", func() error { return mouseButtonDown(1, 2, "left") }},
		{"up", func() error { return mouseButtonUp(1, 2, "left") }},
		{"drag", func() error { return mouseDrag(1, 2, 3, 4, "left") }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); err != nil {
				t.Fatal(err)
			}
		})
	}
	if data, err := os.ReadFile(filepath.Join(dir, "cliclick.log")); err != nil || !strings.Contains(string(data), "r:1,2") || !strings.Contains(string(data), "m:1,2") {
		t.Fatalf("cliclick log = %q, err = %v", data, err)
	}

	bad := filepath.Join(dir, "bad.png")
	if err := os.WriteFile(bad, []byte("bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPNGasRGBA(bad); err == nil || !strings.Contains(err.Error(), "decode screenshot") {
		t.Fatalf("invalid PNG error = %v", err)
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
	if err := mouseMove(1, 1); err != nil {
		t.Skipf("%s: unavailable capability: cursor input (%v)", runtime.GOOS, err)
	}
}
