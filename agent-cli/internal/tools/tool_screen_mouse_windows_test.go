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
	if bounds.Dx() < 2 || bounds.Dy() < 2 {
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
	t.Cleanup(func() { _ = mouseMove(originalX, originalY) })
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
	for _, tt := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"click", map[string]any{"action": "click", "x": float64(targetX), "y": float64(targetY)}, "left click at (" + strconv.Itoa(targetX) + ", " + strconv.Itoa(targetY) + ")"},
		{"double-click", map[string]any{"action": "double_click", "x": float64(targetX), "y": float64(targetY)}, "left double-click at (" + strconv.Itoa(targetX) + ", " + strconv.Itoa(targetY) + ")"},
		{"down", map[string]any{"action": "down", "x": float64(targetX), "y": float64(targetY)}, "left button held at (" + strconv.Itoa(targetX) + ", " + strconv.Itoa(targetY) + ")"},
		{"up", map[string]any{"action": "up", "x": float64(targetX), "y": float64(targetY)}, "left button released at (" + strconv.Itoa(targetX) + ", " + strconv.Itoa(targetY) + ")"},
		{"drag", map[string]any{"action": "drag", "x": float64(targetX), "y": float64(targetY), "to_x": float64(targetX + 1), "to_y": float64(targetY + 1)}, "left drag from (" + strconv.Itoa(targetX) + ", " + strconv.Itoa(targetY) + ") to (" + strconv.Itoa(targetX+1) + ", " + strconv.Itoa(targetY+1) + ")"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			msgs, err := tool.Execute(context.Background(), tt.args)
			if err != nil {
				t.Fatalf("mouse operation failed on a capable desktop: %v", err)
			}
			if len(msgs) != 1 || msgs[0].TextContent() != tt.want {
				t.Fatalf("result = %#v, want %q", msgs, tt.want)
			}
		})
	}
}
