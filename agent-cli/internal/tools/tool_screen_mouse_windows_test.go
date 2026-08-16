//go:build windows

package tools

import (
	"bytes"
	"context"
	"image"
	"image/gif"
	"runtime"
	"strconv"
	"testing"
	"unsafe"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type windowsPoint struct {
	x int32
	y int32
}

func windowsCursorPosition() (int, int, error) {
	procGetCursorPos := user32dll.NewProc("GetCursorPos")
	var point windowsPoint
	ret, _, err := procGetCursorPos.Call(uintptr(unsafe.Pointer(&point)))
	if ret == 0 {
		return 0, 0, err
	}
	return int(point.x), int(point.y), nil
}

func requireWindowsDesktop(t *testing.T) image.Rectangle {
	t.Helper()
	h, _, err := procGetDC.Call(0)
	if h == 0 {
		t.Skipf("%s: unavailable capability: desktop device context (%v)", runtime.GOOS, err)
	}
	procReleaseDC.Call(0, h)
	bounds := screenDisplayBounds(0)
	if bounds.Dx() < 64 || bounds.Dy() < 64 {
		t.Skipf("%s: unavailable capability: usable display bounds (%v)", runtime.GOOS, bounds)
	}
	return bounds
}

func TestS12WindowsScreenCaptureAndRecord(t *testing.T) {
	bounds := requireWindowsDesktop(t)
	tool := NewScreenTool()

	msgs, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("live screenshot failed on a capable desktop: %v", err)
	}
	if len(msgs) != 1 || len(msgs[0].ContentParts) != 2 {
		t.Fatalf("screenshot result shape = %#v", msgs)
	}
	if got := msgs[0].TextContent(); got != "Screenshot: display 0 ("+strconv.Itoa(bounds.Dx())+"x"+strconv.Itoa(bounds.Dy())+" px)" {
		t.Fatalf("screenshot text = %q", got)
	}
	part, ok := msgs[0].ContentParts[1].(messages.ImagePart)
	if !ok || part.MediaType != "image/jpeg" || len(part.Bytes) == 0 {
		t.Fatalf("screenshot image part = %#v", msgs[0].ContentParts[1])
	}

	msgs, err = tool.Execute(context.Background(), map[string]any{"action": "record", "duration": float64(1), "fps": float64(1)})
	if err != nil {
		t.Fatalf("live recording failed on a capable desktop: %v", err)
	}
	recording := msgs[0].ContentParts[1].(messages.ImagePart)
	if recording.MediaType != "image/gif" || len(recording.Bytes) == 0 {
		t.Fatalf("recording image = %#v", recording)
	}
	decoded, err := gif.DecodeAll(bytes.NewReader(recording.Bytes))
	if err != nil || len(decoded.Image) != 1 {
		t.Fatalf("recording GIF frames = %d, err = %v", len(decoded.Image), err)
	}

}

func TestS12WindowsMouseOperationsRestoreCursor(t *testing.T) {
	bounds := requireWindowsDesktop(t)
	tool := NewMouseTool()
	originalX, originalY, err := windowsCursorPosition()
	if err != nil {
		t.Skipf("%s: unavailable capability: cursor position query (%v)", runtime.GOOS, err)
	}
	t.Cleanup(func() {
		_ = mouseButtonUp(originalX, originalY, "left")
		_ = mouseMove(originalX, originalY)
	})
	targetX, targetY := bounds.Min.X+bounds.Dx()/2, bounds.Min.Y+bounds.Dy()/2
	if err := mouseMove(targetX, targetY); err != nil {
		t.Skipf("%s: unavailable capability: cursor input (%v)", runtime.GOOS, err)
	}
	if x, y, err := windowsCursorPosition(); err != nil || x != targetX || y != targetY {
		t.Fatalf("cursor after move = (%d, %d), err = %v; want (%d, %d)", x, y, err, targetX, targetY)
	}

	for _, button := range []string{"left", "right", "middle", "other"} {
		down, up := buttonFlags(button)
		if down == 0 || up == 0 {
			t.Fatalf("buttonFlags(%q) = (%#x, %#x)", button, down, up)
		}
	}
	points := []struct{ x, y int }{
		{targetX + 4, targetY + 4},
		{targetX + 8, targetY + 8},
		{targetX + 12, targetY + 12},
		{targetX + 16, targetY + 16},
		{targetX + 20, targetY + 20},
	}
	operations := []struct {
		name        string
		args        map[string]any
		want        string
		wantCursorX int
		wantCursorY int
	}{
		{"click", map[string]any{"action": "click", "x": float64(points[0].x), "y": float64(points[0].y)}, "left click at (" + strconv.Itoa(points[0].x) + ", " + strconv.Itoa(points[0].y) + ")", points[0].x, points[0].y},
		{"double-click", map[string]any{"action": "double_click", "x": float64(points[1].x), "y": float64(points[1].y)}, "left double-click at (" + strconv.Itoa(points[1].x) + ", " + strconv.Itoa(points[1].y) + ")", points[1].x, points[1].y},
		{"down", map[string]any{"action": "down", "x": float64(points[2].x), "y": float64(points[2].y)}, "left button held at (" + strconv.Itoa(points[2].x) + ", " + strconv.Itoa(points[2].y) + ")", points[2].x, points[2].y},
		{"up", map[string]any{"action": "up", "x": float64(points[3].x), "y": float64(points[3].y)}, "left button released at (" + strconv.Itoa(points[3].x) + ", " + strconv.Itoa(points[3].y) + ")", points[3].x, points[3].y},
		{"drag", map[string]any{"action": "drag", "x": float64(points[4].x), "y": float64(points[4].y), "to_x": float64(points[4].x + 4), "to_y": float64(points[4].y + 4)}, "left drag from (" + strconv.Itoa(points[4].x) + ", " + strconv.Itoa(points[4].y) + ") to (" + strconv.Itoa(points[4].x+4) + ", " + strconv.Itoa(points[4].y+4) + ")", points[4].x + 4, points[4].y + 4},
	}
	lastX, lastY := targetX, targetY
	for _, tt := range operations {
		t.Run(tt.name, func(t *testing.T) {
			msgs, err := tool.Execute(context.Background(), tt.args)
			if err != nil {
				t.Fatalf("mouse operation failed on a capable desktop: %v", err)
			}
			if len(msgs) != 1 || msgs[0].TextContent() != tt.want {
				t.Fatalf("result = %#v, want %q", msgs, tt.want)
			}
			x, y, err := windowsCursorPosition()
			if err != nil {
				t.Skipf("%s: unavailable capability: cursor position query after %s (%v)", runtime.GOOS, tt.name, err)
			}
			if x < tt.wantCursorX-1 || x > tt.wantCursorX+1 || y < tt.wantCursorY-1 || y > tt.wantCursorY+1 {
				t.Fatalf("cursor after %s = (%d, %d), want (%d, %d) within one pixel", tt.name, x, y, tt.wantCursorX, tt.wantCursorY)
			}
			if x == lastX && y == lastY {
				t.Fatalf("cursor did not move for %s; still at (%d, %d)", tt.name, x, y)
			}
			lastX, lastY = x, y
		})
	}
}
