//go:build windows

package mouse

import (
	"bytes"
	"context"
	core "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal"
	display "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/display"
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
	tool := display.NewScreenTool()

	msgs, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("live screenshot failed on a capable desktop: %v", err)
	}
	if len(msgs) != 1 || len(msgs[0].ContentParts) != 2 {
		t.Fatalf("screenshot result shape = %#v", msgs)
	}
	part := assertScreenResult(t, msgs[0], "image/jpeg", bounds.Dx(), bounds.Dy())

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
	originalX, originalY, err := windowsCursorPosition()
	if err != nil {
		t.Skipf("%s: unavailable capability: cursor position query (%v)", runtime.GOOS, err)
	}
	t.Cleanup(func() {
		_ = mouseButtonUp(originalX, originalY, "left")
		_ = mouseMove(originalX, originalY)
	})
	targetX, targetY := bounds.Min.X+bounds.Dx()/2, bounds.Min.Y+bounds.Dy()/2
	assertWindowsCursorMove(t, targetX, targetY)
	assertWindowsButtonFlags(t)
	assertWindowsMouseOperations(t, targetX, targetY)
}

func assertWindowsCursorMove(t *testing.T, targetX, targetY int) {
	t.Helper()
	if err := mouseMove(targetX, targetY); err != nil {
		t.Skipf("%s: unavailable capability: cursor input (%v)", runtime.GOOS, err)
	}
	if x, y, err := windowsCursorPosition(); err != nil || x != targetX || y != targetY {
		t.Fatalf("cursor after move = (%d, %d), err = %v; want (%d, %d)", x, y, err, targetX, targetY)
	}
}

func assertWindowsButtonFlags(t *testing.T) {
	t.Helper()
	for _, button := range []string{"left", "right", "middle", "other"} {
		down, up := buttonFlags(button)
		if down == 0 || up == 0 {
			t.Fatalf("buttonFlags(%q) = (%#x, %#x)", button, down, up)
		}
	}
}

type windowsMouseOperation struct {
	name        string
	args        map[string]any
	want        string
	wantCursorX int
	wantCursorY int
}

func assertWindowsMouseOperations(t *testing.T, targetX, targetY int) {
	t.Helper()
	tool := NewMouseTool()
	points := [][2]int{{targetX + 4, targetY + 4}, {targetX + 8, targetY + 8}, {targetX + 12, targetY + 12}, {targetX + 16, targetY + 16}, {targetX + 20, targetY + 20}}
	operations := windowsMouseOperations(points)
	lastX, lastY := targetX, targetY
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			lastX, lastY = assertWindowsMouseOperation(t, tool, operation, lastX, lastY)
		})
	}
}

func windowsMouseOperations(points [][2]int) []windowsMouseOperation {
	return []windowsMouseOperation{
		{name: "click", args: map[string]any{"action": "click", "x": float64(points[0][0]), "y": float64(points[0][1])}, want: "left click at (" + strconv.Itoa(points[0][0]) + ", " + strconv.Itoa(points[0][1]) + ")", wantCursorX: points[0][0], wantCursorY: points[0][1]},
		{name: "double-click", args: map[string]any{"action": "double_click", "x": float64(points[1][0]), "y": float64(points[1][1])}, want: "left double-click at (" + strconv.Itoa(points[1][0]) + ", " + strconv.Itoa(points[1][1]) + ")", wantCursorX: points[1][0], wantCursorY: points[1][1]},
		{name: "down", args: map[string]any{"action": "down", "x": float64(points[2][0]), "y": float64(points[2][1])}, want: "left button held at (" + strconv.Itoa(points[2][0]) + ", " + strconv.Itoa(points[2][1]) + ")", wantCursorX: points[2][0], wantCursorY: points[2][1]},
		{name: "up", args: map[string]any{"action": "up", "x": float64(points[3][0]), "y": float64(points[3][1])}, want: "left button released at (" + strconv.Itoa(points[3][0]) + ", " + strconv.Itoa(points[3][1]) + ")", wantCursorX: points[3][0], wantCursorY: points[3][1]},
		{name: "drag", args: map[string]any{"action": "drag", "x": float64(points[4][0]), "y": float64(points[4][1]), "to_x": float64(points[4][0] + 4), "to_y": float64(points[4][1] + 4)}, want: "left drag from (" + strconv.Itoa(points[4][0]) + ", " + strconv.Itoa(points[4][1]) + ") to (" + strconv.Itoa(points[4][0]+4) + ", " + strconv.Itoa(points[4][1]+4) + ")", wantCursorX: points[4][0] + 4, wantCursorY: points[4][1] + 4},
	}
}

func assertWindowsMouseOperation(t *testing.T, tool core.Tool, operation windowsMouseOperation, lastX, lastY int) (int, int) {
	t.Helper()
	msgs, err := tool.Execute(context.Background(), operation.args)
	if err != nil {
		t.Fatalf("mouse operation failed on a capable desktop: %v", err)
	}
	if len(msgs) != 1 || msgs[0].TextContent() != operation.want {
		t.Fatalf("result = %#v, want %q", msgs, operation.want)
	}
	x, y, err := windowsCursorPosition()
	if err != nil {
		t.Skipf("%s: unavailable capability: cursor position query after %s (%v)", runtime.GOOS, operation.name, err)
	}
	if x < operation.wantCursorX-1 || x > operation.wantCursorX+1 || y < operation.wantCursorY-1 || y > operation.wantCursorY+1 {
		t.Fatalf("cursor after %s = (%d, %d), want (%d, %d) within one pixel", operation.name, x, y, operation.wantCursorX, operation.wantCursorY)
	}
	if x == lastX && y == lastY {
		t.Fatalf("cursor did not move for %s; still at (%d, %d)", operation.name, x, y)
	}
	return x, y
}
